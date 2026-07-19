---
title: Linear
agentPrompt: >-
  Read https://gratefulagents.dev/docs/integrations/linear/ and explain what the gratefulagents Linear connection does, then help me connect it so Linear issues can create and steer runs.
---

# Linear

Create Linear connections and Linear Entry points from **Project → Entry points**. The app no longer uses a standalone Linear sidebar, detail page, run table, or instructions page for this workflow.

Related pages: [Projects](../projects/projects.md), [Run defaults](../projects/run-defaults.md), [Cron schedules](../projects/cron.md), and [GitHub](./github.md).

## Create a Linear connection

1. Open the Project's **Entry points** and select **Manage connections**.
2. Select **New connection**, enter a DNS-style **Name**, and select **Linear**.
3. Enter the Secret reference and optional workspace identifier.
4. Select **Create connection**.

| Field | Required | Behavior |
| --- | --- | --- |
| **API Key Secret** | Yes | Name of a same-namespace Kubernetes Secret containing the Linear API key under `api-key`. The form stores the reference, not the key value. See [Connection Secrets](./connection-secrets.md). |
| **Workspace ID** | No | Optional Linear workspace identifier. |

The connection must be a Linear connection in the same namespace as the Project. Its name and type cannot be changed after creation, and it cannot be deleted while an Entry point references it.

## Create a Linear Entry point

1. In **Entry points**, select **New trigger**.
2. Enter the trigger **Name**, choose **Linear**, and select a Linear **Connection**.
3. Enter **Team ID** and **Project ID**.
4. Select **Create trigger**.

| Field | Required | Behavior |
| --- | --- | --- |
| **Name** | Yes | DNS-style trigger identifier. `manual` is reserved. |
| **Connection** | Yes | A Linear connection in the Project namespace. |
| **Team ID** | Yes | Linear team identifier. |
| **Project ID** | Yes | Linear project identifier. |

The Entry point inherits the Project's repository, provider and credentials, runtime, Skills, policies, and custom instructions. It does not have separate Linear instructions or run defaults. Configure shared guidance in **Project → Settings → Advanced → Custom instructions**.

## Current intake behavior

The generated Linear runtime polls using its configured project and team. Linear's automatic intake requires both an approved-label workflow and automatic task creation. At the runtime level, the defaults are the `ai-approved` intake label, a 30-second poll interval, and a transition to `ai-in-progress` after a run is created successfully.

The current **New trigger** dialog exposes only connection, Team ID, and Project ID. It does **not** expose an approval-label, polling-interval, or automatic-task-creation field. A Linear Entry point created through this dialog therefore does not automatically create runs from approved Linear issues. Do not rely on it as an automatic Linear issue intake until the UI exposes and enables that setting.

## Status and lifecycle

The Entry-points rail shows last poll activity and one of these states: **applying** before readiness is reported, **ready** when the generated runtime is ready, **degraded** when it reports an error or non-ready state, or **disabled** when its switch is off.

Use the switch to disable or re-enable the generated Linear runtime. **Edit** can change the connection, Team ID, and Project ID but not the source type. **Delete** permanently removes the Entry point; existing runs remain. See [Projects](../projects/projects.md#entry-points-and-connections) for shared lifecycle rules.
