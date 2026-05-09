/**
 * Issue detail screen (V1).
 *
 * Read-mostly + comment composer. Property edits, replies, reactions,
 * attachments inline render, mention chips, and image lightbox are deferred
 * to V2+ — see /Users/qingnaiyuan/.claude/plans/plan-dynamic-narwhal.md.
 *
 * Header note: the parent _layout.tsx already declares the
 * `issue/[id]` Stack.Screen with title "Issue". We override that here once
 * the data lands so the navigation bar shows `MUL-123` (Linear-style).
 */
import { useCallback } from "react";
import {
  ActivityIndicator,
  KeyboardAvoidingView,
  Platform,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { Stack, useLocalSearchParams } from "expo-router";
import { useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { Text } from "@/components/ui/text";
import { Button } from "@/components/ui/button";
import { TimelineList } from "@/components/issue/timeline-list";
import { CommentComposer } from "@/components/issue/comment-composer";
import {
  issueDetailOptions,
  issueKeys,
  issueTimelineInfiniteOptions,
} from "@/data/queries/issues";
import { useCreateComment } from "@/data/mutations/issues";
import { useWorkspaceStore } from "@/data/workspace-store";

export default function IssueDetail() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const qc = useQueryClient();

  const detail = useQuery(issueDetailOptions(wsId, id));
  const timeline = useInfiniteQuery(
    issueTimelineInfiniteOptions(wsId, id),
  );
  const createComment = useCreateComment(id);

  const onRefresh = useCallback(async () => {
    await Promise.all([
      detail.refetch(),
      qc.invalidateQueries({ queryKey: issueKeys.timeline(wsId, id) }),
    ]);
  }, [detail, qc, wsId, id]);

  const onSubmitComment = useCallback(
    (content: string) => createComment.mutateAsync(content),
    [createComment],
  );

  const issue = detail.data;

  return (
    <SafeAreaView className="flex-1 bg-background" edges={["bottom"]}>
      <Stack.Screen
        options={{
          title: issue?.identifier ?? "Issue",
          headerBackTitle: "Back",
        }}
      />
      {detail.isLoading ? (
        <View className="flex-1 items-center justify-center">
          <ActivityIndicator />
        </View>
      ) : detail.error || !issue ? (
        <View className="flex-1 items-center justify-center px-6 gap-3">
          <Text className="text-sm text-destructive text-center">
            Failed to load issue:{" "}
            {detail.error instanceof Error
              ? detail.error.message
              : "not found"}
          </Text>
          <Button variant="outline" onPress={() => detail.refetch()}>
            Retry
          </Button>
        </View>
      ) : (
        <KeyboardAvoidingView
          behavior={Platform.OS === "ios" ? "padding" : undefined}
          className="flex-1"
        >
          <View className="flex-1">
            <TimelineList
              issue={issue}
              pages={timeline.data?.pages}
              timelineLoading={timeline.isLoading}
              hasMoreOlder={timeline.hasNextPage}
              isFetchingOlder={timeline.isFetchingNextPage}
              fetchOlder={() => timeline.fetchNextPage()}
              refreshing={detail.isRefetching || timeline.isRefetching}
              onRefresh={onRefresh}
            />
          </View>
          <CommentComposer onSubmit={onSubmitComment} />
        </KeyboardAvoidingView>
      )}
    </SafeAreaView>
  );
}
