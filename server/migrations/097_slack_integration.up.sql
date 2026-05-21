-- Slack integration: per-agent Slack App and the link between Slack threads
-- and Multica chat sessions. Each Multica agent maps 1:1 to a Slack App so
-- that mention/avatar/scopes feel native ("hired-human" UX, MUL-RFC).

CREATE TABLE slack_agent_app (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id          UUID NOT NULL UNIQUE REFERENCES agent(id) ON DELETE CASCADE,
    workspace_id      UUID NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
    slack_app_id      TEXT NOT NULL,
    slack_team_id     TEXT,
    bot_user_id       TEXT,
    -- bot_token is AES-GCM encrypted at rest by the app layer; nullable until
    -- OAuth install completes.
    bot_token_enc     TEXT,
    signing_secret    TEXT NOT NULL,
    manifest_version  INTEGER NOT NULL DEFAULT 1,
    status            TEXT NOT NULL DEFAULT 'provisioned'
        CHECK (status IN ('provisioned', 'installed', 'uninstalled', 'error')),
    connected_by_id   UUID REFERENCES "user"(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_slack_agent_app_workspace ON slack_agent_app(workspace_id);

-- slack_chat_session_link: maps a Slack thread to a Multica chat_session so
-- thread replies route back to the same session without re-mentioning the
-- bot. Unique on (team, channel, thread_ts) so the lookup at webhook ingress
-- is a single indexed read.
CREATE TABLE slack_chat_session_link (
    chat_session_id   UUID PRIMARY KEY REFERENCES chat_session(id) ON DELETE CASCADE,
    slack_team_id     TEXT NOT NULL,
    slack_channel_id  TEXT NOT NULL,
    slack_thread_ts   TEXT NOT NULL,
    slack_user_id     TEXT NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (slack_team_id, slack_channel_id, slack_thread_ts)
);

CREATE INDEX idx_slack_chat_session_link_thread
    ON slack_chat_session_link(slack_team_id, slack_channel_id, slack_thread_ts);
