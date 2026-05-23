package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// signV0 produces the v0 Slack signature header value for the given
// timestamp + body + secret. Recomputed by hand in the test so any
// future refactor that diverges from Slack's documented format breaks
// here rather than passing silently.
func signV0(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + string(body)))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	const secret = "my-signing-secret"
	body := []byte(`{"type":"event_callback"}`)
	now := time.Unix(1700000000, 0)
	tsStr := strconv.FormatInt(now.Unix(), 10)
	goodSig := signV0(secret, tsStr, body)

	tests := []struct {
		name    string
		headers map[string]string
		body    []byte
		secret  string
		now     time.Time
		want    bool
	}{
		{
			name: "valid signature in window",
			headers: map[string]string{
				"X-Slack-Request-Timestamp": tsStr,
				"X-Slack-Signature":         goodSig,
			},
			body: body, secret: secret, now: now, want: true,
		},
		{
			name: "missing signing secret returns false",
			headers: map[string]string{
				"X-Slack-Request-Timestamp": tsStr,
				"X-Slack-Signature":         goodSig,
			},
			body: body, secret: "", now: now, want: false,
		},
		{
			name: "missing timestamp header returns false",
			headers: map[string]string{
				"X-Slack-Signature": goodSig,
			},
			body: body, secret: secret, now: now, want: false,
		},
		{
			name: "missing signature header returns false",
			headers: map[string]string{
				"X-Slack-Request-Timestamp": tsStr,
			},
			body: body, secret: secret, now: now, want: false,
		},
		{
			name: "tampered body fails",
			headers: map[string]string{
				"X-Slack-Request-Timestamp": tsStr,
				"X-Slack-Signature":         goodSig,
			},
			body: []byte(`{"type":"tampered"}`), secret: secret, now: now, want: false,
		},
		{
			name: "stale timestamp outside drift window rejected",
			headers: map[string]string{
				"X-Slack-Request-Timestamp": tsStr,
				"X-Slack-Signature":         goodSig,
			},
			body: body, secret: secret,
			now:  now.Add(time.Duration(MaxAcceptableTimestampDriftSeconds+1) * time.Second),
			want: false,
		},
		{
			name: "future timestamp outside drift window rejected",
			headers: map[string]string{
				"X-Slack-Request-Timestamp": tsStr,
				"X-Slack-Signature":         goodSig,
			},
			body: body, secret: secret,
			now:  now.Add(-time.Duration(MaxAcceptableTimestampDriftSeconds+1) * time.Second),
			want: false,
		},
		{
			name: "non-numeric timestamp rejected",
			headers: map[string]string{
				"X-Slack-Request-Timestamp": "notanumber",
				"X-Slack-Signature":         goodSig,
			},
			body: body, secret: secret, now: now, want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.headers {
				h.Set(k, v)
			}
			got := VerifySignature(h, tt.body, tt.secret, tt.now)
			if got != tt.want {
				t.Errorf("VerifySignature() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseEnvelope_URLVerification(t *testing.T) {
	body := []byte(`{"type":"url_verification","challenge":"abc123"}`)
	env, err := ParseEnvelope(body)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.Type != "url_verification" {
		t.Errorf("Type = %q, want url_verification", env.Type)
	}
	if env.Challenge != "abc123" {
		t.Errorf("Challenge = %q, want abc123", env.Challenge)
	}
}

func TestParseEnvelope_EventCallback(t *testing.T) {
	body := []byte(`{"type":"event_callback","team_id":"T1","event":{"type":"app_mention","user":"U1","text":"<@UBOT> hi"}}`)
	env, err := ParseEnvelope(body)
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if env.TeamID != "T1" {
		t.Errorf("TeamID = %q, want T1", env.TeamID)
	}
	ev, err := ParseInnerEvent(env.Event)
	if err != nil {
		t.Fatalf("ParseInnerEvent: %v", err)
	}
	if ev.Type != "app_mention" || ev.User != "U1" {
		t.Errorf("inner event = %+v", ev)
	}
}

func TestParseEnvelope_Errors(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"empty body", nil},
		{"empty body slice", []byte{}},
		{"missing type", []byte(`{"challenge":"x"}`)},
		{"invalid json", []byte(`{not json`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseEnvelope(tt.body); err == nil {
				t.Errorf("expected error for %s", tt.name)
			}
		})
	}
}

func TestStripBotMention(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		botUserID string
		want      string
	}{
		{"leading mention stripped", "<@UBOT> hello there", "UBOT", "hello there"},
		{"mention without trailing space", "<@UBOT>hi", "UBOT", "hi"},
		{"no mention untouched", "hi there", "UBOT", "hi there"},
		{"empty bot id no-op", "<@UBOT> hi", "", "<@UBOT> hi"},
		{"only whitespace after mention", "<@UBOT>   ", "UBOT", ""},
		{"different mention not stripped", "<@OTHER> hi", "UBOT", "<@OTHER> hi"},
		{"surrounding whitespace trimmed", "   <@UBOT> hi   ", "UBOT", "hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripBotMention(tt.text, tt.botUserID)
			if got != tt.want {
				t.Errorf("StripBotMention(%q, %q) = %q, want %q", tt.text, tt.botUserID, got, tt.want)
			}
		})
	}
}

func TestEffectiveThreadTS(t *testing.T) {
	tests := []struct {
		name string
		ev   *InnerEvent
		want string
	}{
		{"reply uses thread_ts", &InnerEvent{Timestamp: "100.001", ThreadTS: "99.000"}, "99.000"},
		{"new message uses ts", &InnerEvent{Timestamp: "100.001"}, "100.001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EffectiveThreadTS(tt.ev); got != tt.want {
				t.Errorf("EffectiveThreadTS() = %q, want %q", got, tt.want)
			}
		})
	}
}
