---
title: Connection
seoTitle: Workspace Connection Settings | GratefulAgents
description: Set the Operator URL and Cloudflare Access values for a saved GratefulAgents workspace. Rename, switch, and remove workspace connections on a device.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/connection/ and explain the gratefulagents connection settings and how to fix a failing workspace connection.
---

# Connection

**Connection** is available in the installed apps (desktop, iOS, and Android). It configures the backend for the active workspace on this device. The dashboard always uses the backend that served it, so it has no connection settings.

For the full install and connection walkthrough, see [Install mobile and desktop apps](../getting-started/web-desktop-workspaces.md).

## Update the active workspace connection

1. Open **Settings → Connection**.
2. Enter the **Operator URL** your team provides.
3. If required, enter **CF Access client ID** and **CF Access client secret**. Use these values only when your workspace administrator provides them.
4. Select **Save & connect**.

## Manage saved workspaces

The **Workspaces** section lists the backends saved on this device. You can rename a workspace, select **Switch** to use another one, or select **Remove**. Remove only forgets that workspace and its sign-in on the device; it does not change the backend or delete its data.

## If connection fails

Confirm the Operator URL and any Cloudflare Access values with your workspace administrator, or reconnect from the sign-in screen. For detailed checks, see [Manage or troubleshoot a connection](../getting-started/web-desktop-workspaces.md#manage-or-troubleshoot-a-connection).
