// System tray icon + menu.
//
// Click: show/focus the main window. Menu items: show, new run, settings,
// quit. Each emits `tray://<id>` on the main window (except quit, which just
// exits).

use tauri::{
    menu::{Menu, MenuBuilder, MenuItemBuilder, PredefinedMenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    AppHandle, Emitter, Manager, Runtime,
};

pub fn setup<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<()> {
    let menu = build_menu(app)?;

    let _ = TrayIconBuilder::with_id("gratefulagents-tray")
        .tooltip("gratefulagents")
        .icon(app.default_window_icon().cloned().unwrap_or_else(|| {
            // Fallback: 1×1 transparent PNG-equivalent image.
            tauri::image::Image::new_owned(vec![0; 4], 1, 1)
        }))
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| match event.id().as_ref() {
            "tray-show" => show_main(app),
            "tray-new-run" => relay(app, "new-run"),
            "tray-settings" => relay(app, "settings"),
            "tray-quit" => app.exit(0),
            _ => {}
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                show_main(tray.app_handle());
            }
        })
        .build(app)?;

    Ok(())
}

fn build_menu<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Menu<R>> {
    let show = MenuItemBuilder::with_id("tray-show", "Show gratefulagents").build(app)?;
    let new_run = MenuItemBuilder::with_id("tray-new-run", "New Run").build(app)?;
    let settings = MenuItemBuilder::with_id("tray-settings", "Settings…").build(app)?;
    let quit = MenuItemBuilder::with_id("tray-quit", "Quit gratefulagents").build(app)?;
    // A menu item may only have one parent — build one separator per slot.
    let sep1 = PredefinedMenuItem::separator(app)?;
    let sep2 = PredefinedMenuItem::separator(app)?;

    MenuBuilder::new(app)
        .items(&[&show, &sep1, &new_run, &settings, &sep2, &quit])
        .build()
}

fn show_main<R: Runtime>(app: &AppHandle<R>) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.show();
        let _ = window.unminimize();
        let _ = window.set_focus();
    }
}

fn relay<R: Runtime>(app: &AppHandle<R>, action: &str) {
    show_main(app);
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.emit("menu://action", action.to_string());
    }
}
