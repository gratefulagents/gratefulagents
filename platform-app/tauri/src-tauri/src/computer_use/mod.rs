//! Native computer use (protocol v2): selected-display capture and CGEvent input.
//! See `COMPUTER_USE.md`, sections 1 and 3.

// Off macOS these platform-neutral parts are only exercised by unit tests.
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
mod action;
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
mod encode;
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
mod geometry;
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
mod keys;
#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
mod settle;

#[cfg(target_os = "macos")]
mod macos;
#[cfg(target_os = "macos")]
use macos as platform;
#[cfg(not(target_os = "macos"))]
mod unsupported;
#[cfg(not(target_os = "macos"))]
use unsupported as platform;

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Mutex;

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Emitter, Manager, Runtime};

use action::Action;
use geometry::Mapping;

pub const SESSION_EVENT: &str = "computer-use://session";

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Display {
    pub id: u32,
    pub name: String,
    pub width: u32,
    pub height: u32,
    pub scale: f64,
    pub primary: bool,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct NativeSession {
    pub active: bool,
    pub paused: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub display_id: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub run_key: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub frame_width: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub frame_height: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stopped_reason: Option<String>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct NativeStatus {
    pub supported: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub unsupported_reason: Option<String>,
    pub accessibility: bool,
    pub screen_recording: bool,
    pub emergency_stop: bool,
    pub displays: Vec<Display>,
    pub session: NativeSession,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Screenshot {
    pub media_type: &'static str,
    pub data: String,
    pub width: u32,
    pub height: u32,
}

#[derive(Clone, Copy, Debug, Serialize)]
pub struct Cursor {
    pub x: i64,
    pub y: i64,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ExecResult {
    pub screenshot: Screenshot,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cursor: Option<Cursor>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Permission {
    Accessibility,
    ScreenRecording,
}

#[derive(Clone, Copy, Debug, Default)]
pub struct Permissions {
    pub accessibility: bool,
    pub screen_recording: bool,
}

#[derive(Default)]
pub struct ComputerUse {
    session: Mutex<NativeSession>,
    /// Set on stop or pause; input loops check it between steps.
    interrupted: AtomicBool,
    /// Set once the emergency-stop global shortcut is registered.
    pub emergency_stop: AtomicBool,
    execution: Mutex<()>,
}

impl ComputerUse {
    fn session(&self) -> std::sync::MutexGuard<'_, NativeSession> {
        self.session.lock().unwrap_or_else(|e| e.into_inner())
    }
}

fn emit<R: Runtime>(app: &AppHandle<R>, session: &NativeSession) {
    let _ = app.emit(SESSION_EVENT, session.clone());
    #[cfg(desktop)]
    crate::tray::set_computer_use_active(app, session.active && !session.paused);
}

/// Stops the session (UI, emergency stop, tray, window close, app exit) and
/// releases any held input.
pub fn stop<R: Runtime>(app: &AppHandle<R>, reason: &str) {
    let Some(state) = app.try_state::<ComputerUse>() else {
        return;
    };
    state.interrupted.store(true, Ordering::SeqCst);
    platform::release_all();
    let mut session = state.session();
    if !session.active {
        return;
    }
    *session = NativeSession {
        stopped_reason: Some(reason.to_string()),
        ..NativeSession::default()
    };
    let snapshot = session.clone();
    drop(session);
    log::info!("computer use stopped: {reason}");
    emit(app, &snapshot);
}

fn unsupported_reason() -> Option<String> {
    platform::unsupported_reason()
}

fn require_ready(state: &ComputerUse) -> Result<(), String> {
    if let Some(reason) = unsupported_reason() {
        return Err(reason);
    }
    let permissions = platform::permissions();
    if !permissions.accessibility {
        return Err("Accessibility permission is required for computer use".into());
    }
    if !permissions.screen_recording {
        return Err("Screen Recording permission is required for computer use".into());
    }
    if !state.emergency_stop.load(Ordering::SeqCst) {
        return Err(
            "The emergency stop shortcut (Ctrl+Option+Cmd+Escape) could not be registered".into(),
        );
    }
    Ok(())
}

#[tauri::command]
pub async fn computer_use_status(app: AppHandle) -> Result<NativeStatus, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let state = app.state::<ComputerUse>();
        let unsupported_reason = unsupported_reason();
        let permissions = platform::permissions();
        let displays = if unsupported_reason.is_none() {
            platform::displays(&app)?
        } else {
            Vec::new()
        };
        let session = state.session().clone();
        Ok(NativeStatus {
            supported: unsupported_reason.is_none(),
            unsupported_reason,
            accessibility: permissions.accessibility,
            screen_recording: permissions.screen_recording,
            emergency_stop: state.emergency_stop.load(Ordering::SeqCst),
            displays,
            session,
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

#[tauri::command]
pub fn computer_use_request_permission(
    app: AppHandle,
    permission: Permission,
) -> Result<(), String> {
    platform::request_permission(&app, permission)
}

/// Re-signed builds may need a fresh process for permission grants to apply.
#[tauri::command]
pub fn computer_use_relaunch(app: AppHandle) {
    stop(&app, "Desktop app relaunching");
    app.restart()
}

#[tauri::command]
pub fn computer_use_start(
    app: AppHandle,
    display_id: u32,
    run_key: String,
) -> Result<NativeSession, String> {
    let state = app.state::<ComputerUse>();
    if run_key.trim().is_empty() {
        return Err("A run is required to start computer use".into());
    }
    require_ready(&state)?;
    let display =
        platform::display_bounds(display_id).ok_or("The selected display is not connected")?;
    let mapping = Mapping::new(display);
    let mut session = state.session();
    *session = NativeSession {
        active: true,
        paused: false,
        display_id: Some(display_id),
        run_key: Some(run_key),
        frame_width: Some(mapping.frame_width),
        frame_height: Some(mapping.frame_height),
        stopped_reason: None,
    };
    state.interrupted.store(false, Ordering::SeqCst);
    let snapshot = session.clone();
    drop(session);
    emit(&app, &snapshot);
    Ok(snapshot)
}

#[tauri::command]
pub fn computer_use_stop(app: AppHandle, reason: Option<String>) {
    stop(&app, reason.as_deref().unwrap_or("Stopped by user"));
}

#[tauri::command]
pub fn computer_use_set_paused(app: AppHandle, paused: bool) -> Result<NativeSession, String> {
    let state = app.state::<ComputerUse>();
    if paused {
        state.interrupted.store(true, Ordering::SeqCst);
        platform::release_all();
    }
    let mut session = state.session();
    if !session.active {
        return Err("No computer-use session is active".into());
    }
    if session.paused != paused {
        session.paused = paused;
        if !paused {
            state.interrupted.store(false, Ordering::SeqCst);
        }
        let snapshot = session.clone();
        drop(session);
        emit(&app, &snapshot);
        return Ok(snapshot);
    }
    Ok(session.clone())
}

#[tauri::command]
pub async fn computer_use_execute(
    app: AppHandle,
    action: serde_json::Value,
) -> Result<ExecResult, String> {
    let action = Action::parse(action)?;
    tauri::async_runtime::spawn_blocking(move || execute(&app, &action))
        .await
        .map_err(|e| e.to_string())?
}

fn execute<R: Runtime>(app: &AppHandle<R>, action: &Action) -> Result<ExecResult, String> {
    let state = app.state::<ComputerUse>();
    let _execution = state.execution.lock().unwrap_or_else(|e| e.into_inner());
    let (display_id, frame) = {
        let session = state.session();
        if !session.active {
            return Err("Computer use is stopped".into());
        }
        if session.paused && action.is_input() {
            return Err("Computer use is paused".into());
        }
        (
            session.display_id.ok_or("No display is selected")?,
            (session.frame_width, session.frame_height),
        )
    };
    require_ready(&state)?;
    let display = platform::display_bounds(display_id)
        .ok_or("The selected display is no longer connected")?;
    let mapping = Mapping::new(display);
    if frame != (Some(mapping.frame_width), Some(mapping.frame_height)) {
        let mut session = state.session();
        if session.active && session.display_id == Some(display_id) {
            session.frame_width = Some(mapping.frame_width);
            session.frame_height = Some(mapping.frame_height);
            let snapshot = session.clone();
            drop(session);
            emit(app, &snapshot);
        }
        if !action.points().is_empty() || matches!(action, Action::Zoom { .. }) {
            return Err("The display resolution changed; take a new screenshot first".into());
        }
    }
    action.check_bounds(&mapping)?;
    if action.is_input() && state.interrupted.load(Ordering::SeqCst) {
        return Err("Computer use was stopped".into());
    }
    platform::execute(app, display_id, &mapping, action, &state.interrupted)
}

#[cfg_attr(not(target_os = "macos"), allow(dead_code))]
fn screenshot(frame: &encode::Frame) -> Result<Screenshot, String> {
    use base64::Engine;
    let jpeg = encode::jpeg(frame)?;
    Ok(Screenshot {
        media_type: "image/jpeg",
        data: base64::engine::general_purpose::STANDARD.encode(jpeg),
        width: frame.width,
        height: frame.height,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn computer_use_permission_names_are_closed() {
        assert_eq!(
            serde_json::from_str::<Permission>("\"accessibility\"").unwrap(),
            Permission::Accessibility
        );
        assert_eq!(
            serde_json::from_str::<Permission>("\"screen_recording\"").unwrap(),
            Permission::ScreenRecording
        );
        assert!(serde_json::from_str::<Permission>("\"camera\"").is_err());
    }

    #[test]
    fn computer_use_wire_shapes_are_camel_case() {
        let session = NativeSession {
            active: true,
            display_id: Some(1),
            run_key: Some("ns/run".into()),
            frame_width: Some(1183),
            frame_height: Some(768),
            ..NativeSession::default()
        };
        assert_eq!(
            serde_json::to_value(&session).unwrap(),
            serde_json::json!({"active": true, "paused": false, "displayId": 1, "runKey": "ns/run",
                "frameWidth": 1183, "frameHeight": 768})
        );
        assert_eq!(
            serde_json::to_value(NativeSession::default()).unwrap(),
            serde_json::json!({"active": false, "paused": false})
        );
        let frame = encode::Frame::new(2, 2, vec![0; 16]).unwrap();
        let result = ExecResult {
            screenshot: screenshot(&frame).unwrap(),
            cursor: Some(Cursor { x: 1, y: 2 }),
        };
        let value = serde_json::to_value(&result).unwrap();
        assert_eq!(value["screenshot"]["mediaType"], "image/jpeg");
        assert_eq!(value["screenshot"]["width"], 2);
        assert!(!value["screenshot"]["data"]
            .as_str()
            .unwrap()
            .starts_with("data:"));
        assert_eq!(value["cursor"], serde_json::json!({"x": 1, "y": 2}));
        let status = NativeStatus {
            supported: false,
            unsupported_reason: Some("no".into()),
            accessibility: false,
            screen_recording: false,
            emergency_stop: false,
            displays: vec![Display {
                id: 1,
                name: "Built-in".into(),
                width: 1512,
                height: 982,
                scale: 2.0,
                primary: true,
            }],
            session: NativeSession::default(),
        };
        let value = serde_json::to_value(&status).unwrap();
        assert_eq!(value["unsupportedReason"], "no");
        assert_eq!(value["screenRecording"], false);
        assert_eq!(value["emergencyStop"], false);
        assert_eq!(value["displays"][0]["primary"], true);
    }

    #[test]
    #[cfg(not(target_os = "macos"))]
    fn computer_use_is_unsupported_off_macos() {
        assert!(unsupported_reason().is_some());
        let permissions = platform::permissions();
        assert!(!permissions.accessibility && !permissions.screen_recording);
        assert!(require_ready(&ComputerUse::default()).is_err());
    }
}
