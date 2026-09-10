# Google Play release setup

The `Release` workflow builds a signed Android App Bundle (AAB) for
`dev.gratefulagents.app` on every semantic release from `main`. It attaches the
AAB to the GitHub release and then publishes it through the Google Play
Developer API.

Repository automation cannot create the Play developer account, accept legal
agreements, complete the store listing, or perform Google's required first
manual upload. Complete the one-time setup below before relying on automatic
publishing.

## 1. Create the Play app

1. Register in [Google Play Console](https://play.google.com/console/developers)
   and complete account verification.
2. Create an app whose package name is exactly `dev.gratefulagents.app`.
3. Enroll in **Play App Signing**.
4. Complete all Play Console tasks required for the intended track, including
   app access, ads, content rating, Data safety, target audience, privacy
   policy, store listing, countries, and pricing.
5. New personal developer accounts may have additional closed-testing and
   production-access requirements. Follow the requirements shown for the
   account in Play Console.

The package name is a permanent identity. Changing it requires a separate Play
listing and prevents updates to existing installations.

## 2. Create and protect the upload key

Generate the upload keystore once:

```bash
keytool -genkey -v \
  -keystore upload-keystore.jks \
  -keyalg RSA \
  -keysize 2048 \
  -validity 10000 \
  -alias upload
```

Back up the keystore, alias, and passwords in a secure password manager or
secrets vault. Do not commit them. Encode the keystore for GitHub Actions:

```bash
base64 < upload-keystore.jks | tr -d '\n'
```

On macOS, the following is also supported:

```bash
base64 -i upload-keystore.jks | tr -d '\n'
```

Add these **Actions secrets** under repository **Settings → Secrets and
variables → Actions**:

| Secret | Value |
| --- | --- |
| `ANDROID_UPLOAD_KEYSTORE_BASE64` | Single-line base64 output for `upload-keystore.jks` |
| `ANDROID_UPLOAD_KEYSTORE_PASSWORD` | Keystore password |
| `ANDROID_UPLOAD_KEY_ALIAS` | Key alias, `upload` in the example |
| `ANDROID_UPLOAD_KEY_PASSWORD` | Password for the key alias |

The workflow decodes the keystore only into the ephemeral Actions runner,
validates the alias with `keytool`, and adds a release signing configuration to
Tauri's generated Gradle project. Gradle reads signing values directly from the
build process environment, avoiding plaintext credential files. Credentials and
key material are not uploaded as artifacts.

## 3. Configure Play Developer API access

1. In Google Cloud, enable the **Google Play Android Developer API**.
2. Create a service account and a JSON key.
3. In Play Console, open **Users and permissions**, invite the service-account
   email, and grant it release permissions for `dev.gratefulagents.app`.
4. Add one more GitHub Actions secret:

| Secret | Value |
| --- | --- |
| `GOOGLE_PLAY_SERVICE_ACCOUNT_JSON` | Complete JSON service-account key file contents |

Restrict the service account to this app and only the release permissions it
needs. Rotate the JSON key if it is ever exposed.

## 4. Bootstrap the first Play release

Google requires the package to have an uploaded bundle before the publishing
API can address it. The first AAB must therefore be uploaded manually:

1. With the four Android signing secrets configured, let the release workflow
   build an AAB. The Google Play job may report `Package not found`, but the AAB
   remains attached to the now-published GitHub release.
2. Download that `.aab` from the GitHub release.
3. In Play Console, create an **Internal testing** release and manually upload
   the AAB.
4. Finish and roll out the internal release.
5. Publish the next semantic release to verify automated API delivery. Do not
   retry the already accepted AAB because its `versionCode` is now in use.

Alternatively, build the first signed AAB locally with the same upload key and
upload it before enabling the automated release job.

## 5. Choose the release track

The workflow defaults to the safe `internal` track. Optionally add an Actions
**repository variable** named `GOOGLE_PLAY_TRACK`:

| Value | Behavior |
| --- | --- |
| `internal` | Publishes to internal testers; recommended during setup |
| `alpha` | Publishes to the closed alpha track, if configured |
| `beta` | Publishes to the open beta track, if configured |
| `production` | Publishes a completed release to all production users |

Track availability and names must match Play Console. Keep the variable set to
`internal` until testing and production-access requirements are complete.
When it is changed to `production`, each successful semantic release is sent as
a completed production rollout. Users receive it through Google Play according
to their automatic-update settings.

## Release behavior

`.github/workflows/app-release.yml` performs the following Android steps:

1. Uses the semantic release version as Android `versionName`.
2. Uses the Git commit count as a monotonically increasing `versionCode`.
3. Initializes Tauri's generated Android project and stamps the app icons.
4. Builds a signed universal AAB containing Android ARM64, ARMv7, x86, and
   x86_64 native libraries.
5. Attaches the AAB to the GitHub release and preserves it as a short-lived
   workflow artifact.
6. Publishes the AAB to `GOOGLE_PLAY_TRACK` after the GitHub release succeeds.

The Android and Google Play jobs pin credential-adjacent third-party Actions to
reviewed commits. The Google Play job has no repository token permissions. It
runs after the GitHub release is finalized, so a Play API or store-policy
failure fails that job but does not delete an otherwise valid GitHub release.

Official references:

- [Tauri Google Play distribution](https://v2.tauri.app/distribute/google-play/)
- [Tauri Android code signing](https://v2.tauri.app/distribute/sign/android/)
- [Play Console release documentation](https://support.google.com/googleplay/android-developer/topic/7072031)
