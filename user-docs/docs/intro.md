---
title: Welcome
slug: /
agentPrompt: >-
  Read https://gratefulagents.dev/docs/ and explain what gratefulagents is, what it would do for my team, and the shortest path for me to try it.
---

# GratefulAgents platform app user guide

GratefulAgents is a chat-first app for asking AI agents to work on software tasks. Use it to start chats, organize reusable project defaults, follow agent activity, review results, and collaborate with your workspace.

This guide covers the user-facing app. Features, available models, integrations, and permissions can vary by deployment and your workspace role.

## What you can do

- Start a chat from **Home**. With a configured model provider, your first repo-free chat creates a **Personal workspace** project automatically.
- Use **Agent Ops** to find and follow runs, and **Observability** to inspect operational data when your deployment provides it.
- Organize repository-backed work in the **Projects** tree.
- Find projects and runs others shared with you under **Shared**.
- Configure reusable agent building blocks under [**Resources**](./settings/resources.md), including skills, MCP servers, runtime profiles, policies, guardrails, modes, and roles.
- Set up personal credentials, personas, model preferences, and Git commit identity in [**Settings**](./settings/account-appearance.md).

## Common first path

1. Open the web app or desktop app and sign in.
2. Add a supported provider credential in **Settings → Credentials**.
3. Start a chat from **Home**. If you do not have a project yet, the app creates **Personal workspace** for the chat.
4. Add a project later when work needs repository or shared defaults.
5. Follow the run in **Agent Ops**, then review any available diff or pull-request result.

See [Quick start](./getting-started/quick-start.md) for the detailed path.

## Terms used in the app

| Term | Meaning |
| --- | --- |
| **Workspace** | A backend environment that stores users, projects, runs, credentials, and configuration. In the desktop app, you can save and switch among workspaces on the device. |
| **Personal workspace** | A project the app creates for a first chat when no project exists and a saved provider credential is available. It does not require a repository. |
| **Project** | Reusable defaults for runs. A project can include a repository and model, credential, runtime, and instruction choices. |
| **Run** | One agent session with its chat history, activity, and any results your deployment exposes. |
| **Resource** | A reusable workspace configuration object. [Resources](./settings/resources.md) include skills, MCP servers, runtime profiles, MCP policies, guardrails, modes, and roles. |
| **Skill** | Reusable agent instructions, written inline or installed from the skills.sh catalog. A skill can require MCP servers. |
| **MCP server** | A configured tool server that agents can connect to. It is distinct from a skill. |
| **Mode** | A configurable behavior and execution template. Available modes and their behavior depend on workspace configuration. |
