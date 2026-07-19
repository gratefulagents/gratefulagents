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

The Project's **PR review loop** setting controls autonomous reviewer runs for pull requests opened by future Project runs, including pull requests in additional repositories. It is disabled by default. When enabled, the control plane records pull-request monitoring state and polls active pull requests for review feedback and authorized `@agent` conversation comments. Reviewer completion and implementation follow-up wake the next review-loop step internally. Monitoring ends when the pull request is approved, blocked, cancelled, merged, or closed.

The Project Entry-point UI does not expose repository-maintainer configuration. This page documents only GitHub behavior that is available from Projects and runs.

## Status and lifecycle

The Entry-points rail displays last poll activity and one of these states: **applying** before readiness is reported, **ready** when the generated runtime is ready, **degraded** when it reports an error or non-ready state, or **disabled** after you turn off its switch.

Use the switch to stop GitHub-triggered work without deleting the configuration. **Edit** can change the connection, repository, and events but not the source type. **Delete** permanently removes the Entry point and its generated runtime; already-created runs remain. See [Projects](../projects/projects.md#entry-points-and-connections) for shared connection and lifecycle rules.
