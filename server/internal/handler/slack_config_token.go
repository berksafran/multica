package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/slack"
	"github.com/multica-ai/multica/server/internal/util/secret"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ── Config token service ────────────────────────────────────────────────
//
// Multica's `apps.manifest.*` callers used to read SLACK_CONFIG_TOKEN from
// the env var on every request. Slack rotates that token every 12 hours,
// so the env-var-only flow forced an ops-led restart every half day.
//
// configTokenService moves the token into Postgres (singleton row,
// AES-GCM encrypted via SLACK_TOKEN_ENC_KEY) and rotates it ahead of expiry
// via apps.manifest.rotate / tooling.tokens.rotate. The env vars
// SLACK_CONFIG_TOKEN + SLACK_CONFIG_REFRESH_TOKEN remain as a one-time
// bootstrap: the first call after a clean install seeds the DB from them.
//
// Concurrency model: a single mu serializes rotations within a process so
// two near-simultaneous Current() calls don't both burn a refresh token
// (Slack invalidates the previous refresh immediately on rotate). Across
// processes the UpsertSlackConfigToken query is the atomic point — the
// "loser" of a race rewrites the same row with newer credentials, which
// is benign because the latest Slack response is always authoritative.

// rotationLeadTime is how far before expires_at we proactively rotate.
// Sized to absorb clock skew + a transient Slack outage (one retry tick
// of the scheduler) without ever serving an expired token to a caller.
const rotationLeadTime = 60 * time.Minute

// rotationFloor is the minimum lifetime we require on a freshly minted
// token. Slack documents 12h; if the API ever returns a much shorter
// window we still want the scheduler tick to outpace it.
const rotationFloor = 10 * time.Minute

// errConfigTokenUnconfigured is returned by Current() when neither the DB
// row nor the env-var bootstrap is populated. Callers translate this into
// a 503 with "configure SLACK_CONFIG_TOKEN" guidance instead of bubbling a
// raw "no token" error to the UI.
var errConfigTokenUnconfigured = errors.New("slack: config token not configured")

type configTokenService struct {
	q      *db.Queries
	cipher *secret.AESGCM
	client *slack.ManifestClient
	now    func() time.Time

	mu sync.Mutex
}

// newConfigTokenService is exported only via h.slackConfigTokens which is
// memoized; do not call this directly outside the Handler. cipher must be
// the same instance used by slack_agent_app encryption so a single
// SLACK_TOKEN_ENC_KEY rotation flips both secret stores.
func newConfigTokenService(q *db.Queries, cipher *secret.AESGCM) *configTokenService {
	return &configTokenService{
		q:      q,
		cipher: cipher,
		client: slack.NewManifestClient(""),
		now:    time.Now,
	}
}

