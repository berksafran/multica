-- Persists the user's Step 3 runtime choice across sessions so the
-- workspace-entry onboarding init is a pure function of user state.
--
-- Before this column, the "did the user pick a runtime?" signal lived
-- only in React state inside StepRuntimeConnect — exit Step 3 and the
-- choice was gone. The workspace OnboardingHelperModal had to fall back
-- to `runtimeListOptions(ws).data?.[0]` (pick the first runtime in the
-- workspace), which silently discards the user's selection if they have
-- more than one. Storing the choice here lets the modal accept it as a
-- prop and stay a dumb component.
--
-- onboarding_runtime_id and onboarding_runtime_skipped together encode
-- one of three legal states (see CHECK constraint):
--   (NULL,   false) — Step 3 not yet completed
--   (<uuid>, false) — user picked this runtime in Step 3
--   (NULL,   true)  — user explicitly skipped Step 3
-- The (<uuid>, true) combination is rejected by the constraint.
--
-- ON DELETE SET NULL: if the chosen runtime row is later deleted (daemon
-- removed, runtime evicted), the field degrades to NULL — the user
-- effectively returns to "not yet completed" rather than holding a
-- dangling reference.

ALTER TABLE "user"
    ADD COLUMN IF NOT EXISTS onboarding_runtime_id UUID NULL
        REFERENCES agent_runtime(id) ON DELETE SET NULL;

ALTER TABLE "user"
    ADD COLUMN IF NOT EXISTS onboarding_runtime_skipped BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE "user"
    ADD CONSTRAINT user_onboarding_runtime_choice_check
    CHECK (NOT (onboarding_runtime_id IS NOT NULL AND onboarding_runtime_skipped = TRUE));
