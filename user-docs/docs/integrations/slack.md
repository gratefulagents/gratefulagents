---
title: Slack
seoTitle: Connect Slack to GratefulAgents AI Coding Agent | GratefulAgents
description: Set up a Slack connection and Entry point in GratefulAgents to start and steer AI coding agent runs by @mentioning the bot in any channel or DM.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/integrations/slack/ and explain what the gratefulagents Slack connection does, then help me connect my Slack workspace so I can start and steer runs from Slack.
---

# Slack

Create a Slack connection and Slack Entry point from **Project → Entry points**. A connection stores the Slack app credentials and owner identity; an Entry point controls where and how that app responds for the Project.

Related pages: [Projects](../projects/projects.md), [Run defaults](../projects/run-defaults.md), [Cron schedules](../projects/cron.md), and [Linear](./linear.md).

## Create a Slack connection

1. Open the Project's **Entry points**, select **Manage connections**, then **New connection → Slack**.
2. Select **Copy Agent view manifest**. In [Slack app management](https://api.slack.com/apps), choose **Create New App → From a manifest** and paste the YAML. The manifest enables Slack's current Agent messaging experience (agent sessions, the native stop button, streamed markdown replies), Socket Mode events, and the scopes used by the Slack tools. For an app created from an older manifest, re-apply the current YAML under **App Manifest** so it subscribes to the `agent_session_stopped` event; existing tokens stay valid.
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

The Entry point inherits the Project's repository, model/provider credentials, runtime profile, Skills, and custom instructions. Lifecycle (`enabled`) is controlled by the Entry-point switch. Connector images and shared-workspace topology remain operator-owned rather than per-trigger fields.

## Talking to the agent

Each Slack thread (or DM conversation) is an **agent session**. While the agent works, Slack shows its native working indicator and a **Stop** button; the first message titles the session in the Agent sidebar. Pressing Stop interrupts the current turn the same way the dashboard's Stop does — the conversation stays resumable, and the agent confirms with a message in the thread. Only the connection owner and the configured commanders can stop the agent.

Replies stream into the thread as a single message rendered from the agent's markdown (headings, tables, code blocks, and links keep their formatting) and finish with Slack's thumbs up / thumbs down feedback buttons. Feedback is recorded on the run's activity timeline in the dashboard. When a channel reply is held for owner approval, the session shows as waiting until the owner approves, edits, or dismisses it. Long runs post a "still working" note after 20 minutes and keep the session in the working state until they finish.

Long replies are split into Unicode-safe payloads within Slack's 12,000-character markdown limit, rather than truncated. If streaming is unavailable, the connector posts the reply as one or more messages in the same thread and then posts the feedback controls separately. Approved drafts also use multiple messages when necessary. Across separate fallback messages, Markdown constructs spanning a boundary may render separately; the text is preserved. Delivery is best-effort: network/API failures can still leave partial output, and a live-workspace smoke test is recommended after installation.

## Operational controls in Home

All operational controls are in the app's **Home** tab; no slash commands are required or registered. The connection owner and configured commanders see up to ten active/recent runs (active first), repository/base-branch details, and PR/check summaries. Unknown users see only the introductory placeholder. In a shared-workspace app, a commander must map to exactly one agent; ambiguous mappings cannot operate it.

- **New Run** opens a form with a repository dropdown, a base-branch field, and the task to perform. The repository choices come from the primary and additional repositories already configured on the agent (inherited from the Project). Selection is checked again against live configuration when submitted. Configure repository access/defaults through the existing Project and run-defaults dashboard controls.
- **Stop** interrupts the selected run's current turn without deleting its conversation.
- **Resume** opens a form for optional new instructions (otherwise it continues the previous task). It resumes the selected run without changing its repository or checkout.
- **Refresh** reloads run and PR/check state. Home also refreshes when opened and after control actions.

New Run always creates a fresh run; it never retargets an existing conversation. The branch field selects the new run's base branch, not a branch to force-push. Invalid ref syntax is rejected; a nonexistent branch fails during checkout. The form supports up to 100 configured repositories, Slack's static-select limit. Results, confirmations, and submission errors arrive privately in your DM. Use the run's Home controls for subsequent actions rather than relying on the ordinary DM's conversation mapping.

The owner also sees the pending approval count; approval cards remain in the owner's DM. No operational data is published to unauthorized users, and every button click and form submission is reauthorized. Removing access replaces the operational view with a placeholder on the next Home event. Existing Socket Mode and Block Kit interactivity settings are sufficient; there is no new Slack scope or command registration.

### PR and check notifications

The connector polls the platform's existing PR monitors once per minute and sends private notifications for PR lifecycle changes and completed checks, including failure results. Check results must match the current PR head and include both check-run and commit-status rollups; missing or errored observations are not reported as passing. Terminal run completion is also notified. The original requester receives notifications if still authorized, otherwise the owner does. This keeps automated notifications out of shared channels and does not bypass channel-reply approvals.

Delivery markers are persisted on the run, so routine polls and connector restarts do not repeat delivered updates. A short delivery lease prevents overlapping connectors from sending the same event concurrently. Failed attempts retry after the lease expires (two minutes). Delivery is at-least-once: a crash after Slack accepts a message but before its marker is saved can produce a duplicate. On first deployment, existing monitored runs may report their latest state. PR/check notifications require the platform PR artifact/monitor controllers and working GitHub credentials; they do not create a separate GitHub webhook subscription.

## Status and lifecycle

The Entry-points rail displays the last Slack event and one of these states: **applying** before readiness is reported, **ready** when the generated connector is ready, **degraded** when it reports an error or non-ready state, or **disabled** when its switch is off.

Use the switch to disable or re-enable the generated Slack connector. **Edit** can change the connection, conversation, commanders, reply policy, and memory window without losing advanced settings. **Delete** permanently removes the Entry point and generated connector; existing runs remain. See [Projects](../projects/projects.md#entry-points-and-connections) for shared lifecycle rules.
