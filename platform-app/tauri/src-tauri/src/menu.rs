// Native application menu.
//
// Creates a small set of menu items with real accelerators. When selected, each
// item emits a `menu://<id>` event on the main window; the React shell listens
// and maps these to the same actions as the command palette.

use tauri::{
    menu::{AboutMetadataBuilder, Menu, MenuBuilder, MenuItemBuilder, PredefinedMenuItem, SubmenuBuilder},
    AppHandle, Emitter, Manager, Runtime,
};

/// Build the native app menu. Mac gets the standard services/edit/window
/// submenus alongside our gratefulagents-specific actions; other platforms get a
/// lean File/Edit/View/Help layout.
pub fn build<R: Runtime>(app: &AppHandle<R>) -> tauri::Result<Menu<R>> {
    let new_run = MenuItemBuilder::with_id("new-run", "New Run")
        .accelerator("CmdOrCtrl+N")
        .build(app)?;
    let palette = MenuItemBuilder::with_id("command-palette", "Command Palette…")
        .accelerator("CmdOrCtrl+K")
        .build(app)?;
    let settings = MenuItemBuilder::with_id("settings", "Settings…")
        .accelerator("CmdOrCtrl+,")
        .build(app)?;
    let toggle_theme = MenuItemBuilder::with_id("toggle-theme", "Toggle Theme")
        .accelerator("CmdOrCtrl+Shift+L")
        .build(app)?;
    let reload = MenuItemBuilder::with_id("reload", "Reload")
        .accelerator("CmdOrCtrl+R")
        .build(app)?;
    let reload_hard = MenuItemBuilder::with_id("reload-hard", "Hard Reload")
        .accelerator("CmdOrCtrl+Shift+R")
        .build(app)?;
    let diagnostics = MenuItemBuilder::with_id("open-diagnostics", "Diagnostics")
        .build(app)?;

    let file = SubmenuBuilder::new(app, "File")
        .items(&[&new_run, &palette])
        .separator()
        .items(&[&settings])
        .separator()
        .item(&PredefinedMenuItem::close_window(app, None)?)
        .build()?;

    let edit = SubmenuBuilder::new(app, "Edit")
        .items(&[
            &PredefinedMenuItem::undo(app, None)?,
            &PredefinedMenuItem::redo(app, None)?,
        ])
        .separator()
        .items(&[
            &PredefinedMenuItem::cut(app, None)?,
            &PredefinedMenuItem::copy(app, None)?,
            &PredefinedMenuItem::paste(app, None)?,
            &PredefinedMenuItem::select_all(app, None)?,
        ])
        .build()?;

    let view = SubmenuBuilder::new(app, "View")
        .items(&[&toggle_theme])
        .separator()
        .items(&[&reload, &reload_hard])
        .separator()
        .item(&PredefinedMenuItem::fullscreen(app, None)?)
        .build()?;

    let window = SubmenuBuilder::new(app, "Window")
        .items(&[
            &PredefinedMenuItem::minimize(app, None)?,
            &PredefinedMenuItem::maximize(app, None)?,
        ])
        .build()?;

    let help = SubmenuBuilder::new(app, "Help").items(&[&diagnostics]).build()?;

    #[cfg(target_os = "macos")]
    let mut builder: MenuBuilder<R, AppHandle<R>> = MenuBuilder::new(app);
    #[cfg(not(target_os = "macos"))]
    let builder: MenuBuilder<R, AppHandle<R>> = MenuBuilder::new(app);

    #[cfg(target_os = "macos")]
    {
        let about = PredefinedMenuItem::about(
            app,
            Some("About gratefulagents"),
            Some(AboutMetadataBuilder::new().name(Some("gratefulagents")).build()),
        )?;
        let app_submenu = SubmenuBuilder::new(app, "gratefulagents")
            .item(&about)
            .separator()
            .item(&settings)
            .separator()
            .items(&[
                &PredefinedMenuItem::hide(app, None)?,
                &PredefinedMenuItem::hide_others(app, None)?,
                &PredefinedMenuItem::show_all(app, None)?,
            ])
            .separator()
            .item(&PredefinedMenuItem::quit(app, None)?)
            .build()?;
        builder = builder.item(&app_submenu);
    }

    // Silence unused-import warning on non-mac builds.
    #[cfg(not(target_os = "macos"))]
    let _ = AboutMetadataBuilder::new();

    let menu = builder
        .items(&[&file, &edit, &view, &window, &help])
        .build()?;
    Ok(menu)
}

/// Relay menu selections to the frontend via a single `menu://action` event.
pub fn on_menu_event<R: Runtime>(app: &AppHandle<R>, id: &str) {
    if let Some(window) = app.get_webview_window("main") {
        let _ = window.emit("menu://action", id.to_string());
    }
}
