"use client"

// Re-export the shared ThemeProvider from @multica/ui
export { ThemeProvider } from "@multica/ui/components/common/theme-provider"

// Suppress React 19's false-positive about next-themes' inline <script>.
// The script works correctly; React just warns about any <script> in
// components. See https://github.com/pacocoursey/next-themes/issues/337
//
// The warning fires synchronously during the first client render. We used
// to leave the wrapper installed forever, but then every later error's
// stack trace ended at `orig.apply(console, args)` here — masking the
// real call site in Next.js's overlay (a real API error looked like a
// theme-provider bug). Restoring `console.error` after hydration keeps
// dev-tool stack traces pointing at the actual culprit.
if (typeof window !== "undefined" && process.env.NODE_ENV === "development") {
  const orig = console.error;
  const wrapped = (...args: unknown[]) => {
    if (typeof args[0] === "string" && args[0].includes("Encountered a script tag"))
      return;
    orig.apply(console, args);
  };
  console.error = wrapped;
  // Restore on the next macrotask: by then React's initial hydration has
  // flushed and the next-themes warning (if it was going to fire) has
  // already been intercepted. Guard the swap so we don't clobber a wrapper
  // installed by another library after us.
  setTimeout(() => {
    if (console.error === wrapped) console.error = orig;
  }, 0);
}
