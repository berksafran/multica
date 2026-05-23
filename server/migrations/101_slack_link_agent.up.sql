-- Scope the Slack thread → chat_session link to the agent. Previously the
-- link was unique on (team, channel, thread_ts), so when two agents shared
-- a channel/thread the first one to claim the thread captured every later
-- mention — including mentions of a different bot — and the outbound
-- reply went out under the wrong app's token (e.g. `not_in_channel`
-- because that bot was never invited).

ALTER TABLE slack_chat_session_link
    ADD COLUMN agent_id UUID REFERENCES agent(id) ON DELETE CASCADE;

UPDATE slack_chat_session_link l
SET agent_id = s.agent_id
FROM chat_session s
WHERE l.chat_session_id = s.id;

ALTER TABLE slack_chat_session_link
    ALTER COLUMN agent_id SET NOT NULL;

-- Drop the old (team, channel, thread) UNIQUE constraint regardless of
-- the auto-generated name Postgres assigned in migration 097.
DO $$
DECLARE
    cname TEXT;
BEGIN
    SELECT conname INTO cname
    FROM pg_constraint
    WHERE conrelid = 'slack_chat_session_link'::regclass
      AND contype = 'u'
      AND pg_get_constraintdef(oid) = 'UNIQUE (slack_team_id, slack_channel_id, slack_thread_ts)';
    IF cname IS NOT NULL THEN
        EXECUTE format('ALTER TABLE slack_chat_session_link DROP CONSTRAINT %I', cname);
    END IF;
END $$;

ALTER TABLE slack_chat_session_link
    ADD CONSTRAINT slack_chat_session_link_thread_agent_key
    UNIQUE (slack_team_id, slack_channel_id, slack_thread_ts, agent_id);

DROP INDEX IF EXISTS idx_slack_chat_session_link_thread;
CREATE INDEX idx_slack_chat_session_link_thread_agent
    ON slack_chat_session_link(slack_team_id, slack_channel_id, slack_thread_ts, agent_id);
