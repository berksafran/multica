import { describe, expect, it } from "vitest";
import type { Workspace } from "../types";
import { paths } from "./paths";
import { resolvePostAuthDestination } from "./resolve";

function makeWs(slug: string): Workspace {
  return {
    id: `id-${slug}`,
    name: slug,
    slug,
    description: null,
    context: null,
    settings: {},
    repos: [],
    issue_prefix: slug.toUpperCase(),
    created_at: "",
    updated_at: "",
  };
}

describe("resolvePostAuthDestination", () => {
  it("has workspace → /<first.slug>/issues (regardless of onboarded)", () => {
    // Workspace-presence is now the primary axis. A user who created a
    // workspace but hasn't completed the OnboardingHelperModal (so
    // `onboarded_at` is still NULL) lands directly in the workspace —
    // the modal there picks up the un-finished setup. Routing them to
    // /onboarding instead would loop them through Welcome / questionnaire
    // they already completed.
    const ws = [makeWs("acme"), makeWs("beta")];
    expect(resolvePostAuthDestination(ws, true)).toBe(
      paths.workspace("acme").issues(),
    );
    expect(resolvePostAuthDestination(ws, false)).toBe(
      paths.workspace("acme").issues(),
    );
  });

  it("no workspace + !onboarded → /onboarding", () => {
    expect(resolvePostAuthDestination([], false)).toBe(paths.onboarding());
  });

  it("no workspace + onboarded → /workspaces/new", () => {
    // Already-onboarded user without any workspace — usually a returning
    // user whose last workspace got deleted or who left it. They skip
    // re-onboarding and go straight to workspace creation.
    expect(resolvePostAuthDestination([], true)).toBe(paths.newWorkspace());
  });
});
