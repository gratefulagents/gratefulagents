---
title: Start a run
agentPrompt: >-
  Read https://gratefulagents.dev/docs/runs/start-a-run/ and start my first gratefulagents run with me: help me choose between opening a repository and a repo-free conversation, and explain the launch options.
---

# Start a run

A run is one agent session. Start a run from **Home** or from a project. Automations such as GitHub, Linear, Cron, and Slack can also create runs from their configured sources.

## Start from Home

1. Go to **Home**.
2. Enter the task in the composer.
3. Choose a project if the project picker is available.
4. Optional: select **Options** and enter a model override.
5. Press **Enter** or select **Start**.

If no project is selected, Home uses the personal workspace when one is available. When Home creates or finds that workspace, it also ensures a **Grateful Agents** project exists beside it. That project opens the public platform and SDK repositories in a read-only issue-intake mode, so you can describe a product bug or feature request and have the agent file it in the correct repository. It does not implement changes or open pull requests.

```text
Investigate why checkout totals are sometimes rounded incorrectly. Explain the likely cause before editing.
```

```text
Implement the settings UI from issue #142, run the affected tests, and prepare a PR.
```

## Start from a project

1. Go to **Projects**.
2. Open a project.
3. Select **New Run**.
4. Enter the request.
5. Review the options that the project makes available.
6. Select **Start Run**.

A project can supply defaults for repositories, branch, provider and model, credentials, runtime, permissions, and instructions. The available overrides depend on the project and your permissions; a run does not necessarily expose every project setting.

## Choose an effective mode

The project or its source selects the run's initial mode. Modes are configurable workspace templates, so the displayed name and behavior can differ between projects. Do not infer write access, network access, tool access, or a particular execution style from a mode name alone.

After the run starts, use the session's `/` command menu to see the mode transitions that are available to that run. A requested transition can be denied. See [Interactive, plan, autopilot, stop, retry](./plan-autopilot-stop.md).

## What happens next

The app opens the run session after creation. The run can show a startup message while its workspace and repositories are prepared. Once the composer is enabled, you can message the agent and review activity in the session tabs.
