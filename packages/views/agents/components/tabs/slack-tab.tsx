"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, KeyRound, Loader2, MessageSquare } from "lucide-react";
import type { Agent } from "@multica/core/types";
import { api } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../../i18n";

const slackStatusQueryKey = (wsId: string, agentId: string) => ["agent-slack", wsId, agentId] as const;
const slackCredentialsQueryKey = (wsId: string, agentId: string) => ["agent-slack-credentials", wsId, agentId] as const;

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

  const syncMutation = useMutation({
    mutationFn: () => api.syncAgentSlackApp(workspaceId, agent.id),
    onSuccess: () => setError(null),
    onError: (e) => setError(e instanceof Error ? e.message : t(($) => $.slack_tab.error_sync)),
  });

  const disconnectMutation = useMutation({
    mutationFn: () => api.disconnectAgentSlackApp(workspaceId, agent.id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: slackStatusQueryKey(workspaceId, agent.id) });
      qc.invalidateQueries({ queryKey: slackCredentialsQueryKey(workspaceId, agent.id) });
      setError(null);
    },
    onError: (e) => setError(e instanceof Error ? e.message : t(($) => $.slack_tab.error_disconnect)),
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
          <div className="flex flex-col gap-1 rounded-md border bg-background px-3 py-2 text-xs">
            <div className="flex items-center justify-between">
              <span className="text-muted-foreground">{t(($) => $.slack_tab.status_label)}</span>
              <span className="font-medium text-foreground">
                {t(($) => $.slack_tab.status_installed)}
              </span>
            </div>
            {status.team_id && (
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">{t(($) => $.slack_tab.team_label)}</span>
                <span className="font-mono">{status.team_id}</span>
              </div>
            )}
            {status.bot_user_id && (
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">{t(($) => $.slack_tab.bot_user_label)}</span>
                <span className="font-mono">{status.bot_user_id}</span>
              </div>
            )}
            {status.app_id && (
              <div className="flex items-center justify-between">
                <span className="text-muted-foreground">{t(($) => $.slack_tab.app_id_label)}</span>
                <span className="font-mono">{status.app_id}</span>
              </div>
            )}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              onClick={() => syncMutation.mutate()}
              disabled={syncing}
            >
              {syncing
                ? t(($) => $.slack_tab.syncing)
                : t(($) => $.slack_tab.sync_button)}
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={() => disconnectMutation.mutate()}
              disabled={disconnecting}
            >
              {disconnecting
                ? t(($) => $.slack_tab.disconnecting)
                : t(($) => $.slack_tab.disconnect_button)}
            </Button>
          </div>
        </div>
      )}

      {/* Credentials editor: visible whenever a row exists in DB, even
          after a successful install — Slack-side credentials may rotate
          and the user needs a path to re-paste them without disconnect. */}
      {status.provisioned && (
        <CredentialsSection
          workspaceId={workspaceId}
          agentId={agent.id}
          onError={setError}
          onSaved={() => {
            qc.invalidateQueries({ queryKey: slackStatusQueryKey(workspaceId, agent.id) });
          }}
        />
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
      <div className="flex items-center gap-2 rounded-md border bg-background px-3 py-2 text-xs text-muted-foreground">
        <Loader2 className="h-3.5 w-3.5 animate-spin" /> {t(($) => $.slack_tab.loading)}
      </div>
    );
  }
  if (!credentialsQuery.data) return null;

  const data = credentialsQuery.data;
  const saving = updateMutation.isPending;

  return (
    <section className="flex flex-col gap-3 rounded-md border bg-background px-3 py-3">
      <header className="flex items-start gap-2">
        <KeyRound className="mt-0.5 h-4 w-4 text-muted-foreground" />
        <div className="flex flex-col gap-0.5">
          <h3 className="text-xs font-semibold">{t(($) => $.slack_tab.credentials_title)}</h3>
          <p className="text-[11px] text-muted-foreground">
            {t(($) => $.slack_tab.credentials_description)}
          </p>
        </div>
      </header>

      {!editing && (
        <div className="flex flex-col gap-1 text-xs">
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">
              {t(($) => $.slack_tab.credentials_client_id_label)}
            </span>
            <span className="font-mono">{data.client_id || "—"}</span>
          </div>
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">
              {t(($) => $.slack_tab.credentials_client_secret_label)}
            </span>
            <span className={data.has_client_secret ? "text-foreground" : "text-muted-foreground"}>
              {data.has_client_secret
                ? t(($) => $.slack_tab.credentials_secret_saved)
                : t(($) => $.slack_tab.credentials_secret_not_saved)}
            </span>
          </div>
          <div className="mt-2">
            <Button type="button" variant="outline" size="sm" onClick={() => setEditing(true)}>
              {t(($) => $.slack_tab.credentials_edit)}
            </Button>
          </div>
        </div>
      )}

      {editing && (
        <form
          className="flex flex-col gap-3"
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
