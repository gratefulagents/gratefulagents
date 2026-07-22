---
title: Observability
seoTitle: Coding-Agent Observability, Cost, and Reliability | GratefulAgents
description: Analyze coding-agent spend, token use, tool calls, subagents, errors, reliability, and model performance across GratefulAgents runs.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/results/observability/ and explain what the gratefulagents Observability dashboard shows and how to use it to compare cost, tokens, and reliability across runs.
---

# Observability

**Observability** tracks historical usage, cost, and reliability across runs visible to you. Open it from **Observability** in the app.

This page aggregates recorded historical data. It is not a live activity feed and it does not replace the run session's **Trace** tab.

## Select a range

Choose one of these ranges:

- **24h**
- **7d**
- **30d**
- **90d**

The **24h** range uses hourly points. Longer ranges use daily points. Changing the range reloads the historical summary.

## Read the totals

The summary cards show totals for the selected range:

| Card | What it counts |
| --- | --- |
| **Runs** | Recorded runs. |
| **Run cost** | Generation-attributed cost in USD. |
| **Run tokens** | Generation-attributed input and output tokens. |
| **Tool calls** | Recorded tool calls. |
| **Subagents** | Recorded subagent tasks. |
| **Errors** | Tool errors, subagent failures, and model failures. |

Use the totals to identify costly or unreliable periods, then use Agent Ops or an individual run to investigate the relevant work.

## Charts and breakdowns

The page charts the selected range for:

- **Cost**
- **Tokens**
- **Tools**
- **Subagents**
- **Compactions**
- **Errors**

Hover a chart point to see its value. Each chart also includes **View accessible data**, which exposes the same values as a table.

The bottom breakdowns list up to five entries each:

- **Top tools**, by call count.
- **Subagent roles**, by task count.
- **Models**, by cost, with input and output token detail.

## Partial historical coverage

The page can show **Partial historical coverage** when some sessions do not have complete metrics or activity. Open that section to read the coverage warnings.

Treat totals and charts as the recorded subset when this warning is present. Missing coverage is not evidence that no work, cost, or error occurred.

## Empty and unavailable data

**No historical metrics were recorded in this range** means there is no recorded data for the selected range. If the page cannot load metrics, it shows an error and **Retry**. If a refresh later fails, the page can continue to show the last successfully loaded data.

For a single run's spans, timing, and task usage, open that run and use **Trace**. For operator-level Meta-Harness archives, see [Meta-Harness trace capture](../runs/meta-harness-traces.md).
