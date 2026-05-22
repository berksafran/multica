ALTER TABLE slack_agent_app
    DROP COLUMN IF EXISTS recent_context_channel_count,
    DROP COLUMN IF EXISTS recent_context_thread_count;
