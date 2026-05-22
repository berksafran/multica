package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ManifestClient calls Slack's apps.manifest.* endpoints to create,
// update, and delete apps programmatically. It authenticates with a
// Slack "configuration token" (xoxe.xoxp-…) that rotates every 12
// hours via apps.manifest.rotate; the caller is responsible for
// supplying a fresh access token and persisting the refresh token.
type ManifestClient struct {
	httpClient  *http.Client
	baseURL     string
	accessToken string
}

func NewManifestClient(accessToken string) *ManifestClient {
	return &ManifestClient{
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		baseURL:     webAPIBase,
		accessToken: accessToken,
	}
}

func (m *ManifestClient) WithBaseURL(u string) *ManifestClient {
	m.baseURL = strings.TrimRight(u, "/")
	return m
}

// SetAccessToken swaps the bearer token used for subsequent apps.manifest.*
// calls. Long-lived clients (e.g. configTokenService's reused instance) need
// this when an admin pastes new credentials at runtime — re-newing the whole
// ManifestClient would lose the WithBaseURL override used in tests.
func (m *ManifestClient) SetAccessToken(token string) {
	m.accessToken = token
}

// ManifestCreateResponse is the subset of apps.manifest.create we keep.
// CredentialsResponse fields (client_id, client_secret, signing_secret,
// verification_token) are what we persist on slack_agent_app — bot_token
// arrives later via OAuth.
type ManifestCreateResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	AppID       string `json:"app_id"`
	Credentials struct {
		ClientID          string `json:"client_id"`
		ClientSecret      string `json:"client_secret"`
		VerificationToken string `json:"verification_token"`
		SigningSecret     string `json:"signing_secret"`
	} `json:"credentials"`
}

// Create provisions a new Slack App from the given manifest. Returned
// credentials must be stored before any OAuth install can succeed.
func (m *ManifestClient) Create(ctx context.Context, manifest Manifest) (*ManifestCreateResponse, error) {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	v := url.Values{}
	v.Set("manifest", string(manifestJSON))

	var out ManifestCreateResponse
	if err := m.doForm(ctx, "apps.manifest.create", v, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return &out, fmt.Errorf("slack: %s", out.Error)
	}
	return &out, nil
}

type ManifestUpdateResponse struct {
	OK             bool   `json:"ok"`
	Error          string `json:"error,omitempty"`
	AppID          string `json:"app_id"`
	PermissionsUpdated bool `json:"permissions_updated"`
}

// Update overwrites an app's manifest. permissions_updated=true means
// the app needs reinstall to grant any newly-added scopes — the caller
// should surface that back to the user.
func (m *ManifestClient) Update(ctx context.Context, appID string, manifest Manifest) (*ManifestUpdateResponse, error) {
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	v := url.Values{}
	v.Set("app_id", appID)
	v.Set("manifest", string(manifestJSON))

	var out ManifestUpdateResponse
	if err := m.doForm(ctx, "apps.manifest.update", v, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return &out, fmt.Errorf("slack: %s", out.Error)
	}
	return &out, nil
}

// ManifestExportResponse is the trimmed apps.manifest.export response.
// We only consume OK/Error in the handler — confirming the app still
// exists is enough to flag local rows as orphaned when this call fails.
type ManifestExportResponse struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Manifest any    `json:"manifest,omitempty"`
}

// Export fetches the current manifest for the given app id. The primary
// caller doesn't read the manifest body — it uses success / failure
// (specifically "invalid_app_id" / "app_not_found") as a liveness probe
// for the app on Slack's side. Errors with IsAppMissing(err) == true
// indicate the app has been deleted in the Slack dashboard and the
// local row should be cleaned up.
func (m *ManifestClient) Export(ctx context.Context, appID string) (*ManifestExportResponse, error) {
	v := url.Values{}
	v.Set("app_id", appID)
	var out ManifestExportResponse
	if err := m.doForm(ctx, "apps.manifest.export", v, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return &out, fmt.Errorf("slack: %s", out.Error)
	}
	return &out, nil
}

// IsAppMissing returns true when the error from a Slack API call
// indicates the target app has been removed on Slack's side. The
// Slack error code is unstable across endpoints — different ones
// return "invalid_app_id" vs "app_not_found" vs "app_deleted" — so
// the caller passes the full error string and we match substrings.
func IsAppMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"invalid_app_id", "app_not_found", "app_deleted", "not_found", "missing_app"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// IsTokenExpired returns true when a Slack error indicates the access token
// has aged out and must be rotated. The configTokenService uses this to
// distinguish "rotate and retry" from "real error, surface to caller".
//
// Slack's wire code for config tokens is consistently `token_expired`; the
// substring check also picks up the variants other endpoints return
// (`invalid_auth`, `not_authed`) in case Slack changes the wording on the
// manifest endpoints without notice.
func IsTokenExpired(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"token_expired", "invalid_auth", "not_authed"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// IsInvalidRefreshToken returns true when apps.manifest.rotate cannot use
// the supplied refresh token. This is terminal — only a fresh paste from
// the admin recovers, so the service should stop retrying and surface a
// `last_rotate_error` for the UI.
func IsInvalidRefreshToken(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"invalid_refresh_token", "expired_refresh_token", "token_revoked"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

type ManifestDeleteResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (m *ManifestClient) Delete(ctx context.Context, appID string) error {
	v := url.Values{}
	v.Set("app_id", appID)
	var out ManifestDeleteResponse
	if err := m.doForm(ctx, "apps.manifest.delete", v, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("slack: %s", out.Error)
	}
	return nil
}

// RotateResponse is the apps.manifest.rotate response: we swap our
// in-memory access token for the new one and return the new refresh
// token for the caller to persist (Slack rotates both on every call).
type RotateResponse struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
	IssuedAt     int64  `json:"iat"`
	ExpiresAt    int64  `json:"exp"`
}

// Rotate exchanges a refresh token for a fresh access token. Slack
// access tokens expire every 12h; ops should call this on startup and
// schedule a rotation a few minutes before expiry.
func (m *ManifestClient) Rotate(ctx context.Context, refreshToken string) (*RotateResponse, error) {
	v := url.Values{}
	v.Set("refresh_token", refreshToken)
	// rotate uses a different auth: the refresh token itself.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/tooling.tokens.rotate", strings.NewReader(v.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var out RotateResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode rotate: %w", err)
	}
	if !out.OK {
		return &out, fmt.Errorf("slack: %s", out.Error)
	}
	m.accessToken = out.Token
	return &out, nil
}

func (m *ManifestClient) doForm(ctx context.Context, method string, v url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/"+method, strings.NewReader(v.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+m.accessToken)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

// hush keeps bytes.Buffer in the build graph for future raw-body helpers.
var _ = bytes.Buffer{}
