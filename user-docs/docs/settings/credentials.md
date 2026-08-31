---
title: Credentials
seoTitle: Save Model Provider Credentials | GratefulAgents
description: Store Anthropic, OpenAI, OpenRouter, xAI, and GitHub credentials in GratefulAgents. Includes OAuth sign-in, integration credential groups, and safe sharing.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/credentials/ and help me add model provider credentials to gratefulagents and choose which providers my runs use.
---

# Credentials

Save provider access once in **Settings → Credentials** so you can select it for projects and runs. The providers and OAuth paths available to you depend on the deployment.

## Add or remove credentials

The page supports these saved credential types:

| Credential | Purpose |
| --- | --- |
| **Anthropic API key** or **Claude OAuth** | Anthropic model access. |
| **OpenAI API key** or **ChatGPT OAuth** | OpenAI model access. |
| **OpenRouter API key** | OpenRouter model access. |
| **xAI / Grok API key** | xAI model access. |
| **GitHub Copilot OAuth** | Copilot model access. |
| **GitHub token** | GitHub access for work that needs it. |

Enter or replace values, then select **Save credentials**. Saved values are write-only: the page shows that a credential is saved, but not its value. Select **Remove** beside a saved item to remove it.

Saving an API key also verifies it by listing the models the key can access. A working key shows a confirmation such as **key verified — 12 models available**; a failing check shows a warning that verification failed. The verification is advisory: the key is saved either way, so fix and re-save a key that fails verification. OAuth credentials are not verified this way.

The desktop app can offer **Use your local sign-in** when it detects supported local provider credentials. This option is not available in every environment.

## OAuth

Use the relevant OAuth card when it is available. The page can also accept manually supplied OAuth JSON for Claude, ChatGPT/OpenAI, or Copilot. Follow your workspace's provider-access policy before adding credentials.

For **Claude OAuth**, sign in to Claude through the displayed link, approve access, then paste the whole string from the confirmation page — it looks like `code#state` — into the field labeled **Paste the whole code (code#state)**. The page rejects a pasted value that does not match that format before submitting it.

For **ChatGPT OAuth**, select **Sign in with ChatGPT** to use the browser flow. The desktop app also offers **Use a device code instead**, which shows a code to enter on OpenAI's device page — useful when the browser callback cannot reach the app.

## Integration credentials

Integration credentials are named groups of secret key/value pairs. They are available for MCP server configuration under [Resources](./resources.md).

1. In **Integration credentials**, enter an integration name.
2. Add its secret key/value rows.
3. Save the integration.

The app lists saved integration and key names, not secret values. Use plain environment values only for non-secret configuration.

## Share credentials deliberately

The **Share** action can copy selected saved credentials to another workspace user. The recipient can use the copied credentials in their own runs, and usage counts against the same provider account. Copies do not stay synchronized.

Only share credentials when your organization allows it. A project or run share alone does not copy your credentials.

## Use credentials safely

- Do not paste credentials into chat.
- Use saved credentials rather than putting secrets in project instructions.
- Remove or rotate credentials according to your organization policy.
- Treat a GitHub token for [desktop updates](./desktop-updates.md) as separate from the GitHub token on this page.
