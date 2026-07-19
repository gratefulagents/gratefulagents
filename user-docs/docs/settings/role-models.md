---
title: Role models
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/role-models/ and explain role models in gratefulagents, then help me pick which model each role should use.
---

# Role models

Use **Settings → Role models** to set personal model overrides for the specialist roles available in your workspace.

## Set an override

1. Open **Settings → Role models**.
2. For a role, enter a model for OpenAI, Anthropic, or Copilot when that provider is relevant.
3. Select **Save role models**.

Leave a field blank to use the platform mapping. When the platform has no mapping for that provider, the role inherits the parent model. Saved preferences apply to newly created runs.

Roles can be unavailable when the deployment has not configured them. Role definitions themselves are managed under **Resources → Roles** by administrators.
