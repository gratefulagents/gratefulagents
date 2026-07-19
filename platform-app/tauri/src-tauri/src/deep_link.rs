// Deep-link handler. Forwards every `gratefulagents://...` URL that arrives via OS
// registration (or via the single-instance relay) to the frontend as
// `deep-link://open`.

use tauri::{AppHandle, Emitter, Runtime};

#[cfg(desktop)]
use tauri::Manager;

#[cfg(desktop)]
use tauri_plugin_deep_link::DeepLinkExt;

pub fn setup<R: Runtime>(app: &AppHandle<R>) {
    #[cfg(desktop)]
    {
        let handle = app.clone();
        app.deep_link().on_open_url(move |event| {
            let urls: Vec<String> = event.urls().iter().map(|u| u.to_string()).collect();
            forward(&handle, &urls);
        });
    }
    #[cfg(not(desktop))]
    let _ = app;
}

#[cfg_attr(not(desktop), allow(dead_code))]
pub fn forward<R: Runtime>(app: &AppHandle<R>, urls: &[String]) {
    #[cfg(desktop)]
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
        let _ = window.emit("deep-link://open", urls.to_vec());
    }

    #[cfg(not(desktop))]
    {
        let _ = app.emit("deep-link://open", urls.to_vec());
    }
}
