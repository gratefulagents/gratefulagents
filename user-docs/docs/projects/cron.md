---
title: Cron schedules
seoTitle: Schedule Recurring AI Agent Runs with Cron | GratefulAgents
description: Create cron-scheduled Entry points in GratefulAgents to start AI coding agent runs automatically on a recurring schedule with a custom prompt.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/projects/cron/ and help me schedule a recurring agent run with a cron expression, including choosing what the scheduled run should do.
---

# Cron schedules

A Cron schedule is a Project Entry point that starts a run at scheduled times. Create and manage it from **Project → Entry points**; there is no separate Cron sidebar or detail workflow.

See [Projects](./projects.md) for the shared Entry-point lifecycle and [Run defaults](./run-defaults.md) for the defaults each Cron run inherits.

## Create a Cron Entry point

1. Open the Project.
2. In **Entry points**, select **New trigger → Scheduled**.
3. Write the **Prompt** (what each run should do), then pick **When**: a preset chip (**Every hour**, **Weekdays 9 am**, **Daily 9 am**, **Weekly Monday**) or **Custom** with a five-field cron expression. The preview line reads the schedule back in plain words with its time zone (`Weekdays at 09:00 · Europe/Berlin`).
4. Adjust the suggested **Trigger name** if you like and select **Create trigger**.

Cron does not use a connection. Its runs inherit the Project's repository, model, credentials, runtime, tools, policies, and custom instructions.

| Field | Required | Behavior |
| --- | --- | --- |
| **Prompt** | Yes | The first user message in every scheduled run. |
| **Schedule** | Yes | A standard five-field cron expression or a supported descriptor such as `@hourly`. Defaults to weekdays at 09:00. |
| **Time zone** | No | An IANA time-zone name; the field offers a searchable list and defaults to your browser's zone. Empty means UTC. |
| **Trigger name** | Yes | Suggested from the schedule (`weekdays-0900`); DNS-style, and it cannot be `manual`. |

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