// ConfigTokenStatus is the public-facing snapshot used by the admin UI to
// render the rotation health. AccessToken is intentionally absent — the
// UI never needs the plaintext.
type ConfigTokenStatus struct {
	Configured      bool       `json:"configured"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	LastRotatedAt   *time.Time `json:"last_rotated_at,omitempty"`
	LastRotateError string     `json:"last_rotate_error,omitempty"`
	// FromEnvFallback is true when the deployment is still running on the
	// SLACK_CONFIG_TOKEN env var (no rotation possible, manual refresh
	// every 12h required). UI uses this to nudge the admin into pasting
	// a refresh token so rotation can take over.
	FromEnvFallback bool `json:"from_env_fallback"`
}

// Current returns a valid access token. If the persisted token is within
// rotationLeadTime of expiry, Current rotates inline so the caller is
// never handed a token about to die mid-request. Falls back to the env
// var on a never-bootstrapped deployment (still serves /verify and
// /create flows; rotation is impossible until the admin pastes a
// refresh token).
func (s *configTokenService) Current(ctx context.Context) (string, error) {
	row, err := s.q.GetSlackConfigToken(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		// Bootstrap fallback: env var must serve the very first request
		// after install so the admin can paste tokens via the UI. After
		// that the DB row owns lifecycle.
		if env := strings.TrimSpace(os.Getenv("SLACK_CONFIG_TOKEN")); env != "" {
			return env, nil
		}
		return "", errConfigTokenUnconfigured
	}
	if err != nil {
		return "", fmt.Errorf("load config token: %w", err)
	}

	if s.now().Add(rotationLeadTime).Before(row.ExpiresAt.Time) {
		// Still well inside the safety window — decrypt and return.
		access, err := s.cipher.Decrypt(row.AccessTokenEnc)
		if err != nil {
			return "", fmt.Errorf("decrypt access token: %w", err)
		}
		return access, nil
	}

	// Near or past expiry — rotate then return the new token. The mutex
	// keeps a thundering herd of Current() callers from each calling
	// Slack with the same refresh_token (only the first succeeds; the
	// rest get invalid_refresh_token).
	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-read inside the lock: a sibling goroutine may have already
	// rotated while we were waiting.
	row, err = s.q.GetSlackConfigToken(ctx)
	if err != nil {
		return "", fmt.Errorf("re-load config token: %w", err)
	}
	if s.now().Add(rotationLeadTime).Before(row.ExpiresAt.Time) {
		access, err := s.cipher.Decrypt(row.AccessTokenEnc)
		if err != nil {
			return "", fmt.Errorf("decrypt access token: %w", err)
		}
		return access, nil
	}

	return s.rotateLocked(ctx, row)
}

// Bootstrap writes the initial access + refresh pair after the admin
// pastes them in the UI. It computes a conservative expiry from the
// rotationFloor since Slack does not return one on the OAuth issuance
// path; the next scheduler tick will trigger a real rotate to overwrite
// this placeholder with Slack's actual `exp`.
func (s *configTokenService) Bootstrap(ctx context.Context, accessToken, refreshToken string) error {
	accessToken = strings.TrimSpace(accessToken)
	refreshToken = strings.TrimSpace(refreshToken)
	if accessToken == "" || refreshToken == "" {
		return errors.New("access_token and refresh_token are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify the credentials work before persisting them — pasting a
	// typo and only finding out on the next scheduler tick (up to 5min
	// later) is a poor UX. Rotate immediately so we land Slack's real
	// expires_at on the first write instead of our conservative placeholder.
	s.client.SetAccessToken(accessToken)
	resp, err := s.client.Rotate(ctx, refreshToken)
	if err != nil {
		return fmt.Errorf("validate via rotate: %w", err)
	}

	return s.persistRotation(ctx, resp)
}

// Rotate forces a rotation regardless of the current expires_at. The
// admin UI's "Rotate now" button calls this; the scheduler also uses it
// on its background tick.
func (s *configTokenService) Rotate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	row, err := s.q.GetSlackConfigToken(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return errConfigTokenUnconfigured
	}
	if err != nil {
		return fmt.Errorf("load config token: %w", err)
	}
	if _, err := s.rotateLocked(ctx, row); err != nil {
		return err
	}
	return nil
}

// Status returns the snapshot the admin UI renders.
func (s *configTokenService) Status(ctx context.Context) (ConfigTokenStatus, error) {
	row, err := s.q.GetSlackConfigToken(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		st := ConfigTokenStatus{Configured: false}
		if env := strings.TrimSpace(os.Getenv("SLACK_CONFIG_TOKEN")); env != "" {
			st.Configured = true
			st.FromEnvFallback = true
		}
		return st, nil
	}
	if err != nil {
		return ConfigTokenStatus{}, fmt.Errorf("load config token: %w", err)
	}
	st := ConfigTokenStatus{Configured: true}
	exp := row.ExpiresAt.Time
	st.ExpiresAt = &exp
	last := row.LastRotatedAt.Time
	st.LastRotatedAt = &last
	if row.LastRotateError.Valid {
		st.LastRotateError = row.LastRotateError.String
	}
	return st, nil
}

// rotateLocked is the inner rotation loop. Caller holds s.mu. Returns the
// fresh access token on success. On `invalid_refresh_token` it persists
// last_rotate_error and surfaces the error — the admin must re-paste.
// Transient errors leave the row untouched (the still-valid existing
// token keeps serving callers until the next scheduler tick retries).
func (s *configTokenService) rotateLocked(ctx context.Context, row db.SlackConfigToken) (string, error) {
	refresh, err := s.cipher.Decrypt(row.RefreshTokenEnc)
	if err != nil {
		return "", fmt.Errorf("decrypt refresh token: %w", err)
	}

	resp, err := s.client.Rotate(ctx, refresh)
	if err != nil {
		if slack.IsInvalidRefreshToken(err) {
			// Terminal: only an admin re-paste recovers. Persist the
			// reason so the UI can surface it; leave the token columns
			// alone so existing callers keep working until expiry.
			s.recordRotateError(ctx, err.Error())
		} else {
			// Transient (network, 5xx) — also record so the UI banner
			// flips, but the scheduler will retry on the next tick.
			s.recordRotateError(ctx, err.Error())
		}
		return "", fmt.Errorf("rotate: %w", err)
	}

	if err := s.persistRotation(ctx, resp); err != nil {
		return "", err
	}
	return resp.Token, nil
}

// persistRotation encrypts the new token pair and upserts the singleton
// row. Slack's `exp` is a Unix timestamp; we trust it but enforce
// rotationFloor as a sanity bound so a misformed response can't make us
// hammer Slack.
func (s *configTokenService) persistRotation(ctx context.Context, resp *slack.RotateResponse) error {
	accessEnc, err := s.cipher.Encrypt(resp.Token)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	refreshEnc, err := s.cipher.Encrypt(resp.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}

	expiresAt := time.Unix(resp.ExpiresAt, 0)
	if floor := s.now().Add(rotationFloor); expiresAt.Before(floor) {
		expiresAt = floor
	}

	_, err = s.q.UpsertSlackConfigToken(ctx, db.UpsertSlackConfigTokenParams{
		AccessTokenEnc:  accessEnc,
		RefreshTokenEnc: refreshEnc,
		ExpiresAt:       pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("upsert config token: %w", err)
	}
	return nil
}

func (s *configTokenService) recordRotateError(ctx context.Context, msg string) {
	if err := s.q.SetSlackConfigTokenRotateError(ctx, pgtype.Text{String: msg, Valid: true}); err != nil {
		slog.Warn("slack config token: record rotate error failed", "err", err)
	}
}

// ── Handler integration ─────────────────────────────────────────────────

// slackConfigTokens returns the process-wide service, lazily constructed.
// Mirrors the pattern slackTokenCipher uses so callers do not have to
// know about cipher initialization.
var (
	slackConfigTokensOnce sync.Once
	slackConfigTokensInst *configTokenService
	slackConfigTokensErr  error
)

// SlackConfigTokensForScheduler exposes the singleton service to the
// cmd/server background loop. The returned value is the same instance the
// HTTP handlers use, so manual and automatic rotations share one mutex.
// Returns only the public methods (Rotate) the scheduler needs.
func (h *Handler) SlackConfigTokensForScheduler() (SlackConfigTokenRotator, error) {
	return h.slackConfigTokens()
}

// SlackConfigTokenRotator narrows configTokenService to the surface the
// scheduler is allowed to touch. Kept as an interface so the scheduler
// stays testable without exporting the whole struct.
type SlackConfigTokenRotator interface {
	Rotate(ctx context.Context) error
}

func (h *Handler) slackConfigTokens() (*configTokenService, error) {
	slackConfigTokensOnce.Do(func() {
		cipher, err := slackTokenCipher()
		if err != nil {
			slackConfigTokensErr = err
			return
		}
		slackConfigTokensInst = newConfigTokenService(h.Queries, cipher)
	})
	return slackConfigTokensInst, slackConfigTokensErr
}

// newConfigManifestClient is the shorthand callers use in place of the
// previous slack.NewManifestClient(os.Getenv("SLACK_CONFIG_TOKEN")) — it
// pulls a fresh access token through the service so rotation works without
// every handler having to know about expiry math.
func (h *Handler) newConfigManifestClient(ctx context.Context) (*slack.ManifestClient, error) {
	svc, err := h.slackConfigTokens()
	if err != nil {
		return nil, err
	}
	token, err := svc.Current(ctx)
	if err != nil {
		return nil, err
	}
	return slack.NewManifestClient(token), nil
}

// ── HTTP endpoints ──────────────────────────────────────────────────────
//
// These live under /api/workspaces/{id}/slack/config-token. The token is a
// process-wide singleton but we keep the URL workspace-scoped so it reuses
// the existing requireWorkspaceRole gate (owner-only) — adding a separate
// "platform admin" role for one endpoint group is not worth the surface.

// GetSlackConfigTokenStatus reports rotation health for the admin UI.
// Returns 200 with Configured=false when no DB row and no env fallback;
// the UI then shows the paste form.
func (h *Handler) GetSlackConfigTokenStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := ctxWorkspaceID(r.Context())
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	svc, err := h.slackConfigTokens()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "slack token cipher: "+err.Error())
		return
	}
	st, err := svc.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "status: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// BootstrapSlackConfigTokenRequest carries the initial paste payload.
type BootstrapSlackConfigTokenRequest struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// BootstrapSlackConfigToken accepts the access+refresh pair pasted from
// api.slack.com/apps → "Your config tokens". The handler immediately calls
// Slack's rotate endpoint to validate the credentials and to land Slack's
// authoritative `expires_at` on the first DB write — so a typo'd paste
// surfaces as a 502 here rather than as a silent failure 5 minutes later
// on the scheduler tick.
func (h *Handler) BootstrapSlackConfigToken(w http.ResponseWriter, r *http.Request) {
	workspaceID := ctxWorkspaceID(r.Context())
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	var req BootstrapSlackConfigTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	svc, err := h.slackConfigTokens()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "slack token cipher: "+err.Error())
		return
	}
	if err := svc.Bootstrap(r.Context(), req.AccessToken, req.RefreshToken); err != nil {
		writeError(w, http.StatusBadGateway, "bootstrap: "+err.Error())
		return
	}
	st, err := svc.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "status: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// RotateSlackConfigToken forces an immediate rotation. The admin UI button
// calls this when last_rotate_error is set or as a manual safety net. The
// scheduler triggers the same code path automatically.
func (h *Handler) RotateSlackConfigToken(w http.ResponseWriter, r *http.Request) {
	workspaceID := ctxWorkspaceID(r.Context())
	if _, ok := h.requireWorkspaceRole(w, r, workspaceID, "workspace not found", "owner", "admin"); !ok {
		return
	}
	svc, err := h.slackConfigTokens()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "slack token cipher: "+err.Error())
		return
	}
	if err := svc.Rotate(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, "rotate: "+err.Error())
		return
	}
	st, err := svc.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "status: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}
