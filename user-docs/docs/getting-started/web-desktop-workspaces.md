---
title: Install mobile and desktop apps
agentPrompt: >-
  Read https://gratefulagents.dev/docs/getting-started/web-desktop-workspaces/ and tell me which gratefulagents app to install on each of my devices and how to connect them to my workspace.
---

# Install mobile and desktop apps

The web, iOS, Android, and desktop apps use the same GratefulAgents backend. Installed apps add locally saved workspace connections, Cloudflare Access service-token fields, and a separate sign-in state for each workspace.

## Choose web or an installed app

- **Web:** Open the HTTPS URL your administrator provides. The web app uses the backend that served it, so there is no endpoint to configure.
- **iOS, Android, or desktop:** Install a release supplied by the project or your administrator, then enter the workspace's Operator URL. Use an installed app when you need Cloudflare Access service-token authentication or multiple saved workspaces.

Desktop release artifacts currently target Apple Silicon macOS and AMD64 or ARM64 Linux. Windows is not currently listed as a release target.

## Download a release artifact

1. Open the [GratefulAgents releases page](https://github.com/gratefulagents/gratefulagents/releases).
2. Select the release your administrator supports. Avoid prereleases unless you are testing one intentionally.
3. Expand **Assets** and download the artifact for your device:

   | Device | Artifact to choose |
   | --- | --- |
   | Apple Silicon Mac | macOS ARM64 `.dmg` |
   | Debian or Ubuntu AMD64/ARM64 | matching `.deb` |
   | Fedora, RHEL, or compatible Linux AMD64/ARM64 | matching `.rpm` |
   | Other supported Linux distribution | matching `.AppImage` |
   | iPhone or iPad | Add the official AltStore Classic source under [Install on iOS](#install-on-ios); its direct-install fallback uses `gratefulagents-<tag>-ios-arm64-unsigned.ipa` |
   | Android phone or tablet (ARM64) | `gratefulagents-<tag>-android-arm64-debug.apk` — read [Install on Android](#install-on-android) before downloading |

4. Verify any checksum and signing information published by the release owner. If none is available, confirm the expected release tag and artifact with your administrator before running it.

Do not use an artifact sent through an untrusted file-sharing link. The repository's convenience installer downloads a mutable latest release without verifying a checksum and clears macOS quarantine attributes; it is not recommended for security-sensitive devices.

## Install on macOS

1. Confirm that the Mac has Apple silicon (**Apple menu → About This Mac → Chip**).
2. Open the downloaded `.dmg`.
3. Drag **GratefulAgents** to **Applications**, then eject the disk image.
4. Open GratefulAgents from **Applications**.

Prefer an artifact signed and notarized by a publisher you trust. If macOS reports that it cannot verify the developer, do not disable Gatekeeper globally or clear quarantine attributes. Ask your administrator for a signed and notarized build or for the organization's approved installation procedure.

## Install on Linux

For a Debian package, run from the directory containing the download:

```bash
sudo apt install ./gratefulagents-<version>-<architecture>.deb
```

For an RPM package:

```bash
sudo dnf install ./gratefulagents-<version>-<architecture>.rpm
```

For an AppImage:

```bash
chmod +x ./gratefulagents-<version>-<architecture>.AppImage
./gratefulagents-<version>-<architecture>.AppImage
```

Use the downloaded asset's real filename in place of the examples. Prefer the native `.deb` or `.rpm` package when your distribution supports it, because the package manager can track installation and removal.

## Install on iOS

:::warning AltStore Classic signs the unsigned IPA for your device
The release IPA is intentionally unsigned and cannot be installed by tapping it directly. [AltStore Classic](https://altstore.io/) sends it to AltServer, which signs it with your Apple ID before installation. Do not install a certificate or provisioning profile supplied by an unknown third party.
:::

### Install with AltStore Classic

AltStore Classic is the project's free public installation method. It works worldwide, but Apple requires apps installed with a regular free Apple ID to be refreshed through AltServer every seven days. Apple also limits a device to three active sideloaded apps; AltStore itself normally occupies one slot, leaving two for other apps. Read [AltStore's installation instructions](https://faq.altstore.io/altstore-classic/) and install AltStore and AltServer before continuing.

1. On the iPhone or iPad, open the [Grateful Agents source in AltStore](https://altstore.io/source/github.com/gratefulagents/gratefulagents/releases/latest/download/altstore-source.json?app=dev.gratefulagents.app).
2. Confirm that the source is **Grateful Agents**, the app identifier is `dev.gratefulagents.app`, and the source URL is the official GitHub repository shown below.
3. Add the source, select **Grateful Agents**, and choose **FREE** to install it.
4. Keep AltServer available on the computer AltStore uses. In AltStore, periodically open **My Apps** and choose **Refresh All** if background refresh has not renewed the app.
5. Open Grateful Agents and continue to [Connect the installed app](#connect-the-installed-app).

The canonical source URL is:

```text
https://github.com/gratefulagents/gratefulagents/releases/latest/download/altstore-source.json
```

Every completed release regenerates this source from the IPA's actual bundle version, minimum iOS version, permissions, and versioned GitHub download URL. AltStore downloads the unsigned IPA and re-signs it for the current device; the project never receives your Apple ID or password.

If the source link does not open automatically, add the canonical URL manually in AltStore's **Sources** screen. As a fallback, download `gratefulagents-<tag>-ios-arm64-unsigned.ipa` from the official GitHub release, open AltStore's **My Apps** tab, select **+**, and choose the downloaded IPA. Manual installation works, but adding the source makes future updates visible in AltStore.

AltStore **PAL** is a different, region-limited alternative marketplace and does not install IPA files. Use AltStore Classic for this unsigned IPA distribution method.

### Organization-managed distribution

An administrator must sign the app for the intended devices using an appropriate Apple Developer certificate and provisioning profile, then distribute it using an approved method such as TestFlight or mobile device management (MDM). Apple recommends MDM for internal organization apps because installation and trust can be managed centrally.

For users:

1. Follow the TestFlight invitation or MDM installation instructions from your administrator.
2. Confirm that the displayed app and organization are the ones you expect.
3. Install and open GratefulAgents.
4. Continue to [Connect the installed app](#connect-the-installed-app).

If your organization gives you a signed development or Ad Hoc IPA instead, use only its documented Apple Configurator, Xcode, or MDM procedure. The device must be included by the provisioning profile. A build signed for a different device cannot be installed.

### Build and sign a personal development copy

Developers with macOS, current Xcode command-line tools, and an Apple Developer team can build from source rather than trying to install the unsigned release IPA:

```bash
git clone https://github.com/gratefulagents/gratefulagents.git
cd gratefulagents/platform-app
pnpm install --frozen-lockfile
make tauri-ios-init
```

Open the generated Xcode project below `tauri/src-tauri/gen/apple`, select the app target, and choose your team under **Signing & Capabilities**. Connect and unlock the iPhone or iPad, enable Developer Mode if Xcode requests it, then list its identifier and build/install:

```bash
xcrun devicectl list devices
make tauri-ios-install IOS_DEVICE='<device-identifier>'
```

Personal development signing may expire and require rebuilding. This path is for development devices, not general user distribution.

## Install on Android

:::info The release APK is a debug build
The release workflow currently publishes `gratefulagents-<tag>-android-arm64-debug.apk`, a debug-signed ARM64 build. It installs by sideloading and is intended for evaluation and internal use; it is not distributed through Google Play.
:::

1. Download the `android-arm64-debug.apk` asset on the device (or transfer it from a computer).
2. Open the downloaded file. When Android asks, allow the browser or file manager to install unknown apps for this one installation. Only grant this to install an APK whose release tag you have verified with your administrator.
3. Confirm the install prompt, then open GratefulAgents.
4. Continue to [Connect the installed app](#connect-the-installed-app).

Debug builds are signed with a debug key: an update must come from the same source, and organizations that need managed distribution should re-sign the app and deliver it through their MDM or a private Play track.

## Connect the installed app

Get these values from your workspace administrator before starting:

- a name for the workspace;
- the **Operator URL**, including `https://` and no private cluster port; and
- when Cloudflare Access protects the URL, the **CF Access client ID** and **CF Access client secret**.

Then:

1. Open GratefulAgents.
2. Open the workspace switcher and choose **Add workspace**. On a first launch, the connection form may appear automatically.
3. Enter the workspace name and Operator URL, for example `https://agents.example.com`.
4. If required, expand **Cloudflare Access** and enter the client ID and client secret. These are an Access **service token**, not the Cloudflare Tunnel token.
5. Select **Add** or **Save & connect**.
6. Sign in to GratefulAgents with the method enabled by the workspace.

Each saved workspace has its own endpoint, Cloudflare Access values, and sign-in state on that device. Treat the Access client secret like a password. Do not copy it into chat, logs, screenshots, or issue reports.

If you administer the deployment, follow [Publish securely with Cloudflare](./cloudflare-access.md) to create the public Operator URL and the installed-app service token.

## Manage or troubleshoot a connection

Open **Settings → Connection** to update the active endpoint or Cloudflare Access values, rename a saved workspace, switch workspaces, or remove one from the device.

Removing a workspace forgets its local connection and sign-in. It does not delete backend users, runs, projects, or data.

If connection fails:

1. Confirm that the Operator URL starts with `https://` and opens through the expected Cloudflare Access flow.
2. Confirm that the Access service token has not expired or been revoked and that its policy action is **Service Auth**.
3. Re-enter the client ID and secret without extra spaces.
4. Ask the administrator to check Cloudflare Access logs and the k3s connector status.

The desktop in-app updater is separate from the workspace connection. **Settings → Desktop updates** uses a GitHub token with read access to the configured distribution repository and stores it only on that device. See [Desktop updates](../settings/desktop-updates.md).
