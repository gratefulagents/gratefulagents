---
title: Run defaults
agentPrompt: >-
  Read https://gratefulagents.dev/docs/projects/run-defaults/ and help me configure run defaults for my project so new runs start with the right repository, mode, and model.
---

# Run defaults

A Project owns the defaults for its dashboard chat, **New Run** runs, and all of its automated Entry points. Configure them in **Project → Settings** when you create or edit the Project. GitHub, Slack, Linear, and Cron triggers inherit these values; their trigger dialogs only configure how work enters the Project.

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
| **Network egress** | `unrestricted`, `restricted`, or `disabled`. |
| **MCP servers** | Attaches server configurations to Project runs. |
| **Skills** | Attaches reusable agent skills to Project runs. |
| **MCPPolicy ref** | References a reusable MCP policy. With **Create/update an MCPPolicy** enabled, the Project settings set **Default action** (`Deny` or `Allow`) and **Allowed MCP servers**. |

If an MCP policy denies by default, add the names of selected MCP servers to **Allowed MCP servers** or their tools will not load.

## Custom instructions

**Custom instructions** add Project-wide guidance to every run. They are prepended to the run's `CLAUDE.md`; repository-local `CLAUDE.md` guidance can override them. Do not put secrets in these instructions.

## Inheritance and overrides

When an Entry point starts work, the platform copies the current Project defaults into its generated source runtime and then into the new run. Updating Project settings changes what future trigger-created runs inherit; it does not modify existing runs. Each source has separate ingress fields and behavior, documented in [Cron schedules](./cron.md), [GitHub](../integrations/github.md), [Linear](../integrations/linear.md), and [Slack](../integrations/slack.md).

When you start a run manually from the Project, **New Run** can override available advanced defaults for that one run. The override does not change the Project or future runs.
