#[cfg(desktop)]
use tauri::Manager;
#[cfg(desktop)]
use tauri_plugin_opener::OpenerExt;

/// Open the directory where tauri-plugin-log writes this app's diagnostic logs.
#[tauri::command]
pub fn open_log_directory(app: tauri::AppHandle) -> Result<(), String> {
    #[cfg(desktop)]
    {
        let log_dir = app
            .path()
            .app_log_dir()
            .map_err(|err| format!("Could not locate the app log directory: {err}"))?;
        app.opener()
            .open_path(log_dir.to_string_lossy(), None::<&str>)
            .map_err(|err| format!("Could not open the app log directory: {err}"))?;
        return Ok(());
    }

    #[cfg(mobile)]
    {
        // The app handle is only needed by the desktop implementation.
        let _ = app;
        Err("Opening the app log directory is not supported on mobile".into())
    }
}
