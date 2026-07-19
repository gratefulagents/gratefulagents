---
title: Sharing and permissions
agentPrompt: >-
  Read https://gratefulagents.dev/docs/collaboration/sharing-and-permissions/ and explain how sharing and permissions work in gratefulagents, then help me give a teammate the right access to my project.
---

# Sharing and permissions

Share a project or run with another workspace user when the app shows a **Share** action for that resource.

## Share a project or run

1. Open the project or run.
2. Select **Share**.
3. Search by the person's name or email, or enter their email.
4. Choose **Viewer** or **Collaborator**.
5. Select **Share**.

The dialog lists people who already have access. Use its permission selector to change a share or the remove action to revoke it. Changes apply to future access.

## Permission levels

| Permission | Intended use |
| --- | --- |
| **Viewer** | Let someone inspect the shared project or run. |
| **Collaborator** | Let someone work more actively where the resource, state, workspace policy, and deployment allow it. |

The interface is the source of truth for available actions. It can hide or disable controls based on a user's permission, the resource type, and run state.

## Credentials and sharing

Sharing a project or run does not itself copy credential values to another user. It can expose outputs produced by a run, so share only with people who may view those results.

A user can separately select **Share** on **Settings → Credentials** to copy chosen saved credentials to another user. That operation gives the recipient a separate copy to use in their own runs; it is not tied to project or run sharing and does not stay synchronized. Follow your organization policy before using it.

## Good practice

- Share a project for ongoing collaboration.
- Share a run for feedback on one piece of work.
- Use Viewer when someone only needs to inspect it.
- Revoke access when it is no longer needed.
