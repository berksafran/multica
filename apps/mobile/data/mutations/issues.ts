/**
 * Comment creation mutation. Mirrors the optimistic + invalidate pattern of
 * apps/mobile/data/mutations/inbox.ts:17 — but on the timeline infinite-query
 * cache shape `{ pages: TimelinePage[]; pageParams: ... }` instead of a flat
 * list.
 *
 * Optimistic strategy:
 *   - Cancel timeline refetches.
 *   - Snapshot the current cache.
 *   - Prepend a synthetic comment-typed TimelineEntry to the FIRST page (the
 *     newest page, since timeline pages are DESC newest-first). The screen
 *     reverses the flattened pages for ASC display, so a prepend-on-first-page
 *     surfaces at the bottom of the visible timeline (newest position).
 *   - On error: roll back to the snapshot.
 *   - On settled: invalidate so the server's real comment row replaces the
 *     synthetic one (real id, real created_at).
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { TimelineEntry, TimelinePage } from "@multica/core/types";
import { api } from "@/data/api";
import { issueKeys } from "@/data/queries/issues";
import { useAuthStore } from "@/data/auth-store";
import { useWorkspaceStore } from "@/data/workspace-store";

type InfiniteData = {
  pages: TimelinePage[];
  pageParams: unknown[];
};

export function useCreateComment(issueId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const userId = useAuthStore((s) => s.user?.id ?? null);

  return useMutation({
    mutationFn: (content: string) => api.createComment(issueId, content),
    onMutate: async (content) => {
      const key = issueKeys.timeline(wsId, issueId);
      await qc.cancelQueries({ queryKey: key });
      const prev = qc.getQueryData<InfiniteData>(key);
      if (!userId) return { prev, key };

      const optimistic: TimelineEntry = {
        type: "comment",
        id: `optimistic-${Date.now()}`,
        actor_type: "member",
        actor_id: userId,
        content,
        parent_id: null,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        comment_type: "comment",
        reactions: [],
        attachments: [],
      };

      qc.setQueryData<InfiniteData>(key, (old) => {
        if (!old || old.pages.length === 0) {
          return {
            pages: [
              {
                entries: [optimistic],
                next_cursor: null,
                prev_cursor: null,
                has_more_before: false,
                has_more_after: false,
              },
            ],
            pageParams: [null],
          };
        }
        // Prepend to the first (newest) page. Flattened pages are
        // reversed in the screen for ASC display, so prepend here =
        // appears at the bottom (newest) on screen.
        const [first, ...rest] = old.pages;
        return {
          ...old,
          pages: [
            { ...first!, entries: [optimistic, ...first!.entries] },
            ...rest,
          ],
        };
      });

      return { prev, key };
    },
    onError: (_err, _vars, ctx) => {
      if (ctx?.prev !== undefined && ctx.key) {
        qc.setQueryData(ctx.key, ctx.prev);
      }
    },
    onSettled: () => {
      qc.invalidateQueries({
        queryKey: issueKeys.timeline(wsId, issueId),
      });
    },
  });
}
