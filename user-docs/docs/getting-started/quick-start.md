---
title: Quick start
agentPrompt: >-
  Read https://gratefulagents.dev/docs/getting-started/quick-start/ and get me to a working gratefulagents workspace. Ask me whether this is a laptop evaluation or a server install before you start, then follow the matching guide.
---

# Quick start

Use this path to deploy or connect to a workspace, then start a first chat. You do not need a repository or project for that first chat.

## Choose your starting point

### I administer the workspace

Complete these in order:

1. **Install GratefulAgents.** Use [Kind](./self-hosting-kind.md) for local evaluation on a macOS or Linux laptop. Use [k3s](./self-hosting-k3s.md) for a dedicated Debian or Ubuntu server and the Internet-facing path.
2. **Publish and protect k3s.** Follow [Publish securely with Cloudflare](./cloudflare-access.md) to create a public HTTPS hostname, protect it with Cloudflare Access, and create service-token credentials for installed apps. Keep the origin dashboard port closed to the Internet.
3. **Distribute a client.** Give users the HTTPS Operator URL and, when needed, device-specific Cloudflare Access credentials. Follow [Install mobile and desktop apps](./web-desktop-workspaces.md); the published iOS IPA is unsigned and must be signed before installation, and the Android APK is a debug build for sideloading.

### I am joining an existing workspace

Ask your administrator for the web URL or installed app, the HTTPS Operator URL, and any Cloudflare Access client ID and client secret. Never use or request the deployment's Cloudflare Tunnel token.

## 1. Open the app

- **Web app:** Open the HTTPS URL your team provides.
- **iOS, Android, or desktop app:** [Download and install the appropriate app](./web-desktop-workspaces.md), then add or select the workspace your team provides.

If the installed app's workspace uses Cloudflare Access, enter the client ID and client secret your team provides on the connection screen.

## 2. Sign in

1. In an installed app, connect to the workspace first.
2. Sign in with username/password or Google when your workspace enables it.
3. The app opens **Home**.

See [Sign in](./sign-in.md) for help with either method.

## 3. Add a provider credential

Open **Settings → Credentials** and add the provider credential you intend to use. The available providers and sign-in methods depend on the deployment.

You can add a GitHub token later for repository or pull-request work. Do not paste credentials into chat.

## 4. Start a repo-free first chat

On **Home**:

1. Enter a task.
2. Press <span className="kbd">Enter</span> or click the send button.

Example task:

```text
Explain the trade-offs between optimistic and pessimistic locking for an API that updates inventory.
```

When no project exists, the app creates a **Personal workspace** project using your saved provider credential and starts the run there. This project has no repository, so it is useful for questions, planning, and other repo-free work.

If the app asks you to connect a provider, return to **Settings → Credentials** and save one first.

## 5. Follow the run

The app opens the run after it starts. Use **Agent Ops** to return to runs and check their current state. The run views and controls shown to you depend on the deployment and your permissions.

## 6. Add a repository project when needed

Create a project when the task needs repository defaults or repeatable configuration:

1. Open the **Projects** tree or project list.
2. Create a project and enter its name.
3. Add a repository URL only when the project needs one.
4. Choose the provider and model available to your workspace.
5. Keep saved credentials selected when you want the project to use credentials from **Settings → Credentials**.

You can also attach [skills or MCP servers](../settings/resources.md) when your workspace configuration permits them.
