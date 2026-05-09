/**
 * Bottom-sticky comment input. Plain-text only in V1 — no markdown toolbar,
 * no @mention picker, no attachment uploads. Pressing send fires the
 * optimistic mutation in apps/mobile/data/mutations/issues.ts.
 */
import { useState } from "react";
import { Pressable, TextInput, View } from "react-native";
import { Text } from "@/components/ui/text";
import { cn } from "@/lib/utils";

interface Props {
  onSubmit: (content: string) => Promise<unknown> | void;
}

export function CommentComposer({ onSubmit }: Props) {
  const [value, setValue] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const trimmed = value.trim();
  const canSend = trimmed.length > 0 && !submitting;

  async function handleSend() {
    if (!canSend) return;
    setSubmitting(true);
    const content = trimmed;
    setValue("");
    try {
      await onSubmit(content);
    } catch {
      // Mutation handles rollback + (future) toast. Restore the draft so the
      // user can retry without retyping.
      setValue(content);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <View className="border-t border-border bg-background px-3 py-2 flex-row items-end gap-2">
      <TextInput
        value={value}
        onChangeText={setValue}
        placeholder="Comment"
        multiline
        className="flex-1 bg-secondary rounded-2xl px-4 py-2.5 text-base text-foreground max-h-32"
        editable={!submitting}
      />
      <Pressable
        onPress={handleSend}
        disabled={!canSend}
        className={cn(
          "h-9 px-4 rounded-full items-center justify-center",
          canSend ? "bg-primary active:opacity-80" : "bg-muted",
        )}
      >
        <Text
          className={cn(
            "text-sm font-medium",
            canSend ? "text-primary-foreground" : "text-muted-foreground",
          )}
        >
          Send
        </Text>
      </Pressable>
    </View>
  );
}
