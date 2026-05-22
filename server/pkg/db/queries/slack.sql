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

-- name: ListSlackChatSessionLinksBySessionIDs :many
SELECT * FROM slack_chat_session_link
WHERE chat_session_id = ANY($1::uuid[]);

-- name: UpdateSlackChatSessionLinkPermalink :exec
UPDATE slack_chat_session_link
SET permalink = $2
WHERE chat_session_id = $1;

-- =====================
-- Slack Config Token (singleton)
-- =====================

-- name: GetSlackConfigToken :one
-- Returns the singleton config-token row, or pgx.ErrNoRows when the deployment
-- has not been bootstrapped yet. Callers (configTokenService.Current) treat
-- ErrNoRows as "fall back to env var".
SELECT id, access_token_enc, refresh_token_enc, expires_at, last_rotated_at,
       last_rotate_error, created_at, updated_at
FROM slack_config_token
WHERE id = 1;

-- name: UpsertSlackConfigToken :one
-- Idempotent write used by both bootstrap and rotate. The CHECK on id keeps
-- this row a singleton; ON CONFLICT keeps the upsert atomic so a concurrent
-- bootstrap + rotate cannot insert a second row.
INSERT INTO slack_config_token (id, access_token_enc, refresh_token_enc, expires_at,
                                last_rotated_at, last_rotate_error, updated_at)
VALUES (1, $1, $2, $3, now(), NULL, now())
ON CONFLICT (id) DO UPDATE
SET access_token_enc  = EXCLUDED.access_token_enc,
    refresh_token_enc = EXCLUDED.refresh_token_enc,
    expires_at        = EXCLUDED.expires_at,
    last_rotated_at   = now(),
    last_rotate_error = NULL,
    updated_at        = now()
RETURNING id, access_token_enc, refresh_token_enc, expires_at, last_rotated_at,
          last_rotate_error, created_at, updated_at;

-- name: SetSlackConfigTokenRotateError :exec
-- Records the latest rotation failure without touching the token columns —
-- the previous (still-valid-until-expiry) token must stay usable until the
-- admin re-pastes credentials.
UPDATE slack_config_token
SET last_rotate_error = $1,
    updated_at        = now()
WHERE id = 1;
