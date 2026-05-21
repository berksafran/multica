"use client";

import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Loader2, MessageSquare } from "lucide-react";
import type { Agent } from "@multica/core/types";
import { api } from "@multica/core/api";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../../../i18n";

const slackStatusQueryKey = (wsId: string, agentId: string) => ["agent-slack", wsId, agentId] as const;

/**
 * SlackTab is the per-agent "Connect to Slack" surface inside
 * AgentOverviewPane. Each Multica agent maps 1:1 to its own Slack App so
 * mentioning the agent in Slack feels like mentioning a teammate. The
 * tab walks through three states — provision (create the Slack App via
 * manifest.create) → install (OAuth into a Slack workspace) → connected
 * (disconnect / re-sync after a rename).
 *
 * The Connect button opens Slack's install URL in a popup; React Query
 * refetches the status on focus, so when the user finishes the install
 * in the popup and returns to the Multica window the card flips to
 * "Installed" automatically — no WS event or polling needed.
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
      window.open(resp.install_url, "_blank", "noopener,width=900,height=700");
      qc.invalidateQueries({ queryKey: slackStatusQueryKey(workspaceId, agent.id) });
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
          <p className="text-xs text-muted-foreground">{t(($) => $.slack_tab.install_hint)}</p>
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

      {error && (
        <p className="text-xs text-destructive" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
