ALTER TABLE "user"
    DROP CONSTRAINT IF EXISTS user_onboarding_runtime_choice_check;

ALTER TABLE "user"
    DROP COLUMN IF EXISTS onboarding_runtime_skipped;

ALTER TABLE "user"
    DROP COLUMN IF EXISTS onboarding_runtime_id;
