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
		// Debug visibility: every Slack-delivered event is logged so
		// LOG_LEVEL=debug surfaces the raw stream for troubleshooting.
		// Kept off Info/Warn because non-mention channels can deliver
		// many of these per minute.
		slog.Debug("slack: event received",
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
			slog.Debug("slack: drop (non-DM message without mention)",
				"channel", ev.Channel,
				"channel_type", ev.ChannelType,
				"thread_ts", ev.ThreadTS,
			)
			return
		}
		// DM — accept
	default:
		slog.Debug("slack: drop (event type outside whitelist)", "type", ev.Type)
		return
	}

	// Cost guard 2: ignore the bot's own messages (echo loop).
	if ev.BotID != "" {
		slog.Debug("slack: drop (event has bot_id, echo prevention)", "bot_id", ev.BotID)
		return
	}
	if app.BotUserID.Valid && ev.User == app.BotUserID.String {
		slog.Debug("slack: drop (event from own bot_user)", "user", ev.User)
		return
	}

	// Cost guard 3: drop edits/deletes/system subtypes.
	if ev.Subtype != "" {
		slog.Debug("slack: drop (subtype set)", "subtype", ev.Subtype)
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
	link, err := h.Queries.GetSlackChatSessionLinkByThread(ctx, db.GetSlackChatSessionLinkByThreadParams{
		SlackTeamID:    teamID,
		SlackChannelID: channelID,
		SlackThreadTs:  threadTS,
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

	title := fmt.Sprintf("Slack: %s", channelID)
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
	}); err != nil {
		// The session exists but the link did not persist — without
		// the link the next event will spawn a duplicate session, so
		// log loudly. Don't roll back: the user message will still
		// reach the agent for this turn.
		slog.Warn("slack: create session link failed", "err", err, "session_id", uuidToString(session.ID))
	}
	return &session, nil
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
