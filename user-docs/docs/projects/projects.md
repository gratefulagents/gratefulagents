---
title: Projects
seoTitle: Organize AI Coding Agent Work with Projects | GratefulAgents
description: Create and manage GratefulAgents projects to share repository, model, runtime, and tool defaults across runs. Includes Entry points, file storage, and sharing.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/projects/projects/ and explain how projects organize work in gratefulagents, then help me create one for my repository.
---

# Projects

A Project is the shared home for a codebase or workstream. It holds the defaults used by its runs, durable files and artifacts, its **Dashboard chat** entry point, and automated **Entry points**. Use one Project when the same repository, model, runtime, credentials, tools, and instructions should apply to repeated work.

Related pages: [Run defaults](./run-defaults.md), [Cron schedules](./cron.md), [GitHub](../integrations/github.md), [Linear](../integrations/linear.md), and [Slack](../integrations/slack.md).

## Create a Project

Open **Projects** or **Home**, select **New project**, paste a **Repository URL**, and select **Create project**. The new Project opens when creation succeeds.

Only two fields are shown by default:

| Field | Behavior |
| --- | --- |
| **Repository URL** | Optional primary repository. Leave it empty to start without a repository. |
| **Name** | Required. Suggested automatically from the repository URL (`https://github.com/acme/Payments-API.git` becomes `payments-api`) until you type your own. The display name defaults to the name. |

The **Model** row shows what the Project will use as a receipt (`Anthropic · claude-sonnet-4-6 · saved credentials`). It starts from your **Settings → Model defaults** and **Settings → Credentials**; expand it only to change the provider, model, reasoning level, authentication mode, or to enter inline credentials instead of saved ones. If no saved credential covers the provider, the row opens on its own and explains what to add.

**More options** reveals the remaining groups — **Repository** (base branch, additional repositories), **Agent** (default mode, PR review loop, custom instructions), **Runtime** (image, timeout, RuntimeProfile), and **Tools** (MCP servers and policy). Every one of them can be changed later on the Project's **Settings** tab, so you never need to open them to create a working Project.

The form validates that the chosen credential path is usable. Saved credentials are used only when they are present and applicable to the selected provider. A saved GitHub token is also wired when configured; repository operations that need GitHub authentication can fail without one.

## Project defaults

Project settings are the defaults for dashboard-chat runs, **New Run** runs, and every Entry point attached to that Project. Entry points do not have a separate run-defaults form.

| Default | Effect on future runs |
| --- | --- |
| Repositories and base branch | Selects the primary checkout, extra checkouts, and starting branch. |
| Provider, model, authentication, and reasoning level | Selects the model and credential wiring. |
| Runtime image, timeout, RuntimeProfile, permissions, and egress | Selects the execution environment and its access policy. |
| MCP servers, Skills, and MCP policy | Selects available tools and policy restrictions. |
| Allowed models | Restricts model switching within the run. |
| Custom instructions | Adds Project guidance to each run. Repository-local `CLAUDE.md` guidance can override it. |

See [Run defaults](./run-defaults.md) for field-level guidance. Changing Project settings affects runs created after the change; it does not rewrite completed runs.

## Use a Project

The Project page contains the following shared surfaces:

- **Dashboard chat** under **Entry points** opens a focused chat with this Project selected.
- The run composer at the top of **Overview** (also reached with **New run** in the header) starts a run in this Project. Its **Options** popover can override defaults for that run only.
- **Entry points** lists the Project's GitHub, Slack, Cron, and Linear automations. Create and manage them here, not from a standalone integration page.
- **Files & artifacts** keeps Project-scoped content that outlives an individual run.
- **Runs** shows runs created for the Project.
- **Settings** edits Project defaults. **Share** is available to owners and admins.

## Files & artifacts

**Files & artifacts** is durable Project content, not a run workspace. Viewers can browse and preview content; people who can edit the Project can change it.

### Add and organize content

You can:

- Upload files or an entire folder, including by dragging files onto the drop area.
- Create a folder, Markdown document, or HTML artifact.
- Rename or move an item with a Project-relative path.
- Duplicate an item to another Project-relative path.
- Delete an item.

The direct-upload limit is **25 MiB per file**. The UI accepts PDF, Office documents, CSV, JSON, text and Markdown, images, audio, video, archives, and HTML.

### Preview, edit, and restore

Select an item to preview it or download it. Text-like files and Markdown documents are editable in the Project page; saving creates a new version. Markdown also shows a rendered preview. Images, audio, video, and PDFs use an inline preview when the browser supports it; unsupported formats remain downloadable.

Use **History** to view versions. Selecting **Restore** for an older version creates a new revision rather than replacing history.

## Entry points and connections

Open a Project and use **Entry points**:

1. Select **New trigger** and choose GitHub, Slack, Scheduled, or Linear.
2. GitHub, Slack, and Linear need a connection. If the namespace already has one it is selected for you; otherwise the form says **Connect … first** and **Add connection** opens the connection form for that provider directly — saving it returns you to the trigger with the new connection selected. **Create trigger** stays disabled until then. Scheduled triggers need no connection.
3. Enter the source-specific fields. The **Trigger name** is suggested from them (`gh-acme-payments`, `weekdays-0900`, `slack-engineering`, `linear-eng`) until you type your own; names are DNS-style labels.
4. Select **Create trigger**.

**Manage connections** lists every reusable GitHub, Slack, or Linear connection in the Project namespace for editing or removal.

The rail always includes **Dashboard chat**. Each automation shows its name, source summary, last activity, and, for Cron, the next scheduled activity. See [Cron schedules](./cron.md), [GitHub](../integrations/github.md), [Linear](../integrations/linear.md), and [Slack](../integrations/slack.md) for the exact fields and source behavior.

### Status and lifecycle

An Entry point is **applying** while it has not yet reported a ready state, **ready** after its generated runtime reports Ready, and **degraded** when the runtime reports an error or is not Ready. A disabled trigger displays **disabled**.

People who can edit the Project can use the switch to enable or disable an Entry point. Disabling stops and removes its generated automation runtime; enabling compiles it again. **Edit** preserves the trigger type and lets you update that type's fields. **Delete** permanently removes the Entry point; existing runs remain. Trigger and connection names are DNS-style identifiers, and `manual` is reserved for triggers.

Connections are reusable only within their namespace. Connection name and type cannot be changed after creation. You cannot delete a connection while any Project in that namespace references it.

## Edit a Project

Open the Project's **Settings** tab (the **Settings** button in the header jumps there). Every setting is edited in place — no dialog — in sections you can jump to from the side navigation: **General** (display name, repositories, base branch), **Model & credentials**, **Agent behavior** (default mode, PR review loop, custom instructions, default bug squasher), **Runtime**, **Tools**, and, for workspace admins, **Privileged access**.

Nothing is written until you save. As soon as a value differs from the saved Project, a bar appears naming the changed sections with **Discard** and **Save changes** (⌘S / Ctrl+S also saves); each changed section also gains a **Reset** control to revert just that section. People with view-only access see the same values read-only.

Project owners and admins can use **Share** to invite viewers or collaborators. See [Sharing and permissions](../collaboration/sharing-and-permissions.md).
