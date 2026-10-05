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

## Status and lifecycle

The Entry-points rail displays the last Slack event and one of these states: **applying** before readiness is reported, **ready** when the generated connector is ready, **degraded** when it reports an error or non-ready state, or **disabled** when its switch is off.

Use the switch to disable or re-enable the generated Slack connector. **Edit** can change the connection, conversation, commanders, reply policy, and memory window without losing advanced settings. **Delete** permanently removes the Entry point and generated connector; existing runs remain. See [Projects](../projects/projects.md#entry-points-and-connections) for shared lifecycle rules.

## Capability comparison and integration priorities

Reviewed October 5, 2026 against the current connector and linked upstream documentation. This is a representative comparison of prominent open-source options, not an exhaustive popularity ranking or a claim that unverified competitor features are absent. OpenClaw is the closest general-purpose agent comparison; Onyx is a knowledge/search-focused alternative; Slack's official open-source Bolt samples are the native-platform reference rather than a production orchestration product.

| Area | GratefulAgents | Open-source reference |
| --- | --- | --- |
| Slack entry points | Socket Mode, owner DMs, authorized channel mentions, threaded conversations, App Home | [OpenClaw](https://docs.openclaw.ai/channels/slack) additionally documents HTTP/relay ingress, Grid deployments and slash commands. [Slack's Casey sample](https://docs.slack.dev/ai/agent-quickstart/) covers DMs, mentions, App Home and modals. |
| Execution | Project-backed agent runs, repository context, resumable conversations, dashboard lifecycle and activity | OpenClaw is a broader personal-agent gateway. [Onyx](https://github.com/onyx-dot-app/onyx) emphasizes agentic RAG, indexed knowledge across 50+ applications, actions/MCP and sandbox execution. |
| Native agent UX | Agent View, initial session title, processing/active/suspended status, stop button, markdown stream delivery, reply feedback | [OpenClaw](https://docs.openclaw.ai/channels/slack/messaging) documents live previews and native plan/task progress cards. Our connector delivers stored assistant messages through Slack streams; that is not token-by-token model streaming or live tool progress. |
| Control and trust | Owner/commander authorization, owner approval/edit/dismiss for shared-channel replies, resumable stop | [OpenClaw](https://docs.openclaw.ai/channels/slack) documents pairing, allowlists, action gates and native approvals. These are comparable controls with different deployment assumptions, not identical authorization models. |
| Knowledge and media | Optional user-token workspace search, thread context and inbound file handling | Onyx's indexed multi-source retrieval is a different approach from our Slack read/search tools. OpenClaw documents audio, attachments and outbound file delivery. |
| Long replies and feedback | Bounded streaming payloads, multiple fallback messages, feedback even without streaming | OpenClaw documents chunking and multiple streaming modes. Slack's APIs require explicit payload limits and lifecycle handling regardless of framework. |

### P0/P1 delivered in this change

No prior P0/P1 list was available for this comparison; these are the concrete delivery gaps found in the existing implementation, not a claim to implement every feature below.

- **P0 — Prevent deterministic reply loss:** replace silent markdown truncation with Unicode-safe chunks for streamed replies and fallback/approved-draft messages. Preserve whitespace across streaming boundaries; reject oversized direct stream chunks instead of silently cutting them.
- **P1 — Feedback parity:** when stream creation is unavailable, post feedback once after successful fallback delivery. Keep feedback in a separate Block Kit message because `chat.postMessage` does not allow `markdown_text` together with `blocks` or `text`. Feedback failure must not discard an already delivered reply.

These are automatic delivery corrections: no new dashboard setting, OAuth scope, migration or manifest change is needed.

### Slack platform changes: already covered versus follow-up work

| Official feature/change | Current state and recommended action |
| --- | --- |
| [Agent messaging migration](https://docs.slack.dev/changelog/2026/08/20/agent-updates) | `assistant_view` is deprecated in February 2027. Our generated manifest already uses `agent_view`; session status and rename wrappers already use `agents.sessions.*`. Re-apply the current manifest for older installations. Do not redo this migration as new work. |
| [Agent context](https://docs.slack.dev/changelog/2026/07/02/app-context) | `app_context_changed` is subscribed and handled, alongside context-bearing messages. Channel context is not blanket permission to read every referenced canvas/list; broaden retrieval only with access checks. |
| [Native stop and session lifecycle](https://docs.slack.dev/ai/developing-ai-apps/) | Native stop is already handled with owner/commander authorization. Slack also recommends `agent_session_title_changed`; syncing user-renamed Slack titles into dashboard sessions is a separate follow-up, not currently implemented. |
| [Streaming and task/plan chunks](https://docs.slack.dev/reference/methods/chat.appendStream/) | Markdown streaming exists; task/plan progress does not. Highest-value next UX integration: map safe public activity summaries to task cards, throttle updates, finalize on success/error/stop. Do not expose raw private reasoning or command arguments. `chunks` and `markdown_text` cannot be mixed in one request, and stream mode must stay consistent. |
| [Real-time Search API](https://docs.slack.dev/reference/methods/assistant.search.context/) | Not integrated. Evaluate alongside existing user-token search, not as a silent replacement. Bot calls require an event `action_token`; new granular search scopes and caller-specific access controls must be designed before enabling. |
| [Official Slack MCP server](https://docs.slack.dev/ai/slack-mcp-server/) | Evaluate an explicit authenticated connection for search, messages, canvases, files and lists. Existing generic MCP support is not proof of official Slack MCP integration. Admin approval, OAuth and write-action policy need end-to-end validation. |
| [Slack Code](https://docs.slack.dev/changelog/2026/08/20/slack-code) | Not integrated. Potential fit for coding runs: dedicated work channels, diffs, previews and collaborative artifacts. Requires a separate lifecycle/permission design; not a prerequisite for current thread-based runs. |
| [CLI/SDK releases and discovery changes](https://docs.slack.dev/changelog/) | CLI manifest sync and code-channel support are developer tooling; Node/Bolt migrations do not apply to this Go connector. Agents & Tools discovery and Marketplace metadata are not APIs we need to implement. |

New APIs are not all mandatory. Preserve existing least-privilege behavior and approval boundaries rather than adding scopes or externally visible actions merely to match a feature list.

API contract references: [`chat.postMessage`](https://docs.slack.dev/reference/methods/chat.postMessage/), [`chat.appendStream`](https://docs.slack.dev/reference/methods/chat.appendStream/), and [Slack agent design](https://docs.slack.dev/concepts/agent-design/).
