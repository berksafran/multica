package handler

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/slack"
)

func TestReverseHistoryMessages(t *testing.T) {
	in := []slack.HistoryMessage{
		{TS: "3", Text: "third"},
		{TS: "2", Text: "second"},
		{TS: "1", Text: "first"},
	}
	out := reverseHistoryMessages(in)
	if len(out) != 3 || out[0].TS != "1" || out[2].TS != "3" {
		t.Fatalf("expected chronological order, got %+v", out)
	}
	// Source slice must not be mutated.
	if in[0].TS != "3" {
		t.Fatalf("source slice mutated: %+v", in)
	}
}

func TestFormatSlackContextBlock(t *testing.T) {
	tests := []struct {
		name     string
		messages []slack.HistoryMessage
		thread   bool
		wantSubs []string // substrings the output must contain
		wantSkip int      // empty when expecting non-empty
	}{
		{
			name:     "empty input returns empty",
			messages: nil,
			thread:   true,
		},
		{
			name: "all subtype-filtered returns empty",
			messages: []slack.HistoryMessage{
				{Subtype: "channel_join", User: "U1", Text: "joined"},
				{Subtype: "channel_leave", User: "U2", Text: "left"},
			},
			thread: false,
		},
		{
			name: "thread label and user prefix",
			messages: []slack.HistoryMessage{
				{User: "U04CN", Text: "hey"},
				{User: "U05XY", Text: "there"},
			},
			thread:   true,
			wantSubs: []string{"Recent thread context", "<@U04CN>: hey", "<@U05XY>: there", "2 message(s)"},
		},
		{
			name: "channel label when not thread",
			messages: []slack.HistoryMessage{
				{User: "U1", Text: "one"},
			},
			thread:   false,
			wantSubs: []string{"Recent channel context", "1 message(s)"},
		},
		{
			name: "bot_message subtype kept, plain bot author labelled",
			messages: []slack.HistoryMessage{
				{Subtype: "bot_message", BotID: "B123", Text: "deploy queued"},
			},
			thread:   true,
			wantSubs: []string{"<@bot:B123>: deploy queued"},
		},
		{
			name: "messages with no text are skipped",
			messages: []slack.HistoryMessage{
				{User: "U1", Text: ""},
				{User: "U2", Text: "  "},
				{User: "U3", Text: "real"},
			},
			thread:   true,
			wantSubs: []string{"1 message(s)", "<@U3>: real"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatSlackContextBlock(tt.messages, tt.thread)
			if len(tt.wantSubs) == 0 {
				if got != "" {
					t.Fatalf("expected empty, got %q", got)
				}
				return
			}
			for _, sub := range tt.wantSubs {
				if !strings.Contains(got, sub) {
					t.Errorf("missing %q in:\n%s", sub, got)
				}
			}
		})
	}
}
