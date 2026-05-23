// Package slack contains platform-agnostic helpers for talking to Slack:
// signature verification for incoming events, Web API client for outbound
// chat.postMessage / users.info, and the Manifest API client used to
// provision per-agent apps. No chi/DB imports — handler-level glue lives
// in server/internal/handler/slack*.go.
package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxAcceptableTimestampDriftSeconds is Slack's documented anti-replay
// window. Events older than this (or in the future, e.g. clock skew) are
// rejected even when the signature is valid.
const MaxAcceptableTimestampDriftSeconds = 60 * 5

// VerifySignature implements Slack's v0 signing scheme:
//
//	signature = "v0=" + HMAC_SHA256(secret, "v0:" + timestamp + ":" + body)
//
// header values come from X-Slack-Request-Timestamp and X-Slack-Signature.
// Returns false on any mismatch, malformed input, or stale timestamp — the
// caller writes 401 with no further detail.
func VerifySignature(h http.Header, body []byte, signingSecret string, now time.Time) bool {
	if signingSecret == "" {
		return false
	}
	tsStr := h.Get("X-Slack-Request-Timestamp")
	sig := h.Get("X-Slack-Signature")
	if tsStr == "" || sig == "" {
		return false
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return false
	}
	drift := now.Unix() - ts
	if drift < 0 {
		drift = -drift
	}
	if drift > MaxAcceptableTimestampDriftSeconds {
		return false
	}

	mac := hmac.New(sha256.New, []byte(signingSecret))
	mac.Write([]byte("v0:"))
	mac.Write([]byte(tsStr))
	mac.Write([]byte(":"))
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

// EventEnvelope is the outer wrapper Slack sends to the events URL. The
// Type discriminates url_verification (initial handshake) from
// event_callback (the only one we process).
type EventEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge,omitempty"`
	TeamID    string          `json:"team_id,omitempty"`
	APIAppID  string          `json:"api_app_id,omitempty"`
	Event     json.RawMessage `json:"event,omitempty"`
	EventID   string          `json:"event_id,omitempty"`
	EventTime int64           `json:"event_time,omitempty"`
}

// InnerEvent is the parsed `event` field inside an event_callback. Only
// the fields used by our cost-guarded dispatcher are unmarshalled.
type InnerEvent struct {
	Type        string `json:"type"`
	User        string `json:"user"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	Text        string `json:"text"`
	Timestamp   string `json:"ts"`
	ThreadTS    string `json:"thread_ts"`
	BotID       string `json:"bot_id"`
	Subtype     string `json:"subtype"`
}

// ParseEnvelope decodes the outer JSON. Returns a typed error on malformed
// input so the caller can return 400 without leaking parse details.
func ParseEnvelope(body []byte) (*EventEnvelope, error) {
	if len(body) == 0 {
		return nil, errors.New("empty body")
	}
	var env EventEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if env.Type == "" {
		return nil, errors.New("missing type")
	}
	return &env, nil
}

// ParseInnerEvent decodes envelope.Event into an InnerEvent.
func ParseInnerEvent(raw json.RawMessage) (*InnerEvent, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty inner event")
	}
	var ev InnerEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil, fmt.Errorf("decode inner: %w", err)
	}
	return &ev, nil
}

// EffectiveThreadTS returns the conversation-thread anchor for an event:
// the explicit thread_ts when this is a reply, else ts itself (which then
// becomes the anchor for the new thread we will create on first reply).
func EffectiveThreadTS(ev *InnerEvent) string {
	if ev.ThreadTS != "" {
		return ev.ThreadTS
	}
	return ev.Timestamp
}

// StripBotMention removes a leading `<@U...>` mention so the text we hand
// to the agent does not include the bot user's literal ID.
func StripBotMention(text, botUserID string) string {
	if botUserID == "" {
		return strings.TrimSpace(text)
	}
	prefix := "<@" + botUserID + ">"
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, prefix) {
		t = strings.TrimSpace(t[len(prefix):])
	}
	return t
}
