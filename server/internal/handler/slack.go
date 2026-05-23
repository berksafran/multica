package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/slack"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// maxSlackBodyBytes caps the request body we will read from Slack. Events
// API payloads are tiny in practice (a few KB); the limit is purely a
// resource guard against a misbehaving or hostile caller.
const maxSlackBodyBytes = 256 << 10

// maxSlackTextBytes truncates ultra-long message text before handing it
// to the agent. Slack itself allows ~40k chars but feeding the full
// thing through an LLM is wasteful — cap at 8 KB per cost guards.
const maxSlackTextBytes = 8 << 10

// HandleSlackWebhook (POST /api/webhooks/slack/{agent_id}) is Slack's
// destination for Events API deliveries. We verify the v0 signature
// against the per-agent signing secret stored in slack_agent_app, then
// dispatch to the cost-guarded event handler. Unrecognized event types
// are acknowledged with 200 so Slack does not mark the endpoint failing.
func (h *Handler) HandleSlackWebhook(w http.ResponseWriter, r *http.Request) {
	agentIDStr := chi.URLParam(r, "agent_id")
	agentID, ok := parseUUIDOrBadRequest(w, agentIDStr, "agent_id")
	if !ok {
		return
	}

	app, err := h.Queries.GetSlackAgentAppByAgentID(r.Context(), agentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "slack app not found for agent")
			return
		}
		writeError(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxSlackBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body failed")
		return
	}

	if !slack.VerifySignature(r.Header, body, app.SigningSecret, time.Now()) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	env, err := slack.ParseEnvelope(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid envelope")
		return
	}

	switch env.Type {
	case "url_verification":
		// Slack's initial endpoint handshake. Echo the challenge as
		// plain text per https://api.slack.com/events/url_verification.
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, env.Challenge)
		return
	case "event_callback":
		ev, err := slack.ParseInnerEvent(env.Event)
		if err != nil {
			// Malformed inner — ack and drop so Slack doesn't retry.
			w.WriteHeader(http.StatusOK)
			return
		}
		// TEMP: bumped to Warn so the active LOG_LEVEL=warn surfaces the
		// raw event stream while we debug missing inbound triggers. Revert
		// to Debug before merge — non-mention channels can deliver many
		// of these per minute.
		slog.Warn("slack: event received",
			"agent_id", uuidToString(app.AgentID),
			"type", ev.Type,
			"channel", ev.Channel,
			"channel_type", ev.ChannelType,
			"thread_ts", ev.ThreadTS,
			"user", ev.User,
			"has_bot_id", ev.BotID != "",
			"subtype", ev.Subtype,
		)
		// Run the dispatch in a background goroutine: Slack only gives
		// us 3 seconds to acknowledge before it retries the delivery,
		// and the LLM enqueue path can take longer than that under
		// load. Inherit a fresh context so request cancellation does
		// not abort the enqueue.
		go h.dispatchSlackEvent(context.Background(), app, env.TeamID, ev)
		w.WriteHeader(http.StatusOK)
		return
	default:
		// Ack and drop everything else (rate_limit, app_uninstalled
		// notifications, etc. would be future work).
		w.WriteHeader(http.StatusOK)
	}
}

