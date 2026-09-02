---
title: Maintainer loop review (Sept 2026)
seoTitle: Standing Maintainer Loop Review | GratefulAgents
description: Evidence-based review of the standing maintainer feature from production runs, the failure modes observed, user interventions required, loop-engineering research, and a prioritized improvement plan toward fully autonomous repository maintenance.
---

> Internal engineering document — not part of the published user guide.

# Standing maintainer loop review — September 2026

This review looks at how the `maintainer` mode actually behaved in production, where
it needed human help, and what has to change for it to run a repository without a
human babysitting it. Evidence comes from the durable session store (`activity_events`,
`conversation_messages`), the `GitHubRepository`/`AgentRun` objects in the cluster, and
the controller/tool source in this repository. External design references are in the
[research appendix](#appendix-loop-engineering-research).

## 1. Runs reviewed

| Run | Repo | Window | LLM steps | Tool calls | Cost | Outcome |
| --- | --- | --- | --- | --- | --- | --- |
| `project-quickfrence-gh-maintainer` | Hunter-Thompson/quickfrence (`fullControl: true`, `reviewLoop.disabled: true`, no CI) | 2026-09-02 00:14 → 13:39 UTC (still running) | 675 | 830 | $122 | 27 issues created by the maintainer itself, 26 dispatched, 25 PRs (#77–#101) merged and finalized |
| `project-gf-all-openai-gh-maintainer` | gratefulagents/gratefulagents (human merge, CI + review loop) | 2026-07-21 18:27 → 07-22 08:00 UTC | 432 | 354 | n/a | Decomposed #49 into #64–#66; 3 PRs delivered, all human-merged |

The 50 quickfrence implementer runs the maintainer supervised cost a further $309, so
coordination overhead was ~28% of total spend (≈ $0.18 per LLM step at the end of the
run, almost entirely prompt-cache reads of a 500–780K-token context).

### What worked

- The typed command protocol did what it was designed to do: 26 dispatches, 35 merge
  requests and 30 finalizations were all bound to projection sequences and head SHAs, and
  the controller re-verified every merge. No wrong-PR or double-merge incidents.
- Triage quality was high. Issues were grounded in code inspection, dependency graphs
  were declared and respected (`#55 ← #54`, `#57 ← #52`, `#102 ← #103`), and the
  maintainer deliberately chose non-overlapping items when slots freed.
- The user-requested product review (three read-only review sub-agents + one research
  pass) produced 25 evidence-backed issues in 20 minutes and was exactly what the user
  asked for.
- Reports were useful: `submit_maintainer_report` summaries were accurate, compact, and
  are the best audit trail we have.

## 2. Where it tripped up

### 2.1 The loop parks itself: per-message turn budget (critical)

`maintainer.yaml` sets `constraints.maxTurns: 200`. The SDK counts every LLM call as a
turn and the budget is **per user message**. A standing maintainer that blocks on
`wait_for_repo_events` and wakes on every projection change burns a turn per wake; in
quickfrence 302 of the 675 LLM steps were wake handling. The budget ran out three times:

| Exhausted | Recovered | Gap | How it recovered |
| --- | --- | --- | --- |
| 01:27:46 (mid-finalize of #67) | 06:14:34 | **4h 47m** | 6h run timeout paused the run → `MaintainerEngine` nudged the Paused run |
| 07:37:49 | 07:37:56 | 7s | A queued message happened to be pending |
| 10:11:48 (mid `request_merge` retry) | 10:31:56 | 20m | User typed `g` |

On exhaustion `cmd/agent/loop.go` sets `UserInputCircuitBreak` and parks the session.
`MaintainerEngine.maintainerRunWakeable` only nudges a *Running* run whose pending input is
`idle`, so a circuit-broken maintainer is invisible to the controller until something else
changes its phase. The July run shows the same shape (implementer #65 "exhausted its first
200-turn budget mid-implementation").

### 2.2 The 6h run timeout kills the standing pod (critical)

The maintainer run inherits `spec.defaults.timeout: 6h` from the repository. Pods were
terminated at 06:14:33 and 12:14:35 — exactly 6h after start and after the previous wake.
Each time:

- the run went `Paused`; the second time it stayed paused for **80 minutes** until the
  user extended `limits.maxRuntime` by 4h in the dashboard (13:34:14) and typed a message;
- the new pod restored 378–525 transcript items (so the bloated context came back), and
- the workspace was gone: "local checkout is stale (pre-#52) and `.git` is read-only, so
  I'll inspect the merged failover/run code via the PR tools" — the maintainer could no
  longer read the code it was maintaining and fell back to 32KB PR diffs.

The current run will pause again at ~17:34 UTC unless extended.

### 2.3 Context bloat: 780K tokens before compaction (high)

Peak `cache_read_input_tokens` reached 781,982 before the first compaction at 06:47, and
545K again after the 13:34 restart. Contributors, from tool output sizes:

| Source | Calls | Chars injected | Notes |
| --- | --- | --- | --- |
| `wait_for_repo_events` | 302 | 2.15 MB | **1.16 MB (54%) is the deprecated `cursor` blob** the prompt tells the agent to never use |
| `Bash` | 122 | 1.34 MB | mostly the initial product review |
| `get_pull_request` | 20 | 654 KB | full diffs read to "verify scope" because no reviewer existed |
| `WebFetch` | 46 | 263 KB | |

225 of 302 waits returned in under 2 seconds: the waiter is edge-triggered on *every*
projection-sequence bump, including the maintainer's own command receipts moving
`Pending → Succeeded`, `observedAt` refreshes and run-phase flips. Every typed command
therefore costs ~3 LLM steps (submit → wait sees Pending → wait sees Succeeded), and 48
assistant messages are of the form "Only the run-phase flip for #58. Continuing the
standing wait."

### 2.4 Optimistic-concurrency rejections drive LLM retries (high)

28 of 386 observed `latest_command` results were `Rejected`, 25 of them for
`target work-item resourceVersion does not match command preconditions`.
`resolveMaintainerCommandTarget` (maintainerworkitem_controller.go) requires the exact
`MaintainerWorkItem.resourceVersion`, which moves on **any** controller status write, not
just semantic changes. The maintainer learned to expect it ("Recurring papercut… the
projection ticks between event and command") and even pre-emptively predicted rejection
from the receipt. One merge (#68 / PR #88) was rejected six times as "not merge candidates
with fresh observations" while GitHub recomputed mergeability. Each rejection costs 2–4
LLM steps and, in the 10:11 case, was the step on which the turn budget expired.

### 2.5 Fully autonomous merges with zero independent verification (high, safety)

quickfrence had `fullControl: true`, `reviewLoop.disabled: true`, no branch protection and
no CI. `maintainerworkitem_delivery.go` allows the explicit zero-check path
(`observedChecks == 0 && rollups == None`), so 25 PRs were merged on the strength of the
implementer's self-reported test results ("full local checks green: vitest 780, Playwright
137, cargo 281+43" — quoted from PR bodies). The mode prompt says "do not perform code
review yourself… rely on the PR-loop reviewer verdict and reported checks", but there was
neither, so the maintainer improvised scope reviews from diffs.

The consequence surfaced the same afternoon: the user reported that one-click Run failover
now silently rents a $2/h instance when the user chose a $1/h one — behaviour introduced by
`#73` (offer failover), which the maintainer authored, triaged as Bounded, dispatched and
merged without any human seeing it. `maintainer-work-item-cutover.md` still says "zero
total reported checks/statuses fails closed", which is no longer accurate.

### 2.6 Self-authored backlog dispatched without owner acknowledgement (medium, governance)

The user asked: "do the research and create the github issues." The maintainer created
#52–#76 and then — because they were now open issues in its repository — triaged all 25 as
Bounded and drove them to merge over the next ten hours (≈ $430). It also noted while
triaging #74 that "this is really a product-expansion decision moving from macOS-only to
broader platform support" and dispatched anyway. Nothing in the mode distinguishes
issues the maintainer wrote from issues the owner filed, and the ESCALATE disposition was
never used in either run.

### 2.7 Maintainer-orchestrated rebase churn (medium)

Four concurrent implementers on a small codebase meant almost every PR conflicted with
`main` by the time it was reviewed. The maintainer sent 24 `wake_agent_run` messages,
most of them "PR #NN is NOT mergeable — it conflicts with main… merge main", and waited
for GitHub to recompute `mergeable` after each merge ("After #83 merged, PRs #84 and #85
lost their `mergeable` flag"). It is doing a merge queue's job by hand.

### 2.8 Watch stream recycles cost LLM turns (low)

The semantic watch closed after ~34–43 minutes of idleness three times
(`reconnect_required: true`, `watch_error: "semantic work-item watch closed"`). The tool
returns to the model instead of reconnecting internally, so each recycle is a paid,
budget-consuming turn that produces "Reconnecting with cursor `latest`".

### 2.9 Smaller papercuts

- The wait result for a snapshot without cursor (`changed: true, elapsed 0`) returned 25
  work items with full projections (32 KB) after the 07:37 budget reset.
- `Maintainer resume` messages from the controller were recorded as `cancelled` in both
  runs (the run resumed from the transcript instead), so the durable message log does not
  show why the run woke.
- After restart the maintainer noted rustup/pnpm/npm were unavailable in the read-only
  sandbox (DNS `EAI_AGAIN`), so "run the tests myself" was never an option.

## 3. User interactions observed

| When | Who | Message | Why it was needed |
| --- | --- | --- | --- |
| 09-02 00:16 | user | "do a review of the project… create the github issues" | Feature request routed through the maintainer chat |
| 09-02 06:14 | controller | "Maintainer resume: open work exists…" (cancelled) | Recovery after 6h timeout; 4h47m after the budget parked the run |
| 09-02 08:30 | user | `g` | Circuit-break after budget exhaustion |
| 09-02 10:31 | user | `g` | Circuit-break after budget exhaustion |
| 09-02 13:34 | user | dashboard: extend timeout +4h | Run paused by 6h timeout for 80 min |
| 09-02 13:35 | user | product bug report (stale offers, price jump) | Regression from an autonomously merged PR |
| 07-22 03:14 | user | "ive merged the pr" | No merge authority; human merge needed |
| 07-22 05:19 | user | "ignore the test failure, it happens sometimes" | Maintainer correctly refused to merge a red check but had no override path |
| 07-22 05:20 | user | "merged" | |
| 07-22 05:25 | user | "i retriggered" | Dispatch lost to a SandboxClaim/SandboxTemplate race |
| 07-22 07:29 | user | "ask it to fix pr review comments" | Reviewer feedback not relayed automatically |

Every intervention except the first and the bug report was the human acting as the
loop's scheduler or its missing gate — the two things an autonomous maintainer must own.

## 4. Root causes

1. **The conversation is the episode.** One LLM thread is expected to live forever, so
   every per-message safety limit (turn budget, run timeout, context size) is a bug for
   this mode rather than a safeguard. Anthropic's long-running-agent guidance and
   Temporal's Continue-As-New both make the *wake* the unit of execution and keep durable
   state outside the context.
2. **The LLM is the event filter and the retry loop.** The waiter surfaces raw
   projection bumps and the controller rejects on `resourceVersion`, so the model spends
   most of its steps on plumbing a deterministic adapter should own.
3. **Autonomy is configured, but the verification gate is optional.** `fullControl`
   can be combined with no reviewer and no CI, so the only thing standing between an
   implementer's claim and `main` is a prompt instruction.
4. **No notion of "who asked for this work".** Owner-filed, maintainer-authored and
   discovery issues are all just open issues.

## 5. Improvement plan

Ordered by impact on unattended operation. Items 1–4 are the minimum for "runs for a
week without a human"; 5–7 make the results trustworthy; 8–12 reduce cost and churn.

### P0 — keep the loop alive

1. **Exempt standing runs from per-message budgets and timeouts, or auto-resume them.**
   - `cmd/agent/loop.go`: for runs labelled `standing-run-role=maintainer`, treat
     `MaxTurnsExceeded` as *continue-as-new*: persist a structured handoff note, compact,
     and re-enter `wait_for_repo_events` without a human message. Never set
     `UserInputCircuitBreak` on a standing run.
   - `MaintainerEngine.maintainerRunWakeable`: also nudge runs whose pending input is
     `circuit_breaker` (and `Paused` runs past timeout by extending `limits.maxRuntime`)
     — today they are invisible to the resume path.
   - `desiredMaintainerRun`: do not inherit `defaults.timeout`; set a rolling window
     (`runPastTimeout` restarts at `LastWakeTime`, so a controller-driven periodic wake is
     enough) or make `MaxRuntime` unlimited for standing runs and rely on the cost cap.
2. **Fix the wake predicate for parked maintainers.** `requeueAfter` returns the 12h
   standup interval when the run is not "wakeable"; a maintainer with open work must be
   reconsidered within minutes regardless of how it stopped.
3. **Reconnect watches inside the tool.** `semanticWatchReconnectResult` should re-list
   and re-watch transparently (bounded retries) and only return to the model on real
   change or timeout.
4. **Make the workspace disposable and recoverable.** On resume the standing pod should
   re-clone/fetch the maintained repository at the current base (the maintainer is
   read-only, so a fresh clone is safe) and run a bearings ritual: read the handoff note,
   `git log -5`, snapshot. Never leave the maintainer with a stale checkout and no fetch.

### P1 — make autonomous merges trustworthy

5. **Require one independent gate for any merge the controller performs.** In
   `mergeMaintainerPullRequest`, when `fullControl` is set and both `policy.RequiredChecks`
   is false and no PR-loop reviewer verdict exists for the head, reject with a clear
   message instead of taking the zero-check path. Offer the platform as the gate: a
   `verifier` fleet run (fresh context, no access to the implementer transcript) that
   re-runs the project's declared test commands in a clean sandbox and posts a check run
   via the GitHub App. Update `maintainer-work-item-cutover.md` to describe the actual
   zero-check behaviour.
6. **Distinguish owner-filed from maintainer-authored issues.** Record `authorLogin` vs the
   App identity in the work-item projection. Maintainer-authored issues start in a
   `Proposed` state; dispatch requires an owner acknowledgement (`@agent approve`, a label,
   or a dashboard action) or an explicit repository setting
   (`maintainer.autoDispatchSelfAuthored: true`). Surface a daily/weekly **spend** cap next
   to `maxDispatchesPerDay` — dispatch count is not the cost driver.
7. **Give humans an override path for red checks and reviewer findings.** "Ignore the
   test failure, it happens sometimes" should become a typed, audited
   `@agent waive-check <name>: <reason>` command (webhook-authenticated) rather than a chat
   instruction the maintainer must refuse.

### P2 — reduce cost and churn

8. **Stop shipping the deprecated cursor.** Drop `cursor` from `maintainerSemanticWaitOutput`
   when `cursor_handle` is returned (54% of waiter bytes, ~290K tokens over the run).
9. **Level-trigger the brain.** Add a `wake_class` to work-item projections
   (`decision_needed`, `command_terminal`, `informational`) and let
   `wait_for_repo_events` accept `min_class`. Command receipts should be awaited by a
   blocking `await_command(name, timeout)` or by making the command tools block until
   `Succeeded|Rejected|Failed` (they are sub-second today), removing two LLM steps per
   command. Coalesce bursts: return once per ~2s window, not per event.
10. **Move preconditions out of the model.** Drop the `resourceVersion` precondition (keep
    `projectionSequence`, UID, head SHA and the semantic gates), or have the tool retry
    once when only `resourceVersion` moved and `projectionSequence` is unchanged. The
    controller's own merge path already re-reads every GitHub gate, so `resourceVersion`
    adds no safety — only rejections.
11. **Platform-owned rebase / merge queue.** When a fleet PR is merged, the controller
    should queue "merge base into your branch and re-verify" to every other open fleet PR
    in the repository (idempotent, deduplicated), and the readiness projection should
    expose `mergeable: recomputing` so the maintainer does not poll GitHub's recompute.
    Longer term: enqueue green PRs into GitHub's merge queue where available.
12. **Dispatch partitioning.** Have implementers (or the triage step) declare intended
    paths; the controller serialises dispatches whose path sets overlap instead of leaving
    the maintainer to reason about `store.rs` collisions.

### Prompt-level changes that help now (cheap)

- Tell the maintainer to call `get_pull_request` only for the summary/threads, never the
  full diff, when a reviewer or verifier exists; and to record per-item decisions in a
  compact handoff note via `submit_maintainer_report` *before* any long wait so a fresh
  episode can rebuild state from reports + snapshot rather than the transcript.
- Add a rule: issues authored by the maintainer or its implementers are `Proposed` and
  require owner acknowledgement before dispatch; report the batch and `AskUserQuestion`
  once instead of dispatching 25 self-generated issues.
- Add a rule: never merge under `fullControl` when neither a reviewer verdict nor a
  reported check exists for the head; report `blocked` and ask the owner to enable one.

## 6. Success metrics for the next iteration

- Unattended uptime: hours between required human messages (baseline: ~3.4h between
  parks; three parks and one manual resume in 13h).
- Wake efficiency: LLM steps per delivered work item (baseline: 675 / 25 = 27) and share
  of waits returning in <2s (baseline 75%).
- Command rejection rate (baseline 7.3% of observed results; 17% of typed commands).
- Coordination overhead: maintainer cost / fleet cost (baseline 0.39).
- Independent-gate coverage: merges with a reviewer verdict or check run (baseline 0/25).

## Appendix: loop-engineering research

Sources fetched 2026-09-02 (vendor docs change; re-verify before implementing).

**Fresh episodes, not one long conversation.** Anthropic's harness guide states the core
challenge is that long-running agents "must work in discrete sessions" and recommends an
initializer/coding-agent split with `progress` files, one increment per session, and a
bearings ritual at start; "compaction isn't sufficient" and models "declare victory"
prematurely without the scaffolding
([effective-harnesses-for-long-running-agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)).
Claude 4 best practices: "consider starting with a brand new context window rather than
using compaction" and store structured state as JSON, notes as prose
([claude-4-best-practices](https://docs.claude.com/en/docs/build-with-claude/prompt-engineering/claude-4-best-practices)).
Context rot: recall degrades as tokens grow; use compaction, structured notes and
sub-agents returning 1–2K-token summaries
([effective-context-engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)).
Server-side compaction (`compact-2026-01-12`, `pause_after_compaction`) and context editing
(`clear_tool_uses_20250919`) are available API primitives
([compaction](https://docs.claude.com/en/docs/build-with-claude/compaction),
[context-editing](https://docs.claude.com/en/docs/build-with-claude/context-editing)).

**Outer loops.** The Ralph Wiggum pattern — `while :; do cat PROMPT.md | claude ; done`,
fresh context per iteration, durable state in git, "one thing per loop", primary context
acting as a scheduler, back-pressure from tests/linters
([ghuntley.com/ralph](https://ghuntley.com/ralph/)). Anthropic's Claude Code plugin
implements it as a Stop hook and warns that `--max-iterations` must be the primary safety
mechanism because a completion promise cannot encode BLOCKED
([ralph-wiggum README](https://raw.githubusercontent.com/anthropics/claude-code/main/plugins/ralph-wiggum/README.md)).

**OpenAI.** Agents SDK `max_turns` raises `MaxTurnsExceeded`, but `RunState` makes
exhaustion a resumable checkpoint; `call_model_input_filter` trims history per call
([running_agents](https://openai.github.io/openai-agents-python/running_agents/)).
Responses API compaction (`context_management: compaction`, `/responses/compact`)
([compaction guide](https://developers.openai.com/api/docs/guides/compaction.md)).
Codex cloud ends every task in human review; Codex review rules "don't replace tests,
branch protections, or required approvals"
([codex cloud](https://developers.openai.com/codex/cloud.md),
[codex github](https://developers.openai.com/codex/integrations/github.md)).

**Production coding agents.** Copilot coding agent: one PR per task, 59-minute hard
session cap, its own approval does not count, Actions do not run on its pushes until a
human approves
([about](https://docs.github.com/en/copilot/concepts/agents/coding-agent/about-coding-agent),
[review PRs](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/cloud-agent/review-copilot-prs)).
GitHub merge queue builds base+queued PRs, requires CI on `merge_group`, evicts
conflicting/failing PRs
([merge queue](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)).
Cursor cloud agents auto-approve only under a risk threshold and per-directory
`APPROVAL_POLICY.md` ([approval agents](https://cursor.com/docs/approval-agents)).
Jules plans first and asks for approval before code changes ([jules](https://jules.google/docs)).
Devin separates authoring from review with confidence-scored findings
([devin review](https://docs.devin.ai/work-with-devin/devin-review)). Common shape:
episodic implementer → independent reviewer → platform merge gate; none accept the
implementer's self-report as verification.

**Systems analogues.** Kubernetes controllers are level-triggered, idempotent, one item
at a time via a deduplicating workqueue, and record what they last decided on
(`observedGeneration`) to skip no-op wakes
([controllers.md](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-api-machinery/controllers.md),
[kubebuilder good practices](https://book.kubebuilder.io/reference/good-practices)).
Server-Side Apply replaces `resourceVersion` races with field ownership
([SSA](https://kubernetes.io/docs/reference/using-api/server-side-apply/)). Temporal's
Continue-As-New checkpoints state and starts a fresh history when the server signals
"Continue-As-New suggested" — the direct analogue of episodic compaction for an
"entity workflow" that runs forever
([continue-as-new](https://docs.temporal.io/workflow-execution/continue-as-new),
[history limits](https://docs.temporal.io/workflow-execution/event#event-history-limits)).
Restate keeps the decision maker stateless with journaled steps and single-writer keyed
objects, and caps concurrency because agent calls "translate directly into model or API
spend" ([restate](https://docs.restate.dev/concepts/durable_execution)). Inngest
`step.waitForEvent` is a durable wait with timeout and match expression
([inngest](https://www.inngest.com/docs/features/inngest-functions/steps-workflows/wait-for-event)).

**Long-horizon reliability.** METR: success falls below 10% on >4h tasks; failure is
"stringing together longer sequences of actions"
([METR](https://metr.org/blog/2025-03-19-measuring-ai-ability-to-complete-long-tasks/)).
τ-bench pass^k shows reliability across trials, not single-shot accuracy, is the
bottleneck ([τ-bench](https://arxiv.org/abs/2406.12045)). Vending-Bench documents
"doom loops" that are not explained by memory fill alone
([vending-bench](https://andonlabs.com/evals/vending-bench)) — an argument for a
stuck-detector (repeated identical intents across wakes → BLOCKED note + page a human)
in addition to fresh episodes.
