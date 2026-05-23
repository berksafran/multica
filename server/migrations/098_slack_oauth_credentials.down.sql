ALTER TABLE slack_agent_app
    DROP COLUMN IF EXISTS oauth_client_secret_enc,
    DROP COLUMN IF EXISTS oauth_client_id_enc;
