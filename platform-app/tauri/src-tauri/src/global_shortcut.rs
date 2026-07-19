// Global shortcut registration. Alt+Space summons/hides the main window and
// nudges it to open the command palette.

#![cfg(desktop)]

use tauri::{AppHandle, Emitter, Manager, Runtime};
use tauri_plugin_global_shortcut::{Code, GlobalShortcutExt, Modifiers, Shortcut, ShortcutState};

pub fn setup<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<()> {
    let summon = Shortcut::new(Some(Modifiers::ALT), Code::Space);
    let gs = app.global_shortcut();

    if let Err(err) = gs.on_shortcut(summon, move |app, _shortcut, event| {
        if event.state != ShortcutState::Pressed {
            return;
        }
        toggle(app);
    }) {
        log::warn!("failed to register global shortcut Alt+Space: {err}");
    }

    Ok(())
}

fn toggle<R: Runtime>(app: &AppHandle<R>) {
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    match window.is_focused() {
        Ok(true) => {
            let _ = window.hide();
        }
        _ => {
            let _ = window.show();
            let _ = window.unminimize();
            let _ = window.set_focus();
            let _ = window.emit("menu://action", "command-palette".to_string());
        }
    }
}
