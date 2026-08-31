---
title: Sign in
seoTitle: Sign In to GratefulAgents | GratefulAgents
description: Sign in to GratefulAgents with a one-time setup link, username and password, or Google. Includes troubleshooting for failed sign-ins and Cloudflare Access.
agentPrompt: >-
  Read https://gratefulagents.dev/docs/getting-started/sign-in/ and explain how signing in to gratefulagents works, then walk me through my first sign-in.
---

# Sign in

The sign-in methods available depend on your workspace configuration.

## One-time setup link

The [Kind](./self-hosting-kind.md) and [k3s](./self-hosting-k3s.md) installers print a one-time sign-in link of the form `<dashboard-url>/login?setup_token=...`. Opening it in a browser signs you in as `admin` without entering a password.

The link is single use and expires seven days after the admin account is created. The token is stored in the `setup-token` key of the `gratefulagents-admin-credentials` Secret until it is redeemed. If the link was already used or has expired, the sign-in page shows **This setup link is invalid or was already used. Sign in with your password instead.** — retrieve the admin password with the command each installer prints and sign in with username and password.

## Username and password

1. Open the app.
2. In the desktop app, connect to the workspace first.
3. Enter your username and password.
4. Click **Sign In**.

## Google sign-in

If the sign-in screen shows Google, select it and complete the prompted flow. Google sign-in is available only when the workspace enables it.

## Sign out

1. Open **Settings**.
2. In **Account**, click **Sign out**.
3. Confirm the dialog.

You must sign in again before using that workspace.

## If sign-in fails

- Confirm that the desktop app is connected to the correct workspace URL.
- Confirm your credentials with a workspace administrator.
- If Google is unavailable, ask whether the workspace enables it.
- If the workspace uses Cloudflare Access, confirm the client ID and client secret.
- In the desktop app, remove and add the workspace again if its locally saved connection is stale. This only removes it from that device.
