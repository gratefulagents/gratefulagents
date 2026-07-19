---
title: Git identity
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/git-identity/ and help me configure the Git identity and commit attribution my gratefulagents runs use.
---

# Git identity

Use **Settings → Git identity** to set the name and email that new runs use when authoring commits.

## Set or clear the identity

1. Enter both **Name** and **Email**.
2. Select **Save git settings**.

Both fields are required together. Clear both fields and save to remove the personal identity. When no identity is set, the app authors commits as the gratefulagents GitHub App. The app is always credited with a `Co-authored-by` trailer.

Use a GitHub noreply address if you do not want a personal email in public commits, for example `username@users.noreply.github.com`.

This setting affects new runs. It does not rewrite existing commits.
