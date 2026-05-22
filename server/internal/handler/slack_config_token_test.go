package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/slack"
	"github.com/multica-ai/multica/server/internal/util/secret"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// newTestCipher returns an AES-GCM cipher with a deterministic key so the
// encrypted-then-decrypted round trip is exercised end-to-end. We don't
// want the test to depend on SLACK_TOKEN_ENC_KEY being set in the env.
func newTestCipher(t *testing.T) *secret.AESGCM {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	c, err := secret.NewAESGCMFromBase64(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return c
}

// newRotateServer stands up a mock Slack endpoint that responds to
// tooling.tokens.rotate with the supplied access/refresh/exp tuple.
// `calls` is incremented per request so tests can assert single-flight.
func newRotateServer(t *testing.T, token, refresh string, exp time.Time, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tooling.tokens.rotate") {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusBadRequest)
			return
		}
		if calls != nil {
			*calls++
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":            true,
			"token":         token,
			"refresh_token": refresh,
			"iat":           time.Now().Unix(),
			"exp":           exp.Unix(),
		})
	}))
}

// withRotateBaseURL points the service's ManifestClient at a test server.
// configTokenService keeps the client private; rather than expose it, we
// reach in via the package-private field — the file lives in the same
// package, so this is supported.
func withRotateBaseURL(s *configTokenService, baseURL string) {
	s.client = s.client.WithBaseURL(baseURL)
}

// TestSlackConfigToken_Bootstrap requires DB.
func TestSlackConfigToken_BootstrapStoresEncryptedTokens(t *testing.T) {
	if testHandler == nil {
		t.Skip("test DB not available")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM slack_config_token`)
	})

	exp := time.Now().Add(12 * time.Hour).UTC().Truncate(time.Second)
	srv := newRotateServer(t, "new-access", "new-refresh", exp, nil)
	t.Cleanup(srv.Close)

	svc := newConfigTokenService(testHandler.Queries, newTestCipher(t))
	withRotateBaseURL(svc, srv.URL)

	if err := svc.Bootstrap(context.Background(), "initial-access", "initial-refresh"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	row, err := testHandler.Queries.GetSlackConfigToken(context.Background())
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	// The encrypted column must NOT equal the plaintext we passed — that
	// would mean we forgot to call Encrypt before persisting.
	if row.AccessTokenEnc == "new-access" || row.RefreshTokenEnc == "new-refresh" {
		t.Fatalf("tokens persisted unencrypted")
	}
	plain, err := newTestCipher(t).Decrypt(row.AccessTokenEnc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != "new-access" {
		t.Fatalf("decrypted access = %q, want new-access", plain)
	}
	if !row.ExpiresAt.Time.Equal(exp) {
		t.Fatalf("expires_at = %v, want %v", row.ExpiresAt.Time, exp)
	}
}

// TestSlackConfigToken_CurrentRotatesNearExpiry verifies that Current()
// triggers a rotation when the existing token is inside the lead-time
// window, and serves the new token from then on.
func TestSlackConfigToken_CurrentRotatesNearExpiry(t *testing.T) {
	if testHandler == nil {
		t.Skip("test DB not available")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM slack_config_token`)
	})

	cipher := newTestCipher(t)
	// Seed the row with a token expiring inside the lead window.
	accessEnc, _ := cipher.Encrypt("stale-access")
	refreshEnc, _ := cipher.Encrypt("stale-refresh")
	if _, err := testHandler.Queries.UpsertSlackConfigToken(context.Background(), db.UpsertSlackConfigTokenParams{
		AccessTokenEnc:  accessEnc,
		RefreshTokenEnc: refreshEnc,
		ExpiresAt:       pgtype.Timestamptz{Time: time.Now().Add(5 * time.Minute), Valid: true},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	newExp := time.Now().Add(12 * time.Hour).UTC().Truncate(time.Second)
	calls := 0
	srv := newRotateServer(t, "fresh-access", "fresh-refresh", newExp, &calls)
	t.Cleanup(srv.Close)

	svc := newConfigTokenService(testHandler.Queries, cipher)
	withRotateBaseURL(svc, srv.URL)

	tok, err := svc.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if tok != "fresh-access" {
		t.Fatalf("Current returned %q, want fresh-access (post-rotation)", tok)
	}
	if calls != 1 {
		t.Fatalf("rotate calls = %d, want 1", calls)
	}

	// Second call should serve from the now-fresh DB row, no extra rotate.
	tok2, err := svc.Current(context.Background())
	if err != nil {
		t.Fatalf("Current 2: %v", err)
	}
	if tok2 != "fresh-access" {
		t.Fatalf("Current 2 returned %q", tok2)
	}
	if calls != 1 {
		t.Fatalf("rotate calls after second Current = %d, want 1", calls)
	}
}

// TestSlackConfigToken_StatusFromEnvFallback ensures the admin UI can tell
// the operator that the deployment is still on the env-var bootstrap path.
func TestSlackConfigToken_StatusFromEnvFallback(t *testing.T) {
	if testHandler == nil {
		t.Skip("test DB not available")
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM slack_config_token`)
	})

	t.Setenv("SLACK_CONFIG_TOKEN", "xoxe.xoxp-fallback")
	svc := newConfigTokenService(testHandler.Queries, newTestCipher(t))

	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Configured {
		t.Fatalf("expected Configured=true under env fallback")
	}
	if !st.FromEnvFallback {
		t.Fatalf("expected FromEnvFallback=true")
	}
	if st.ExpiresAt != nil {
		t.Fatalf("ExpiresAt should be nil for env fallback")
	}
}

// TestSlackIsTokenExpired_Matchers locks down the substring matchers so a
// future rename in the slack package surfaces here instead of silently
// breaking auto-rotation detection.
func TestSlackIsTokenExpired_Matchers(t *testing.T) {
	cases := []struct {
		err      error
		expected bool
	}{
		{errors.New("slack: token_expired"), true},
		{errors.New("invalid_auth"), true},
		{errors.New("not_authed"), true},
		{errors.New("slack: invalid_app_id"), false},
		{nil, false},
	}
	for _, tc := range cases {
		got := slack.IsTokenExpired(tc.err)
		if got != tc.expected {
			t.Errorf("IsTokenExpired(%v) = %v, want %v", tc.err, got, tc.expected)
		}
	}
}

// Silences the import — fmt is used only when a test fails with %v above.
var _ = fmt.Sprintf
