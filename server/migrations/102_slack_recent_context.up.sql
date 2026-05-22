-- Per-agent opt-in: when a Slack mention arrives, optionally fetch the
-- last N messages before the mention from Slack and prepend them to the
-- user turn so the LLM has surrounding context. Counts are separate for
-- thread mentions and top-level channel mentions because the cost and
-- usefulness profiles differ — a thread is usually self-contained
-- (small, on-topic), a channel can be hundreds of unrelated lines.
--
-- Hard upper bound (20) is a cost guard: even at 0-config, no agent
-- can be configured into a runaway-token regime. Default 0 keeps the
-- feature dormant for existing installs.

ALTER TABLE slack_agent_app
    ADD COLUMN recent_context_thread_count  INTEGER NOT NULL DEFAULT 0
        CHECK (recent_context_thread_count BETWEEN 0 AND 20),
    ADD COLUMN recent_context_channel_count INTEGER NOT NULL DEFAULT 0
        CHECK (recent_context_channel_count BETWEEN 0 AND 20);
