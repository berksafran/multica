"use client";

import Image from "next/image";
import Link from "next/link";
import { ArrowRight, Download } from "lucide-react";
import { useAuthStore } from "@multica/core/auth";
import { captureDownloadIntent } from "@multica/core/analytics";
import { useLocale } from "../i18n";
import {
  ClaudeCodeLogo,
  CodexLogo,
  GeminiCliLogo,
  OpenClawLogo,
  OpenCodeLogo,
  heroButtonClassName,
} from "./shared";

export function LandingHero() {
  const { t } = useLocale();
  const user = useAuthStore((s) => s.user);
  const latestRelease = t.changelog.entries[0];

  return (
    <div className="relative min-h-full overflow-hidden bg-[#05070b] text-white">
      <LandingBackdrop />

      <main className="relative z-10">
        <section
          id="product"
          className="mx-auto max-w-[1320px] px-4 pb-16 pt-28 sm:px-6 sm:pt-32 lg:px-8 lg:pb-24 lg:pt-36"
        >
          <div className="mx-auto max-w-[1120px] text-center">
            {latestRelease && (
              <WhatsNewBadge
                label={t.hero.whatsNewLabel}
                version={latestRelease.version}
                title={latestRelease.title}
              />
            )}
            <h1 className="font-[family-name:var(--font-serif)] text-[3.65rem] leading-[0.93] tracking-[-0.038em] text-white drop-shadow-[0_10px_34px_rgba(0,0,0,0.32)] sm:text-[4.85rem] lg:text-[6.4rem]">
              {t.hero.headlineLine1}
              <br />
              {t.hero.headlineLine2}
            </h1>

            <p className="mx-auto mt-7 max-w-[820px] text-[15px] leading-7 text-white/84 sm:text-[17px]">
              {t.hero.subheading}
            </p>

            <div className="mt-8 flex flex-wrap items-center justify-center gap-3">
              <Link href={user ? "/" : "/login"} className={heroButtonClassName("solid")}>
                {user ? t.header.dashboard : t.hero.cta}
              </Link>
              <Link
                href="/download"
                className={heroButtonClassName("ghost")}
                onClick={() => captureDownloadIntent("landing_hero")}
              >
                <Download className="size-4" aria-hidden />
                {t.hero.downloadDesktop}
              </Link>
            </div>
          </div>

          <div className="mt-10 flex flex-wrap items-center justify-center gap-x-6 gap-y-3">
            <span className="text-[15px] text-white/50">
              {t.hero.worksWith}
            </span>
            <div className="flex flex-wrap items-center justify-center gap-x-5 gap-y-3">
              <div className="flex items-center gap-2.5 text-white/80">
                <ClaudeCodeLogo className="size-5" />
                <span className="text-[15px] font-medium">Claude Code</span>
              </div>
              <div className="flex items-center gap-2.5 text-white/80">
                <CodexLogo className="size-5" />
                <span className="text-[15px] font-medium">Codex</span>
              </div>
              <div className="flex items-center gap-2.5 text-white/80">
                <GeminiCliLogo className="size-5" />
                <span className="text-[15px] font-medium">Gemini CLI</span>
              </div>
              <div className="flex items-center gap-2.5 text-white/80">
                <OpenClawLogo className="size-5" />
                <span className="text-[15px] font-medium">OpenClaw</span>
              </div>
              <div className="flex items-center gap-2.5 text-white/80">
                <OpenCodeLogo className="size-5" />
                <span className="text-[15px] font-medium">OpenCode</span>
              </div>
            </div>
          </div>

          <div id="preview" className="mt-10 sm:mt-12">
            <ProductImage alt={t.hero.imageAlt} />
          </div>
        </section>
      </main>
    </div>
  );
}

function WhatsNewBadge({
  label,
  version,
  title,
}: {
  label: string;
  version: string;
  title: string;
}) {
  const anchor = `release-${version.replace(/\./g, "-")}`;
  return (
    <div className="mb-7 flex justify-center">
      <Link
        href={`/changelog#${anchor}`}
        className="group inline-flex max-w-full items-center gap-2 rounded-full border border-white/18 bg-white/8 px-3 py-1.5 text-[12px] font-medium text-white/85 backdrop-blur-sm transition-colors hover:border-white/32 hover:bg-white/12 sm:gap-2.5 sm:text-[13px]"
      >
        <span className="inline-flex items-center gap-1.5 rounded-full bg-white/12 px-2 py-0.5 text-[11px] font-semibold uppercase tracking-[0.08em] text-white">
          {label}
        </span>
        <span className="tabular-nums text-white/70">v{version}</span>
        <span aria-hidden className="hidden h-3 w-px bg-white/18 sm:inline-block" />
        <span className="hidden truncate sm:inline-block sm:max-w-[420px]">
          {title}
        </span>
        <ArrowRight
          aria-hidden
          className="size-3.5 shrink-0 text-white/60 transition-transform group-hover:translate-x-0.5 group-hover:text-white"
        />
      </Link>
    </div>
  );
}

function LandingBackdrop() {
  return (
    <div className="pointer-events-none absolute inset-0">
      <Image
        src="/images/landing-bg.jpg"
        alt=""
        fill
        className="object-cover object-center"
      />
    </div>
  );
}

function ProductImage({ alt }: { alt: string }) {
  return (
    <div>
      <div className="relative overflow-hidden border border-white/14">
        <Image
          src="/images/landing-hero.png"
          alt={alt}
          width={3532}
          height={2382}
          priority
          className="block h-auto w-full"
          sizes="(max-width: 1320px) 100vw, 1320px"
          quality={85}
        />
      </div>
    </div>
  );
}
