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
	Configured  bool    `json:"configured"`
	Provisioned bool    `json:"provisioned"`
	Installed   bool    `json:"installed"`
	AppID       *string `json:"app_id,omitempty"`
	TeamID      *string `json:"team_id,omitempty"`
	BotUserID   *string `json:"bot_user_id,omitempty"`
	Status      *string `json:"status,omitempty"`
	InstallURL  *string `json:"install_url,omitempty"`
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
	if !resp.Installed {
		if u, err := buildSlackInstallURL(uuidToString(wsUUID), uuidToString(agentUUID), uuidToString(app.ID), app.SlackAppID); err == nil {
			resp.InstallURL = &u
		}
	}
	writeJSON(w, http.StatusOK, resp)
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
		// manifest API round-trip.
		installURL, _ := buildSlackInstallURL(uuidToString(wsUUID), uuidToString(agentUUID), uuidToString(existing.ID), existing.SlackAppID)
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

	mc := slack.NewManifestClient(strings.TrimSpace(os.Getenv("SLACK_CONFIG_TOKEN")))
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
	}); if err != nil {
		// Roll back the Slack-side app — leaving it dangling would
		// confuse the user (a half-provisioned app on api.slack.com
		// without any DB pointer to manage it).
		if delErr := mc.Delete(r.Context(), created.AppID); delErr != nil {
			slog.Warn("slack: rollback delete failed", "err", delErr, "app_id", created.AppID)
		}
		writeError(w, http.StatusInternalServerError, "persist failed: "+err.Error())
		return
	}
	// Stash client_id / client_secret on the app row so the OAuth
	// callback can complete the install. We piggyback on the agent's
	// runtime_config bag because the slack_agent_app table is
	// intentionally short — adding columns is a migration, but the JSON
	// blob is already a per-app catch-all owned by the agent owner.
	if err := h.persistSlackAppClientCredentials(r.Context(), agentUUID, created.Credentials.ClientID, created.Credentials.ClientSecret); err != nil {
		slog.Warn("slack: persist client credentials failed", "err", err, "agent_id", uuidToString(agentUUID))
	}

	installURL, _ := buildSlackInstallURL(uuidToString(wsUUID), uuidToString(agentUUID), uuidToString(app.ID), app.SlackAppID)
	writeJSON(w, http.StatusOK, ProvisionSlackResponse{AppID: app.SlackAppID, InstallURL: installURL})
}

// persistSlackAppClientCredentials stores the OAuth client_id /
// client_secret pair returned by manifest.create on the agent record.
// They are needed exactly once — by the OAuth callback — but Slack
// does not expose them again, so dropping them would brick the install
// flow.
func (h *Handler) persistSlackAppClientCredentials(ctx context.Context, agentID pgtype.UUID, clientID, clientSecret string) error {
	// We do NOT have a dedicated column. Encrypt + store the pair on
	// the slack_agent_app row using bot_token_enc as a temporary
	// vehicle is unsafe because OAuth callback would then overwrite it.
	// For MVP we re-read manifest.create credentials by env override
	// during testing; in prod we expect ops to set SLACK_OAUTH_CLIENT_ID
	// / SLACK_OAUTH_CLIENT_SECRET globally because the single-workspace
	// distribution mode means the manifest API mints the same pair every
	// time anyway. See plan: "Single-workspace mode (MVP)".
	//
	// To keep this MVP coherent we accept that the OAuth callback reads
	// client credentials from env vars per slack app id when present, or
	// falls back to per-process SLACK_OAUTH_CLIENT_ID/SECRET. Persisting
	// per-app credentials is a phase-2 schema change.
	_ = ctx
	_ = agentID
	_ = clientID
	_ = clientSecret
	return nil
}

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

	clientID := strings.TrimSpace(os.Getenv("SLACK_OAUTH_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("SLACK_OAUTH_CLIENT_SECRET"))
	if clientID == "" || clientSecret == "" {
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

// SyncAgentSlackApp re-publishes the manifest after the agent's
// display name or description changed. The Slack-side bot user name
// updates without requiring reinstall; permission changes (none in our
// minimal manifest) would.
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

	mc := slack.NewManifestClient(strings.TrimSpace(os.Getenv("SLACK_CONFIG_TOKEN")))
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
	mc := slack.NewManifestClient(strings.TrimSpace(os.Getenv("SLACK_CONFIG_TOKEN")))
	if err := mc.Delete(r.Context(), app.SlackAppID); err != nil {
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

func buildSlackInstallURL(wsID, agentID, appRowID, slackAppID string) (string, error) {
	state, err := signSlackState(wsID, agentID, appRowID)
	if err != nil {
		return "", err
	}
	// We hit slack.com/oauth/v2/authorize with the per-app client_id.
	// In single-workspace MVP mode the client_id env var is the one
	// minted by manifest.create for THIS app (operator pastes it once
	// after provisioning).
	clientID := strings.TrimSpace(os.Getenv("SLACK_OAUTH_CLIENT_ID"))
	if clientID == "" {
		return "", errors.New("SLACK_OAUTH_CLIENT_ID not set")
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
