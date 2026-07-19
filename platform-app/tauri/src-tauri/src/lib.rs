// gratefulagents — Tauri 2 shell entry point.
//
// The frontend (React) renders inside WKWebView (macOS/iOS). This Rust side:
//   - wires Tauri plugins (http, store, os, shell, window-state on desktop,
//     notification, clipboard-manager, log, opener, deep-link,
//     global-shortcut + single-instance on desktop)
//   - installs the native application menu, tray icon, and drag-drop bridge
//   - applies macOS vibrancy + inset traffic lights on window-create
//   - exposes a tiny `platform_info` command
//
// Keep this file thin. Feature wiring lives in sibling modules.

#[cfg(desktop)]
use tauri::Manager;

#[cfg(target_os = "macos")]
mod macos;

mod deep_link;
mod anthropic_oauth;
mod copilot_oauth;
mod diagnostics;
mod drag_drop;
#[cfg(desktop)]
mod global_shortcut;
mod local_creds;
#[cfg(desktop)]
mod menu;
mod oauth_common;
mod openai_oauth;
#[cfg(desktop)]
mod tray;
mod updater;

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    // WebKitGTK's DMA-BUF renderer is broken on many Linux setups (Wayland,
    // NVIDIA, VMs): the webview renders but wheel/trackpad scrolling is dead
    // or rubber-bands (https://github.com/tauri-apps/tauri/issues/14427).
    // Disabling it here — before the webview is created — is the reliable
    // fix; setting the variable in the shell is inconsistent. Users can
    // still override by exporting the variable themselves.
    #[cfg(target_os = "linux")]
    if std::env::var_os("WEBKIT_DISABLE_DMABUF_RENDERER").is_none() {
        // Called at startup before any other threads are spawned.
        std::env::set_var("WEBKIT_DISABLE_DMABUF_RENDERER", "1");
    }

    let mut builder = tauri::Builder::default();

    // Single-instance MUST be registered first on desktop so subsequent
    // launches relay their argv (including `gratefulagents://…` deep links) to the
    // running process instead of spawning a second window.
    #[cfg(desktop)]
    {
        builder = builder.plugin(tauri_plugin_single_instance::init(|app, argv, _cwd| {
            // Surface any deep links carried in argv, then focus the main
            // window.
            let urls: Vec<String> = argv
                .iter()
                .filter(|a| a.starts_with("gratefulagents://"))
                .cloned()
                .collect();
            if !urls.is_empty() {
                deep_link::forward(app, &urls);
            } else if let Some(window) = app.get_webview_window("main") {
                let _ = window.show();
                let _ = window.unminimize();
                let _ = window.set_focus();
            }
        }));
    }

    builder = builder
        .manage(anthropic_oauth::AnthropicOAuthState::default())
        .manage(openai_oauth::OpenAIOAuthState::default())
        .plugin(tauri_plugin_http::init())
        .plugin(tauri_plugin_os::init())
        .plugin(tauri_plugin_shell::init())
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_store::Builder::new().build())
        .plugin(tauri_plugin_clipboard_manager::init())
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_deep_link::init())
        .plugin(tauri_plugin_google_oauth::init())
        .plugin(tauri_plugin_mobile_navigation::init())
        .plugin(
            tauri_plugin_log::Builder::default()
                .level(log::LevelFilter::Info)
                .build(),
        );

    #[cfg(desktop)]
    {
        builder = builder
            .plugin(tauri_plugin_window_state::Builder::new().build())
            .plugin(tauri_plugin_global_shortcut::Builder::new().build())
            // Auto-update from public signed release assets (see updater.rs).
            .plugin(tauri_plugin_updater::Builder::new().build())
            .manage(updater::PendingUpdate::default());
    }

    builder
        .on_window_event(|window, event| {
            drag_drop::on_window_event(window, event);
        })
        .setup(|app| {
            #[cfg(target_os = "macos")]
            {
                app.set_activation_policy(tauri::ActivationPolicy::Regular);
                app.set_dock_visibility(true);
            }

            let handle = app.handle().clone();

            // Native app menu + listener.
            #[cfg(desktop)]
            {
                match menu::build(&handle) {
                    Ok(menu) => {
                        let _ = app.set_menu(menu);
                        app.on_menu_event(|app, event| {
                            menu::on_menu_event(app, event.id().as_ref());
                        });
                    }
                    Err(err) => {
                        log::warn!("failed to build app menu: {err}");
                    }
                }
            }

            // System tray.
            #[cfg(desktop)]
            if let Err(err) = tray::setup(&handle) {
                log::warn!("failed to setup tray: {err}");
            }

            // Global shortcut (Alt+Space summon).
            #[cfg(desktop)]
            if let Err(err) = global_shortcut::setup(&handle) {
                log::warn!("failed to setup global shortcut: {err}");
            }

            // Deep-link handler.
            deep_link::setup(&handle);

            #[cfg(desktop)]
            if let Some(window) = app.get_webview_window("main") {
                // The static `backgroundColor` in tauri.conf.json is the
                // dark-theme color; repaint from the OS theme before first
                // paint so launching in light mode doesn't flash dark.
                if matches!(window.theme(), Ok(tauri::Theme::Light)) {
                    // --background light: oklch(0.985 0.003 265) ≈ #fafafc.
                    let _ = window.set_background_color(Some(tauri::window::Color(
                        0xfa, 0xfa, 0xfc, 0xff,
                    )));
                }

                let _ = window.show();
                let _ = window.unminimize();
                let _ = window.set_focus();

                let webview = window.as_ref();
                let _ = webview.show();
                let _ = webview.set_focus();

                #[cfg(not(target_os = "macos"))]
                let _ = window.set_skip_taskbar(false);

                #[cfg(target_os = "macos")]
                macos::configure_window(&window);

                #[cfg(not(target_os = "macos"))]
                let _ = &window;
            }
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            platform_info,
            diagnostics::open_log_directory,
            local_creds::detect_local_credentials,
            copilot_oauth::start_copilot_oauth,
            copilot_oauth::poll_copilot_oauth,
            anthropic_oauth::start_anthropic_oauth,
            anthropic_oauth::complete_anthropic_oauth,
            openai_oauth::start_openai_oauth,
            openai_oauth::start_openai_device_oauth,
            openai_oauth::poll_openai_oauth,
            openai_oauth::cancel_openai_oauth,
            updater::updater_check,
            updater::updater_install,
            updater::updater_restart,
        ])
        .build(tauri::generate_context!())
        .expect("error while running gratefulagents")
        .run(|_app, _event| {
            // Re-activation (dock icon click, notification click, …) on macOS
            // arrives as a Reopen event — surface the main window.
            #[cfg(target_os = "macos")]
            if let tauri::RunEvent::Reopen { .. } = _event {
                if let Some(window) = _app.get_webview_window("main") {
                    let _ = window.show();
                    let _ = window.unminimize();
                    let _ = window.set_focus();
                }
            }
        });
}

#[derive(serde::Serialize)]
struct PlatformInfo {
    os: String,
    is_macos: bool,
    is_ios: bool,
    is_linux: bool,
}

#[tauri::command]
fn platform_info() -> PlatformInfo {
    PlatformInfo {
        os: std::env::consts::OS.to_string(),
        is_macos: cfg!(target_os = "macos"),
        is_ios: cfg!(target_os = "ios"),
        is_linux: cfg!(target_os = "linux"),
    }
}
