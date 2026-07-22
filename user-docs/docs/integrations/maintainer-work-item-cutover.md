---
title: Maintainer delivery and waiter cutover
---

# Maintainer work-item delivery and waiter cutover

Maintainer delivery uses authenticated `MaintainerWorkItemCommand` resources. The standing maintainer submits intent; the repository controller owns irreversible side effects.

## Guarded delivery commands

`request_merge` binds a command to the current work-item projection sequence and resource version, `owner/repository`, pull-request number, and exact 40-character head SHA. Repository configuration must set `spec.maintainer.allowPullRequestMerge: true`.

Immediately before merge, the controller re-reads GitHub and requires the pull request to remain open, non-draft, mergeable, approved, and at the expected head. Check runs and commit statuses are re-read for that head. It also requires GitHub branch protection to report server-enforced required reviews and checks, proves the actor has repository merge (`push`/`maintain`) permission, and rejects an actor with repository `admin` permission; do not add the controller GitHub App to a ruleset bypass list. **Zero total reported checks/statuses fails closed**, as do a blank review decision, stale/error observations, a changed head, or an unconfirmed mergeability result. Before calling GitHub, the controller durably reserves a merge attempt. It then re-reads GitHub and records success only when `MERGED`, `mergedAt`, and the expected merged head are all confirmed. A queued or ambiguous attempt remains retryable only for verification, is never automatically resubmitted, and does not imply delivery.

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
| `Controller` (default) | `wait_for_repo_events` reads only durable semantic work-item/issue observations and watches from the list resource version. The cursor contains only per-item persisted projection sequences. No direct waiter GitHub or PR polling occurs. |
| `DualRead` | Legacy polling remains authoritative while the semantic source is shadow-read and parity is reported in the waiter result. Use this before cutover on an existing installation. |
| `Legacy` | Restores the prior issue/fleet/PR polling and signature cursor for rollback. |

A projection sequence increments only when observable semantic status changes. Every reconnect first lists the current snapshot and starts its watch from that list's resource version, so a change between list and watch is replayed. New GitHub issues enter through the repository controller's durable issue-observation reconciliation; the Kubernetes watch is notification after durable observation, not a replacement for GitHub discovery.

Recommended rollout:

1. Upgrade CRDs/controller and set selected repositories to `DualRead`.
2. Observe `semantic_parity` results through normal maintainer waits.
3. Set repositories to `Controller` after parity is established.
4. Roll back to `Legacy` if required; start one wait without the old cursor after changing cursor formats.
