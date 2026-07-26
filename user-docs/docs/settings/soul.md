---
title: SOUL persona
seoTitle: Configure Your SOUL Agent Persona | GratefulAgents
description: Save a personal SOUL persona in GratefulAgents so teammates can ask an AI agent for your perspective on code reviews, plans, and architectural decisions.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/soul/ and explain what a SOUL persona is in gratefulagents, then help me draft one for my agent.
---

# SOUL persona

**SOUL** is your personal agent persona. Teammates can ask an agent for your perspective on a question, plan, or diff, and the response uses the guidance you save.

## Save a SOUL

1. Open **Settings → SOUL**.
2. Describe your role, priorities, review approach, and communication preferences.
3. Select **Save SOUL**.

Use concise, durable guidance. For example:

```markdown
## What I care about
- Backward-compatible API changes.
- Tests for failure paths.
- Small, reversible diffs.
```

The page shows when the SOUL was last saved. Until you save it, other agents have no personal guidance to consult.

## Keep it safe

Do not include secrets, customer data, or information you would not want workspace teammates to use as guidance.
