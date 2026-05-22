package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/slack"
	"github.com/multica-ai/multica/server/internal/util/secret"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ── Cipher singleton ────────────────────────────────────────────────────

var (
	slackCipherOnce sync.Once
	slackCipherInst *secret.AESGCM
	slackCipherErr  error
)

// slackTokenCipher returns a process-wide AES-GCM cipher built from
// SLACK_TOKEN_ENC_KEY. Memoized so we don't re-derive on every webhook.
func slackTokenCipher() (*secret.AESGCM, error) {
	slackCipherOnce.Do(func() {
		key := strings.TrimSpace(os.Getenv("SLACK_TOKEN_ENC_KEY"))
		slackCipherInst, slackCipherErr = secret.NewAESGCMFromBase64(key)
	})
	return slackCipherInst, slackCipherErr
}

// ── State token (per-agent, HMAC) ───────────────────────────────────────

// slackStateSecret keys the OAuth state HMAC. Reuses SLACK_CONFIG_TOKEN
// rather than introducing yet another secret — the config token is
// already process-private and rotates with the manifest credentials.
func slackStateSecret() string {
	if s := strings.TrimSpace(os.Getenv("SLACK_STATE_SECRET")); s != "" {
		return s
	}
	return strings.TrimSpace(os.Getenv("SLACK_CONFIG_TOKEN"))
}

// signSlackState binds the OAuth callback to the workspace + agent +
// app combination that initiated it. Format: "<ws>.<agent>.<appRowID>.<nonce>.<sigHex>".
func signSlackState(workspaceID, agentID, appRowID string) (string, error) {
	secretKey := slackStateSecret()
	if secretKey == "" {
		return "", errors.New("slack integration is not configured")
	}
	nonceBytes := make([]byte, 12)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(nonceBytes)
	payload := workspaceID + "." + agentID + "." + appRowID + "." + nonce
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func verifySlackState(token string) (workspaceID, agentID, appRowID string, ok bool) {
	secretKey := slackStateSecret()
	if secretKey == "" {
		return "", "", "", false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 5 {
		return "", "", "", false
	}
	ws, agent, app, nonce, sig := parts[0], parts[1], parts[2], parts[3], parts[4]
	payload := ws + "." + agent + "." + app + "." + nonce
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(payload))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(sig)) {
		return "", "", "", false
	}
	return ws, agent, app, true
}

// ── Response shapes ─────────────────────────────────────────────────────

type SlackStatusResponse struct {
	Configured     bool    `json:"configured"`
	Provisioned    bool    `json:"provisioned"`
	Installed      bool    `json:"installed"`
	HasCredentials bool    `json:"has_credentials"`
	AppID          *string `json:"app_id,omitempty"`
	TeamID         *string `json:"team_id,omitempty"`
	BotUserID      *string `json:"bot_user_id,omitempty"`
	Status         *string `json:"status,omitempty"`
	InstallURL     *string `json:"install_url,omitempty"`
}

// SlackVerifyResponse is the dedicated probe result returned by
// /slack/verify. We return AppExists rather than baking it into the
// status response so callers can decide when to pay the network cost
// (one round-trip to Slack per verify call).
type SlackVerifyResponse struct {
	AppExists bool   `json:"app_exists"`
	Error     string `json:"error,omitempty"`
}

// SlackCredentialsResponse exposes the per-app OAuth client credentials.
// client_id is plaintext-safe (it's sent in the OAuth URL anyway);
// client_secret is *never* returned — only a presence flag — so the UI
// can render "saved / not saved" without ever holding the plaintext.
type SlackCredentialsResponse struct {
	ClientID        string `json:"client_id"`
	HasClientSecret bool   `json:"has_client_secret"`
}

type UpdateSlackCredentialsRequest struct {
	ClientID     *string `json:"client_id,omitempty"`
	ClientSecret *string `json:"client_secret,omitempty"`
}

