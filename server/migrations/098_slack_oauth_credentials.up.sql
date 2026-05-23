-- Per-app OAuth credentials. apps.manifest.create returns a fresh
-- client_id + client_secret for every Slack App we provision; storing them
-- on the row makes the OAuth install flow self-contained instead of
-- requiring an env-var dance after every provision. Both columns are
-- AES-GCM encrypted at rest with SLACK_TOKEN_ENC_KEY (same key that
-- protects bot_token_enc).
--
-- Nullable on purpose: a row created before this migration ran (e.g.
-- the MVP shortcut row from #1) reads the credentials from env vars
-- until the user re-provisions or pastes them via the new edit UI.
ALTER TABLE slack_agent_app
    ADD COLUMN oauth_client_id_enc      TEXT,
    ADD COLUMN oauth_client_secret_enc  TEXT;
