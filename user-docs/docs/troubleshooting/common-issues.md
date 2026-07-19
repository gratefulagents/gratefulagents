---
title: Common issues
agentPrompt: >-
  My gratefulagents workspace is misbehaving. Read https://gratefulagents.dev/docs/troubleshooting/common-issues/ and help me diagnose and fix the problem step by step, asking me for symptoms as needed.
---

# Common issues

Use this page for dashboard, sign-in, run, or self-hosted control-plane problems. Start with the smallest relevant check, and redact sensitive output before sharing it.

## The desktop app will not install or open

Check the [GitHub releases page](https://github.com/gratefulagents/gratefulagents/releases) for an artifact that matches your operating system and CPU. On macOS, published builds currently target Apple Silicon. On Linux, confirm the AppImage matches `uname -m` and is executable:

```bash
chmod +x "$HOME/.local/bin/gratefulagents"
```

Do not use the repository's convenience installer as a recovery path on a security-sensitive device: it selects the mutable latest release, does not verify an asset checksum, and clears macOS quarantine attributes. Never pipe it from a mutable branch directly into a shell. On macOS, do not bypass Gatekeeper with a blanket `xattr -cr`. If the release does not provide a checksum or a verifiable signed and notarized build, ask the release maintainer or your workspace administrator for one.

## The app cannot connect or sign in

Symptoms include an offline banner, an unreachable sign-in screen, or lists that fail to load.

1. Refresh the app.
2. In the desktop app, open **Settings → Connection** and confirm the Operator URL.
3. If your workspace uses Cloudflare Access, confirm its client ID and client secret.
4. Switch workspaces and back again, then sign out and sign in.
5. For Google sign-in, ask the operator to confirm Google auth is enabled and that your account has the intended role.

## Check a self-hosted installation

For Kind, first set the dedicated kubeconfig and tools path described in [Run locally with Kind](../getting-started/self-hosting-kind.md). For k3s, run the commands on the server as the configured login user.

```bash
kubectl get nodes
kubectl -n gratefulagents-system get pods,pvc,svc
helm -n gratefulagents-system status gratefulagents
kubectl -n gratefulagents-system logs \
  -l control-plane=controller-manager -c manager --tail=200
```

The dashboard service is `gratefulagents-controller-manager-dashboard` for the default release. The controller manager deployment and services use the `control-plane=controller-manager` label, so use that label instead of guessing names when you changed the Helm release name.

If the dashboard is unreachable, check:

- the dashboard service type: `ClusterIP` requires port-forwarding;
- the port selected by the installer (`8090` for the default k3s `LoadBalancer` path);
- provider and host firewall rules;
- whether pods are pending for image pulls or persistent storage; and
- whether a printed node IP is private rather than the server's public address.

For a `ClusterIP` dashboard with default names:

```bash
kubectl -n gratefulagents-system port-forward \
  service/gratefulagents-controller-manager-dashboard 8090:8090
```

## Inspect entry-point resources and runs

The controller creates and reconciles Kubernetes custom resources. Use fully qualified resource names so these commands do not depend on client-side short names:

```bash
kubectl -n gratefulagents-system get agentruns.platform.gratefulagents.dev
kubectl -n gratefulagents-system get projects.triggers.gratefulagents.dev
kubectl -n gratefulagents-system get githubrepositories.triggers.gratefulagents.dev
kubectl -n gratefulagents-system get linearprojects.triggers.gratefulagents.dev
kubectl -n gratefulagents-system get slackagents.triggers.gratefulagents.dev
kubectl -n gratefulagents-system get crons.triggers.gratefulagents.dev
```

Inspect one resource's events and status when an entry point does not create a run:

```bash
kubectl -n gratefulagents-system describe \
  githubrepositories.triggers.gratefulagents.dev <resource-name>
```

Replace the resource type and name with the failing `Project`, `GitHubRepository`, `LinearProject`, `SlackAgent`, `Cron`, or `AgentRun`. If kubectl reports that the resource type does not exist, check the chart release and CRD installation before changing application configuration.

## A project, run, or diff is missing

For a missing project, confirm you are in the expected workspace and have access. It may have been renamed, deleted, or shared with another account.

A run cannot accept messages when you have Viewer access, it is terminal/stopped/failed, it is waiting for a specific quick action, the workspace is offline, or it is still starting. If an active run is stuck, use **Stop current turn** or ask an owner/admin to stop or retry it.

For a missing or stale diff, wait for the current tool step, switch away from and back to **Diff**, and confirm the run edited files. If the diff is truncated, ask the agent for a changed-file summary.

## Pull request creation or automation fails

For a manually created pull request, confirm a diff exists, the saved GitHub token can read the repository and create branches/PRs, organization SSO is authorized if required, and the base branch exists.

For a GitHub trigger, inspect the `GitHubRepository` resource and controller logs. Confirm the source is Ready, the repository's configured credential or GitHub App installation can access the repository, and the issue/comment satisfies the resource's trigger rules. Pull-request monitoring can use outbound GitHub polling; inbound webhooks are optional for that path.

## A Project Entry point did not create a run

Open the Project and inspect the automation under **Entry points**. Check its status badge and last activity before looking at the generated Kubernetes resource and controller logs.

- **Cron:** confirm the Entry point is enabled, the schedule and time zone are correct, and the default `Forbid` concurrency policy is not skipping a tick while an earlier run remains active.
- **Slack:** confirm the Entry point is enabled, its connection references a Secret containing `bot-token` and `app-token`, and the Slack app is installed in the intended workspace with Socket Mode available.
- **GitHub:** confirm the Entry point is enabled, its connection can access the repository, and the configured event matches the request. Issue polling requires a resolvable ModeTemplate label; issue comments require the webhook path and trigger keyword.
- **Linear:** the current Project trigger dialog does not enable automatic approved-issue intake. See [Linear](../integrations/linear.md#current-intake-behavior) before treating a UI-created Entry point as automatic issue intake.

## Ask for help safely

For a non-security bug, open a [public GitHub issue](https://github.com/gratefulagents/gratefulagents/issues) with:

- the version, chart version, or commit;
- deployment path (Kind, k3s, or another Kubernetes installation);
- resource type/name, namespace, and visible status or error;
- redacted controller logs and relevant `kubectl describe` events; and
- steps to reproduce and expected versus actual behavior.

Before posting, remove passwords, API keys, OAuth tokens, GitHub App private keys, webhook secrets, kubeconfig contents, Cloudflare tunnel tokens, private repository URLs, and customer data. Use [private vulnerability reporting](https://github.com/gratefulagents/gratefulagents/security/advisories/new) for security issues; never file those as public issues.
