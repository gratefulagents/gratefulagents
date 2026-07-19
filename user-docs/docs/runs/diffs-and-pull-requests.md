---
title: Diffs and pull requests
agentPrompt: >-
  Read https://gratefulagents.dev/docs/runs/diffs-and-pull-requests/ and explain how runs produce diffs and pull requests in gratefulagents and how I should review them.
---

# Diffs and pull requests

Use the run session to review changes while the agent works, request a pull request, and follow the resulting PR.

## Review a diff

Open the **Diff** tab. It shows the tracked repository diff with file and hunk headers, additions, deletions, and context. The diff can update while the run is active.

If the workspace contains more than one repository, choose the repository in the selector. The tab can also list **New files** that are not represented in the tracked diff; select a file to read it. The diff, new-file list, or selected file can be truncated, so do not treat a truncation notice as a complete review.

Before requesting a PR, return to **Chat** to correct issues you find.

```text
The validation change is good, but keep the existing public error message. Only change the status-code handling.
```

## Create a pull request

When the run has a diff and no pull request is already associated with it, **Create PR** appears in the session header. On a compact layout, find **Create PR…** in **More actions**.

1. Select **Create PR**.
2. Add optional guidance for the PR title, description, reviewers, or rollout notes.
3. Submit the dialog.

The platform sends the agent a request to create the PR from the current run context. Creating a PR remains an agent action, so the button does not guarantee that a PR will be created successfully.

```text
Use a concise title, mention the flaky test in the summary, and include a manual verification checklist.
```

## Follow the pull request

After a PR is associated with the run:

- The header provides the PR link. If there are several PRs, it provides a list of links.
- The **PR** tab appears.
- The panel refreshes while visible and also has a **Refresh** button.
- Each PR card shows the repository and number, title, state, review decision when available, checks, and review threads.

For unresolved threads, users who can message the agent can select one or more threads and choose **Send to agent**. The platform sends the selected feedback as a queued message, so it waits for the next turn boundary rather than interrupting active work. Review the next diff before asking the agent to update the PR again.

See [Pull request feedback](../results/pull-request-feedback.md) for checks, review threads, and the optional review loop.

## Merge decisions

The session exposes PR status and feedback, but it does not replace your GitHub review and merge process. Make final merge decisions in GitHub according to your team's workflow.
