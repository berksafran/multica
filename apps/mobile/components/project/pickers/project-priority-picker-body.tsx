/**
 * Pure picker body for project priority — single-select over the 5
 * ProjectPriority enum values. See issue/pickers/status-picker-body.tsx for
 * the "extract body, route owns shell" rationale.
 */
import { Pressable, ScrollView, View } from "react-native";
import type { ProjectPriority } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { ProjectPriorityIcon } from "@/components/ui/project-priority-icon";
import {
  PROJECT_PRIORITIES,
  PROJECT_PRIORITY_LABEL,
} from "@/lib/project-status";
import { cn } from "@/lib/utils";

interface Props {
  value: ProjectPriority | string;
  onChange: (next: ProjectPriority) => void;
}

export function ProjectPriorityPickerBody({ value, onChange }: Props) {
  return (
    <ScrollView showsVerticalScrollIndicator={false}>
      <View className="px-4 pt-3 pb-2">
        <Text className="text-lg font-semibold text-foreground">Priority</Text>
      </View>
      <View className="px-2">
        {PROJECT_PRIORITIES.map((priority) => {
          const selected = priority === value;
          return (
            <Pressable
              key={priority}
              onPress={() => onChange(priority)}
              className={cn(
                "flex-row items-center gap-3 rounded-lg px-3 py-3 active:bg-secondary",
                selected && "bg-secondary",
              )}
            >
              <ProjectPriorityIcon priority={priority} size={18} />
              <Text className="flex-1 text-base text-foreground">
                {PROJECT_PRIORITY_LABEL[priority]}
              </Text>
              {selected ? (
                <Text className="text-sm text-muted-foreground">✓</Text>
              ) : null}
            </Pressable>
          );
        })}
      </View>
    </ScrollView>
  );
}
