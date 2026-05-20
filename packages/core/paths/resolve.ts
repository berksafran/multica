import type { Workspace } from "../types";
import { useAuthStore } from "../auth";
import { paths } from "./paths";

/**
 * Priority (workspace-presence first):
 *   has workspace                          → /<first.slug>/issues  (modal handles un-onboarded)
 *   no workspace + !hasOnboarded           → /onboarding
 *   no workspace + hasOnboarded            → /workspaces/new
 *
 * Why workspace-presence is the primary axis (not `onboarded_at`):
 * `CreateWorkspace` no longer marks the user as onboarded — that mark
 * is reserved for the `BootstrapOnboardingRuntime` path that creates
 * the user's first Multica Helper (triggered by the workspace
 * `OnboardingHelperModal`). So a user can legitimately land in the
 * "has workspace but !onboarded" mid-flow state. Routing them back to
 * /onboarding would loop them through Welcome → questionnaire steps
 * they've already completed; routing them to the workspace lets the
 * blocking modal pick up exactly where they left off (the modal stays
 * up until `onboarded_at != null`).
 *
 * `AcceptInvitation` still marks onboarded — invitees skip the helper
 * modal entirely, so they hit `hasOnboarded` true and route normally.
 *
 * Callers that need invitation-aware routing (callback / login) handle the
 * "un-onboarded with pending invites" branch themselves before calling
 * this resolver — this resolver only deals with the post-invite-check
 * destination.
 */
export function resolvePostAuthDestination(
  workspaces: Workspace[],
  hasOnboarded: boolean,
): string {
  const first = workspaces[0];
  if (first) {
    // Workspace exists → land in it regardless of onboarded status.
    // The workspace-layer OnboardingHelperModal will fire if needed.
    return paths.workspace(first.slug).issues();
  }
  if (!hasOnboarded) {
    return paths.onboarding();
  }
  return paths.newWorkspace();
}

/**
 * Single source of truth: backed by `users.onboarded_at`, which
 * arrives with the user object on every auth response.
 */
export function useHasOnboarded(): boolean {
  return useAuthStore((s) => s.user?.onboarded_at != null);
}
