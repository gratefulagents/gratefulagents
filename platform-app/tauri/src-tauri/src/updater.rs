// Desktop auto-updater backed by public GitHub release assets.
//
// The release workflow uploads signed updater artifacts plus `latest.json`.
// The manifest and artifacts use public browser-download URLs, so update
// checks do not collect or send a user GitHub token. Mobile builds keep stub
// commands because store-distributed apps cannot self-update in place.

#[derive(Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UpdateCheckResult {
    /// True when a newer build is available.
    pub available: bool,
    /// Version of the running app.
    pub current_version: String,
    /// Version of the available update, when `available`.
    pub version: Option<String>,
    /// Release notes of the available update.
    pub notes: Option<String>,
    /// Whether this install can self-update (false for deb/rpm installs).
    pub can_auto_install: bool,
    /// Human hint shown when `can_auto_install` is false.
    pub install_hint: Option<String>,
    /// Web URL of the latest release (manual download fallback).
    pub release_url: Option<String>,
}

#[derive(Clone, serde::Serialize)]
#[serde(rename_all = "camelCase")]
#[cfg(desktop)]
pub struct UpdateProgress {
    pub downloaded: u64,
    pub total: Option<u64>,
}

#[cfg(desktop)]
pub use desktop::*;
#[cfg(mobile)]
pub use mobile::*;

#[cfg(desktop)]
mod desktop {
    use std::sync::{Arc, Mutex};
    use std::time::{Duration, Instant};

    use tauri::{AppHandle, Emitter, Manager, Runtime};
    use tauri_plugin_updater::{Update, UpdaterExt};

    use super::{UpdateCheckResult, UpdateProgress};

    const MANIFEST_URL: &str =
        "https://github.com/gratefulagents/gratefulagents/releases/latest/download/latest.json";
    const RELEASE_URL: &str = "https://github.com/gratefulagents/gratefulagents/releases/latest";

    /// Update found by the last `updater_check`, consumed by
    /// `updater_install`.
    #[derive(Default)]
    pub struct PendingUpdate(Mutex<Option<Arc<Update>>>);

    fn can_auto_install() -> (bool, Option<String>) {
        #[cfg(target_os = "linux")]
        {
            if std::env::var_os("APPIMAGE").is_some() {
                (true, None)
            } else {
                (
                    false,
                    Some(
                        "This install was set up from a .deb/.rpm package — download the new build from the release page and install it manually. (AppImage installs update automatically.)"
                            .into(),
                    ),
                )
            }
        }
        #[cfg(not(target_os = "linux"))]
        {
            (true, None)
        }
    }

    /// Check the public release manifest for a newer desktop build.
    #[tauri::command]
    pub async fn updater_check<R: Runtime>(app: AppHandle<R>) -> Result<UpdateCheckResult, String> {
        let current_version = app.package_info().version.to_string();
        let updater = app
            .updater_builder()
            .endpoints(vec![MANIFEST_URL
                .parse()
                .map_err(|err| format!("Invalid manifest URL: {err}"))?])
            .map_err(|err| format!("Invalid updater endpoint: {err}"))?
            .build()
            .map_err(|err| format!("Could not initialize updater: {err}"))?;

        let update = updater
            .check()
            .await
            .map_err(|err| format!("Update check failed: {err}"))?;

        let (auto, hint) = can_auto_install();
        let state = app.state::<PendingUpdate>();
        let result = match update {
            Some(update) => {
                let result = UpdateCheckResult {
                    available: true,
                    current_version,
                    version: Some(update.version.clone()),
                    notes: update.body.clone(),
                    can_auto_install: auto,
                    install_hint: hint,
                    release_url: Some(RELEASE_URL.into()),
                };
                *state.0.lock().unwrap() = Some(Arc::new(update));
                result
            }
            None => {
                *state.0.lock().unwrap() = None;
                UpdateCheckResult {
                    available: false,
                    current_version,
                    version: None,
                    notes: None,
                    can_auto_install: auto,
                    install_hint: None,
                    release_url: Some(RELEASE_URL.into()),
                }
            }
        };
        Ok(result)
    }

    /// Download and install the update found by the last `updater_check`.
    /// Emits `updater-progress` events; on success the app is ready to be
    /// restarted (`updater_restart`).
    #[tauri::command]
    pub async fn updater_install<R: Runtime>(app: AppHandle<R>) -> Result<(), String> {
        let (auto, hint) = can_auto_install();
        if !auto {
            return Err(hint.unwrap_or_else(|| {
                "Auto-install is not supported for this install type.".into()
            }));
        }

        let update = app
            .state::<PendingUpdate>()
            .0
            .lock()
            .unwrap()
            .clone()
            .ok_or("No pending update — check for updates first.")?;

        let progress_app = app.clone();
        let mut downloaded: u64 = 0;
        let mut last_emit: Option<Instant> = None;
        update
            .download_and_install(
                move |chunk, total| {
                    downloaded += chunk as u64;
                    // Throttle IPC traffic; the UI only needs coarse progress.
                    let done = total.is_some_and(|t| downloaded >= t);
                    if done
                        || last_emit.map_or(true, |t| t.elapsed() >= Duration::from_millis(150))
                    {
                        last_emit = Some(Instant::now());
                        let _ = progress_app.emit(
                            "updater-progress",
                            UpdateProgress { downloaded, total },
                        );
                    }
                },
                || {
                    log::info!("update downloaded; installing");
                },
            )
            .await
            .map_err(|err| format!("Update failed: {err}"))?;

        log::info!("update installed; restart to apply");
        Ok(())
    }

    /// Relaunch the app to finish applying an installed update.
    #[tauri::command]
    pub fn updater_restart<R: Runtime>(app: AppHandle<R>) {
        app.restart();
    }
}

#[cfg(mobile)]
mod mobile {
    use tauri::{AppHandle, Runtime};

    use super::UpdateCheckResult;

    /// Auto-update is desktop-only; mobile builds ship through the stores.
    #[tauri::command]
    pub async fn updater_check<R: Runtime>(
        _app: AppHandle<R>,
    ) -> Result<UpdateCheckResult, String> {
        Err("Auto-update is not available on this platform.".into())
    }

    #[tauri::command]
    pub async fn updater_install<R: Runtime>(_app: AppHandle<R>) -> Result<(), String> {
        Err("Auto-update is not available on this platform.".into())
    }

    #[tauri::command]
    pub fn updater_restart<R: Runtime>(_app: AppHandle<R>) {}
}
