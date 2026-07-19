---
title: Interactive, plan, autopilot, stop, retry
agentPrompt: >-
  Read https://gratefulagents.dev/docs/runs/plan-autopilot-stop/ and explain the gratefulagents run modes — interactive, plan, autopilot — when to use each, and how stopping and retrying work.
---

# Interactive, plan, autopilot, stop, retry

A run has a current mode. Modes are workspace-defined templates: their instructions, available tools, permissions, and limits can differ by workspace. The run session shows the active mode, but it does not guarantee that every named mode is available or that it behaves identically across projects.

## Interactive and autopilot

Workspaces commonly provide modes named `interactive` and `autopilot`:

- **Interactive** is intended for collaboration. The agent can make progress while asking focused questions when it needs a decision or approval.
- **Autopilot** is intended for autonomous execution. The agent continues work until it reaches its completion condition or needs input.

These names describe the supplied template, not a platform-wide contract. Check the mode description and the run's effective permissions before relying on a mode for write access, network access, or a particular tool.

## Plan mode

Plan mode is intended for research and planning before implementation. Use `/plan` in the composer to request it.

When a plan is available, the session shows **Plan ready**. You can open the plan with **View plan**. **Accept & build** approves the plan and requests a switch to `autopilot`.

```text
/plan
Investigate the flaky checkout test, identify the smallest safe fix, and list the exact tests you would run.
```

Use `/chat` only while in plan mode to request `autopilot`. The command is an exit from plan mode, not a general-purpose chat command. A workspace can deny the requested mode switch; read the resulting timeline notice before continuing.

## Stop the current turn

While the agent is working and the composer is empty, the send button becomes **Stop the current turn**. It interrupts the in-progress turn without stopping the run.

Use it to interrupt incorrect or unexpectedly long work, then send a corrected instruction. This is a UI action, not a `/stop` slash command.

## Stop the run

Owners and admins can use **Stop** for a non-terminal run. Stopping ends active work but preserves the run's history and diff. The session displays the resulting state as **Run stopped**.

Use it when the work should not continue. If you only need to redirect the current work, stop the current turn or send a **Steer** message instead.

## Mark as succeeded

Owners and admins can use **Mark as succeeded** for an eligible non-terminal run when the requested outcome is complete but the run did not finish normally. This ends remaining active work and records the run as successful.

## Retry a failed or stopped run

Owners and admins can use **Retry** only after a run has failed or been stopped. Retry resumes the same run from its persisted session; it does not create a separate run or discard its history.

For a failed run, the default retry instruction asks the agent to continue from where it failed. For a stopped run, it asks the agent to continue from where it stopped. Review the error, activity, and diff before retrying so you can send corrective guidance if the failure was not transient.

## Extend runtime

**Extend runtime…** appears only for eligible runs. Choose a duration, such as `30m`, `1h`, `2h`, or `4h`, then select **Extend**. Paused eligible runs resume automatically after their runtime is extended.
