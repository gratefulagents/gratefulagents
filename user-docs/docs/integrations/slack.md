---
title: Slack
agentPrompt: >-
  Read https://gratefulagents.dev/docs/integrations/slack/ and explain what the gratefulagents Slack connection does, then help me connect my Slack workspace so I can start and steer runs from Slack.
---

# Slack

Create a Slack connection and a Slack Entry point from **Project → Entry points**. The app no longer provides standalone Slack-agent, shared-workspace-app, inbox, draft-approval, or Slack detail workflows for Project setup.

Related pages: [Projects](../projects/projects.md), [Run defaults](../projects/run-defaults.md), [Cron schedules](../projects/cron.md), and [Linear](./linear.md).

## Create a Slack connection

1. Open the Project's **Entry points** and select **Manage connections**.
2. Select **New connection**, enter a DNS-style **Name**, and select **Slack**.
3. Enter **Tokens Secret** and, if useful, **Team ID**.
4. Select **Create connection**.

| Field | Required | Behavior |
| --- | --- | --- |
| **Tokens Secret** | Yes | Name of a same-namespace Kubernetes Secret containing `bot-token` and `app-token` for Socket Mode. It can also contain optional `user-token`. See [Connection Secrets](./connection-secrets.md). |
| **Team ID** | No | Optional Slack workspace/team identifier. |

The form stores Secret references, not token values. The bot token is the bot identity and the app token opens the Socket Mode connection. A user token is optional for Slack search and can let the connector resolve the owner when no Slack user ID is set.

A connection is reusable only in its namespace. Its name and type are immutable, and it cannot be deleted while a Project Entry point references it.

## Create a Slack Entry point

1. In **Entry points**, select **New trigger**.
2. Enter the trigger **Name**, choose **Slack**, and select a Slack **Connection**.
3. Enter the required **Channel** value, such as `#engineering`.
4. Select **Create trigger**.

| Field | Required | Behavior |
| --- | --- | --- |
| **Name** | Yes | DNS-style trigger identifier. `manual` is reserved. |
| **Connection** | Yes | A Slack connection in the Project namespace. |
| **Channel** | Yes | Channel value recorded for this Project Entry point. |

The Entry point inherits the Project's repository defaults, provider and credentials, runtime, Skills, MCP policy, and custom instructions. It has no separate Slack run-defaults form.

The current dialog does not expose Slack user ID, inbox monitoring, shared workspace apps, allowed commanders, reply mode, or conversation-memory settings. Do not expect those removed standalone configuration workflows to be available from a Project Entry point. At the generated-runtime level, channel replies default to requiring approval, no additional commanders are configured, and the default conversation idle window is 12 hours.

## Status and lifecycle

The Entry-points rail displays the last Slack event and one of these states: **applying** before readiness is reported, **ready** when the generated connector is ready, **degraded** when it reports an error or non-ready state, or **disabled** when its switch is off.

Use the switch to disable or re-enable the generated Slack connector. **Edit** can change the connection and channel but not the source type. **Delete** permanently removes the Entry point and generated connector; existing runs remain. See [Projects](../projects/projects.md#entry-points-and-connections) for shared lifecycle rules.
