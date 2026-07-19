---
title: Pull request feedback
agentPrompt: >-
  Read https://gratefulagents.dev/docs/results/pull-request-feedback/ and explain how gratefulagents handles pull-request review feedback, including the autonomous review loop.
---

# Pull request feedback

After a run creates or is associated with a pull request, the run's **PR** tab lets you follow checks and review feedback without leaving the session.

## Open and refresh PR feedback

1. Open the run.
2. Select **PR**.
3. Select **Refresh** when you need the newest data immediately.

The panel also refreshes every 30 seconds while it is open. A refresh failure leaves the last loaded data visible when available.

## Pull request cards

Each card can show:

- Repository and pull request number.
- Title and state: open, merged, or closed.
- Review decision, when GitHub reports one.
- A link to open the pull request on GitHub.
- Checks and commit statuses.
- Review threads, comments, and their resolved or outdated state.

A run can have more than one pull request. Each appears as its own card.

## Checks

Checks can be incomplete or complete. Common complete conclusions are:

| Conclusion | Meaning |
| --- | --- |
| **success** | The check passed. |
| **failure**, **error**, **timed_out**, or **startup_failure** | The check failed or did not complete successfully. |
| **cancelled** or **action_required** | The check was cancelled or needs manual action. |
| Another or empty conclusion | GitHub reported a neutral or unrecognized state. |

Select a check name when it has a link to open its details in the CI provider.

## Send review feedback to the agent

If you can send messages to the run, unresolved review threads have checkboxes.

1. Select one or more unresolved threads, or use **Select all**.
2. Select **Send to agent**.
3. Review the resulting diff and PR update.

The action sends the selected thread content as a queued run message. It does not resolve GitHub threads or merge the pull request.

```text
Address the unresolved PR review thread in src/billing/tax.ts. Keep the public behavior the same, add a regression test, and update the PR.
```

## Autonomous PR review loop

The autonomous PR review loop is disabled by default. Enable it in **Project → Settings → PR review loop** for pull requests created by future runs from that Project.

When enabled, an agent-created PR can receive a reviewer run. A request-changes verdict wakes the implementer run with the feedback; the cycle continues until approval or the configured round cap. It does not replace required human review or GitHub branch protection.

The loop's exact reviewer mode, defaults, and round limit are configuration choices. If a loop stalls or reaches its limit, open the implementer run, review the visible feedback and diff, then send explicit guidance or continue the review in GitHub.

## Final review and merge

Use the session to triage feedback, but make final human review and merge decisions in GitHub according to your team's workflow.