// dispatchSlackEvent applies the cost-guard whitelist and turns surviving
// events into Multica chat messages. Returning early without writing
// anywhere is the intended path for filtered-out events.
func (h *Handler) dispatchSlackEvent(ctx context.Context, app db.SlackAgentApp, teamID string, ev *slack.InnerEvent) {
	// Cost guard 1: strict mention-only event whitelist.
	//   - app_mention: explicit invocation in any channel (top-level
	//     or inside a thread); Slack fires this for every @bot mention
	//   - message.im: DM = implicit invocation
	//   - anything else: drop
	// The earlier "sticky thread" mode (process thread replies without
	// re-mention) was removed at user request — it burned LLM tokens
	// on every reply in a busy thread and made cost unpredictable. Now
	// the rule is: no mention, no LLM call.
	switch ev.Type {
	case "app_mention":
		// accept (Slack only fires this on explicit @bot mention)
	case "message":
		if ev.ChannelType != "im" {
			slog.Warn("slack: drop (non-DM message without mention)",
				"channel", ev.Channel,
				"channel_type", ev.ChannelType,
				"thread_ts", ev.ThreadTS,
			)
			return
		}
		// DM — accept
	default:
		slog.Warn("slack: drop (event type outside whitelist)", "type", ev.Type)
		return
	}

	// Cost guard 2: ignore the bot's own messages (echo loop).
	if ev.BotID != "" {
		slog.Warn("slack: drop (event has bot_id, echo prevention)", "bot_id", ev.BotID)
		return
	}
	if app.BotUserID.Valid && ev.User == app.BotUserID.String {
		slog.Warn("slack: drop (event from own bot_user)", "user", ev.User)
		return
	}

	// Cost guard 3: drop edits/deletes/system subtypes.
	if ev.Subtype != "" {
		slog.Warn("slack: drop (subtype set)", "subtype", ev.Subtype)
		return
	}

	// Cost guard 4: truncate oversize text.
	text := slack.StripBotMention(ev.Text, valueOrEmpty(app.BotUserID))
	if text == "" {
		return
	}
	if len(text) > maxSlackTextBytes {
		text = text[:maxSlackTextBytes]
	}

	threadTS := slack.EffectiveThreadTS(ev)

	// Session resolve: existing thread → existing session, otherwise
	// create a new session anchored to this thread.
	session, err := h.resolveSlackSession(ctx, app, teamID, ev.Channel, threadTS, ev.User)
	if err != nil {
		slog.Warn("slack: failed to resolve session", "err", err, "agent_id", uuidToString(app.AgentID))
		return
	}
	if session == nil {
		// Channel reply that does not match a known session — drop.
		return
	}

	// Optional: prepend last N messages before the mention so the LLM
	// has surrounding context. Per-agent counts default to 0 (dormant).
	// Fetch failures are non-fatal — we still process the mention, just
	// without context.
	if ctxBlock := h.fetchSlackRecentContext(ctx, app, ev); ctxBlock != "" {
		text = ctxBlock + "\n\n" + text
	}

	msg, err := h.Queries.CreateChatMessage(ctx, db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "user",
		Content:       text,
	})
	if err != nil {
		slog.Warn("slack: create chat message failed", "err", err, "session_id", uuidToString(session.ID))
		return
	}

	task, err := h.TaskService.EnqueueChatTask(ctx, *session)
	if err != nil {
		slog.Warn("slack: enqueue chat task failed", "err", err, "session_id", uuidToString(session.ID))
		return
	}
	slog.Info("slack: enqueued chat task",
		"agent_id", uuidToString(app.AgentID),
		"session_id", uuidToString(session.ID),
		"task_id", uuidToString(task.ID),
		"channel", ev.Channel,
		"thread_ts", threadTS,
	)

	if err := h.Queries.TouchChatSession(ctx, session.ID); err != nil {
		slog.Debug("slack: touch session failed", "err", err)
	}

	resolvedSessionID := uuidToString(session.ID)
	h.publishChat(protocol.EventChatMessage,
		uuidToString(session.WorkspaceID),
		"system",
		"",
		resolvedSessionID,
		protocol.ChatMessagePayload{
			ChatSessionID: resolvedSessionID,
			MessageID:     uuidToString(msg.ID),
			Role:          "user",
			Content:       text,
			TaskID:        uuidToString(task.ID),
			CreatedAt:     timestampToString(msg.CreatedAt),
		})
}

