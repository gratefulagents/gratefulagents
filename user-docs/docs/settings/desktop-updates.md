---
title: Desktop updates
agentPrompt: >-
  Read https://gratefulagents.dev/docs/settings/desktop-updates/ and explain how gratefulagents desktop app updates work and how I stay on a supported version.
---

# Desktop updates

**Desktop updates** is a desktop-app setting. The web app is served at its current deployment version and does not use this updater.

## Add the update token

The in-app updater downloads from a private distribution repository. Before it can check for an update:

1. Open **Settings → Desktop updates**.
2. Under **GitHub token**, enter a personal access token with read access to the distribution repository's releases. The UI recommends fine-grained **Contents** read-only access.
3. Select **Save token**.

The app stores this token only on the device. Select **Clear** to remove it and disable in-app update checks.

This token is for the in-app updater, not a general app credential. It is separate from the public installer script and from a GitHub token saved under [Credentials](./credentials.md).

## Check and install an update

1. Under **App updates**, select **Check for updates**.
2. If a compatible update is available, select **Download & install** when the app offers it.
3. Select **Restart now** after installation completes.

Some releases cannot be installed in place. In that case, the page provides a release-page hint instead.

## Choose automatic checks

Use **Automatic checks** to select **Launch only**, **Hourly**, **6h**, **12h**, or **Daily**. Automatic checks require a saved update token. A new version notification opens this page; failed background checks stay silent.
