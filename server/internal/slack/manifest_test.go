package slack

import (
	"encoding/json"
	"testing"
)

func TestBuildAgentManifest_CostGuardedScope(t *testing.T) {
	m := BuildAgentManifest(AgentManifestInput{
		DisplayName: "Ada",
		Description: "test agent",
		WebhookURL:  "https://example.test/api/webhooks/slack/abc",
		RedirectURL: "https://example.test/api/slack/oauth/callback",
	})

	if m.DisplayInformation.Name != "Ada" {
		t.Errorf("display name = %q, want Ada", m.DisplayInformation.Name)
	}
	if m.Features.BotUser.DisplayName != "Ada" {
		t.Errorf("bot display name = %q, want Ada", m.Features.BotUser.DisplayName)
	}
	if !m.Features.BotUser.AlwaysOnline {
		t.Errorf("always_online = false, want true")
	}

	// The cost-guard whitelist is the single most important property of
	// the manifest: subscribing to broader event types would silently
	// burn LLM tokens. Lock the exact list.
	wantEvents := map[string]bool{
		"app_mention":      true,
		"message.im":       true,
		"message.channels": true,
	}
	if len(m.Settings.EventSubscriptions.BotEvents) != len(wantEvents) {
		t.Errorf("bot_events count = %d, want %d (%v)",
			len(m.Settings.EventSubscriptions.BotEvents), len(wantEvents),
			m.Settings.EventSubscriptions.BotEvents)
	}
	for _, e := range m.Settings.EventSubscriptions.BotEvents {
		if !wantEvents[e] {
			t.Errorf("unexpected event subscription %q (cost guard violation)", e)
		}
		delete(wantEvents, e)
	}
	for missing := range wantEvents {
		t.Errorf("missing required event subscription %q", missing)
	}

	if m.Settings.EventSubscriptions.RequestURL != "https://example.test/api/webhooks/slack/abc" {
		t.Errorf("request_url = %q", m.Settings.EventSubscriptions.RequestURL)
	}
	if len(m.OAuthConfig.RedirectURLs) != 1 || m.OAuthConfig.RedirectURLs[0] != "https://example.test/api/slack/oauth/callback" {
		t.Errorf("redirect_urls = %v", m.OAuthConfig.RedirectURLs)
	}

	// Verify required bot scopes are present. Adding scopes here is
	// safe; dropping one breaks ingress (missing app_mentions:read) or
	// outbound (missing chat:write) so we lock them in.
	requiredScopes := []string{
		"app_mentions:read",
		"chat:write",
		"im:history",
		"users:read",
		"users:read.email",
	}
	have := map[string]bool{}
	for _, s := range m.OAuthConfig.Scopes.Bot {
		have[s] = true
	}
	for _, want := range requiredScopes {
		if !have[want] {
			t.Errorf("missing required bot scope %q", want)
		}
	}

	if m.Settings.Interactivity.IsEnabled {
		t.Errorf("interactivity should be disabled by default (we don't ship buttons yet)")
	}
	if m.Settings.SocketModeEnabled {
		t.Errorf("socket mode should be disabled — we use HTTP events")
	}
}

func TestBuildAgentManifest_RoundTripsJSON(t *testing.T) {
	// The manifest is sent to Slack as a JSON string parameter. A
	// silent serialization failure would be impossible to debug —
	// confirm the struct produces valid JSON in CI.
	m := BuildAgentManifest(AgentManifestInput{
		DisplayName: `Quote " in name`,
		Description: "agent for tests",
		WebhookURL:  "https://example.test/h",
		RedirectURL: "https://example.test/c",
	})
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	var roundtrip map[string]any
	if err := json.Unmarshal(b, &roundtrip); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	display, _ := roundtrip["display_information"].(map[string]any)
	if display == nil || display["name"] != `Quote " in name` {
		t.Errorf("display.name not preserved through json round-trip: %v", display)
	}
}