// resolveSlackSession looks up an existing thread→session link or creates
// a new chat_session. Returns (nil, nil) when the caller should drop the
// event without erroring (e.g. reply to a thread we don't track).
func (h *Handler) resolveSlackSession(ctx context.Context, app db.SlackAgentApp, teamID, channelID, threadTS, slackUserID string) (*db.ChatSession, error) {
	// Scope the lookup to this app's agent. Two agents can coexist in
	// the same channel/thread; without this filter the first one to
	// claim the thread captures every later mention — including
	// mentions of a different bot — and the outbound reply ends up
	// being sent under the wrong app's token.
	link, err := h.Queries.GetSlackChatSessionLinkByThread(ctx, db.GetSlackChatSessionLinkByThreadParams{
		SlackTeamID:    teamID,
		SlackChannelID: channelID,
		SlackThreadTs:  threadTS,
		AgentID:        app.AgentID,
	})
	if err == nil {
		s, err := h.Queries.GetChatSession(ctx, link.ChatSessionID)
		if err != nil {
			return nil, fmt.Errorf("get linked session: %w", err)
		}
		return &s, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("link lookup: %w", err)
	}

	// No link → we may need to create a session. Only app_mention and
	// DMs are allowed to start sessions; non-anchored channel replies
	// were already dropped upstream.
	creatorID, ok := h.resolveSlackCreator(ctx, app, slackUserID)
	if !ok {
		return nil, nil
	}

	title := h.buildSlackSessionTitle(ctx, app, channelID, slackUserID)
	session, err := h.Queries.CreateChatSession(ctx, db.CreateChatSessionParams{
		WorkspaceID: app.WorkspaceID,
		AgentID:     app.AgentID,
		CreatorID:   creatorID,
		Title:       title,
	})
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	if _, err := h.Queries.CreateSlackChatSessionLink(ctx, db.CreateSlackChatSessionLinkParams{
		ChatSessionID:  session.ID,
		SlackTeamID:    teamID,
		SlackChannelID: channelID,
		SlackThreadTs:  threadTS,
		SlackUserID:    slackUserID,
		AgentID:        app.AgentID,
	}); err != nil {
		// The session exists but the link did not persist — without
		// the link the next event will spawn a duplicate session, so
		// log loudly. Don't roll back: the user message will still
		// reach the agent for this turn.
		slog.Warn("slack: create session link failed", "err", err, "session_id", uuidToString(session.ID))
	} else {
		// Best-effort: cache the thread permalink so the UI's "Reply in
		// Slack" banner has a clickable link without paying chat.getPermalink
		// on every session load. Failure is non-fatal — the column stays
		// null and the UI falls back to plain text.
		h.persistSlackThreadPermalink(ctx, app, session.ID, channelID, threadTS)
	}
	return &session, nil
}

// persistSlackThreadPermalink resolves the thread's web URL via chat.getPermalink
// and stores it on the session's link row. Non-fatal: any failure (missing token,
// API error, missing scope) leaves the permalink null and is logged at debug.
func (h *Handler) persistSlackThreadPermalink(ctx context.Context, app db.SlackAgentApp, sessionID pgtype.UUID, channelID, threadTS string) {
	botToken, err := h.decryptSlackBotToken(app)
	if err != nil || botToken == "" {
		return
	}
	client := slack.NewClient(botToken)
	resp, err := client.GetPermalink(ctx, channelID, threadTS)
	if err != nil || resp == nil || resp.Permalink == "" {
		slog.Debug("slack: get permalink failed", "err", err, "session_id", uuidToString(sessionID))
		return
	}
	if err := h.Queries.UpdateSlackChatSessionLinkPermalink(ctx, db.UpdateSlackChatSessionLinkPermalinkParams{
		ChatSessionID: sessionID,
		Permalink:     pgtype.Text{String: resp.Permalink, Valid: true},
	}); err != nil {
		slog.Debug("slack: persist permalink failed", "err", err, "session_id", uuidToString(sessionID))
	}
}

