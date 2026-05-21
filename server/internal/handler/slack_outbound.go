package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/slack"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// RegisterSlackOutboundListeners wires the handler into the in-process
// event bus so assistant chat replies from Slack-originated sessions get
// posted back into the Slack thread. Called once from server startup
// (alongside other bus subscriptions). Each handler invocation runs in a
// goroutine so the Bus publish path is never blocked by an outbound
// HTTP call to Slack.
func (h *Handler) RegisterSlackOutboundListeners() {
	if h.Bus == nil {
		return
	}
	h.Bus.Subscribe(protocol.EventChatMessage, func(ev events.Event) {
		go h.handleChatMessageEventForSlack(context.Background(), ev)
	})
}

// handleChatMessageEventForSlack runs out-of-band for each chat:message
// event. We only act on assistant messages whose session has a Slack
// thread link; everything else short-circuits early.
func (h *Handler) handleChatMessageEventForSlack(ctx context.Context, ev events.Event) {
	payloadBytes, err := json.Marshal(ev.Payload)
	if err != nil {
		return
	}
	var payload protocol.ChatMessagePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return
	}
	if payload.Role != "assistant" {
		return
	}
	if payload.Content == "" {
		return
	}

	sessionUUID, err := parseStrictUUID(payload.ChatSessionID)
	if err != nil {
		return
	}
	link, err := h.Queries.GetSlackChatSessionLinkBySessionID(ctx, sessionUUID)
	if err != nil {
		// pgx.ErrNoRows is the common, expected case (non-Slack
		// session). Anything else is a real DB problem worth a debug
		// line but not a noisy warn.
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Debug("slack outbound: link lookup failed", "err", err)
		}
		return
	}

	session, err := h.Queries.GetChatSession(ctx, sessionUUID)
	if err != nil {
		return
	}
	app, err := h.Queries.GetSlackAgentAppByAgentID(ctx, session.AgentID)
	if err != nil {
		return
	}
	if app.Status != "installed" {
		return
	}
	botToken, err := h.decryptSlackBotToken(app)
	if err != nil {
		slog.Warn("slack outbound: bot token unavailable", "err", err, "agent_id", uuidToString(session.AgentID))
		return
	}

	client := slack.NewClient(botToken)
	if _, err := client.PostMessage(ctx, slack.PostMessageRequest{
		Channel:  link.SlackChannelID,
		Text:     payload.Content,
		ThreadTS: link.SlackThreadTs,
	}); err != nil {
		slog.Warn("slack outbound: post message failed",
			"err", err,
			"channel", link.SlackChannelID,
			"thread_ts", link.SlackThreadTs,
		)
	}
}
