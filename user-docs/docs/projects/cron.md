---
title: Cron schedules
agentPrompt: >-
  Read https://gratefulagents.dev/docs/projects/cron/ and help me schedule a recurring agent run with a cron expression, including choosing what the scheduled run should do.
---

# Cron schedules

A Cron schedule is a Project Entry point that starts a run at scheduled times. Create and manage it from **Project → Entry points**; there is no separate Cron sidebar or detail workflow.

See [Projects](./projects.md) for the shared Entry-point lifecycle and [Run defaults](./run-defaults.md) for the defaults each Cron run inherits.

## Create a Cron Entry point

1. Open the Project.
2. In **Entry points**, select **New trigger**.
3. Enter a DNS-style **Name**, choose **Cron** as the source type, then enter **Schedule**, optional **Time zone**, and **Prompt**.
4. Select **Create trigger**.

Cron does not use a connection. Its runs inherit the Project's repository, model, credentials, runtime, tools, policies, and custom instructions.

| Field | Required | Behavior |
| --- | --- | --- |
| **Name** | Yes | Identifies this Entry point. It cannot be `manual`. |
| **Schedule** | Yes | A standard five-field cron expression or a supported descriptor such as `@hourly`. |
| **Time zone** | No | An IANA time-zone name. Leave it empty to use UTC. |
| **Prompt** | Yes | The first user message in every scheduled run. |

## Scheduling behavior

The default concurrency policy is **Forbid**: if an earlier run from the same Cron is still active, the due tick is skipped. The current Entry-point form does not expose a concurrency-policy control.

When a tick is skipped or the controller resumes after downtime, skipped times are not backfilled. A schedule or time-zone change starts from the next future matching time.

## Status and lifecycle

The Entry-points rail shows the Cron's last and next scheduled activity. Its badge is:

- **applying** until the generated schedule has reported activity or a Ready condition;
- **ready** when the runtime reports Ready or active scheduling;
- **degraded** when it reports an error or a non-ready state; or
- **disabled** when you turn off the Entry point switch.

Use the switch to disable scheduling without deleting the Entry point. Disabling removes the generated Cron runtime and stops new scheduled runs; existing runs are kept. Turn it back on to recreate the runtime.

Use **Edit** to change the schedule, time zone, or prompt. The source type remains Cron. Use **Delete** to permanently remove the Entry point; it does not delete runs already created by it.
