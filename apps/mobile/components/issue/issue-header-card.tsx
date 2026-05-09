/**
 * Slim header for the issue detail screen.
 *
 * Differs from Linear iOS: Linear hides every property behind the ⋯ menu and
 * shows only the title. We keep a single visible row with status / priority /
 * assignee / due date because mobile users are still building a mental model
 * of where issue state lives — it would be a bad time to copy Linear's
 * power-user-only minimalism. (See conversation that produced
 * /Users/qingnaiyuan/.claude/plans/plan-dynamic-narwhal.md.)
 *
 * The native iOS Stack header already renders `issue.identifier` as the
 * navigation title, so the body skips it.
 */
import { View } from "react-native";
import type { Issue } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { StatusIcon } from "@/components/ui/status-icon";
import { PriorityIcon } from "@/components/ui/priority-icon";
import { ActorAvatar } from "@/components/ui/actor-avatar";

function formatDueDate(iso: string | null): string | null {
  if (!iso) return null;
  return new Date(iso).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
  });
}

export function IssueHeaderCard({ issue }: { issue: Issue }) {
  const due = formatDueDate(issue.due_date);
  return (
    <View className="px-4 pt-4 pb-3 gap-3">
      <Text className="text-xl font-semibold text-foreground">
        {issue.title}
      </Text>
      <View className="flex-row items-center gap-3">
        <StatusIcon status={issue.status} size={18} />
        <PriorityIcon priority={issue.priority} size={16} />
        {issue.assignee_type && issue.assignee_id ? (
          <View className="flex-row items-center gap-1.5">
            <ActorAvatar
              type={issue.assignee_type}
              id={issue.assignee_id}
              size={20}
            />
          </View>
        ) : (
          <Text className="text-xs text-muted-foreground">Unassigned</Text>
        )}
        {due ? (
          <Text className="text-xs text-muted-foreground ml-auto">
            Due {due}
          </Text>
        ) : null}
      </View>
    </View>
  );
}
