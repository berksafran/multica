/**
 * Cross-screen channel between the chat tab (`(tabs)/chat.tsx`) and the
 * `chat-sessions` formSheet route. The formSheet can't push selections back
 * up the React tree (different routes), so we hand off through a small
 * store with three slots:
 *
 *   - `activeSessionId` — mirrored from the chat tab so the picker can
 *     render the current selection's check mark. The chat tab calls
 *     `setActiveSessionId` whenever its local state changes.
 *   - `selectRequest` — the picker writes the id (or null) the user picked;
 *     the chat tab `useEffect`s on it, applies it, then `consume()`s.
 *   - `openAgentPickerRequest` — bumped when the user taps "Switch agent"
 *     so the chat tab can open its (still-modal) agent picker after the
 *     formSheet dismisses.
 *
 * Both request slots are one-shot (consumed after read). This avoids
 * re-firing on every render or after a soft navigation back.
 *
 * Workspace lifecycle: this store holds workspace-scoped state
 * (`activeSessionId` belongs to the workspace whose chat tab seeded it).
 * When the user switches workspaces, the previous session id is invalid
 * and any pending one-shot request belongs to the old tree. Reset is
 * wired in `app/(app)/[workspace]/_layout.tsx` via
 * `useResetOnWorkspaceChange()` — that's the only place that calls it.
 */
import { useEffect } from "react";
import { create } from "zustand";

interface ChatSessionPickerState {
  activeSessionId: string | null;
  setActiveSessionId: (id: string | null) => void;
  selectRequest: { id: string | null; nonce: number } | null;
  requestSelect: (id: string | null) => void;
  openAgentPickerRequest: { nonce: number } | null;
  requestNewWithAgent: () => void;
  consumeSelect: () => void;
  consumeOpenAgentPicker: () => void;
  reset: () => void;
}

const INITIAL = {
  activeSessionId: null,
  selectRequest: null,
  openAgentPickerRequest: null,
} as const;

export const useChatSessionPickerStore = create<ChatSessionPickerState>(
  (set, get) => ({
    ...INITIAL,
    setActiveSessionId: (id) => set({ activeSessionId: id }),
    requestSelect: (id) =>
      set({ selectRequest: { id, nonce: (get().selectRequest?.nonce ?? 0) + 1 } }),
    requestNewWithAgent: () =>
      set({
        openAgentPickerRequest: {
          nonce: (get().openAgentPickerRequest?.nonce ?? 0) + 1,
        },
      }),
    consumeSelect: () => set({ selectRequest: null }),
    consumeOpenAgentPicker: () => set({ openAgentPickerRequest: null }),
    reset: () => set({ ...INITIAL }),
  }),
);

/**
 * Clears the chat session picker store whenever the active workspace id
 * changes. Mounted once from the workspace `_layout.tsx`; relies on the
 * workspace store being the source of truth for "which workspace am I
 * looking at right now". The first time it runs there's no prior id, so
 * the initial mount is effectively a no-op (we already initialise to the
 * INITIAL shape).
 */
export function useChatSessionPickerResetOnWorkspaceChange(
  wsId: string | null,
) {
  useEffect(() => {
    useChatSessionPickerStore.getState().reset();
  }, [wsId]);
}