// buildSlackSessionTitle produces the human-readable label shown in the
// session picker. We try Slack's conversations.info + users.info so the
// dropdown reads "#channel · Alice" instead of an opaque "C04CD6QRMQE";
// on any API failure (missing scope, network error, deleted user) we
// fall back to whatever piece we still have, then to the raw IDs. The
// title is purely cosmetic, so failure must never block session creation.
func (h *Handler) buildSlackSessionTitle(ctx context.Context, app db.SlackAgentApp, channelID, slackUserID string) string {
	botToken, err := h.decryptSlackBotToken(app)
	if err != nil || botToken == "" {
		return fmt.Sprintf("Slack: %s", channelID)
	}
	client := slack.NewClient(botToken)

	channelLabel := channelID
	isDM := false
	if info, err := client.ConversationsInfo(ctx, channelID); err == nil {
		if info.Channel.IsIM {
			isDM = true
		} else if info.Channel.Name != "" {
			channelLabel = "#" + info.Channel.Name
		}
	}

	userLabel := ""
	if slackUserID != "" {
		if info, err := client.UsersInfo(ctx, slackUserID); err == nil {
			switch {
			case info.User.Profile.DisplayName != "":
				userLabel = info.User.Profile.DisplayName
			case info.User.Profile.RealName != "":
				userLabel = info.User.Profile.RealName
			case info.User.Name != "":
				userLabel = info.User.Name
			}
		}
	}

	switch {
	case isDM && userLabel != "":
		return fmt.Sprintf("Slack DM · %s", userLabel)
	case isDM:
		return "Slack DM"
	case userLabel != "":
		return fmt.Sprintf("%s · %s", channelLabel, userLabel)
	default:
		return channelLabel
	}
}

// resolveSlackCreator finds a Multica user to attribute the Slack message
// to. Tries: users.info email lookup → connected_by_id fallback. Returns
// ok=false when neither resolves (the event should be dropped silently
// rather than attributed to a wrong user).
func (h *Handler) resolveSlackCreator(ctx context.Context, app db.SlackAgentApp, slackUserID string) (pgtype.UUID, bool) {
	botToken, err := h.decryptSlackBotToken(app)
	if err == nil && botToken != "" && slackUserID != "" {
		client := slack.NewClient(botToken)
		if info, err := client.UsersInfo(ctx, slackUserID); err == nil {
			email := strings.TrimSpace(info.User.Profile.Email)
			if email != "" {
				if u, err := h.Queries.GetUserByEmail(ctx, email); err == nil {
					return u.ID, true
				}
			}
		}
	}
	if app.ConnectedByID.Valid {
		return app.ConnectedByID, true
	}
	slog.Info("slack: no creator could be resolved, dropping event",
		"agent_id", uuidToString(app.AgentID),
		"slack_user_id", slackUserID,
	)
	return pgtype.UUID{}, false
}

// decryptSlackBotToken returns the plaintext bot token for an app row.
// Returns ErrSlackTokenUnavailable if the row has no token or the cipher
// env var is missing — callers should treat that as "Slack call cannot
// proceed", never as "send unauthenticated request".
func (h *Handler) decryptSlackBotToken(app db.SlackAgentApp) (string, error) {
	if !app.BotTokenEnc.Valid || app.BotTokenEnc.String == "" {
		return "", ErrSlackTokenUnavailable
	}
	aead, err := slackTokenCipher()
	if err != nil {
		return "", err
	}
	pt, err := aead.Decrypt(app.BotTokenEnc.String)
	if err != nil {
		return "", err
	}
	return pt, nil
}

// ErrSlackTokenUnavailable is returned when the bot token cannot be read
// (missing row, missing cipher key, decryption failure).
var ErrSlackTokenUnavailable = errors.New("slack: bot token unavailable")

// ── Slack helpers shared with slack_connect.go / slack_outbound.go ──────

