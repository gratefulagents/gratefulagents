---
title: Maintainer delivery and waiter cutover
seoTitle: Maintainer Work-Item Delivery and Cutover | GratefulAgents
description: Reference for MaintainerWorkItemCommand delivery rules, guarded merge conditions, waiter cutover, and branch-protection requirements in GratefulAgents.
---

> Internal engineering/reference document — not part of the published user guide.

# Maintainer work-item delivery and waiter cutover

Maintainer delivery uses authenticated `MaintainerWorkItemCommand` resources. The standing maintainer submits intent; the repository controller owns irreversible side effects.

## Guarded delivery commands

`request_merge` binds a command to the current work-item projection sequence (the semantic optimistic-concurrency token; the work item's `resourceVersion` is recorded but no longer a rejection gate, because it churns on non-semantic status writes), `owner/repository`, pull-request number, and exact 40-character head SHA. Typed command tools wait up to 45s for the controller's terminal phase and return it in the receipt; a receipt with `awaiting_controller: true` is the only case that still requires observing `latest_command` through the waiter. Repository configuration must set `spec.maintainer.allowPullRequestMerge: true`, or opt into `spec.maintainer.fullControl: true`.

By default, the controller waits for human approval and `allowPullRequestMerge` only removes the final human merge step. The higher-risk `fullControl` option lets the maintainer manage and merge its pull requests without human approval. Full control requires branch protection or rulesets with required status checks enabled and required approving reviews disabled. The controller GitHub App must still have merge permission without repository admin or ruleset-bypass authority.

Immediately before merge, the controller re-reads GitHub and requires the pull request to remain open, non-draft, mergeable, and at the expected head. Approval is required unless full control is enabled. Check runs and commit statuses are re-read for that head. It requires GitHub branch protection to report server-enforced required checks (and required reviews outside full control), proves the actor has repository merge (`push`/`maintain`) permission, and rejects an actor with repository `admin` permission; do not add the controller GitHub App to a ruleset bypass list. **Zero total reported checks/statuses fails closed**, as do a blank review decision when approval is required, stale/error observations, a changed head, or an unconfirmed mergeability result. Before calling GitHub, the controller durably reserves a merge attempt. It then re-reads GitHub and records success only when `MERGED`, `mergedAt`, and the expected merged head are all confirmed. A queued or ambiguous attempt remains retryable only for verification, is never automatically resubmitted, and does not imply delivery. When a merge is first recorded, the controller delivers one idempotent `[maintainer]` message per other open fleet pull request in the repository asking its implementer to merge the base branch and re-verify, so the standing maintainer does not orchestrate rebases itself.

`finalize_work_item` carries an explicit semantic delivery summary and evidence. Its authenticated command proof is persisted in an attestation bound to the SHA-256 hash of the accepted scope. The controller does not interpret arbitrary free-text acceptance criteria. It structurally requires:

- every required pull request is re-confirmed merged at its projected head;
- every declared child and dependency has an exact name/UID projection and is re-read as durably finalized;
- the command remains bound to the immutable work-item UID, and implementer side effects remain bound to projected AgentRun UIDs;
- no decision is pending;
- the exact projected implementer-run set is supplied and no implementer failed or was cancelled; and
- an accepted scope exists and matches the attestation hash.

Run-success requests are written before issue closure. Each side effect and its audit progress are idempotent, so a close outage or controller restart can retry without losing the attestation or claiming final success. The work item becomes `Delivered` only after the issue is re-read as `closed` with reason `completed`.

The default maintainer mode no longer authorizes generic `merge_pull_request`, `mark_run_succeeded`, or `close_github_issue` mutations. Those tools can remain registered for other explicitly authorized modes.

## Waiter v2 migration

`spec.maintainer.workItemCutover` is rollbackable:

| Value | Behavior |
| --- | --- |
| `Controller` (default) | `wait_for_repo_events` reads only durable semantic work-item/issue observations and watches from the list resource version. A no-cursor snapshot durably checkpoints per-item projection sequences on the maintainer `AgentRun`; later calls use `cursor: "latest"`. No direct waiter GitHub or PR polling occurs. |
| `DualRead` | Legacy polling remains authoritative while the semantic source is shadow-read and parity is reported in the waiter result. Use this before cutover on an existing installation. |
| `Legacy` | Restores the prior issue/fleet/PR polling and signature cursor for rollback. |

A projection sequence increments only when observable semantic status changes. Every reconnect first lists the current snapshot and starts its watch from that list's resource version, so a change between list and watch is replayed. New GitHub issues enter through the repository controller's durable issue-observation reconciliation; the Kubernetes watch is notification after durable observation, not a replacement for GitHub discovery.

The controller pre-creates a compact, `AgentRun`-owned Kubernetes Secret for the `latest` checkpoint; the worker receives only resource-name-scoped read/update access. The checkpoint is bound to both the immutable maintainer-run UID and `GitHubRepository` UID and advances with Kubernetes resource-version compare-and-swap. It therefore survives worker and tool-runtime reconstruction but not deletion/recreation of either identity. Checkpoints expire after 30 days without a successful wait; omission remains the explicit reset/snapshot operation. Unknown, malformed, stale, expired, and cross-boundary handles fail closed. Encoded waiter-v2 cursors remain accepted as input for compatibility, but the waiter no longer returns the encoded `cursor` field (it was more than half of every wait payload); maintainers use `cursor: "latest"`. Watch-stream recycles are re-established inside the tool; `reconnect_required` is only reported after repeated reconnect failures.

Recommended rollout:

1. Upgrade CRDs/controller and set selected repositories to `DualRead`.
2. Observe `semantic_parity` results through normal maintainer waits.
3. Set repositories to `Controller` after parity is established.
4. Roll back to `Legacy` if required; start one wait without the old cursor after changing cursor formats.

## Standing run lifecycle

The maintainer `AgentRun` is a standing run (label `platform.gratefulagents.dev/standing-run-role`). It is exempt from the `maxRuntime` pause that recycles ordinary runs; its spend is bounded by the cost cap and the per-episode turn budget. When an episode exhausts its turn budget the runtime enqueues a durable continuation prompt and resumes on the preserved transcript instead of parking on a circuit breaker (at most 12 consecutive automatic rollovers without a human or controller message). The repository controller nudges a Running maintainer that is parked on either `idle` or `circuit_breaker` input whenever open work exists, and re-evaluates open work every five minutes. Because the maintainer's mode is read-only, a resumed pod keeps its fresh base-branch clone instead of rewinding to the previous pod's workspace checkpoint, and the runtime fast-forwards the checkout to the base-branch tip after each waiter wake that reports changes.
