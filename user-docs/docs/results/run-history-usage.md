---
title: Agent Ops console
agentPrompt: >-
  Read https://gratefulagents.dev/docs/results/run-history-usage/ and explain how to use the Agent Ops console to monitor live runs, find the ones that need attention, and review recent outcomes.
---

# Agent Ops console

The **Agent Ops** console at **Runs** is the workspace view for operating on current and recent runs. It replaces a generic run-history list with buckets, attention signals, filters, saved operations views, comparisons, and eligible bulk actions.

Open a row to enter its run session.

## Buckets and attention

The overview divides loaded runs into these buckets:

| Bucket | Meaning |
| --- | --- |
| **Needs attention** | A run has an actionable condition. |
| **Active** | A run is working now. This is the default bucket. |
| **Queued** | A run is waiting to start. |
| **Recently completed** | A run finished recently. |

Select **All loaded** to remove the bucket restriction. A run row can also show an attention label and the latest activity or blocking reason, so use the row details to understand why it needs action.

## Find and organize runs

Use the search box to search runs, repositories, sources, and activity. Add filters for **Status**, **Mode**, **Source**, and **Repo**.

The view controls also let you:

- Restrict completed-run recency to **Last 24 hours**, **Last 7 days**, **Last 30 days**, or **All loaded**.
- Filter cost by **Any cost**, **Under $1**, **$1 to $5**, or **Over $5**.
- Group by **Operational state**, **Status**, **Project / source**, **Repository**, or **Mode**.
- Sort by **Attention first**, **Newest**, **Oldest first**, **Highest cost**, **Status**, **Mode**, **Project / source**, **Repository**, or **Name**.

The console only organizes the runs it has loaded. A recency choice limits terminal runs; it does not change active or queued runs into historical records.

## Saved operations views

Choose **Saved views** and then **Save current view…** to save the current filters, bucket, age and cost settings, grouping, and sort order. Select a saved view to apply it, or delete it from the same menu.

Saved operations views are stored on the current device. They are not shared with teammates or synchronized between browsers.

## Act on one or many runs

Select one or more rows to expose bulk actions. Availability depends on the run state and your permissions. Eligible actions include:

- **Stop** active runs.
- **Retry** failed or stopped runs from their persisted state.
- **Mark as succeeded** for eligible non-terminal runs.
- **Extend runtime…** for eligible runs. Enter a duration; paused eligible runs resume automatically.

The confirmation dialog identifies how many selected runs are eligible. Stopping preserves history and diffs. Retrying does not create a new run. See [Interactive, plan, autopilot, stop, retry](../runs/plan-autopilot-stop.md).

## Compare attempts and related runs

Use **Compare attempts** on a run with related attempts, or compare selected runs when offered. The comparison shows outcome, runtime, cost, tokens, tools, result or attention, and artifacts side by side.

Use it to distinguish a transient failure from a behavioral regression before retrying or changing the task.

## Team progress graph

Expand a run with child work to open **Team progress**. The graph shows the parent, its child runs, each child’s role and step, status, and any blocking reason. For declared team work, it also shows task dependencies and the summary of succeeded, running, and failed children.

This is an operations view of the run team. For the session-level activity graph, open the run and use the **Graph** tab.

## Usage and observability

Use **Observability** for historical cost, reliability, charts, and breakdowns across visible runs. See [Observability](./observability.md). Run-specific cost and context information remains available in the run session.
