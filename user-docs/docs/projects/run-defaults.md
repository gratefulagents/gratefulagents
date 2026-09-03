---
title: Run defaults
seoTitle: Configure AI Agent Run Defaults for Projects | GratefulAgents
description: Set repository, model, credentials, runtime, tools, and custom instructions as reusable defaults for all GratefulAgents project runs and Entry points.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/projects/run-defaults/ and help me configure run defaults for my project so new runs start with the right repository, mode, and model.
---

# Run defaults

A Project owns the defaults for its **Dashboard chat** entry point, **New Run** runs, and all of its automated Entry points. Configure them in **Project → Settings** when you create or edit the Project. GitHub, Slack, Linear, and Cron triggers inherit these values; their trigger dialogs only configure how work enters the Project.

Related pages: [Projects](./projects.md), [Cron schedules](./cron.md), [GitHub](../integrations/github.md), [Linear](../integrations/linear.md), and [Slack](../integrations/slack.md).

## Repository defaults

| Default | Effect |
| --- | --- |
| **Repository URL** | Primary repository cloned into Project runs. It may be empty for work without a repository. |
| **Base branch** | Optional branch from which runs start. |
| **Additional repositories** | Extra repositories cloned alongside the primary repository. |

## Model and credential defaults

| Default | Effect |
| --- | --- |
| **Provider** and **Default model** | Select the AI provider and optional model. An empty model uses the provider default. |
| **Authentication** | Uses API-key or OAuth authentication where that provider exposes a choice. |
| **Reasoning level** | Optional provider- or model-specific reasoning setting. |
| **Allowed models** | Comma-separated list that restricts model switching inside a run. |
| **Use my saved credentials** | Uses saved credentials that are present and applicable for the selected provider. It also uses the saved GitHub token when configured. |

When saved credentials are off, the Project settings form uses existing Secret references instead of accepting secret values:

| Field | Use |
| --- | --- |
| **OAuth Secret** | Name of a Secret containing `auth.json`; required for OAuth authentication. |
| **GitHub token Secret** | Optional name of the GitHub-token Secret used for repository operations. |
| **Anthropic Secret** | Anthropic API-key Secret reference. |
| **Provider key Secret** and **Provider key field** | Secret name and data-key reference for the selected non-Anthropic API-key provider. The key defaults to `api-key`. |

The form rejects a saved-credential choice when no usable saved credential exists, and rejects a required explicit reference when it is missing. For GitHub, Slack, and Linear **connections**, see the source-specific connection tables in [GitHub](../integrations/github.md), [Slack](../integrations/slack.md), and [Linear](../integrations/linear.md).

## Runtime and policy defaults

| Default | Effect |
| --- | --- |
| **Runtime image** | Chooses the sandbox image. |
| **Timeout** | Optional maximum runtime duration, such as `30m`. |
| **RuntimeProfile ref** | References a reusable runtime profile. With **Create/update a RuntimeProfile** enabled, the Project settings also set its permission mode and network egress. |
| **Permission mode** | `read-only`, `workspace-write`, or `danger-full-access`. |
| **Git remote writes** | `enabled` (default) or `disabled`. Disabling removes push and pull-request creation tools and rejects `git push` from shell tools while preserving workspace edits, local commits, fetches, and pulls. |
| **Network egress** | `unrestricted`, `restricted`, or `disabled`. |
| **MCP servers** | Attaches server configurations to Project runs. |
| **Skills** | Attaches reusable agent skills to Project runs. |
| **MCPPolicy ref** | References a reusable MCP policy. With **Create/update an MCPPolicy** enabled, the Project settings set **Default action** (`Deny` or `Allow`) and **Allowed MCP servers**. |

If an MCP policy denies by default, add the names of selected MCP servers to **Allowed MCP servers** or their tools will not load.

## Docker-in-Docker (admin-only)

Cluster admins can let a Project's runs execute real `docker` commands (`docker build`, `docker run`, `docker compose`) by setting `spec.dockerInDocker: true` on the `Project` resource — or `spec.defaults.dockerInDocker: true` on an individual trigger resource, including `SecurityScan` — via kubectl/GitOps. Each run pod then gets a **privileged** `docker:dind` sidecar listening on loopback, and the worker's `DOCKER_HOST` points at it (including inside the command sandbox). Because the sidecar requires a privileged container, the option is off by default, is not exposed through the dashboard, and is stripped from exported and imported security packs.


## Custom instructions

**Custom instructions** add Project-wide guidance to every run. They are prepended to the run's `CLAUDE.md`; repository-local `CLAUDE.md` guidance can override them. Do not put secrets in these instructions.

## Inheritance and overrides

When an Entry point starts work, the platform copies the current Project defaults into its generated source runtime and then into the new run. Updating Project settings changes what future trigger-created runs inherit; it does not modify existing runs. Each source has separate ingress fields and behavior, documented in [Cron schedules](./cron.md), [GitHub](../integrations/github.md), [Linear](../integrations/linear.md), and [Slack](../integrations/slack.md).

When you start a run from the composer (on **Home** or at the top of the Project's **Overview**), its **Options** popover can override model, repository, runtime, overseer, and credential defaults for that one run. The override does not change the Project or future runs.
