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

## Run controls in the Home tab

Everything operational lives in the app's **Home** tab; there are no slash commands to register. Anyone in the workspace who opens the app sees the header and info line configured on the dashboard. The connection owner and the configured commanders also see an **Operations** panel underneath:

- **New Run** and **Refresh** buttons, followed by the repositories the agent can work in and its default base branch.
- The owner's count of channel replies waiting for approval (the approval cards themselves stay in the owner's DM).
- One card per Slack run, active runs first, showing its status, repository, base branch, start time, and any pull requests it opened with their lifecycle and check results. Up to ten cards are shown.

Each card carries the controls that apply to its state:

- **Stop** (running turns only) asks for confirmation, then interrupts the current turn the same way the dashboard and Slack's native stop button do. The conversation stays resumable.
- **Resume** opens a form that summarizes the run and takes optional instructions. Leave it empty to continue the previous task from where it stopped (no synthetic message is added to the transcript); type instructions to make them the run's next turn. Resuming never changes the run's repository or checkout.
- **New Run** opens a form with a repository menu (the primary and additional repositories configured on the agent's Project), the base branch prefilled with the configured default, and the task. Invalid branch names and stale repository choices are reported inline in the form; a branch that does not exist fails during checkout. New Run always starts a fresh run and never retargets an existing conversation.

Confirmations, results, and notifications arrive privately in your DM with the app, and Home refreshes after every action. Every click and submission is re-authorized against the live owner and commander list; a user whose access was removed sees only the introductory copy the next time Home opens. In a shared-workspace app, a commander must map to exactly one agent; an ambiguous mapping cannot operate any of them. The form lists at most 100 repositories, Slack's menu limit. Existing Socket Mode and interactivity settings are sufficient; no new scope is needed.

### PR and check notifications

The connector polls the platform's PR monitors once per minute and sends private DMs when a run's pull request is opened, becomes ready, is merged, or is closed, and when every check on the PR's current head has finished, passed or failed. Check results are only reported once both the check-run and commit-status rollups for the current head are complete; missing or errored observations are never reported as passing. A run reaching a terminal phase is also announced. The original requester receives the notification while still authorized; otherwise the owner does. Notifications never go to shared channels and do not bypass channel-reply approvals.

Delivery markers are stored on the run, so routine polls and connector restarts do not repeat an update. A two-minute delivery lease keeps overlapping connectors from sending the same update, and a failed attempt retries once the lease expires. Delivery is at-least-once: a crash after Slack accepts a message but before its marker is saved can repeat it. On first deployment, existing monitored runs may report their current state. Notifications rely on the platform's PR artifact and monitor controllers and working GitHub credentials; no separate GitHub webhook is created.

## Status and lifecycle

The Entry-points rail displays the last Slack event and one of these states: **applying** before readiness is reported, **ready** when the generated connector is ready, **degraded** when it reports an error or non-ready state, or **disabled** when its switch is off.

Use the switch to disable or re-enable the generated Slack connector. **Edit** can change the connection, conversation, commanders, reply policy, and memory window without losing advanced settings. **Delete** permanently removes the Entry point and generated connector; existing runs remain. See [Projects](../projects/projects.md#entry-points-and-connections) for shared lifecycle rules.
