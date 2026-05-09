/**
 * Issue detail + timeline queries. Mobile-owned; mirrors a strict subset of
 * packages/core/issues/queries.ts (issueDetailOptions and
 * issueTimelineInfiniteOptions). Mobile v1 only needs latest-mode for the
 * initial page and before-cursor for older pages — no around-mode (no deep
 * link jump) and no after-mode (no WS prepend).
 *
 * Workspace-scoped query keys. Mandatory per repo-root CLAUDE.md state-mgmt
 * rules ("Workspace-scoped queries must key on wsId") — switching workspace
 * causes the cache key to change, which automatically refetches detail and
 * timeline for the right workspace.
 */
import {
  infiniteQueryOptions,
  queryOptions,
} from "@tanstack/react-query";
import type { TimelinePage } from "@multica/core/types";
import { api } from "@/data/api";

type TimelineCursor = { mode: "before"; cursor: string } | null;

export const issueKeys = {
  detail: (wsId: string | null, id: string) =>
    ["issue", wsId, "detail", id] as const,
  timeline: (wsId: string | null, id: string) =>
    ["issue", wsId, "timeline", id] as const,
};

export const issueDetailOptions = (wsId: string | null, id: string) =>
  queryOptions({
    queryKey: issueKeys.detail(wsId, id),
    queryFn: () => api.getIssue(id),
    enabled: !!wsId && !!id,
  });

export const issueTimelineInfiniteOptions = (
  wsId: string | null,
  id: string,
) =>
  infiniteQueryOptions<
    TimelinePage,
    Error,
    { pages: TimelinePage[]; pageParams: TimelineCursor[] },
    readonly unknown[],
    TimelineCursor
  >({
    queryKey: issueKeys.timeline(wsId, id),
    queryFn: ({ pageParam }) => api.listTimeline(id, pageParam),
    initialPageParam: null,
    getNextPageParam: (lastPage) =>
      lastPage.has_more_before && lastPage.next_cursor
        ? { mode: "before" as const, cursor: lastPage.next_cursor }
        : undefined,
    enabled: !!wsId && !!id,
  });
