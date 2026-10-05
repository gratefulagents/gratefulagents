---
title: Skills
seoTitle: Create and Install Agent Skills | GratefulAgents
description: Build inline skills or install from the skills.sh catalog in GratefulAgents. Attach reusable agent instructions and MCP server dependencies to projects.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/skill-packages/ and explain how skills work in gratefulagents, then help me install a skill package.
---

# Skills

**Skills** live under **Resources**, not Settings. A skill is reusable agent instruction that you write inline, point at a folder in a GitHub repository, or install from the skills.sh catalog. It can require MCP servers, which the app attaches automatically when the skill is used.

For the full resource inventory, see [Resources](./resources.md).

## Create a skill

1. Open **Resources → Skills**.
2. Select **New skill**.
3. Enter a name, optional version, and a description. The description is what the agent sees in its skill menu, so say when the skill applies.
4. Choose a source:
   - **Write instructions**: paste the guidance directly. Inline instructions are capped at 256 KiB.
   - **From a Git repository**: paste a GitHub link to a folder that contains a `SKILL.md`, for example `https://github.com/anthropics/skills/tree/main/document-skills/pdf`. The branch and path are parsed from the link. Only `SKILL.md` is fetched; scripts or reference files next to it are not delivered to runs. Only public `github.com` repositories are supported.
5. Select any required MCP servers. Every server you pick must already exist under **Resources → MCP servers**.
6. Save the skill.

Use a stable, descriptive name. Names cannot change after creation because projects, triggers, and modes reference skills by name.

## Install from skills.sh

1. Open **Resources → Skills**.
2. Select **Browse skills.sh**.
3. Search or browse the catalog.
4. Select **Install** for the skill you want.

A catalog install stores a snapshot of the skill's `SKILL.md` as inline instructions and records where it came from. The catalog is not checked again after install, and a skill that is already installed cannot be installed twice. Editing the instructions of an installed skill detaches it from its catalog entry; delete and reinstall it to pick up a newer catalog version.

Catalog availability depends on the deployment. Review a skill's source and instructions before attaching it to work.

## Skill health

Each skill shows a phase once the platform has processed it:

- **Ready**: the instructions are available to runs.
- **Error**: the latest fetch of a Git-sourced skill failed, for example because GitHub rate-limited the request. Previously fetched instructions stay available, and the fetch is retried every five minutes.
- **Invalid**: the skill cannot be used until you fix it. Typical causes are a link that is not a GitHub repository, a missing or malformed `SKILL.md` frontmatter, or content over 256 KiB. The skill's row shows the reason.

Git-sourced skills are fetched again only when you edit the skill. Save the skill after the upstream `SKILL.md` changes to pull the new version.

## Attach and manage skills

Installing a skill adds it to your personal library; it does **not** enable it in every project. In project settings, open **Tools & skills** and select only the skills relevant to that project's work. Trigger and Slack agent configurations offer the same selector. Mode defaults can add skills to runs alongside these selections. Only enabled skills are advertised to the agent and available for on-demand loading; skills that are missing or Invalid are left out of the agent's menu.

For example, a frontend project can enable `frontend-design` and `astro` without exposing unrelated installed skills to its runs.

This selection behavior applies to newly initialized runs. Existing runs retain their recorded skills; start a new run to use a narrower selection after updating project settings. A run's **Context** tab lists the skills enabled for it and marks the ones the agent has loaded.

Only members and administrators can create, edit, install, or delete skills; viewers can read them. Deleting a skill prevents future configurations from loading it; review references before deletion.

Skills are not MCP servers. Configure the underlying tool command and credential mappings in **Resources → MCP servers**. See the [Resources guide](./resources.md).
