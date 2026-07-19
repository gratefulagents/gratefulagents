---
title: Credentials and integrations
agentPrompt: >-
  A credential or integration in my gratefulagents workspace is failing. Read https://gratefulagents.dev/docs/troubleshooting/credentials-integrations/ and debug it with me.
---

# Credentials and integrations troubleshooting

Credential and integration failures often appear as clone failures, provider errors, unavailable models, missing GitHub repositories, undelivered webhooks, or Slack connection errors. Do not paste secret values into logs, screenshots, support tickets, or public GitHub issues.

## Saved credential is not detected

1. Open **Settings → Credentials**.
2. Confirm the credential shows **Saved**.
3. Save it again if you recently pasted a value.
4. Return to the project and keep **Use my saved credentials** enabled.
5. For Copilot, complete OAuth before expecting model suggestions.

For an API-key provider, confirm the key is valid, belongs to the selected provider, can use the selected model, and is allowed by the provider's organization/account policy. Also confirm the workspace can reach the provider network.

## OAuth flow does not finish

- Complete the browser or device-code prompt in the same session.
- Wait briefly after approval, then refresh the credential page.
- Cancel and start a new flow if the browser handoff expired.
- In the desktop app, confirm the OS did not block deep links or browser handoff.
- If your team permits a supported manual credential format, use it only through **Settings → Credentials**.

## GitHub token cannot clone or create a pull request

Check that the token can read the repository. For branch and pull-request creation, it also needs the required write access. For organizations that require SSO, authorize the token. For fine-grained tokens, include the target repository in the token's repository selection.

Do not run a command that prints the token to check it. Re-save a replacement token through the dashboard if you suspect it was copied incorrectly.

## A GitHub connection cannot access its repository

Open **Project → Entry points → Manage connections** and confirm the Entry point uses the intended GitHub connection.

For token authentication, confirm the referenced same-namespace Secret contains a `token` key and that the token can access the repository. For GitHub App authentication, confirm the App is installed for the repository, the App ID and installation ID are correct, and the referenced Secret contains the PEM under `private-key`.

Operators can confirm a Secret's key names and byte counts without displaying values:

```bash
kubectl -n <project-namespace> describe secret <secret-name>
```

Do not use `kubectl get secret ... -o yaml` in a public report because it can expose encoded secret data. See [Connection Secrets](../integrations/connection-secrets.md) for required schemas, creation, and rotation.

## GitHub webhook delivery fails

GitHub App webhooks use the controller's webhook listener on port `8091`. With the default chart release, the service is named `gratefulagents-controller-manager-github-webhooks` when `githubWebhook.enabled` is true.

Check the Kubernetes service and controller logs:

```bash
kubectl -n gratefulagents-system get service \
  gratefulagents-controller-manager-github-webhooks
kubectl -n gratefulagents-system logs \
  -l control-plane=controller-manager -c manager --tail=200
```

Then check the GitHub App's recent delivery view. For a GitHub App, configure the public HTTPS webhook URL to end in:

```text
/webhooks/github/app
```

The exact callback path must be publicly reachable through HTTPS without an interactive login or Cloudflare Access prompt; GitHub cannot complete those user authentication flows. Route only the required path to the webhook service, do not expose port `8091` directly, and validate each delivery's signature with the configured webhook secret. A per-repository classic webhook uses `/webhooks/github/<namespace>/<resource-name>` instead. Webhooks are optional for pull-request monitoring, but issue-comment trigger workflows need webhook delivery.

## Integration credential is unavailable to an MCP server

1. Open **Settings → Credentials** and confirm the integration and required key names exist.
2. Open **Resources → MCP servers** and verify each secret environment row references the intended integration and key.
3. Confirm the MCP server and any related Skill are attached to the Project.
4. If an MCP policy denies by default, allow the intended server and tools.

## Slack connection problems

For a Project Slack Entry point, check that:

- the Entry point is enabled and does not show **degraded**;
- its selected Slack connection references the intended Kubernetes Secret;
- the Secret contains `bot-token` and `app-token` keys;
- the bot token starts with `xoxb-` and the app-level token with `xapp-`;
- the Slack app is installed in the intended workspace with Socket Mode enabled; and
- the optional Team ID matches the intended Slack workspace.

The Project Entry-point UI does not provide the older Slack-agent inbox, draft, commander-allowlist, or shared-workspace-app settings.

## Cron credentials fail when the schedule runs

Cron Entry points run without an interactive user and inherit credentials from their Project. Open the failed run and use the first provider, clone, or credential error to correct **Project → Settings**. Retry manually with the same prompt after updating the Project.

## Safe recovery and escalation

1. Remove stale credentials you no longer use.
2. Save a known-good replacement through the dashboard.
3. Create a small test project or run with the least access needed.
4. Confirm the provider call and repository access succeed.
5. Re-enable the integration or trigger.

For a non-security issue, report only redacted symptoms and resource metadata in a [public GitHub issue](https://github.com/gratefulagents/gratefulagents/issues). For exposed credentials, webhook validation bypass, privilege escalation, or any suspected vulnerability, use [private vulnerability reporting](https://github.com/gratefulagents/gratefulagents/security/advisories/new) and do not open a public issue.
