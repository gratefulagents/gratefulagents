/**
 * Client-side Slack app-manifest generator. It fills in the app name while
 * keeping the supported bot scopes and event subscriptions.
 *
 * Create the app from the output at https://api.slack.com/apps → Create New App
 * → From a manifest. It sets every scope, event, and feature the connector uses
 * (Socket Mode, the assistant pane, the App Home tab, Block Kit interactivity).
 */
export function buildSlackManifest(appName: string): string {
  const name = ((appName || "").trim() || "My Agent").slice(0, 35);
  const quoted = JSON.stringify(name);

  return `# Slack app manifest for a personal agent (Socket Mode, no webhooks).
display_information:
  name: ${quoted}
  description: My personal AI agent in Slack.

features:
  bot_user:
    display_name: ${quoted}
    always_online: true
  assistant_view:
    assistant_description: DM me a task, or @mention me in a channel.
    suggested_prompts: []
  app_home:
    home_tab_enabled: true
    messages_tab_enabled: true
    messages_tab_read_only_enabled: false

oauth_config:
  scopes:
    bot:
      - app_mentions:read
      - assistant:write
      - chat:write
      - im:history
      - im:write
      - reactions:write

settings:
  event_subscriptions:
    bot_events:
      - app_mention
      - app_home_opened
      - assistant_thread_started
      - message.im
  interactivity:
    is_enabled: true
  socket_mode_enabled: true
  org_deploy_enabled: false
  token_rotation_enabled: false
`;
}
