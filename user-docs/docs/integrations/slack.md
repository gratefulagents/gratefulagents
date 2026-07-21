---
title: Slack
agentPrompt: >-
  Read https://gratefulagents.dev/docs/integrations/slack/ and explain what the gratefulagents Slack connection does, then help me connect my Slack workspace so I can start and steer runs from Slack.
---

# Slack

Create a Slack connection and Slack Entry point from **Project → Entry points**. A connection stores the Slack app credentials and owner identity; an Entry point controls where and how that app responds for the Project.

Related pages: [Projects](../projects/projects.md), [Run defaults](../projects/run-defaults.md), [Cron schedules](../projects/cron.md), and [Linear](./linear.md).

## Create a Slack connection

1. Open the Project's **Entry points**, select **Manage connections**, then **New connection → Slack**.
2. Select **Copy Agent view manifest**. In [Slack app management](https://api.slack.com/apps), choose **Create New App → From a manifest** and paste the YAML. The manifest enables Slack's current Agent messaging experience, Socket Mode events, and the scopes used by the Slack tools.
3. Under **Basic Information → App-Level Tokens**, generate an `xapp-` token with `connections:write`.
4. Under **OAuth & Permissions**, install the app and copy its `xoxb-` bot token. If the agent should search the workspace or resolve the owner automatically, also copy the optional `xoxp-` user token.
5. Enter the credentials and owner identity, then select **Create connection**.

| Field | Required | Behavior |
| --- | --- | --- |
| **Bot token** | Yes for pasted credentials | Write-only `xoxb-` token used for bot identity, reading conversations, and posting. |
| **App token** | Yes for pasted credentials | Write-only `xapp-` token that opens the Socket Mode connection. |
| **User token** | No | Write-only `xoxp-` token that enables workspace search and automatic owner-ID resolution. |
| **Owner Slack user ID** | Yes unless a user token resolves it | Slack member ID (`U…` or `W…`) authorized to DM the agent and perform owner-only actions. |
| **Team / workspace ID** | No | Expected Slack team ID (`T…`). The connector rejects credentials for a different workspace. |
| **Tokens Secret** | Alternative | Advanced same-namespace Kubernetes Secret containing `bot-token` and `app-token`, plus optional `user-token`. See [Connection Secrets](./connection-secrets.md). |

Pasted tokens are moved into a platform-managed Secret and are never returned by the API. Editing a connection with empty token fields keeps the stored values; select **Remove the stored user token** to revoke optional workspace search. A connection's name and type are immutable, and it cannot be deleted while a Project Entry point references it. Because Slack load-balances Socket Mode events across sockets, one Slack connection may be used by only one enabled Entry point.

## Create a Slack Entry point

1. In **Entry points**, select **New trigger → Slack** and choose a Slack connection.
2. Optionally enter a Slack **#channel name or Conversation ID** to scope the agent to one conversation. The dashboard resolves a `#channel-name` through Slack when you save and persists its stable conversation ID. You can also open the channel details, select **About**, and copy the ID directly. Leave the field empty to let the agent respond in any conversation the bot is invited to and @mentioned in.
3. Configure who may command the agent, how channel replies are posted, and how long conversations reuse a run.
4. Enter a DNS-style trigger name and select **Create trigger**.

| Field | Required | Behavior |
| --- | --- | --- |
| **Name** | Yes | DNS-style trigger identifier. `manual` is reserved. |
| **Connection** | Yes | Slack app credentials and owner identity from the Project namespace. |
| **Conversation** | No | A `#channel-name` resolved at save time, or a stable Slack ID beginning with `C`, `G`, or `D`. Empty means the agent responds wherever the bot is invited and @mentioned. DMs with the owner always work regardless of scope. The bot must be invited to channels it watches. |
| **Allowed commanders** | No | Additional comma-separated Slack user IDs (`U…` or `W…`) allowed to invoke the agent by mention. Empty means owner only. |
| **Channel replies** | No | **Require owner approval** (default) holds shared-channel replies for approval; **Post directly** sends them immediately. DM and Agent-view replies remain direct. |
| **Conversation memory** | No | Positive idle time in minutes before a new conversation starts a fresh run. Empty uses the 12-hour default. |

The Entry point inherits the Project's repository, model/provider credentials, runtime profile, Skills, MCP policy, and custom instructions. Lifecycle (`enabled`) is controlled by the Entry-point switch. Connector images and shared-workspace topology remain operator-owned rather than per-trigger fields.

## Status and lifecycle

The Entry-points rail displays the last Slack event and one of these states: **applying** before readiness is reported, **ready** when the generated connector is ready, **degraded** when it reports an error or non-ready state, or **disabled** when its switch is off.

Use the switch to disable or re-enable the generated Slack connector. **Edit** can change the connection, conversation, commanders, reply policy, and memory window without losing advanced settings. **Delete** permanently removes the Entry point and generated connector; existing runs remain. See [Projects](../projects/projects.md#entry-points-and-connections) for shared lifecycle rules.
