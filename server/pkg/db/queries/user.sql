-- name: GetUser :one
SELECT * FROM "user"
WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM "user"
WHERE email = $1;

-- name: CreateUser :one
INSERT INTO "user" (name, email, avatar_url)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdateUser :one
UPDATE "user" SET
    name = COALESCE($2, name),
    avatar_url = COALESCE($3, avatar_url),
    language = COALESCE($4, language),
    profile_description = COALESCE(sqlc.narg('profile_description'), profile_description),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkUserOnboarded :one
UPDATE "user" SET
    onboarded_at = COALESCE(onboarded_at, now()),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: PatchUserOnboarding :one
-- Partial update of the user's onboarding decision fields. Each field is
-- optional (sqlc.narg) so the handler can patch one slot without
-- clobbering others — but the runtime fields are NOT independent. They
-- encode a 3-state machine guarded by user_onboarding_runtime_choice_check:
--   (NULL,   false)  not yet decided
--   (uuid,   false)  picked a runtime
--   (NULL,   true)   explicitly skipped
-- (uuid, true) is rejected by the CHECK constraint, which means a naive
-- COALESCE on each field independently breaks under a switch path —
-- e.g. user picks runtime → row is (X, false) → user later sends
-- {skipped: true} → COALESCE preserves X → SQL writes (X, true) → 500.
--
-- The CASE expressions below collapse the switch atomically: any caller
-- that sets one side of the pair clears the other in the same statement.
-- Callers that omit both leave the row untouched.
UPDATE "user" SET
    onboarding_questionnaire   = COALESCE(sqlc.narg('questionnaire'), onboarding_questionnaire),
    -- Casts on every reference are required for PG to infer parameter types
    -- inside a CASE branch (otherwise: "could not determine data type of
    -- parameter $N", SQLSTATE 42P08).
    onboarding_runtime_id = CASE
        -- Caller explicitly skipped: drop any previously-chosen runtime.
        WHEN sqlc.narg('runtime_skipped')::boolean IS TRUE THEN NULL
        -- Caller picked a runtime: write it.
        WHEN sqlc.narg('runtime_id')::uuid IS NOT NULL THEN sqlc.narg('runtime_id')::uuid
        -- Caller touched neither field: preserve.
        ELSE onboarding_runtime_id
    END,
    onboarding_runtime_skipped = CASE
        -- Caller picked a runtime: skipped must become FALSE (whether it
        -- was previously TRUE from an earlier Skip, or FALSE already).
        WHEN sqlc.narg('runtime_id')::uuid IS NOT NULL THEN FALSE
        -- Caller explicitly set skipped to true/false: write it.
        WHEN sqlc.narg('runtime_skipped')::boolean IS NOT NULL THEN sqlc.narg('runtime_skipped')::boolean
        -- Caller touched neither field: preserve.
        ELSE onboarding_runtime_skipped
    END,
    updated_at = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- name: JoinCloudWaitlist :one
-- Records interest in cloud runtimes. Does NOT mark onboarding
-- complete — the user still has to pick a real path (CLI / Skip)
-- in Step 3. Repeating the call overwrites email + reason.
UPDATE "user" SET
    cloud_waitlist_email = $2,
    cloud_waitlist_reason = $3,
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetStarterContentState :one
-- Atomically transition starter_content_state. The handler is
-- responsible for checking the current value first (to decide between
-- "transition NULL -> imported and run the seeding" vs "already
-- decided, short-circuit"). Using COALESCE here would swallow the
-- transition, so this is a straight assignment.
UPDATE "user" SET
    starter_content_state = $2,
    updated_at = now()
WHERE id = $1
RETURNING *;
