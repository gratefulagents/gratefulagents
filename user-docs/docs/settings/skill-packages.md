---
title: Skills
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/skill-packages/ and explain how skills work in gratefulagents, then help me install a skill package.
---

# Skills

**Skills** live under **Resources**, not Settings. A skill is reusable agent instruction that you write inline or install from the skills.sh catalog. It can require MCP servers, which the app attaches automatically when the skill is used.

For the full resource inventory, see [Resources](./resources.md).

## Create an inline skill

1. Open **Resources → Skills**.
2. Select **New inline skill**.
3. Enter a name, optional version and description, and the instructions.
4. Select any required MCP servers.
5. Save the skill.

Use a stable, descriptive name. A skill's instructions should say when the agent should use it and what outcome it should produce.

## Install from skills.sh

1. Open **Resources → Skills**.
2. Select **Install from skills.sh**.
3. Search or browse the catalog.
4. Select **Install** for the skill you want.

Catalog availability depends on the deployment. Review a skill's source and instructions before attaching it to work.

## Attach and manage skills

Attach a skill where your project or agent configuration offers a skill selector. You can edit or delete skills that you manage. Deleting a skill prevents future configurations from loading it; review references before deletion.

Skills are not MCP servers. Configure the underlying tool command and credential mappings in **Resources → MCP servers**. See the [Resources guide](./resources.md).
