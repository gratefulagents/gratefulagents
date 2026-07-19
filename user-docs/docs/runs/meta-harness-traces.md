---
title: Meta-Harness trace capture
agentPrompt: >-
  Read https://gratefulagents.dev/docs/runs/meta-harness-traces/ and explain what gratefulagents records in a run trace and how to read one to understand cost, latency, and failures.
---

# Meta-Harness trace capture

Meta-Harness is an operator-level observation feature for platform self-development. When capture is enabled for a run, the runner records its execution trace so a later analysis or proposer agent can study how the harness behaved.

It is not a user-facing scoring or comparison feature, and it is different from the run session's **Trace** tab:

- **Trace** displays the run's OpenTelemetry spans in the UI when a trace ID is available.
- **Meta-Harness** stores an encrypted execution archive for operator analysis when capture was enabled.

Capture is disabled by default. It applies only when a cluster operator enables it.

## Enable capture

Both enablement paths require cluster-level access:

- **One run:** set the `platform.gratefulagents.dev/metaharness` label on the `AgentRun`. Its value is recorded as the evaluation candidate identity (`candidate_id`).
- **All runs:** set `ENABLE_METAHARNESS=true` for the controller manager. Use this only in a dedicated self-development environment because it captures every tenant's runs.

There is no dashboard toggle. Treat capture as a sensitive operator control because traces can include prompts, model output, and tool results.

## Captured data

Capture includes the parent run and subagent work. It records LLM calls, tool invocations and their inputs and outputs, agent handoffs, mode switches, and structural spans such as session lifecycle, retries, and context compaction.

Known secret patterns, including bearer tokens, API keys, GitHub tokens, and `sk-…` keys, are redacted before storage. Redaction does not make a trace safe to share broadly: prompts, model output, and tool output can still contain sensitive information.

During a run, trace data is written in the runner workspace as NDJSON files with metadata and, after finalization, aggregate metrics such as tokens, cost, tool calls, turns, retries, and duration.

## Durable archive

Runner pods and their workspaces can be deleted, so the runner finalizes and uploads an archive when a run ends successfully, fails, or shuts down during pod replacement. It:

1. finalizes metadata and metrics;
2. archives the trace directory, encrypts it with the run's workspace snapshot key, and uploads it to the workspace object store; and
3. publishes the most recent archive location as `status.artifacts.metaHarnessTraceRef` on the run.

Each pod creates its own timestamped archive. A restarted run can therefore retain earlier-pod traces even though the status reference identifies only the most recent archive.

To inspect an archive, an authorized operator retrieves it from the workspace object store, decrypts it with the run's workspace snapshot key, and extracts it. This workflow is intentionally outside the user run session.

## Retention and deletion

Meta-Harness archives use the workspace object store under the `metaharness-traces/` prefix. Workspace checkpoint cleanup does not remove them.

Set the bucket lifecycle rule for that prefix to the retention period required for your self-development analysis. Deleting an `AgentRun` removes its status reference but does not remove archived objects; delete the run's `metaharness-traces/` prefix to purge its capture.
