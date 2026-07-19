---
title: Projects
agentPrompt: >-
  Read https://gratefulagents.dev/docs/projects/projects/ and explain how projects organize work in gratefulagents, then help me create one for my repository.
---

# Projects

A Project is the shared home for a codebase or workstream. It holds the defaults used by its runs, durable files and artifacts, dashboard chat, and automated **Entry points**. Use one Project when the same repository, model, runtime, credentials, tools, and instructions should apply to repeated work.

Related pages: [Run defaults](./run-defaults.md), [Cron schedules](./cron.md), [GitHub](../integrations/github.md), [Linear](../integrations/linear.md), and [Slack](../integrations/slack.md).

## Create a Project

Open **Projects** or **Home**, select **Create project**, complete the form, then select **Create project**. The new Project opens when creation succeeds.

| Area | Fields and behavior |
| --- | --- |
| Basic details | **Name** is required. **Display name** defaults to the name when left blank. Add an optional primary **Repository URL** and **Additional repositories**. |
| Model & credentials | Select the provider, model, authentication mode when available, and optional reasoning level. Use **Use my saved credentials** to use applicable credentials from **Settings → Credentials**. When it is off, creation accepts inline GitHub and provider API-key values; OAuth uses an existing Secret reference. |
| Repository details | Set an optional base branch. The primary and additional repository URLs become the repositories for future Project runs. |
| PR review loop | The switch is disabled by default. Enable it to start autonomous reviewer runs for pull requests created by future runs from this Project, including pull requests in additional repositories. |
| Runtime | Select a runtime image and optional timeout. You can also reference or create a RuntimeProfile with permission mode and network-egress settings. |
| Tools & skills | Attach MCP server configurations and **Skills**. An MCP policy can restrict the selected servers. |
| Advanced | Restrict in-run model switching with **Allowed models** and add **Custom instructions**. Workspace admins can also configure cluster access. |

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
- **New Run** starts a Project run. Its advanced fields can override defaults for that run only.
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

1. Select **Manage connections** to create a reusable GitHub, Slack, or Linear connection in the Project namespace.
2. Select **New trigger** and choose GitHub, Slack, Cron, or Linear.
3. Choose a matching connection for GitHub, Slack, or Linear; Cron needs no connection.
4. Enter the source-specific fields and select **Create trigger**.

The rail always includes **Dashboard chat**. Each automation shows its name, source summary, last activity, and, for Cron, the next scheduled activity. See [Cron schedules](./cron.md), [GitHub](../integrations/github.md), [Linear](../integrations/linear.md), and [Slack](../integrations/slack.md) for the exact fields and source behavior.

### Status and lifecycle

An Entry point is **applying** while it has not yet reported a ready state, **ready** after its generated runtime reports Ready, and **degraded** when the runtime reports an error or is not Ready. A disabled trigger displays **disabled**.

People who can edit the Project can use the switch to enable or disable an Entry point. Disabling stops and removes its generated automation runtime; enabling compiles it again. **Edit** preserves the trigger type and lets you update that type's fields. **Delete** permanently removes the Entry point; existing runs remain. Trigger and connection names are DNS-style identifiers, and `manual` is reserved for triggers.

Connections are reusable only within their namespace. Connection name and type cannot be changed after creation. You cannot delete a connection while any Project in that namespace references it.

## Edit a Project

Open the Project, select **Settings**, change the required fields or an expanded settings group, then select **Save changes**. The grouped settings mirror creation: **Model & credentials**, **Repository details**, **PR review loop**, **Runtime**, **Tools & skills**, **MCP policy**, **Cluster access** for admins, and **Advanced**.

Project owners and admins can use **Share** to invite viewers or collaborators. See [Sharing and permissions](../collaboration/sharing-and-permissions.md).
