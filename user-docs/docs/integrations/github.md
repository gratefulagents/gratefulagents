---
title: GitHub
agentPrompt: >-
  Read https://gratefulagents.dev/docs/integrations/github/ and explain what the gratefulagents GitHub connection does, then help me set it up so issues and pull-request comments can create and steer runs.
---

# GitHub

Use a GitHub connection and a GitHub Project Entry point for GitHub-originated work. Create them from **Project → Entry points**; the app no longer provides a standalone GitHub repository list or detail workflow for this setup.

Related pages: [Projects](../projects/projects.md), [Run defaults](../projects/run-defaults.md), [Cron schedules](../projects/cron.md), [Linear](./linear.md), and [Slack](./slack.md).

## Create a GitHub connection

1. Open the Project's **Entry points** and select **Manage connections**.
2. Select **New connection**, enter a DNS-style **Name**, and select **GitHub**.
3. Enter either a token Secret or a complete GitHub App configuration.
4. Select **Create connection**.

Connection fields are references to Kubernetes Secrets; the form never accepts a token or private key value. Ask a cluster operator to create the required Secret in the Project namespace first. See [Connection Secrets](./connection-secrets.md).

| Field | Required | Behavior |
| --- | --- | --- |
| **Token Secret** | One credential path | Name of a same-namespace Secret containing the GitHub token under `token`. |
| **App ID** | With GitHub App authentication | Numeric GitHub App ID. |
| **Installation ID** | With GitHub App authentication | Numeric installation ID. |
| **Private Key Secret** | With GitHub App authentication | Name of a same-namespace Secret containing the GitHub App PEM under `private-key`. |

The connection must have **Token Secret**, or all three GitHub App fields. A connection name and type are immutable after creation. You cannot delete a connection that any Project Entry point references.

## Create a GitHub Entry point

1. In the same Project, select **New trigger**.
2. Enter the trigger **Name**, select **GitHub**, then select a GitHub **Connection**.
3. Enter **Repository** as `owner/repository`.
4. In **Events**, enter `issues`, `comments`, or both as a comma-separated list.
5. Select **Create trigger**.

| Field | Required | Behavior |
| --- | --- | --- |
| **Name** | Yes | DNS-style trigger identifier. `manual` is reserved. |
| **Connection** | Yes | A GitHub connection in the Project namespace. |
| **Repository** | Yes | GitHub owner and repository, entered as `owner/repository`. |
| **Events** | No | Enables issue polling when it includes `issues`, and issue-comment handling when it includes `comments`. An empty value enables neither. |

This Entry point inherits the Project's repository defaults, model and credential defaults, runtime, Skills, MCP policy, and custom instructions. It does not have its own run-defaults form.

## Inbound behavior

With **issues** enabled, the runtime polls open non-pull-request issues. An issue creates work only when it has a label that resolves to an available ModeTemplate. The issue author must also pass the configured GitHub authorization policy. Processed issues are not created again.

With **comments** enabled, a newly created issue comment can create work when it contains the trigger keyword. The default keyword is `@agent`. Comment delivery depends on the GitHub webhook path being configured; polling does not replace issue-comment webhook delivery. The commenter must pass the GitHub authorization policy.

The current Entry-point dialog exposes only the fields in the table. Trigger keyword, polling interval, and per-trigger authorization are not configurable in that dialog.

## Pull requests and the review loop

Project runs can still clone repositories, create pull requests, and show pull-request links, checks, review decisions, and review threads in the run view. From a run with a diff, use **Create PR**, then use its **PR** tab to monitor the pull request.

The Project's **PR review loop** setting controls autonomous reviewer runs for pull requests opened by future Project runs, including pull requests in additional repositories. It is disabled by default. Pull-request lifecycle monitoring is independent of that setting: the control plane continues recording authoritative lifecycle, aggregate review, and head-bound CI facts after an AI review loop becomes approved, blocked, inactive, or cancelled. Feedback dispatch remains conditional on review-loop policy. Closed pull requests are observed periodically so reopening is detected; merged pull requests are terminal.

GitHub App installations used for this monitoring must grant read access to **Checks** and **Commit statuses**, in addition to Metadata, Issues, and Pull requests. Existing App installations may need their permissions accepted again before private-repository CI facts can become fresh.

Controller-guarded maintainer merges additionally require **Administration: read** so branch-protection requirements can be verified, plus repository merge permission through **Contents: write** (and **Pull requests: write** for PR operations). The App must not have repository `admin` permission and must not be placed on a ruleset bypass list; guarded merge fails closed when server-enforced reviews/checks or non-bypass merge authority cannot be proven.

The Project Entry-point UI does not expose repository-maintainer configuration. This page documents only GitHub behavior that is available from Projects and runs.

## Status and lifecycle

The Entry-points rail displays last poll activity and one of these states: **applying** before readiness is reported, **ready** when the generated runtime is ready, **degraded** when it reports an error or non-ready state, or **disabled** after you turn off its switch.

Use the switch to stop GitHub-triggered work without deleting the configuration. **Edit** can change the connection, repository, and events but not the source type. **Delete** permanently removes the Entry point and its generated runtime; already-created runs remain. See [Projects](../projects/projects.md#entry-points-and-connections) for shared connection and lifecycle rules.
