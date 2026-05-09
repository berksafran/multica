# Mobile App Rules (apps/mobile/)

For cross-app sharing rules, see the root `CLAUDE.md` *Sharing Principles* section. This file documents the locked tech-stack baseline and the few mobile-specific rules — so AI doesn't suggest outdated alternatives.

## What mobile may import from `packages/`

- `import type` from `@multica/core/types/*` (zero runtime coupling)
- Pure functions from `@multica/core/`

Everything else, mobile writes its own.

## Behavioral parity with web/desktop

Mobile is allowed to differ in **UI and interaction** — it's a phone, not a port. It is NOT allowed to differ in **product semantics**. Users should not get a different mental model of "what's there" depending on which client they open.

Concrete rules:

- **Counts and visibility must agree.** If web shows the user N comments on an issue under a given filter, mobile must show the same N (subject to identical pagination/coalescing rules). If mobile silently re-implements timeline grouping with different coalescing windows, mobile is wrong.
- **Permissions and access checks must agree.** "Can comment", "can change status", "can archive inbox item" — mobile decides via the same logic web does (mirrored from packages/core, not re-derived from feel).
- **State enums and transitions must agree.** Issue status set, priority set, inbox item types, comment types — mobile renders all of them (with a sensible fallback for unknown values, per "API Response Compatibility" in the root CLAUDE.md). Mobile does NOT silently drop categories.
- **Data identity must agree.** Same `id`, same `slug`, same canonical fields. Mobile does not invent its own ids or normalize differently.

**Concrete UX divergence is fine** when it preserves semantics:

- ✅ Web shows comment thread as a recursive tree; mobile shows a flat list (because phone screens). Same comments, different layout.
- ✅ Web has a sidebar workspace switcher; mobile puts it in Settings. Same switching semantics.
- ✅ Web shows inbox item read-state with a filled background; mobile uses a leading dot. Same boolean.
- ❌ Web counts both replies and parent comments in the comment count; mobile counts only top-level. **Not allowed** — same N rule.
- ❌ Web treats `status="cancelled"` as visible; mobile silently hides it. **Not allowed** — same enums rule.

When UI requires a divergence, write down at the divergence point what the rule is mirroring (point at the source function in packages/core or packages/views) and why mobile renders it differently. Future readers should be able to tell, in 30 seconds, that the mobile divergence is intentional and which web-side function is the source of truth.

### ⚠️ Incident (2026-05-09): inbox dedup missing — counts disagreed

**Symptom**: Web sidebar showed "Inbox 1" while mobile rendered 3+ unread dots on the same workspace, same user, same moment.

**Root cause**: Backend `GET /api/inbox` returns raw rows that include:
1. archived items, and
2. multiple inbox notifications per issue (a comment, a status change, and an assignment on the same issue each create one row).

Web/desktop run those raw rows through `deduplicateInboxItems` (`packages/core/inbox/queries.ts`) before rendering and before counting unread:
1. filter `archived = true` out
2. group by `issue_id`, keep the newest in each group
3. sort by `created_at` desc

Mobile's first cut rendered the raw list directly. So a single issue with 3 notifications showed as 3 rows with 3 unread dots, while web showed 1.

**Fix**: mirror `deduplicateInboxItems` into `apps/mobile/lib/inbox-display.ts`, run mobile's inbox tab through it before rendering and before any counting.

**Lesson — encode this into your reflexes when adding any new mobile screen that consumes a list endpoint**:

> Before rendering an API list response, grep `packages/core/<domain>/queries.ts` and `packages/views/<domain>/components/*.tsx` for any preprocessing — `dedupe*`, `coalesce*`, `filter*`, `*-display.ts`, `useMemo(() => transform(raw))`. Mirror everything that runs between `useQuery` and the JSX in web/desktop. **Do not assume the backend returns "what should be displayed"** — it usually returns the raw cache shape, and the client is responsible for shaping it.

This pattern repeats: timeline coalescing (`buildTimelineGroups`), inbox dedup, comment thread flattening, etc. Each one is a behavioral parity hazard if mobile skips it.

## Tech-stack baseline

Start minimal. Add to this list when actually adopted — do NOT pre-list libraries.

- **Expo SDK 55**
- **React Native 0.82**
- **React 19.1** — whatever Expo SDK 55 ships. Pinned in `apps/mobile/package.json` directly, NOT via root `catalog:`.
- **TypeScript** strict
- **Expo Router 55** (file-based routing — version aligns with Expo SDK)
- **NativeWind 4** + **Tailwind 3.4** — NativeWind 5 is unstable and doesn't support Expo Go; stay on v4. (Note: web/desktop use Tailwind v4 — versions intentionally differ.)
- **react-native-reusables (RNR)** — the shadcn equivalent for React Native. Uses NativeWind + RN-Primitives + CVA. Component API mirrors shadcn.
- **TanStack Query 5** — mobile owns its `QueryClient` with `AppState` focus listener + `NetInfo` online listener.
- **Zustand** — mobile-local state only.
- **expo-secure-store** — auth token persistence.

When upgrading any of these, update this list.

## Visual tokens (separate from web)

Mobile maintains its own design tokens in `apps/mobile/tailwind.config.js`. You MAY reference `packages/ui/styles/tokens.css` (web/desktop tokens) as inspiration, but **do not import or symlink the file**. Tokens are transcribed by hand and may diverge for mobile (touch-friendly spacing, no hover states, native typography).

Tailwind version mismatch (mobile v3.4 vs web v4) makes file sharing impractical anyway — this isolation is intentional.

## Build & release

