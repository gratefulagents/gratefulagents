---
title: Connection
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/connection/ and explain the gratefulagents connection settings and how to run the diagnostics when something is off.
---

# Connection

**Connection** is available in the desktop app. It configures the backend for the active workspace on this device. The web app always uses the backend that served it.

## Update the active workspace connection

1. Open **Settings → Connection**.
2. Enter the **Endpoint URL** your team provides.
3. If required, enter **CF Access client ID** and **CF Access client secret**.
4. Select **Save & connect**.

Use Cloudflare Access values only when your workspace administrator provides them.

## Manage saved workspaces

The **Workspaces** section lists the backends saved on this device. You can rename a workspace, select **Switch** to use another one, or select **Remove**.

Remove only forgets that workspace and its sign-in on the device. It does not change the backend or delete its data.

## If connection fails

Confirm the endpoint URL and any Cloudflare Access values with your workspace administrator. You can also reconnect from the sign-in screen.