// ── GET /api/workspaces/{id}/agents/{agentId}/slack ─────────────────────

func (h *Handler) GetAgentSlackStatus(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agent_id")
	if !ok {
		return
	}
	// Verify agent belongs to workspace (returns 404 if not).
	if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	resp := SlackStatusResponse{Configured: slackConfigured()}
	app, err := h.Queries.GetSlackAgentAppByAgentID(r.Context(), agentUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, resp)
			return
		}
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	resp.Provisioned = true
	resp.Installed = app.Status == "installed"
	resp.HasCredentials = app.OauthClientIDEnc.Valid && app.OauthClientIDEnc.String != "" &&
		app.OauthClientSecretEnc.Valid && app.OauthClientSecretEnc.String != ""
	resp.AppID = &app.SlackAppID
	resp.Status = &app.Status
	if app.SlackTeamID.Valid {
		v := app.SlackTeamID.String
		resp.TeamID = &v
	}
	if app.BotUserID.Valid {
		v := app.BotUserID.String
		resp.BotUserID = &v
	}
	if !resp.Installed && resp.HasCredentials {
		if clientID, derr := h.decryptSlackOAuthClientID(app); derr == nil {
			if u, err := buildSlackInstallURL(uuidToString(wsUUID), uuidToString(agentUUID), uuidToString(app.ID), clientID); err == nil {
				resp.InstallURL = &u
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ── GET / PUT /api/workspaces/{id}/agents/{agentId}/slack/credentials ──

func (h *Handler) GetAgentSlackCredentials(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agent_id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID: agentUUID, WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	app, err := h.Queries.GetSlackAgentAppByAgentID(r.Context(), agentUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, SlackCredentialsResponse{})
			return
		}
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	resp := SlackCredentialsResponse{
		HasClientSecret: app.OauthClientSecretEnc.Valid && app.OauthClientSecretEnc.String != "",
	}
	if clientID, derr := h.decryptSlackOAuthClientID(app); derr == nil {
		resp.ClientID = clientID
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) UpdateAgentSlackCredentials(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agent_id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID: agentUUID, WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	app, err := h.Queries.GetSlackAgentAppByAgentID(r.Context(), agentUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "slack app not provisioned")
		return
	}

	var req UpdateSlackCredentialsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	cid := strings.TrimSpace(deref(req.ClientID))
	csec := strings.TrimSpace(deref(req.ClientSecret))
	if cid == "" && csec == "" {
		writeError(w, http.StatusBadRequest, "client_id or client_secret required")
		return
	}
	if err := h.persistSlackOAuthCredentials(r.Context(), app.ID, cid, csec); err != nil {
		writeError(w, http.StatusInternalServerError, "persist failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ── POST /api/workspaces/{id}/agents/{agentId}/slack/provision ──────────

type ProvisionSlackResponse struct {
	AppID      string `json:"app_id"`
	InstallURL string `json:"install_url"`
}

// ProvisionAgentSlackApp creates a Slack App via the Manifest API, stores
// the credentials, and returns the URL the user should open to install
// the app into their Slack workspace.
func (h *Handler) ProvisionAgentSlackApp(w http.ResponseWriter, r *http.Request) {
	if !slackConfigured() {
		writeError(w, http.StatusServiceUnavailable, "slack integration not configured")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agent_id")
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	if existing, err := h.Queries.GetSlackAgentAppByAgentID(r.Context(), agentUUID); err == nil {
		// Already provisioned. Return the install URL so the caller can
		// resume the flow without making the user wait on a second
		// manifest API round-trip. If credentials happen to be missing
		// (legacy row before #098, or a manual wipe), we return the row
		// without an install URL so the UI can prompt for them.
		installURL := ""
		if clientID, derr := h.decryptSlackOAuthClientID(existing); derr == nil {
			if u, err := buildSlackInstallURL(uuidToString(wsUUID), uuidToString(agentUUID), uuidToString(existing.ID), clientID); err == nil {
				installURL = u
			}
		}
		writeJSON(w, http.StatusOK, ProvisionSlackResponse{AppID: existing.SlackAppID, InstallURL: installURL})
		return
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	manifest := slack.BuildAgentManifest(slack.AgentManifestInput{
		DisplayName: nonEmptyOr(agent.Name, "Multica Agent"),
		Description: fmt.Sprintf("Multica agent %q wired to Slack", agent.Name),
		WebhookURL:  slackWebhookURL(uuidToString(agentUUID)),
		RedirectURL: slackRedirectURI(),
	})

	mc, err := h.newConfigManifestClient(r.Context())
	if err != nil {
		slog.Error("slack: config token unavailable for create", "err", err, "agent_id", uuidToString(agentUUID))
		writeError(w, http.StatusServiceUnavailable, "slack config token: "+err.Error())
		return
	}
	created, err := mc.Create(r.Context(), manifest)
	if err != nil {
		slog.Error("slack: manifest create failed", "err", err, "agent_id", uuidToString(agentUUID))
		writeError(w, http.StatusBadGateway, "slack manifest create failed: "+err.Error())
		return
	}

	connectedBy := pgtype.UUID{}
	if userID := requestUserID(r); userID != "" {
		if u, err := parseStrictUUID(userID); err == nil {
			connectedBy = u
		}
	}

	app, err := h.Queries.CreateSlackAgentApp(r.Context(), db.CreateSlackAgentAppParams{
		AgentID:         agentUUID,
		WorkspaceID:     wsUUID,
		SlackAppID:      created.AppID,
		SigningSecret:   created.Credentials.SigningSecret,
		ManifestVersion: 1,
		Status:          "provisioned",
		ConnectedByID:   connectedBy,
	})
	if err != nil {
		// Roll back the Slack-side app — leaving it dangling would
		// confuse the user (a half-provisioned app on api.slack.com
		// without any DB pointer to manage it).
		if delErr := mc.Delete(r.Context(), created.AppID); delErr != nil {
			slog.Warn("slack: rollback delete failed", "err", delErr, "app_id", created.AppID)
		}
		writeError(w, http.StatusInternalServerError, "persist failed: "+err.Error())
		return
	}
	// Encrypt + persist per-app OAuth credentials. Failure here is
	// fatal to the provision: without these the install URL cannot be
	// built and the user would be stuck with an unreachable app. Roll
	// back the Slack-side app so the next provision starts clean.
	if err := h.persistSlackOAuthCredentials(r.Context(), app.ID, created.Credentials.ClientID, created.Credentials.ClientSecret); err != nil {
		slog.Error("slack: persist oauth credentials failed", "err", err, "agent_id", uuidToString(agentUUID))
		if delErr := mc.Delete(r.Context(), created.AppID); delErr != nil {
			slog.Warn("slack: rollback delete failed", "err", delErr, "app_id", created.AppID)
		}
		if delErr := h.Queries.DeleteSlackAgentApp(r.Context(), db.DeleteSlackAgentAppParams{ID: app.ID, WorkspaceID: wsUUID}); delErr != nil {
			slog.Warn("slack: rollback row delete failed", "err", delErr, "id", uuidToString(app.ID))
		}
		writeError(w, http.StatusInternalServerError, "persist credentials failed: "+err.Error())
		return
	}

	installURL, err := buildSlackInstallURL(uuidToString(wsUUID), uuidToString(agentUUID), uuidToString(app.ID), created.Credentials.ClientID)
	if err != nil {
		slog.Warn("slack: build install URL failed", "err", err)
	}
	writeJSON(w, http.StatusOK, ProvisionSlackResponse{AppID: app.SlackAppID, InstallURL: installURL})
}

// persistSlackOAuthCredentials encrypts and stores the per-app OAuth
// credentials returned by apps.manifest.create (or pasted by the user
// via the credentials edit UI). Both ciphertexts are produced by the
// same AES-GCM helper that wraps the bot token. Pass empty strings to
// leave the existing ciphertext in place — the UPDATE query is
// COALESCE-guarded so partial updates are safe.
func (h *Handler) persistSlackOAuthCredentials(ctx context.Context, appID pgtype.UUID, clientID, clientSecret string) error {
	if clientID == "" && clientSecret == "" {
		return nil
	}
	aead, err := slackTokenCipher()
	if err != nil {
		return fmt.Errorf("cipher unavailable: %w", err)
	}
	params := db.UpdateSlackAgentAppOAuthCredentialsParams{ID: appID}
	if clientID != "" {
		enc, err := aead.Encrypt(clientID)
		if err != nil {
			return fmt.Errorf("encrypt client_id: %w", err)
		}
		params.OauthClientIDEnc = pgtype.Text{String: enc, Valid: true}
	}
	if clientSecret != "" {
		enc, err := aead.Encrypt(clientSecret)
		if err != nil {
			return fmt.Errorf("encrypt client_secret: %w", err)
		}
		params.OauthClientSecretEnc = pgtype.Text{String: enc, Valid: true}
	}
	return h.Queries.UpdateSlackAgentAppOAuthCredentials(ctx, params)
}

// decryptSlackOAuthClientID returns the plaintext client_id stored on
// the app row, or ErrSlackOAuthCredentialsMissing if none has been
// persisted yet. Errors from cipher initialization or decryption are
// surfaced verbatim so the caller can map them onto user-facing 4xx.
func (h *Handler) decryptSlackOAuthClientID(app db.SlackAgentApp) (string, error) {
	if !app.OauthClientIDEnc.Valid || app.OauthClientIDEnc.String == "" {
		return "", ErrSlackOAuthCredentialsMissing
	}
	aead, err := slackTokenCipher()
	if err != nil {
		return "", err
	}
	return aead.Decrypt(app.OauthClientIDEnc.String)
}

// decryptSlackOAuthClientSecret mirrors decryptSlackOAuthClientID for
// the secret half of the pair. The secret value is never returned to
// API clients — only consumed in the OAuth callback handler.
func (h *Handler) decryptSlackOAuthClientSecret(app db.SlackAgentApp) (string, error) {
	if !app.OauthClientSecretEnc.Valid || app.OauthClientSecretEnc.String == "" {
		return "", ErrSlackOAuthCredentialsMissing
	}
	aead, err := slackTokenCipher()
	if err != nil {
		return "", err
	}
	return aead.Decrypt(app.OauthClientSecretEnc.String)
}

// ErrSlackOAuthCredentialsMissing is returned when the per-app
// client_id / client_secret have not been persisted yet. UI exposes
// this via the has_credentials=false flag on the status response.
var ErrSlackOAuthCredentialsMissing = errors.New("slack: oauth credentials missing")

// ── GET /api/slack/oauth/callback ───────────────────────────────────────

func (h *Handler) SlackOAuthCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	frontend := strings.TrimSpace(os.Getenv("FRONTEND_ORIGIN"))
	if frontend == "" {
		frontend = "http://localhost:3000"
	}
	resultURL := strings.TrimRight(frontend, "/") + "/settings?tab=agents"

	if code == "" || state == "" {
		http.Redirect(w, r, resultURL+"&slack_error=missing_params", http.StatusFound)
		return
	}
	wsStr, agentStr, appRowStr, ok := verifySlackState(state)
	if !ok {
		http.Redirect(w, r, resultURL+"&slack_error=invalid_state", http.StatusFound)
		return
	}
	appUUID, err := parseStrictUUID(appRowStr)
	if err != nil {
		http.Redirect(w, r, resultURL+"&slack_error=bad_app", http.StatusFound)
		return
	}
	_ = wsStr
	_ = agentStr

	app, err := h.Queries.GetSlackAgentAppByID(r.Context(), appUUID)
	if err != nil {
		http.Redirect(w, r, resultURL+"&slack_error=app_not_found", http.StatusFound)
		return
	}

	clientID, err := h.decryptSlackOAuthClientID(app)
	if err != nil {
		http.Redirect(w, r, resultURL+"&slack_error=oauth_client_not_configured", http.StatusFound)
		return
	}
	clientSecret, err := h.decryptSlackOAuthClientSecret(app)
	if err != nil {
		http.Redirect(w, r, resultURL+"&slack_error=oauth_client_not_configured", http.StatusFound)
		return
	}

	res, err := slack.OAuthV2Access(r.Context(), nil, "", code, clientID, clientSecret, slackRedirectURI())
	if err != nil {
		slog.Error("slack: oauth exchange failed", "err", err)
		http.Redirect(w, r, resultURL+"&slack_error=oauth_failed", http.StatusFound)
		return
	}

	aead, err := slackTokenCipher()
	if err != nil {
		http.Redirect(w, r, resultURL+"&slack_error=cipher_unavailable", http.StatusFound)
		return
	}
	enc, err := aead.Encrypt(res.AccessToken)
	if err != nil {
		http.Redirect(w, r, resultURL+"&slack_error=encrypt_failed", http.StatusFound)
		return
	}

	if _, err := h.Queries.UpdateSlackAgentAppInstall(r.Context(), db.UpdateSlackAgentAppInstallParams{
		ID:           app.ID,
		SlackTeamID:  pgtype.Text{String: res.Team.ID, Valid: res.Team.ID != ""},
		BotUserID:    pgtype.Text{String: res.BotUserID, Valid: res.BotUserID != ""},
		BotTokenEnc:  pgtype.Text{String: enc, Valid: true},
	}); err != nil {
		http.Redirect(w, r, resultURL+"&slack_error=persist_failed", http.StatusFound)
		return
	}

	http.Redirect(w, r, resultURL+"&slack_connected=1", http.StatusFound)
}

// ── POST /api/workspaces/{id}/agents/{agentId}/slack/sync ───────────────

// SyncAgentSlackApp re-publishes the manifest (name + description) so
// the Slack-side app metadata matches the Multica agent after a
// rename. Bot user avatars cannot be set via API — Slack requires
// manual upload in the app dashboard — so this endpoint covers only
// name + description.
func (h *Handler) SyncAgentSlackApp(w http.ResponseWriter, r *http.Request) {
	if !slackConfigured() {
		writeError(w, http.StatusServiceUnavailable, "slack integration not configured")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agent_id")
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	app, err := h.Queries.GetSlackAgentAppByAgentID(r.Context(), agentUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "slack app not provisioned")
		return
	}

	manifest := slack.BuildAgentManifest(slack.AgentManifestInput{
		DisplayName: nonEmptyOr(agent.Name, "Multica Agent"),
		Description: fmt.Sprintf("Multica agent %q wired to Slack", agent.Name),
		WebhookURL:  slackWebhookURL(uuidToString(agentUUID)),
		RedirectURL: slackRedirectURI(),
	})

	mc, err := h.newConfigManifestClient(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "slack config token: "+err.Error())
		return
	}
	if _, err := mc.Update(r.Context(), app.SlackAppID, manifest); err != nil {
		writeError(w, http.StatusBadGateway, "slack manifest update failed: "+err.Error())
		return
	}
	if err := h.Queries.UpdateSlackAgentAppManifestVersion(r.Context(), db.UpdateSlackAgentAppManifestVersionParams{
		ID:              app.ID,
		ManifestVersion: app.ManifestVersion + 1,
	}); err != nil {
		slog.Warn("slack: bump manifest version failed", "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── POST /api/workspaces/{id}/agents/{agentId}/slack/verify ────────────

// VerifyAgentSlackApp probes Slack to confirm the provisioned app
// still exists on their side. The user case it solves: they deleted
// the app manually from api.slack.com/apps and now the Multica row
// is orphaned. UI calls this on tab open + on focus to detect drift
// and offers a one-click "Remove from Multica" when app_exists=false.
func (h *Handler) VerifyAgentSlackApp(w http.ResponseWriter, r *http.Request) {
	if !slackConfigured() {
		writeError(w, http.StatusServiceUnavailable, "slack integration not configured")
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agent_id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID: agentUUID, WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	app, err := h.Queries.GetSlackAgentAppByAgentID(r.Context(), agentUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No row, no app to verify — treat as "doesn't exist" so
			// the UI doesn't render an orphan banner.
			writeJSON(w, http.StatusOK, SlackVerifyResponse{AppExists: false})
			return
		}
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	mc, err := h.newConfigManifestClient(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "slack config token: "+err.Error())
		return
	}
	_, err = mc.Export(r.Context(), app.SlackAppID)
	if err == nil {
		writeJSON(w, http.StatusOK, SlackVerifyResponse{AppExists: true})
		return
	}
	if slack.IsAppMissing(err) {
		writeJSON(w, http.StatusOK, SlackVerifyResponse{AppExists: false, Error: err.Error()})
		return
	}
	// Network blip, expired config token, etc — return 502 so the UI
	// keeps the previous state instead of falsely flagging an orphan.
	slog.Warn("slack: verify failed", "err", err, "app_id", app.SlackAppID)
	writeError(w, http.StatusBadGateway, "verify failed: "+err.Error())
}

// ── DELETE /api/workspaces/{id}/agents/{agentId}/slack ──────────────────

func (h *Handler) DisconnectAgentSlackApp(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agent_id")
	if !ok {
		return
	}
	app, err := h.Queries.GetSlackAgentAppByAgentID(r.Context(), agentUUID)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	mc, mcErr := h.newConfigManifestClient(r.Context())
	if mcErr != nil {
		// Best-effort cleanup: if we can't reach Slack we still purge the
		// local row so the agent's slack tab stops showing a phantom app.
		slog.Warn("slack: config token unavailable for delete", "err", mcErr, "app_id", app.SlackAppID)
	} else if err := mc.Delete(r.Context(), app.SlackAppID); err != nil {
		slog.Warn("slack: manifest delete failed (continuing)", "err", err, "app_id", app.SlackAppID)
	}
	if err := h.Queries.DeleteSlackAgentApp(r.Context(), db.DeleteSlackAgentAppParams{
		ID:          app.ID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Helpers ─────────────────────────────────────────────────────────────

// buildSlackInstallURL composes the Slack OAuth v2 authorize URL for a
// specific provisioned app. clientID comes from the row's encrypted
// oauth_client_id_enc column (decrypted by the caller) — the env-var
// fallback used in the MVP-foundation commit is gone now that the
// schema can hold per-app credentials.
func buildSlackInstallURL(wsID, agentID, appRowID, clientID string) (string, error) {
	if clientID == "" {
		return "", ErrSlackOAuthCredentialsMissing
	}
	state, err := signSlackState(wsID, agentID, appRowID)
	if err != nil {
		return "", err
	}
	v := url.Values{}
	v.Set("client_id", clientID)
	v.Set("scope", "app_mentions:read,chat:write,chat:write.customize,im:history,im:read,channels:history,groups:history,users:read,users:read.email")
	v.Set("redirect_uri", slackRedirectURI())
	v.Set("state", state)
	return "https://slack.com/oauth/v2/authorize?" + v.Encode(), nil
}

func slackWebhookURL(agentID string) string {
	origin := strings.TrimSpace(os.Getenv("PUBLIC_API_URL"))
	if origin == "" {
		origin = "http://localhost:8080"
	}
	return strings.TrimRight(origin, "/") + "/api/webhooks/slack/" + agentID
}

func nonEmptyOr(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
}

// debugMarshal is unused in prod but keeps json.Marshal in the import
// graph for future test seams.
var _ = func() any { _, _ = json.Marshal(struct{}{}); return nil }
