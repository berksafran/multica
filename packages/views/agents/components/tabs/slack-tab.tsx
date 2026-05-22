"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  Check,
  ExternalLink,
  HelpCircle,
  KeyRound,
  Loader2,
  MessageSquare,
  Pencil,
  RefreshCw,
  Layers,
} from "lucide-react";
import type { Agent } from "@multica/core/types";
import { api } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@multica/ui/components/ui/tooltip";
import { useT } from "../../../i18n";

const slackStatusQueryKey = (wsId: string, agentId: string) => ["agent-slack", wsId, agentId] as const;
const slackCredentialsQueryKey = (wsId: string, agentId: string) => ["agent-slack-credentials", wsId, agentId] as const;
const slackVerifyQueryKey = (wsId: string, agentId: string) => ["agent-slack-verify", wsId, agentId] as const;

/**
 * SlackTab is the per-agent "Connect to Slack" surface inside
 * AgentOverviewPane. Each Multica agent maps 1:1 to its own Slack App so
 * mentioning the agent in Slack feels like mentioning a teammate.
 *
 * The tab walks four states — provision (create the Slack App via
 * manifest.create) → credentials present? (auto-saved from manifest
 * response, editable in UI) → install (OAuth into a Slack workspace) →
 * connected (disconnect / re-sync after a rename).
 *
 * The credentials block is always reachable once an app is provisioned;
 * it's the manual rescue path if Slack rotates the client_secret or the
 * auto-persist somehow misses.
 */
