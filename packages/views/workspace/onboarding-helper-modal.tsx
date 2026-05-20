"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, Loader2 } from "lucide-react";
import { bootstrapRuntimeOnboarding } from "@multica/core/onboarding";
import { runtimeListOptions } from "@multica/core/runtimes";
import { issueKeys } from "@multica/core/issues/queries";
import { workspaceKeys } from "@multica/core/workspace/queries";
import { useAuthStore } from "@multica/core/auth";
import { useCurrentWorkspace, paths } from "@multica/core/paths";
import {
  Dialog,
  DialogContent,
  DialogTitle,
  DialogDescription,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";
import { useNavigation } from "../navigation";
import { useT } from "../i18n";

/**
 * Inline copy of the Multica Helper avatar SVG (kept in sync with the
 * server-side constant `onboardingAssistantAvatarURL` in
 * server/internal/handler/onboarding.go). We could fetch the agent record
 * after bootstrap to get the real avatar URL, but at modal-render time
 * the agent doesn't exist yet — this constant is the introduction.
 */
const HELPER_AVATAR_URL =
  "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 128 128'%3E%3Cdefs%3E%3ClinearGradient id='t' x1='0' y1='0' x2='0' y2='1'%3E%3Cstop offset='0%25' stop-color='%2323242C'/%3E%3Cstop offset='100%25' stop-color='%2313141A'/%3E%3C/linearGradient%3E%3C/defs%3E%3Crect width='128' height='128' rx='28' fill='url(%23t)'/%3E%3Cg stroke='%23FFFFFF' stroke-width='13' stroke-linecap='round'%3E%3Cline x1='64' y1='32' x2='64' y2='96'/%3E%3Cline x1='32' y1='64' x2='96' y2='64'/%3E%3Cline x1='41.4' y1='41.4' x2='86.6' y2='86.6'/%3E%3Cline x1='86.6' y1='41.4' x2='41.4' y2='86.6'/%3E%3C/g%3E%3C/svg%3E";

/**
 * Three card ids — used as i18n lookup keys AND as the message body when
 * we POST to bootstrap. The actual user-facing prompt text comes from the
 * locale file (onboarding.json) keyed by id, so localization is centralized.
 */
const STARTER_CARD_IDS = ["intro", "assign", "second_agent"] as const;
type StarterCardId = (typeof STARTER_CARD_IDS)[number];

/**
 * Blocking onboarding modal mounted inside the workspace shell.
 *
 * Trigger: `me.onboarded_at == null` AND a workspace is in context AND
 * the workspace already has at least one runtime. Renders nothing in any
 * other state.
 *
 * What it does on submit:
 *   1. POST /api/me/onboarding/runtime-bootstrap with workspace_id,
 *      runtime_id, and the picked card's prompt as starter_prompt.
 *   2. Backend single-tx creates Multica Helper + onboarding issue
 *      (description = starter_prompt) and marks the user onboarded.
 *   3. bootstrapRuntimeOnboarding's internal refreshMe pulls the new
 *      user into Zustand — `me.onboarded_at` is now non-null, so this
 *      component returns null next render (no flicker).
 *   4. We invalidate workspace.agents and issues query caches so the
 *      issue page renders Helper as assignee immediately.
 *   5. Navigate into the seeded onboarding issue.
 *
 * Why "blocking":
 *   - No close button rendered.
 *   - `disablePointerDismissal` so outside-click can't dismiss.
 *   - `onOpenChange` is a no-op so Escape can't dismiss.
 *   - `me.onboarded_at == null` is the only way out; the user must
 *     pick a card and complete bootstrap.
 *
 * Why "no runtime" silently no-ops (rather than rendering an alt UI):
 *   - A user who skipped runtime on the onboarding screen takes the
 *     bootstrapNoRuntimeOnboarding path which sets onboarded_at — this
 *     modal won't fire for them.
 *   - A user who reached this point WITH a runtime then later lost it
 *     (e.g. daemon disconnect) shouldn't be stuck behind the modal.
 *     The condition gates on `runtimes[0]` so the modal hides while we
 *     wait for / re-establish a runtime.
 */
export function OnboardingHelperModal() {
  const { t } = useT("onboarding");
  const me = useAuthStore((s) => s.user);
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const runtimes = useQuery({
    ...runtimeListOptions(wsId),
    enabled: !!wsId && me?.onboarded_at == null,
  });
  const navigation = useNavigation();
  const qc = useQueryClient();

  const [submittingId, setSubmittingId] = useState<StarterCardId | null>(null);
  const [error, setError] = useState<string | null>(null);

  if (!me || me.onboarded_at != null) return null;
  if (!workspace) return null;
  const runtime = runtimes.data?.[0];
  if (!runtime) return null;

  const handlePick = async (cardId: StarterCardId) => {
    if (submittingId !== null) return;
    setError(null);
    setSubmittingId(cardId);
    const prompt = t(($) => $.onboarding_helper_modal.cards[cardId].prompt);
    try {
      const result = await bootstrapRuntimeOnboarding(
        workspace.id,
        runtime.id,
        prompt,
      );
      // bootstrapRuntimeOnboarding internally refreshes the auth store, so
      // `me.onboarded_at` is non-null by the time we get here — this
      // component will unmount on the next render. We still invalidate
      // agents + issues so the destination issue page renders the new
      // Helper as assignee immediately rather than waiting for a WS event.
      await Promise.all([
        qc.invalidateQueries({ queryKey: workspaceKeys.agents(workspace.id) }),
        qc.invalidateQueries({ queryKey: issueKeys.all(workspace.id) }),
      ]);
      if (result.issue_id) {
        navigation.push(paths.workspace(workspace.slug).issueDetail(result.issue_id));
      }
    } catch (err) {
      setError(
        err instanceof Error
          ? err.message
          : t(($) => $.onboarding_helper_modal.error_generic),
      );
      setSubmittingId(null);
    }
  };

  return (
    <Dialog
      open={true}
      modal={true}
      disablePointerDismissal={true}
      // Swallow every close attempt (escapeKey, outsidePress, focusOut).
      // The modal is dismissed implicitly when bootstrap completes and
      // `me.onboarded_at` flips non-null, causing this component to return
      // null on the next render.
      onOpenChange={() => {
        /* no-op */
      }}
    >
      <DialogContent
        showCloseButton={false}
        className="max-w-md sm:max-w-md"
        aria-describedby="onboarding-helper-modal-subtitle"
      >
        <div className="flex flex-col items-center gap-3 pt-2">
          {/* Avatar */}
          <img
            src={HELPER_AVATAR_URL}
            alt=""
            aria-hidden
            className="h-14 w-14 rounded-xl ring-1 ring-foreground/10"
          />
          <DialogTitle className="text-center text-base font-medium">
            {t(($) => $.onboarding_helper_modal.title)}
          </DialogTitle>
          <DialogDescription
            id="onboarding-helper-modal-subtitle"
            className="text-center text-sm text-muted-foreground"
          >
            {t(($) => $.onboarding_helper_modal.subtitle)}
          </DialogDescription>
        </div>

        {/* Cards */}
        <div className="mt-2 flex flex-col gap-2">
          {STARTER_CARD_IDS.map((id, idx) => {
            const busy = submittingId === id;
            const otherBusy = submittingId !== null && submittingId !== id;
            return (
              <button
                key={id}
                type="button"
                onClick={() => handlePick(id)}
                disabled={otherBusy}
                aria-busy={busy}
                className={cn(
                  "group flex items-center gap-3 rounded-lg border bg-background px-3 py-2.5 text-left transition-colors",
                  "hover:border-foreground/30 hover:bg-muted/40",
                  "disabled:cursor-not-allowed disabled:opacity-50",
                  // First card has a subtle "recommended" affordance — it's
                  // the broadest, safest first task for users who don't know
                  // what to try.
                  idx === 0 && "border-foreground/30",
                )}
              >
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-medium leading-tight">
                    {t(($) => $.onboarding_helper_modal.cards[id].title)}
                  </p>
                  <p className="mt-0.5 text-xs text-muted-foreground leading-snug">
                    {t(($) => $.onboarding_helper_modal.cards[id].subtitle)}
                  </p>
                </div>
                {busy ? (
                  <Loader2 className="h-4 w-4 shrink-0 animate-spin text-muted-foreground" />
                ) : (
                  <ArrowRight className="h-4 w-4 shrink-0 text-muted-foreground transition-transform group-hover:translate-x-0.5" />
                )}
              </button>
            );
          })}
        </div>

        {error ? (
          <div
            role="alert"
            className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
          >
            <p>{error}</p>
            <Button
              variant="ghost"
              size="sm"
              className="mt-1 h-6 px-2 text-xs"
              onClick={() => setError(null)}
            >
              {t(($) => $.onboarding_helper_modal.dismiss_error)}
            </Button>
          </div>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}
