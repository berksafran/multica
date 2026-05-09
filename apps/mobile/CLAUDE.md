# Mobile App Rules (apps/mobile/)

For cross-app sharing rules, see the root `CLAUDE.md` *Sharing Principles* section. This file documents the locked tech-stack baseline and the few mobile-specific rules — so AI doesn't suggest outdated alternatives.

## What mobile may import from `packages/`

- `import type` from `@multica/core/types/*` (zero runtime coupling)
- Pure functions from `@multica/core/`

Everything else, mobile writes its own.

## Tech-stack baseline (locked)

Start minimal. Add to this list when actually adopted — do NOT pre-list libraries.

- **Expo SDK 54**
- **React Native 0.81**
- **React 19.x** — whatever Expo SDK 54 ships. Pinned in `apps/mobile/package.json` directly, NOT via root `catalog:`.
- **TypeScript** strict
- **Expo Router 6** — file-based routing
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
