-- =====================
-- Slack Agent App
-- =====================

-- name: CreateSlackAgentApp :one
INSERT INTO slack_agent_app (
    agent_id, workspace_id, slack_app_id, signing_secret,
    manifest_version, status, connected_by_id
) VALUES (
    $1, $2, $3, $4, $5, $6, sqlc.narg('connected_by_id')
)
RETURNING *;

-- name: GetSlackAgentAppByAgentID :one
SELECT * FROM slack_agent_app
WHERE agent_id = $1;

-- name: GetSlackAgentAppByAppID :one
SELECT * FROM slack_agent_app
WHERE slack_app_id = $1;

-- name: GetSlackAgentAppByID :one
SELECT * FROM slack_agent_app
WHERE id = $1;

-- name: ListSlackAgentAppsByWorkspace :many
SELECT * FROM slack_agent_app
WHERE workspace_id = $1
ORDER BY created_at ASC;

-- name: UpdateSlackAgentAppInstall :one
-- Called after OAuth callback completes. Persists the bot token (already
-- encrypted by the caller) plus team/bot-user identifiers.
UPDATE slack_agent_app
SET slack_team_id = $2,
    bot_user_id   = $3,
    bot_token_enc = $4,
    status        = 'installed',
    updated_at    = now()
WHERE id = $1
RETURNING *;

-- name: UpdateSlackAgentAppOAuthCredentials :exec
-- Stores the per-app OAuth client_id + client_secret returned by
-- apps.manifest.create. Either field may be omitted on update (caller
-- passes the existing ciphertext to keep it). Both ciphertexts are
-- produced by the app-level AES-GCM helper.
UPDATE slack_agent_app
SET oauth_client_id_enc     = COALESCE(sqlc.narg('oauth_client_id_enc'),     oauth_client_id_enc),
    oauth_client_secret_enc = COALESCE(sqlc.narg('oauth_client_secret_enc'), oauth_client_secret_enc),
    updated_at              = now()
WHERE id = $1;

-- name: UpdateSlackAgentAppManifestVersion :exec
UPDATE slack_agent_app
SET manifest_version = $2,
    updated_at       = now()
WHERE id = $1;

-- name: MarkSlackAgentAppUninstalled :exec
UPDATE slack_agent_app
SET status        = 'uninstalled',
    bot_token_enc = NULL,
    updated_at    = now()
WHERE id = $1;

-- name: DeleteSlackAgentApp :exec
DELETE FROM slack_agent_app WHERE id = $1 AND workspace_id = $2;

-- =====================
-- Slack Chat Session Link
-- =====================

-- name: CreateSlackChatSessionLink :one
INSERT INTO slack_chat_session_link (
    chat_session_id, slack_team_id, slack_channel_id, slack_thread_ts, slack_user_id
) VALUES (
    $1, $2, $3, $4, $5
)
RETURNING *;

-- name: GetSlackChatSessionLinkByThread :one
SELECT * FROM slack_chat_session_link
WHERE slack_team_id = $1 AND slack_channel_id = $2 AND slack_thread_ts = $3;

-- name: GetSlackChatSessionLinkBySessionID :one
SELECT * FROM slack_chat_session_link
WHERE chat_session_id = $1;