- **Main CI** (`.github/workflows/ci.yml`) excludes mobile via `--filter='!@multica/mobile'`. Mobile failures do NOT block web/desktop PRs.
- **Mobile verify** (`.github/workflows/mobile-verify.yml`): triggered on `apps/mobile/**` or `packages/core/types/**` changes — runs typecheck/lint/test only, no IPA build.
- **Mobile release** (`.github/workflows/mobile-release.yml`): triggered by `mobile-v*.*.*` tag → `eas build` + `eas submit`.
- **OTA** — EAS Update for JS-only fixes that don't change the runtime version. Manual / on-demand push to preview/production channels.

Mobile release cadence is decoupled from main `v*.*.*` tags (server / CLI / desktop).

## Lessons learned (encode into reflexes)

These are real mistakes that have been made building the mobile shell. Each one cost time to find. Treat as enforceable rules, not suggestions.

### 1. Install/upgrade any dependency: check `dist-tags` first

Do NOT hardcode version numbers from memory. Run `pnpm view <pkg> dist-tags` to see `latest / sdk-XX / canary` and decide which tag to lock. For Expo packages (`expo-*` / `react-native-*` that Expo aligns), use `pnpm exec expo install <pkg>` — it queries Expo's dependency manifest and picks the SDK-compatible version. `pnpm add <pkg>` will silently install the npm `latest`, which often outpaces the SDK and breaks at runtime. Past mistakes: hardcoded `expo@~54.0.0` (latest was already `55.x`); installed `lucide-react-native@0.468` without checking React 19 peer compatibility.

### 2. New source subdirectory: verify git tracking

Every time you create a new source subdirectory under `apps/mobile/` (e.g. `data/`, `lib/foo/`, `components/inbox/`):

1. Run `git check-ignore -v <dir>/<file>` immediately. The repo-root `.gitignore` has generic rules (`data/`, `build/`, `bin/`, `*.app`, `*.dmg`) that are intended for backend runtime/output dirs but will silently swallow mobile source.
2. If a rule matches, add `!<dir>/` and `!<dir>/**` to `apps/mobile/.gitignore` (subtree override beats parent rule).
3. After the commit lands, run `git ls-files <dir>` to confirm every file is tracked.

This rule exists because `apps/mobile/data/` was once committed-but-not-tracked — 14 source files (ApiClient, all queries, all stores) were missing from the git tree even though `git status` was clean. Local builds worked because Metro reads the filesystem; CI / clones would have died.

### 3. ApiClient capability list (4 must-haves)

Mobile's fetch wrapper (`apps/mobile/data/api.ts`) MUST implement all four. Missing any of them is a bug, not a deferred polish item.

1. **Zod `parseWithFallback` for response validation.** Strictly enforced by the root CLAUDE.md "API Response Compatibility" section and the "Type drift defense" section above. **Any new endpoint method that does `as T` on the response body is a bug.** Reuse schemas from `packages/core/api/schemas.ts` (pure Zod exports, on the mobile sharing whitelist); define mobile-side fallbacks for new endpoints in `apps/mobile/data/`.

2. **`onUnauthorized` 401 callback.** The `ApiClientOptions.onUnauthorized` hook fires on every 401 and must be wired in `app/_layout.tsx` to: clear auth token, clear workspace store, clear TanStack Query cache, navigate to `/login`. Without it a session that expired server-side puts every subsequent request into a 401 loop and the user sees opaque "API error: 401" toasts on every screen. Use a `signingOutRef` to make the callback idempotent — multiple in-flight requests will all 401 simultaneously when a session expires.

3. **`X-Request-ID` per request.** Generate a short random ID (`createRequestId()` in `apps/mobile/lib/request-id.ts`), send as `X-Request-ID` header. The same ID goes into client-side log lines so backend telemetry can be cross-referenced (server picks it up via the same header).

4. **Structured request logger.** Two log lines per request: `[api] → METHOD path` (start, with `rid`) and `[api] ← STATUS path` (end, with `rid` + `duration`). Use `console.error` for 5xx, `console.warn` for 404s, `console.log` for success. Without this, debugging mobile API issues means staring at the React Native Network panel; with it, the dev console is self-explanatory and prod telemetry already comes structured.

**What mobile correctly does NOT need (don't add these):** CSRF token (`X-CSRF-Token`), `credentials: "include"`, cookie reading. Mobile is Bearer-token auth, not cookie auth — the cookie attack surface that requires CSRF protection on web doesn't exist on mobile.

### 4. Visual alignment is baseline, not polish

When implementing a mobile screen / row / list:

1. Open the web/desktop equivalent source file (e.g. `packages/views/inbox/components/inbox-list-item.tsx`) and compare its JSX structure side-by-side with the mobile JSX you're about to write.
2. Run a screenshot of the web/desktop view next to a screenshot of the simulator.
3. The four items below are **baseline**, not polish for a later iteration:
   - **Tab bar must have icons** (Ionicons / SF Symbols / lucide-react-native) with focused/unfocused state switch.
   - **Each screen has a title at the top** (Stack large title, or a custom `ScreenHeader`).
   - **Row's right-side elements stack vertically into a column** when there are multiple (status above, time below). Pattern: nested flex-rows, each with its own right-aligned element. NOT a single horizontal flex-row with status and time competing for the same trailing slot.
   - **Secondary lines must use a type-aware label component** (mirror, e.g., `InboxDetailLabel`'s type switch). Rendering raw `item.body` directly leaks server-side markdown markers (`##`, `*`) and stale debug strings into the UI.

Skipping any of these in a "first cut" turns the v1 into something that prompts a "you didn't care about interaction at all" review — every time. Easier to do them up-front (15 min total) than to retrofit.
