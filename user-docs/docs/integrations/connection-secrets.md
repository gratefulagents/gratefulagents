---
title: Connection Secrets
agentPrompt: >-
  Read https://gratefulagents.dev/docs/integrations/connection-secrets/ and explain how Connection Secrets store integration credentials, then help me add the one I need safely.
---

# Connection Secrets

GitHub, Slack, and Linear Project connections store references to Kubernetes Secrets. The connection dialog does not create those Secrets or accept secret values. A cluster operator must create each Secret in the **same Kubernetes namespace as the Project** before a user creates the connection.

Only people with permission to create or update Kubernetes Secrets can perform these steps. Dashboard access alone does not grant that permission.

## Required Secret data keys

| Connection | Secret data keys | Notes |
| --- | --- | --- |
| GitHub token | `token` | Use a fine-grained token limited to the repositories and actions the Entry point needs. |
| GitHub App | `private-key` | Store the GitHub App private key in PEM format. The connection also needs the App ID and installation ID. |
| Linear | `api-key` | Use an API key with access only to the intended Linear workspace, team, and project. |
| Slack | `bot-token`, `app-token`; optional `user-token` | `bot-token` is the bot OAuth token. `app-token` is the Socket Mode app-level token. |

The Secret **name** goes into **Manage connections**. Do not paste a token, API key, or private key into a Secret-name field.

## Create a Secret safely

Single-line tokens and API keys must not include a trailing newline. Prompt for them without echoing, then pipe the exact bytes to `kubectl`; the credential does not appear in the command or shell history.

For a GitHub token:

```bash
project_namespace='replace-with-project-namespace'
secret_name='replace-with-secret-name'
read -rsp 'GitHub token: ' secret_value; printf '\n'
printf '%s' "$secret_value" | kubectl -n "$project_namespace" \
  create secret generic "$secret_name" --from-file=token=/dev/stdin
unset secret_value
```

For a Linear API key, use the same pattern with the required `api-key` data key:

```bash
project_namespace='replace-with-project-namespace'
secret_name='replace-with-secret-name'
read -rsp 'Linear API key: ' secret_value; printf '\n'
printf '%s' "$secret_value" | kubectl -n "$project_namespace" \
  create secret generic "$secret_name" --from-file=api-key=/dev/stdin
unset secret_value
```

A GitHub App private key is multiline PEM data, so preserve its line breaks. Keep the downloaded key file private and import it directly:

```bash
project_namespace='replace-with-project-namespace'
secret_name='replace-with-secret-name'
private_key_file='/path/to/github-app-private-key.pem'
chmod 600 "$private_key_file"
kubectl -n "$project_namespace" create secret generic "$secret_name" \
  --from-file=private-key="$private_key_file"
```

Slack needs two single-line values. Create newline-free files in a private temporary directory, import them, then remove the directory:

```bash
project_namespace='replace-with-project-namespace'
secret_name='replace-with-secret-name'
umask 077
secret_dir="$(mktemp -d)"
trap 'rm -rf "$secret_dir"' EXIT
read -rsp 'Slack bot token: ' secret_value; printf '\n'
printf '%s' "$secret_value" > "$secret_dir/bot-token"
read -rsp 'Slack app token: ' secret_value; printf '\n'
printf '%s' "$secret_value" > "$secret_dir/app-token"
unset secret_value
kubectl -n "$project_namespace" create secret generic "$secret_name" \
  --from-file=bot-token="$secret_dir/bot-token" \
  --from-file=app-token="$secret_dir/app-token"
rm -rf "$secret_dir"
trap - EXIT
unset secret_dir
```

Add a newline-free `user-token` file and `--from-file=user-token=...` only when your Slack setup requires that optional token.

Do not use `--from-literal` with a credential typed directly on the command line: it can remain in shell history or process metadata. Do not include `kubectl get secret -o yaml` output in logs or public issues; Kubernetes Secret data is encoded, not encrypted for display.

## Verify without displaying values

Confirm that the Secret exists and contains the expected key names without printing its data:

```bash
project_namespace='replace-with-project-namespace'
secret_name='replace-with-secret-name'
kubectl -n "$project_namespace" describe secret "$secret_name"
```

`describe` shows key names and byte counts. It does not print the credential values.

## Rotate or remove credentials

1. Create a replacement Secret with the same required data keys.
2. Open **Project → Entry points → Manage connections**.
3. Edit the connection to reference the replacement Secret.
4. Disable and re-enable each affected Entry point so its generated runtime is recreated, then test it.
5. Remove the old Secret only after the new connection works and no other Project or workload references it.

Existing runs can retain references or material created before rotation. Stop or replace sensitive active runs when the credential itself was compromised.

Use namespace-scoped RBAC and grant Secret read/write access only to operators and service accounts that need it. Encrypt Kubernetes Secret data at rest and include credential rotation in the deployment's incident-response plan.

Continue with [GitHub](./github.md), [Slack](./slack.md), or [Linear](./linear.md) after the Secret is ready.
