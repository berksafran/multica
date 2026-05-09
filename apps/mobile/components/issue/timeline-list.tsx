/**
 * The scrolling timeline. ASC chronological — oldest at top, newest near the
 * bottom (above the composer). Pull-to-refresh refetches issue + timeline;
 * scrolling toward the top while older history exists triggers
 * `fetchNextPage` (we use a non-inverted FlatList, so "older" lives above).
 *
 * Uses native FlatList (mobile baseline doesn't include FlashList — see
 * apps/mobile/CLAUDE.md "Tech-stack baseline"). For the issue volumes the
 * product targets, FlatList is fine.
 */
import { useCallback, useMemo, useRef } from "react";
import {
  ActivityIndicator,
  FlatList,
  RefreshControl,
  View,
  type NativeScrollEvent,
  type NativeSyntheticEvent,
} from "react-native";
import type { Issue, TimelineEntry, TimelinePage } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { IssueHeaderCard } from "./issue-header-card";
import { IssueDescription } from "./issue-description";
import { ActivityRow } from "./activity-row";
import { CommentCard } from "./comment-card";
import { coalesceTimeline } from "@/lib/timeline-coalesce";
import { buildTimelineRows, type TimelineRow } from "@/lib/timeline-thread";

interface Props {
  issue: Issue;
  pages: TimelinePage[] | undefined;
  timelineLoading: boolean;
  hasMoreOlder: boolean;
  isFetchingOlder: boolean;
  fetchOlder: () => void;
  refreshing: boolean;
  onRefresh: () => void;
}

const NEAR_TOP_THRESHOLD = 120;

export function TimelineList({
  issue,
  pages,
  timelineLoading,
  hasMoreOlder,
  isFetchingOlder,
  fetchOlder,
  refreshing,
  onRefresh,
}: Props) {
  // Server pages are DESC (newest first). Concatenate then reverse → ASC,
  // matching how web's useIssueTimeline shapes its consumer view
  // (packages/views/issues/hooks/use-issue-timeline.ts:106). Pipeline:
  //   1. coalesceTimeline → merge consecutive identical activities
  //   2. buildTimelineRows → reorder so replies sit adjacent to their parent
  //      and tag each reply with `replyTo` for the card to render the
  //      "↪ Replying to" header + thread-line border. This is the mobile
  //      flat-list interpretation of web's recursive reply tree.
  const data = useMemo<TimelineRow[]>(() => {
    if (!pages) return [];
    const flat: TimelineEntry[] = [];
    for (const page of pages) {
      for (const entry of page.entries) flat.push(entry);
    }
    flat.reverse();
    return buildTimelineRows(coalesceTimeline(flat));
  }, [pages]);

  // Manual top-detection for "older" pagination. FlatList only ships a
  // bottom-anchored `onEndReached`; here we want the inverse, so we watch
  // contentOffset against the threshold and fire once per crossing.
  const firingRef = useRef(false);
  const onScroll = useCallback(
    (e: NativeSyntheticEvent<NativeScrollEvent>) => {
      const y = e.nativeEvent.contentOffset.y;
      if (y < NEAR_TOP_THRESHOLD) {
        if (!firingRef.current && hasMoreOlder && !isFetchingOlder) {
          firingRef.current = true;
          fetchOlder();
        }
      } else {
        firingRef.current = false;
      }
    },
    [hasMoreOlder, isFetchingOlder, fetchOlder],
  );

  const ListHeader = (
    <View>
      <IssueHeaderCard issue={issue} />
      <IssueDescription description={issue.description} />
      <View className="px-4 pt-4 pb-2 border-t border-border">
        <Text className="text-xs uppercase tracking-wider text-muted-foreground font-medium">
          Activity
        </Text>
      </View>
      {hasMoreOlder ? (
        <View className="py-3 items-center">
          {isFetchingOlder ? (
            <ActivityIndicator size="small" />
          ) : (
            <Text className="text-xs text-muted-foreground">
              Pull to load older
            </Text>
          )}
        </View>
      ) : null}
      {timelineLoading && (!pages || pages.length === 0) ? (
        <View className="py-6 items-center">
          <ActivityIndicator />
        </View>
      ) : null}
    </View>
  );

  return (
    <FlatList
      data={data}
      keyExtractor={(row) => row.entry.id}
      ListHeaderComponent={ListHeader}
      renderItem={({ item }) =>
        item.entry.type === "comment" ? (
          <CommentCard entry={item.entry} replies={item.replies} />
        ) : (
          <ActivityRow entry={item.entry} />
        )
      }
      onScroll={onScroll}
      scrollEventThrottle={64}
      refreshControl={
        <RefreshControl refreshing={refreshing} onRefresh={onRefresh} />
      }
      contentContainerClassName="pb-4"
    />
  );
}
