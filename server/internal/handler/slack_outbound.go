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
// event bus so assistant chat replies from Slack-originated sessions
// get posted back into the Slack thread. Called once from server
// startup (alongside other bus subscriptions).
//
// We subscribe to EventChatDone (chat:done) because TaskService
// publishes that exact event when the daemon completes a chat task —
// EventChatMessage is only fired for the user-side message in
// chat.go, so subscribing to it would never see the assistant turn.
// Each handler invocation runs in a goroutine so the Bus publish
// path is never blocked by an outbound HTTP call to Slack.
func (h *Handler) RegisterSlackOutboundListeners() {
	if h.Bus == nil {
		return
	}
	h.Bus.Subscribe(protocol.EventChatDone, func(ev events.Event) {
		go h.handleChatDoneEventForSlack(context.Background(), ev)
	})
}

// handleChatDoneEventForSlack runs out-of-band for each chat:done
// event. We only act on events whose session has a Slack thread link;
// everything else short-circuits early.
func (h *Handler) handleChatDoneEventForSlack(ctx context.Context, ev events.Event) {
	payloadBytes, err := json.Marshal(ev.Payload)
	if err != nil {
		return
	}
	var payload protocol.ChatDonePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return
	}
	// Debug-level: every chat:done in the deployment fires this,
	// most are non-Slack sessions and would spam Warn.
	slog.Debug("slack outbound: chat:done observed",
		"session_id", payload.ChatSessionID,
		"task_id", payload.TaskID,
		"message_id", payload.MessageID,
		"content_len", len(payload.Content),
	)
	if payload.Content == "" {
		// chat:done fires even when the daemon completes without an
		// assistant message (e.g. tool-only turn). Nothing to post.
		slog.Debug("slack outbound: drop (chat:done has no content)", "session_id", payload.ChatSessionID)
		return
	}

	sessionUUID, err := parseStrictUUID(payload.ChatSessionID)
	if err != nil {
		slog.Warn("slack outbound: drop (bad session uuid)", "session_id", payload.ChatSessionID, "err", err)
		return
	}
	link, err := h.Queries.GetSlackChatSessionLinkBySessionID(ctx, sessionUUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Common, expected: not a Slack-originated session.
			return
		}
		slog.Warn("slack outbound: link lookup failed", "err", err, "session_id", payload.ChatSessionID)
		return
	}

	session, err := h.Queries.GetChatSession(ctx, sessionUUID)
	if err != nil {
		slog.Warn("slack outbound: get chat session failed", "err", err, "session_id", payload.ChatSessionID)
		return
	}
	app, err := h.Queries.GetSlackAgentAppByAgentID(ctx, session.AgentID)
	if err != nil {
		slog.Warn("slack outbound: get app failed", "err", err, "agent_id", uuidToString(session.AgentID))
		return
	}
	if app.Status != "installed" {
		slog.Warn("slack outbound: drop (app not installed)", "status", app.Status, "agent_id", uuidToString(session.AgentID))
		return
	}
	botToken, err := h.decryptSlackBotToken(app)
	if err != nil {
		slog.Warn("slack outbound: bot token unavailable", "err", err, "agent_id", uuidToString(session.AgentID))
		return
	}

	// Translate standard Markdown (LLM output) to Slack's mrkdwn so
	// **bold**, [text](url), and # headers actually render instead of
	// leaking literal asterisks/brackets into the thread.
	text := slack.ConvertMarkdownToSlack(payload.Content)

	client := slack.NewClient(botToken)
	resp, err := client.PostMessage(ctx, slack.PostMessageRequest{
		Channel:  link.SlackChannelID,
		Text:     text,
		ThreadTS: link.SlackThreadTs,
	})
	if err != nil {
		slog.Warn("slack outbound: post message failed",
			"err", err,
			"channel", link.SlackChannelID,
			"thread_ts", link.SlackThreadTs,
			"agent_id", uuidToString(session.AgentID),
		)
		return
	}
	slog.Info("slack outbound: posted reply",
		"agent_id", uuidToString(session.AgentID),
		"channel", link.SlackChannelID,
		"thread_ts", link.SlackThreadTs,
		"slack_message_ts", resp.TS,
	)
}
