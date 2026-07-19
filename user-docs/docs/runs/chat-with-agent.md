---
title: Chat with an agent
agentPrompt: >-
  Read https://gratefulagents.dev/docs/runs/chat-with-agent/ and explain how I chat with a running agent: replying, queueing versus steering, and sharing files or images mid-run.
---

# Chat with an agent

The **Chat** tab is the main workspace for collaborating with an agent. It combines your messages, agent replies, activity, questions, approvals, and live working state in one timeline.

The timeline follows new activity while you are at the bottom. Use the scroll controls to jump to the beginning or end of a long session.

## Send a message

Type in the composer and press **Enter** to send. Press **Shift+Enter** for a new line.

Use a message to clarify the task, add a constraint, answer a question, or redirect work.

```text
Before changing anything else, show me the failing test and the smallest fix you plan to make.
```

### Delivery

The **Delivery** control has two choices:

| Choice | Delivery behavior |
| --- | --- |
| **Steer** | Delivers the message into the in-progress turn. This is the default. Use it for an urgent correction. |
| **Queue** | Holds the message until the agent reaches the next turn boundary. Use it when the current work can finish first. |

Your selection stays in effect until you change it. A sent message is shown above the composer until the agent consumes it. Before delivery, you can **Edit** it to return its text and images to the composer, or **Cancel** it. Delivered messages cannot be withdrawn.

A message may be marked **Delivery unconfirmed — run ended** if the run ends before it is consumed.

## Attach images and mention files

Click the image button or paste an image to attach it. Images appear above the composer before you send them and in the resulting chat message.

- Each selected image can be up to 20 MB.
- A message can include up to eight images.
- The selected model must support image input to use the image as intended.

Type `@` to open the workspace file picker and insert a file mention. The picker is available after the workspace is ready.

```text
The bug is probably in @frontend/src/components/BillingSummary.tsx. Check that first.
```

## Slash commands and modes

Type `/` to open the command menu. The commands are mode switches, not chat messages.

| Command | When it appears | Effect |
| --- | --- | --- |
| `/plan` | When the run is not in plan mode | Requests the `plan` mode. |
| `/chat` | When the run is in plan mode | Requests `autopilot` to leave plan mode and begin building. |
| `/mode <name>` | For each other available mode | Requests that workspace mode. |

The available modes come from the workspace configuration. A requested transition can be denied or have no effect, so use the system notice in the timeline to confirm the result. Do not assume every workspace has the same modes or that a mode grants the same tools and permissions in every workspace. See [Interactive, plan, autopilot, stop, retry](./plan-autopilot-stop.md).

## Questions, approvals, and quick actions

When the agent needs input, a banner appears above the composer. Depending on the run, it can indicate **Waiting for your input**, **Approval required**, **Plan ready for review**, **Turn limit reached**, or **Agent needs help**.

Use a displayed quick action or send a response in the composer. A focused answer helps the agent continue without guessing.

## View-only access

People with **Viewer** access can read a run but cannot send messages, change runtime settings, create a PR, stop the run, or edit it.
