---
title: Review run activity, graph, errors, trace, and usage
seoTitle: Monitor Agent Runs, Traces, Errors, and Usage | GratefulAgents
description: Follow live agent activity, subagent graphs, errors, traces, token use, and cost from a GratefulAgents run session.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/runs/review-activity/ and explain how to review a gratefulagents run end to end: the activity feed, subagent graph, errors, trace, and usage tabs.
---

# Review run activity, graph, errors, trace, and usage

Run sessions use tabs to separate the conversation from supporting evidence. **Chat**, **Graph**, **Diff**, and **Errors** are always available. **PR** appears after the run has a pull request, and **Trace** appears when the run has an OpenTelemetry trace ID.

## Chat

**Chat** is the chronological session timeline. It shows your messages, agent replies, grouped activity, questions and approvals, pending input, and live working state. See [Chat with an agent](./chat-with-agent.md).

## Graph

**Graph** visualizes delegated and subagent work. Use it to see parallel tasks, dependencies, and the status of child work. It is most useful when a run delegates research, implementation, testing, or review.

While delegated work is active, the **Chat** tab also pins a subagent dock above the composer. Its summary row shows the total ("Delegated 6 tasks") with running and waiting counts, and expands into the full task roster grouped into dependency waves: tasks in the same wave run in parallel, and later waves appear below a divider naming the tasks they run after. Every task gets a stable number (#1, #2, …); dependent tasks state exactly which tasks they run after ("after #1, #3"), and gated tasks show "Waiting on #n" for their unfinished dependencies. Hover a dependency reference for the full task titles. Cards show each subagent's status, elapsed time or duration, current activity, model, tokens, and cost, and the dock's expand icon opens the full **Graph** tab. The delegation entry in the chat transcript uses the same numbering and "after #n" references. The dock disappears once all delegated tasks finish; the roster remains in the transcript and the **Graph** tab.

## Diff

**Diff** shows repository changes and any available new files. If the run has multiple workspace repositories, select the repository before reviewing changes. A live diff can change while the run is active, and large diffs or new-file lists can be truncated.

See [Diffs and pull requests](./diffs-and-pull-requests.md).

## PR

**PR** lists pull requests associated with the run, including checks and review threads. You can refresh the panel and, when you can send messages, select unresolved review threads and use **Send to agent** to create a focused follow-up.

See [Pull request feedback](../results/pull-request-feedback.md).

## Errors

**Errors** keeps recovered and terminal run errors visible after retries. It can include errors reported by the worker pod, run status, or activity. It intentionally excludes routine pod output and trace data. The tab retains the 200 most recent errors.

Use **Errors** for a concise failure history; use **Trace** when you need span-level timing and telemetry.

## Trace

**Trace** displays OpenTelemetry spans when the run emits tracing data. It includes a waterfall view and, when available, usage summaries and task breakdowns for top-level work and subagents. A trace can be unavailable or contain no spans even when the run has activity.

The Trace tab is not Meta-Harness capture. Meta-Harness is an operator-controlled archive for platform self-development; see [Meta-Harness trace capture](./meta-harness-traces.md).

## Usage and context

The session can show run cost and context-window usage. The Trace tab can also show recorded input and output tokens, tool usage, and task-level usage when that data is available.

High context usage means the agent is carrying substantial history. If the next step does not need that detail, ask for a compact handoff:

```text
Before continuing, summarize the current state, files changed, tests run, decisions made, and remaining risks in 10 bullets.
```
