"use client";

import { ExternalLink, Hash } from "lucide-react";
import type { SlackThreadInfo } from "@multica/core/types";
import { useT } from "../../i18n";

interface SlackThreadBannerProps {
  thread: SlackThreadInfo;
}

// Sibling of ChatInput, occupying the same banner slot as OfflineBanner /
// NoAgentBanner. Shown when the session was opened from a Slack thread —
// the input above is locked, attachments are hidden, and rename is hidden.
//
// Layout (`px-5` outer, `mx-auto max-w-4xl` inner) mirrors the other banners
// so edges line up with the input on every viewport size. Permalink is
// best-effort: missing when chat.getPermalink failed at link-creation, in
// which case the banner degrades to plain copy.
export function SlackThreadBanner({ thread }: SlackThreadBannerProps) {
  const { t } = useT("chat");
  return (
    <div className="px-5 mb-1.5">
      <div className="mx-auto flex w-full max-w-4xl items-center gap-2 rounded-md bg-muted px-2.5 py-1.5 text-xs text-muted-foreground ring-1 ring-border">
        <Hash className="size-3.5 shrink-0" />
        <span className="truncate">{t(($) => $.slack_thread_banner.message)}</span>
        {thread.permalink && (
          <a
            href={thread.permalink}
            target="_blank"
            rel="noopener noreferrer"
            aria-label={t(($) => $.slack_thread_banner.open_link_aria)}
            className="ml-auto inline-flex shrink-0 items-center gap-1 rounded-sm font-medium text-foreground underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {t(($) => $.slack_thread_banner.open_link)}
            <ExternalLink className="size-3" />
          </a>
        )}
      </div>
    </div>
  );
}