func valueOrEmpty(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

// fetchSlackRecentContext optionally pulls the last N messages before
// the mention from Slack and renders them as a block we prepend to the
// user turn. Two independent counts on slack_agent_app gate this:
// recent_context_thread_count for in-thread mentions and
// recent_context_channel_count for top-level channel mentions. DMs are
// skipped because the session's own chat_message history already gives
// the assistant continuity.
//
// Every failure path returns "" so the mention still gets dispatched;
// missing context is a soft degradation, not a hard error.
func (h *Handler) fetchSlackRecentContext(ctx context.Context, app db.SlackAgentApp, ev *slack.InnerEvent) string {
	isThread := ev.ThreadTS != "" && ev.ThreadTS != ev.Timestamp
	var limit int32
	if isThread {
		limit = app.RecentContextThreadCount
	} else {
		// app_mention without thread_ts and DMs both land here; we
		// only want channel-history fetch for actual channel mentions.
		if ev.ChannelType == "im" {
			return ""
		}
		limit = app.RecentContextChannelCount
	}
	if limit <= 0 {
		return ""
	}

	botToken, err := h.decryptSlackBotToken(app)
	if err != nil || botToken == "" {
		slog.Debug("slack: skip context fetch, bot token unavailable", "err", err)
		return ""
	}
	client := slack.NewClient(botToken)

	var messages []slack.HistoryMessage
	if isThread {
		resp, err := client.ConversationsReplies(ctx, ev.Channel, ev.ThreadTS, ev.Timestamp, int(limit))
		if err != nil {
			slog.Warn("slack: conversations.replies failed", "err", err, "channel", ev.Channel, "thread_ts", ev.ThreadTS)
			return ""
		}
		messages = resp.Messages
	} else {
		resp, err := client.ConversationsHistory(ctx, ev.Channel, ev.Timestamp, int(limit))
		if err != nil {
			slog.Warn("slack: conversations.history failed", "err", err, "channel", ev.Channel)
			return ""
		}
		// history returns newest-first; flip to chronological so the
		// LLM reads the conversation forwards.
		messages = reverseHistoryMessages(resp.Messages)
	}

	return formatSlackContextBlock(messages, isThread)
}

// reverseHistoryMessages returns a chronological (oldest-first) copy
// so the formatted block reads in natural order regardless of which
// Slack endpoint produced it.
func reverseHistoryMessages(in []slack.HistoryMessage) []slack.HistoryMessage {
	out := make([]slack.HistoryMessage, len(in))
	for i, m := range in {
		out[len(in)-1-i] = m
	}
	return out
}

// formatSlackContextBlock renders the messages as a labelled block
// the LLM can clearly separate from the user's actual instruction.
// Messages with no text (file-only uploads, system joins, etc.) are
// skipped so we don't pad the prompt with empty `<@U…>:` lines.
func formatSlackContextBlock(messages []slack.HistoryMessage, isThread bool) string {
	var lines []string
	for _, m := range messages {
		if m.Subtype != "" && m.Subtype != "bot_message" && m.Subtype != "thread_broadcast" {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		author := m.User
		if author == "" && m.BotID != "" {
			author = "bot:" + m.BotID
		}
		if author == "" {
			author = "unknown"
		}
		lines = append(lines, fmt.Sprintf("<@%s>: %s", author, text))
	}
	if len(lines) == 0 {
		return ""
	}
	label := "Recent channel context"
	if isThread {
		label = "Recent thread context"
	}
	return fmt.Sprintf("[%s — %d message(s) before this mention]\n%s",
		label, len(lines), strings.Join(lines, "\n"))
}

func slackConfigured() bool {
	return strings.TrimSpace(os.Getenv("SLACK_CONFIG_TOKEN")) != "" &&
		strings.TrimSpace(os.Getenv("SLACK_TOKEN_ENC_KEY")) != ""
}

// slackRedirectURI returns the URL Slack will redirect to after OAuth
// install. Built from FRONTEND_ORIGIN to keep the dev/prod split aligned
// with the rest of the integration handshakes.
func slackRedirectURI() string {
	explicit := strings.TrimSpace(os.Getenv("SLACK_REDIRECT_URI"))
	if explicit != "" {
		return explicit
	}
	origin := strings.TrimSpace(os.Getenv("PUBLIC_API_URL"))
	if origin == "" {
		origin = "http://localhost:8080"
	}
	return strings.TrimRight(origin, "/") + "/api/slack/oauth/callback"
}
