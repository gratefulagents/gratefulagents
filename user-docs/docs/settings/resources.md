---
title: Resources
seoTitle: Agent Skills, MCP Servers, Policies, and Roles | GratefulAgents
description: Configure reusable agent skills, MCP servers, runtime profiles, policies, modes, guardrails, and specialist roles in GratefulAgents.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/resources/ and explain gratefulagents resources — skills, MCP servers, runtime profiles, policies, modes, and roles — and how I attach them to runs.
---

# Resources

**Resources** is the workspace configuration area for reusable agent building blocks. Open it from the sidebar. Availability and ability to change a resource depend on your workspace role and deployment.

## Resource types

| Resource | Use it for |
| --- | --- |
| [**Skills**](./skill-packages.md) | Reusable inline instructions or skills installed from the skills.sh catalog. Skills can require MCP servers. |
| **MCP servers** | Tool-server configurations: a command, arguments, environment, and references to saved integration credentials. |
| **Runtime profiles** | Runtime permissions, network access, workspace defaults, timeout, persistence, and allowed writable paths. |
| **MCP policies** | Rules that control which MCP servers and tools runs may use. |
| **Guardrails** | Enforceable rules that inspect tool input or output and block, warn, or log matches. |
| **Modes** | Behavior and execution templates, including instructions, permissions, defaults, and optional limits. |
| **Roles** | Reusable specialist instructions, model mappings, reasoning level, and tool-access boundaries. |

## Skills and MCP servers

A **skill** gives an agent reusable guidance. An **MCP server** gives it tools. Create integration credentials first in [Credentials](./credentials.md) when an MCP server needs secrets, then reference those credentials from the server configuration.

Attach skills or MCP servers only where a project or agent configuration provides the relevant selector. A resource's existence does not guarantee that every run can use it: the project, mode, policy, and deployment can restrict access.

## Manage resources

Select a resource type, then use **Create**, **Edit**, or **Delete** where available. Names identify resources and cannot be changed during edit.

**Modes** and **Roles** can be changed only by administrators. Other resource types can still be restricted by workspace policy. Confirm the impact on existing project and agent configurations before deleting a resource.

## Resources and Settings

Resources are not personal Settings pages. Personal provider and integration secrets remain under [Credentials](./credentials.md); the desktop connection and update controls remain under [Settings](./account-appearance.md).
