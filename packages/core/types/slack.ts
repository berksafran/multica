/**
 * Per-agent Slack integration responses returned by the backend
 * /api/workspaces/:wsId/agents/:agentId/slack* routes.
 *
 * The shape is intentionally narrow because an older desktop binary may
 * outlive the backend it was built against — every consumer treats every
 * field as possibly absent and falls back to a "not connected" view.
 */

export interface AgentSlackStatusResponse {
  /** True when the deployment has SLACK_CONFIG_TOKEN + SLACK_TOKEN_ENC_KEY
   * set. When false the Connect button is hidden — there is no useful
   * flow the user could complete. */
  configured: boolean;
  /** A Slack App has been created via manifest.create for this agent. */
  provisioned: boolean;
  /** OAuth install has completed — bot token stored. */
  installed: boolean;
  /** Per-app OAuth client_id + client_secret are stored. False means
   * the install URL cannot be built yet — UI prompts the user to
   * paste them. */
  has_credentials: boolean;
  app_id?: string;
  team_id?: string;
  bot_user_id?: string;
  status?: string;
  /** When provisioned + has_credentials but not yet installed, the URL
   * the user should open to install the app into a Slack workspace. */
  install_url?: string;
  /** Per-agent count of messages preceding an in-thread mention to
   * fetch from Slack and prepend to the user turn. 0 = dormant. The
   * server enforces an upper bound of 20. Optional on the wire because
   * an older backend won't return it; UI defaults to 0 in that case. */
  recent_context_thread_count?: number;
  /** Same as above for top-level channel mentions. Independent so a
   * busy channel can stay context-free while threads (smaller, more
   * focused) opt in. */
  recent_context_channel_count?: number;
}

export interface UpdateAgentSlackSettingsRequest {
  recent_context_thread_count: number;
  recent_context_channel_count: number;
}

export interface ProvisionAgentSlackResponse {
  app_id: string;
  install_url: string;
}

/** Backend never returns the client_secret value; the boolean flag is
 * the only signal the UI gets about its presence. */
export interface AgentSlackCredentialsResponse {
  client_id: string;
  has_client_secret: boolean;
}

export interface UpdateAgentSlackCredentialsRequest {
  client_id?: string;
  client_secret?: string;
}

/** Verify endpoint result — probes Slack to confirm the app still
 * exists on their side. The UI uses app_exists=false to render an
 * orphan banner and offer one-click cleanup.
 *
 * request_url_stale catches the silent-dead-integration case: the
 * deployment's public host changed (ngrok restart, domain swap) and
 * Slack is still POSTing events to the old URL. UI nudges the user
 * to re-sync the manifest. Older backends omit the field; UI treats
 * undefined as "not stale" so a stale banner never fires on partial
 * data. */
export interface AgentSlackVerifyResponse {
  app_exists: boolean;
  error?: string;
  request_url_stale?: boolean;
  expected_request_url?: string;
  current_request_url?: string;
}