export function SlackTab({ agent, workspaceId }: { agent: Agent; workspaceId: string }) {
  const { t } = useT("agents");
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);

  const statusQuery = useQuery({
    queryKey: slackStatusQueryKey(workspaceId, agent.id),
    queryFn: () => api.getAgentSlackStatus(workspaceId, agent.id),
    refetchOnWindowFocus: true,
  });

  // Probe Slack to detect orphaned rows (app deleted in dashboard).
  // Only runs once the local row exists; staleTime keeps the network
  // cost down (one Slack round-trip per minute even with focus events).
  const verifyQuery = useQuery({
    queryKey: slackVerifyQueryKey(workspaceId, agent.id),
    queryFn: () => api.verifyAgentSlackApp(workspaceId, agent.id),
    enabled: !!statusQuery.data?.provisioned,
    staleTime: 60_000,
    refetchOnWindowFocus: true,
    // 502 means probe-failed-but-row-still-trusted — don't retry to
    // avoid spamming Slack while the network is flaky.
    retry: false,
  });

  const provisionMutation = useMutation({
    mutationFn: () => api.provisionAgentSlackApp(workspaceId, agent.id),
    onSuccess: (resp) => {
      if (resp.install_url) {
        window.open(resp.install_url, "_blank", "noopener,width=900,height=700");
      }
      qc.invalidateQueries({ queryKey: slackStatusQueryKey(workspaceId, agent.id) });
      qc.invalidateQueries({ queryKey: slackCredentialsQueryKey(workspaceId, agent.id) });
      setError(null);
    },
    onError: (e) => setError(e instanceof Error ? e.message : t(($) => $.slack_tab.error_provision)),
  });

  const [syncedAt, setSyncedAt] = useState<number | null>(null);

  // Auto-clear the "Name synced" confirmation so it doesn't linger past
  // its useful window — otherwise the message implies a fresh sync on a
  // stale view long after the action completed.
  useEffect(() => {
    if (syncedAt === null) return;
    const handle = window.setTimeout(() => setSyncedAt(null), 4000);
    return () => window.clearTimeout(handle);
  }, [syncedAt]);

  const syncMutation = useMutation({
    mutationFn: () => api.syncAgentSlackApp(workspaceId, agent.id),
    onSuccess: () => {
      setError(null);
      setSyncedAt(Date.now());
    },
    onError: (e) => {
      setSyncedAt(null);
      setError(e instanceof Error ? e.message : t(($) => $.slack_tab.error_sync));
    },
  });

  const disconnectMutation = useMutation({
    mutationFn: () => api.disconnectAgentSlackApp(workspaceId, agent.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: slackStatusQueryKey(workspaceId, agent.id) });
      qc.invalidateQueries({ queryKey: slackCredentialsQueryKey(workspaceId, agent.id) });
      qc.invalidateQueries({ queryKey: slackVerifyQueryKey(workspaceId, agent.id) });
      setError(null);
    },
    onError: (e) => setError(e instanceof Error ? e.message : t(($) => $.slack_tab.error_disconnect)),
  });

  const verifyMutation = useMutation({
    mutationFn: () => api.verifyAgentSlackApp(workspaceId, agent.id),
    onSuccess: (data) => {
      qc.setQueryData(slackVerifyQueryKey(workspaceId, agent.id), data);
      setError(null);
    },
    onError: (e) => setError(e instanceof Error ? e.message : t(($) => $.slack_tab.error_sync)),
  });

  if (statusQuery.isLoading) {
    return (
      <div className="flex items-center gap-2 px-4 py-8 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" /> {t(($) => $.slack_tab.loading)}
      </div>
    );
  }
  if (statusQuery.isError || !statusQuery.data) {
    return (
      <div className="px-4 py-8 text-sm text-destructive">
        {t(($) => $.slack_tab.load_failed)}
      </div>
    );
  }

  const status = statusQuery.data;
  const reinstallURL = status.install_url;
  const provisioning = provisionMutation.isPending;
  const syncing = syncMutation.isPending;
  const disconnecting = disconnectMutation.isPending;
  const verifying = verifyMutation.isPending || verifyQuery.isFetching;
  // Treat the row as orphaned only when verify completed AND came back
  // with app_exists=false. While verify is loading / errored we trust
  // the local row to avoid flashing a misleading banner.
  const isOrphaned = status.provisioned && verifyQuery.data?.app_exists === false;

  return (
    <div className="flex flex-col gap-6 px-4 py-6">
      <header className="flex items-start gap-3">
        <div className="rounded-md border bg-muted/40 p-2">
          <MessageSquare className="h-5 w-5" />
        </div>
        <div className="flex flex-col gap-1">
          <h2 className="text-sm font-semibold">{t(($) => $.slack_tab.title)}</h2>
          <p className="text-xs text-muted-foreground">
            {t(($) => $.slack_tab.description, { name: agent.name })}
          </p>
        </div>
      </header>

      {!status.configured && (
        <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {t(($) => $.slack_tab.not_configured)}
        </div>
      )}

      {isOrphaned && (
        <div className="flex items-start gap-3 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-3 text-xs">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
          <div className="flex flex-1 flex-col gap-2">
            <div className="flex flex-col gap-0.5">
              <p className="font-semibold text-destructive">
                {t(($) => $.slack_tab.orphaned_title)}
              </p>
              <p className="text-muted-foreground">
                {t(($) => $.slack_tab.orphaned_description)}
              </p>
            </div>
            <div>
              <Button
                type="button"
                variant="destructive"
                size="sm"
                onClick={() => disconnectMutation.mutate()}
                disabled={disconnecting}
              >
                {disconnecting
                  ? t(($) => $.slack_tab.disconnecting)
                  : t(($) => $.slack_tab.orphaned_remove)}
              </Button>
            </div>
          </div>
        </div>
      )}

      {status.configured && !status.provisioned && (
        <div className="flex flex-col gap-3">
          <p className="text-xs text-muted-foreground">{t(($) => $.slack_tab.provision_hint)}</p>
          <div>
            <Button
              type="button"
              onClick={() => provisionMutation.mutate()}
              disabled={provisioning}
            >
              {provisioning ? (
                <>
                  <Loader2 className="mr-2 h-4 w-4 animate-spin" />{" "}
                  {t(($) => $.slack_tab.provisioning)}
                </>
              ) : (
                <>
                  <MessageSquare className="mr-2 h-4 w-4" />{" "}
                  {t(($) => $.slack_tab.provision_button)}
                </>
              )}
            </Button>
          </div>
        </div>
      )}

      {status.configured && status.provisioned && !status.installed && (
        <div className="flex flex-col gap-3">
          {!status.has_credentials && (
            <div className="rounded-md border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300">
              {t(($) => $.slack_tab.credentials_missing)}
            </div>
          )}
          {status.has_credentials && (
            <p className="text-xs text-muted-foreground">{t(($) => $.slack_tab.install_hint)}</p>
          )}
          <div className="flex flex-wrap gap-2">
            {reinstallURL && (
              <Button
                type="button"
                onClick={() =>
                  window.open(reinstallURL, "_blank", "noopener,width=900,height=700")
                }
              >
                <ExternalLink className="mr-2 h-4 w-4" />{" "}
                {t(($) => $.slack_tab.install_button)}
              </Button>
            )}
            <Button
              type="button"
              variant="outline"
              onClick={() => disconnectMutation.mutate()}
              disabled={disconnecting}
            >
              {disconnecting
                ? t(($) => $.slack_tab.removing)
                : t(($) => $.slack_tab.install_cancel)}
            </Button>
          </div>
        </div>
      )}

      {status.installed && (
        <div className="flex flex-col gap-3">
          <div className="grid grid-cols-1 items-start gap-3 lg:grid-cols-2">
          <div className="overflow-hidden rounded-lg border bg-background">
            <div className="flex items-center justify-between gap-2 border-b bg-muted/30 px-3 py-2">
              <div className="flex items-center gap-2">
                <span className="relative flex h-2 w-2" aria-hidden>
                  <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-60" />
                  <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-500" />
                </span>
                <span className="text-xs font-medium">
                  {t(($) => $.slack_tab.status_installed)}
                </span>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-6 px-2 text-xs text-muted-foreground hover:text-foreground"
                onClick={() => verifyMutation.mutate()}
                disabled={verifying}
              >
                <RefreshCw className={`mr-1.5 h-3 w-3 ${verifying ? "animate-spin" : ""}`} />
                {verifying
                  ? t(($) => $.slack_tab.verifying)
                  : t(($) => $.slack_tab.verify_button)}
              </Button>
            </div>
            <dl className="divide-y text-xs">
              {status.team_id && (
                <div className="grid grid-cols-[120px_1fr] items-center gap-3 px-3 py-2">
                  <dt className="text-muted-foreground">{t(($) => $.slack_tab.team_label)}</dt>
                  <dd className="truncate font-mono text-foreground">{status.team_id}</dd>
                </div>
              )}
              {status.bot_user_id && (
                <div className="grid grid-cols-[120px_1fr] items-center gap-3 px-3 py-2">
                  <dt className="text-muted-foreground">{t(($) => $.slack_tab.bot_user_label)}</dt>
                  <dd className="truncate font-mono text-foreground">{status.bot_user_id}</dd>
                </div>
              )}
              {status.app_id && (
                <div className="grid grid-cols-[120px_1fr] items-center gap-3 px-3 py-2">
                  <dt className="text-muted-foreground">{t(($) => $.slack_tab.app_id_label)}</dt>
                  <dd className="truncate font-mono text-foreground">{status.app_id}</dd>
                </div>
              )}
            </dl>
          </div>
            <CredentialsSection
              workspaceId={workspaceId}
              agentId={agent.id}
              onError={setError}
              onSaved={() => {
                qc.invalidateQueries({ queryKey: slackStatusQueryKey(workspaceId, agent.id) });
              }}
            />
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => syncMutation.mutate()}
              disabled={syncing}
            >
              {syncing ? (
                <>
                  <Loader2 className="mr-2 h-3.5 w-3.5 animate-spin" />
                  {t(($) => $.slack_tab.syncing)}
                </>
              ) : (
                <>
                  <RefreshCw className="mr-2 h-3.5 w-3.5" />
                  {t(($) => $.slack_tab.sync_button)}
                </>
              )}
            </Button>
            {syncedAt !== null && (
              <span
                className="flex items-center gap-1 text-xs text-emerald-600 dark:text-emerald-400"
                role="status"
              >
                <Check className="h-3.5 w-3.5" />
                {t(($) => $.slack_tab.sync_success)}
              </span>
            )}
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="ml-auto text-destructive hover:bg-destructive/10 hover:text-destructive"
              onClick={() => disconnectMutation.mutate()}
              disabled={disconnecting}
            >
              {disconnecting
                ? t(($) => $.slack_tab.disconnecting)
                : t(($) => $.slack_tab.disconnect_button)}
            </Button>
          </div>

          <p className="text-xs text-muted-foreground">
            {t(($) => $.slack_tab.sync_avatar_hint)}
          </p>
        </div>
      )}

      {/* Credentials editor: visible whenever a row exists in DB, even
          after a successful install — Slack-side credentials may rotate
          and the user needs a path to re-paste them without disconnect.
          When installed it renders side-by-side with the status card
          above; pre-install it stacks as its own row below. */}
      {status.provisioned && !status.installed && (
        <CredentialsSection
          workspaceId={workspaceId}
          agentId={agent.id}
          onError={setError}
          onSaved={() => {
            qc.invalidateQueries({ queryKey: slackStatusQueryKey(workspaceId, agent.id) });
          }}
        />
      )}

      {/* Per-agent recent-context settings: only useful once installed
          (no bot token = no API to fetch from). Defaults to 0/0 so the
          feature stays dormant for agents that don't opt in. */}
      {status.installed && (
        <SettingsSection
          workspaceId={workspaceId}
          agentId={agent.id}
          threadCount={status.recent_context_thread_count ?? 0}
          channelCount={status.recent_context_channel_count ?? 0}
          onError={setError}
        />
      )}

      {/* Verify control lives in the installed-state status header
          (and inline next to the not-installed actions when needed) so
          users can manually re-check after a Slack-side change without
          waiting for the focus-driven refresh. */}
      {status.provisioned && !status.installed && (
        <div className="flex items-center justify-end">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => verifyMutation.mutate()}
            disabled={verifying}
          >
            <RefreshCw className={`mr-2 h-3.5 w-3.5 ${verifying ? "animate-spin" : ""}`} />
            {verifying
              ? t(($) => $.slack_tab.verifying)
              : t(($) => $.slack_tab.verify_button)}
          </Button>
        </div>
      )}

      {error && (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}

function CredentialsSection({
  workspaceId,
  agentId,
  onError,
  onSaved,
}: {
  workspaceId: string;
  agentId: string;
  onError: (msg: string | null) => void;
  onSaved: () => void;
}) {
  const { t } = useT("agents");
  const qc = useQueryClient();
  const credentialsQuery = useQuery({
    queryKey: slackCredentialsQueryKey(workspaceId, agentId),
    queryFn: () => api.getAgentSlackCredentials(workspaceId, agentId),
  });

  const [editing, setEditing] = useState(false);
  const [clientID, setClientID] = useState("");
  const [clientSecret, setClientSecret] = useState("");

  // Seed the edit form with the current client_id whenever we enter
  // edit mode. The secret stays blank (we never reveal it) — submitting
  // a blank secret means "keep the saved value".
  useEffect(() => {
    if (editing && credentialsQuery.data) {
      setClientID(credentialsQuery.data.client_id ?? "");
      setClientSecret("");
    }
  }, [editing, credentialsQuery.data]);

  const updateMutation = useMutation({
    mutationFn: () =>
      api.updateAgentSlackCredentials(workspaceId, agentId, {
        client_id: clientID.trim() || undefined,
        client_secret: clientSecret.trim() || undefined,
      }),
    onSuccess: () => {
      onError(null);
      setEditing(false);
      setClientSecret("");
      qc.invalidateQueries({ queryKey: slackCredentialsQueryKey(workspaceId, agentId) });
      onSaved();
    },
    onError: (e) => onError(e instanceof Error ? e.message : t(($) => $.slack_tab.error_credentials)),
  });

  if (credentialsQuery.isLoading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border bg-background px-3 py-2 text-xs text-muted-foreground">
        <Loader2 className="h-3.5 w-3.5 animate-spin" /> {t(($) => $.slack_tab.loading)}
      </div>
    );
  }
  if (!credentialsQuery.data) return null;

  const data = credentialsQuery.data;
  const saving = updateMutation.isPending;

  return (
    <section className="overflow-hidden rounded-lg border bg-background">
      <header className="flex items-center justify-between gap-2 border-b bg-muted/30 px-3 py-2">
        <div className="flex items-center gap-2">
          <KeyRound className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
          <h3 className="text-xs font-medium">
            {t(($) => $.slack_tab.credentials_title)}
          </h3>
          <Tooltip>
            <TooltipTrigger
              render={
                <button
                  type="button"
                  aria-label={t(($) => $.slack_tab.credentials_description)}
                  className="flex h-4 w-4 items-center justify-center rounded-full text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                >
                  <HelpCircle className="h-3.5 w-3.5" />
                </button>
              }
            />
            <TooltipContent className="max-w-xs whitespace-normal px-3 py-2 text-left leading-snug">
              {t(($) => $.slack_tab.credentials_description)}
            </TooltipContent>
          </Tooltip>
        </div>
        {!editing && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-6 shrink-0 px-2 text-xs text-muted-foreground hover:text-foreground"
            onClick={() => setEditing(true)}
          >
            <Pencil className="mr-1.5 h-3 w-3" />
            {t(($) => $.slack_tab.credentials_edit)}
          </Button>
        )}
      </header>

      {!editing && (
        <dl className="divide-y text-xs">
          <div className="grid grid-cols-[120px_1fr] items-center gap-3 px-3 py-2">
            <dt className="text-muted-foreground">
              {t(($) => $.slack_tab.credentials_client_id_label)}
            </dt>
            <dd className="truncate font-mono text-foreground">{data.client_id || "—"}</dd>
          </div>
          <div className="grid grid-cols-[120px_1fr] items-center gap-3 px-3 py-2">
            <dt className="text-muted-foreground">
              {t(($) => $.slack_tab.credentials_client_secret_label)}
            </dt>
            <dd>
              {data.has_client_secret ? (
                <span className="inline-flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
                  <Check className="h-3 w-3" />
                  {t(($) => $.slack_tab.credentials_secret_saved)}
                </span>
              ) : (
                <span className="text-muted-foreground">
                  {t(($) => $.slack_tab.credentials_secret_not_saved)}
                </span>
              )}
            </dd>
          </div>
        </dl>
      )}

      {editing && (
        <form
          className="flex flex-col gap-3 px-3 py-3"
          onSubmit={(e) => {
            e.preventDefault();
            updateMutation.mutate();
          }}
        >
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground">
              {t(($) => $.slack_tab.credentials_client_id_label)}
            </span>
            <input
              type="text"
              className="rounded-md border bg-background px-2 py-1 font-mono text-xs focus:outline-none focus:ring-1 focus:ring-ring"
              value={clientID}
              onChange={(e) => setClientID(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </label>
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground">
              {t(($) => $.slack_tab.credentials_client_secret_label)}
            </span>
            <input
              type="password"
              className="rounded-md border bg-background px-2 py-1 font-mono text-xs focus:outline-none focus:ring-1 focus:ring-ring"
              value={clientSecret}
              placeholder={t(($) => $.slack_tab.credentials_client_secret_placeholder)}
              onChange={(e) => setClientSecret(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </label>
          <div className="flex gap-2">
            <Button type="submit" size="sm" disabled={saving}>
              {saving
                ? t(($) => $.slack_tab.credentials_saving)
                : t(($) => $.slack_tab.credentials_save)}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                setEditing(false);
                setClientSecret("");
              }}
              disabled={saving}
            >
              {t(($) => $.slack_tab.credentials_cancel)}
            </Button>
          </div>
        </form>
      )}
    </section>
  );
}

const RECENT_CONTEXT_MAX = 20;

function clampCount(raw: string): number {
  const n = Math.floor(Number(raw));
  if (!Number.isFinite(n) || n < 0) return 0;
  if (n > RECENT_CONTEXT_MAX) return RECENT_CONTEXT_MAX;
  return n;
}

function SettingsSection({
  workspaceId,
  agentId,
  threadCount,
  channelCount,
  onError,
}: {
  workspaceId: string;
  agentId: string;
  threadCount: number;
  channelCount: number;
  onError: (msg: string | null) => void;
}) {
  const { t } = useT("agents");
  const qc = useQueryClient();

  // Local edit state seeded from the status query. We deliberately do
  // NOT useEffect-reset on every status refetch — that would clobber
  // the user's in-flight edits during a background refresh.
  const [thread, setThread] = useState(String(threadCount));
  const [channel, setChannel] = useState(String(channelCount));

  const dirty = clampCount(thread) !== threadCount || clampCount(channel) !== channelCount;

  const saveMutation = useMutation({
    mutationFn: () =>
      api.updateAgentSlackSettings(workspaceId, agentId, {
        recent_context_thread_count: clampCount(thread),
        recent_context_channel_count: clampCount(channel),
      }),
    onSuccess: () => {
      onError(null);
      qc.invalidateQueries({ queryKey: slackStatusQueryKey(workspaceId, agentId) });
    },
    onError: (e) =>
      onError(e instanceof Error ? e.message : t(($) => $.slack_tab.error_context)),
  });

  const saving = saveMutation.isPending;

  return (
    <section className="overflow-hidden rounded-lg border bg-background">
      <header className="flex items-center justify-between gap-2 border-b bg-muted/30 px-3 py-2">
        <div className="flex items-center gap-2">
          <Layers className="h-3.5 w-3.5 text-muted-foreground" aria-hidden />
          <h3 className="text-xs font-medium">
            {t(($) => $.slack_tab.context_title)}
          </h3>
          <Tooltip>
            <TooltipTrigger
              render={
                <button
                  type="button"
                  aria-label={t(($) => $.slack_tab.context_description)}
                  className="flex h-4 w-4 items-center justify-center rounded-full text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                >
                  <HelpCircle className="h-3.5 w-3.5" />
                </button>
              }
            />
            <TooltipContent className="max-w-xs whitespace-normal px-3 py-2 text-left leading-snug">
              {t(($) => $.slack_tab.context_description)}
            </TooltipContent>
          </Tooltip>
        </div>
      </header>

      <form
        className="flex flex-col gap-3 px-3 py-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (!dirty) return;
          saveMutation.mutate();
        }}
      >
        <div className="grid grid-cols-2 gap-3">
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground">
              {t(($) => $.slack_tab.context_thread_label)}
            </span>
            <input
              type="number"
              min={0}
              max={RECENT_CONTEXT_MAX}
              step={1}
              inputMode="numeric"
              className="rounded-md border bg-background px-2 py-1 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
              value={thread}
              onChange={(e) => setThread(e.target.value)}
            />
          </label>
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-muted-foreground">
              {t(($) => $.slack_tab.context_channel_label)}
            </span>
            <input
              type="number"
              min={0}
              max={RECENT_CONTEXT_MAX}
              step={1}
              inputMode="numeric"
              className="rounded-md border bg-background px-2 py-1 text-xs focus:outline-none focus:ring-1 focus:ring-ring"
              value={channel}
              onChange={(e) => setChannel(e.target.value)}
            />
          </label>
        </div>
        <p className="text-[11px] text-muted-foreground">
          {t(($) => $.slack_tab.context_hint)}
        </p>
        <div>
          <Button type="submit" size="sm" disabled={saving || !dirty}>
            {saving
              ? t(($) => $.slack_tab.context_saving)
              : t(($) => $.slack_tab.context_save)}
          </Button>
        </div>
      </form>
    </section>
  );
}
