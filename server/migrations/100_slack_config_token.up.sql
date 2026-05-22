-- Slack app-config token storage. The config token (xoxe.xoxp-…) is what
-- Multica uses to call apps.manifest.* — it is SEPARATE from the per-agent
-- bot_token in slack_agent_app. Slack enforces a 12-hour TTL on this token
-- and ships a refresh_token alongside it; the caller must rotate via
-- apps.manifest.rotate before expiry or every Slack-app provisioning /
-- verify call starts failing with token_expired.
--
-- Single-row by design (CHECK (id = 1)). One self-hosted deployment uses
-- one Slack workspace's config token; multi-tenant support can come later
-- by widening this table with a workspace_id FK without changing callers
-- (they already go through configTokenService, not the env var directly).
--
-- Both tokens are stored AES-GCM encrypted via SLACK_TOKEN_ENC_KEY — same
-- cipher we use for bot_token_enc, so a single key rotation flips all
-- Slack-related secrets at once.
CREATE TABLE slack_config_token (
    id                  SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    access_token_enc    TEXT NOT NULL,
    refresh_token_enc   TEXT NOT NULL,
    -- expires_at is the wall-clock instant when the current access_token
    -- becomes invalid. The scheduler rotates ~1 hour before this to keep
    -- a safety buffer against clock skew + transient Slack errors.
    expires_at          TIMESTAMPTZ NOT NULL,
    last_rotated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- last_rotate_error captures the most recent rotation failure (e.g.
    -- "invalid_refresh_token") so the admin UI can surface "Rotation
    -- broken — please re-paste tokens" instead of a silent 401 storm.
    last_rotate_error   TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
