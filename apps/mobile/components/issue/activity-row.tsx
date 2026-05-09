/**
 * Activity (non-comment) timeline row. Linear-style: hollow circle bullet on
 * the left, single-line sentence ("<actor> <verb> <details>"), absolute
 * timestamp on the next line.
 *
 * Bullet circle is intentionally distinct from the avatar bubble used on
 * comment rows — it's the visual cue that "this is a system event, not a
 * conversation."
 */
import { View } from "react-native";
import type { TimelineEntry } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { formatActivity, formatActivityTimestamp } from "@/lib/format-activity";
import { useActorLookup } from "@/data/use-actor-name";

export function ActivityRow({ entry }: { entry: TimelineEntry }) {
  const { getName } = useActorLookup();
  // Bridge useActorLookup's narrow type (member | agent) to formatActivity's
  // permissive `string` (timeline `details.to_type` is `string`, since the
  // server may legitimately surface other actor kinds in future).
  const resolveName = (
    type: string | null | undefined,
    id: string | null | undefined,
  ): string =>
    getName(type as "member" | "agent" | null | undefined, id);
  const actorName = resolveName(entry.actor_type, entry.actor_id);
  const verb = formatActivity(entry, resolveName);
  return (
    <View className="px-4 py-2">
      <Text className="text-sm text-foreground">
        <Text className="font-medium">{actorName}</Text>
        {verb ? <Text> {verb}</Text> : null}
      </Text>
      <Text className="text-xs text-muted-foreground mt-0.5">
        {formatActivityTimestamp(entry.created_at)}
      </Text>
    </View>
  );
}
