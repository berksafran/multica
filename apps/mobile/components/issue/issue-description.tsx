/**
 * Description block. V1 renders raw markdown source as plain text — the
 * `react-native-marked` adapter described in
 * apps/mobile/docs/markdown-renderer-research.md is V2 work, not blocking
 * v1. So `**bold**`, fenced code, and `![alt](url)` are shown verbatim.
 *
 * RN <Text> respects `\n` so paragraph breaks survive intact.
 */
import { View } from "react-native";
import { Text } from "@/components/ui/text";

export function IssueDescription({
  description,
}: {
  description: string | null;
}) {
  if (!description || description.trim().length === 0) {
    return (
      <View className="px-4 pb-4">
        <Text className="text-sm text-muted-foreground italic">
          No description.
        </Text>
      </View>
    );
  }
  return (
    <View className="px-4 pb-4">
      <Text className="text-sm text-foreground leading-5">{description}</Text>
    </View>
  );
}
