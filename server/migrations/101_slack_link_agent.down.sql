-- Rollback note: if any thread has rows for more than one agent (the
-- whole reason we added agent_id), the old single-agent UNIQUE will
-- refuse to re-create. That is intentional — collapsing those rows is
-- a destructive choice the operator has to make manually.

DROP INDEX IF EXISTS idx_slack_chat_session_link_thread_agent;

ALTER TABLE slack_chat_session_link
    DROP CONSTRAINT IF EXISTS slack_chat_session_link_thread_agent_key;

ALTER TABLE slack_chat_session_link
    DROP COLUMN IF EXISTS agent_id;

ALTER TABLE slack_chat_session_link
    ADD CONSTRAINT slack_chat_session_link_slack_team_id_slack_channel_id_slack_key
    UNIQUE (slack_team_id, slack_channel_id, slack_thread_ts);

CREATE INDEX idx_slack_chat_session_link_thread
    ON slack_chat_session_link(slack_team_id, slack_channel_id, slack_thread_ts);
