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
  app_id?: string;
  team_id?: string;
  bot_user_id?: string;
  status?: string;
  /** When provisioned but not yet installed, the URL the user should
   * open to install the app into a Slack workspace. */
  install_url?: string;
}

export interface ProvisionAgentSlackResponse {
  app_id: string;
  install_url: string;
}
