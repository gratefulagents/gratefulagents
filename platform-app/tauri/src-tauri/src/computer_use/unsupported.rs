//! Stubs for platforms without native computer use.

use std::sync::atomic::AtomicBool;

use tauri::{AppHandle, Runtime};

use super::action::Action;
use super::geometry::{Mapping, Rect};
use super::{Display, ExecResult, Permission, Permissions};

const UNSUPPORTED: &str = "Computer use requires the macOS desktop app";

pub fn unsupported_reason() -> Option<String> {
    Some(UNSUPPORTED.into())
}

pub fn permissions() -> Permissions {
    Permissions::default()
}

pub fn request_permission<R: Runtime>(
    _app: &AppHandle<R>,
    _permission: Permission,
) -> Result<(), String> {
    Err(UNSUPPORTED.into())
}

pub fn displays<R: Runtime>(_app: &AppHandle<R>) -> Result<Vec<Display>, String> {
    Ok(Vec::new())
}

pub fn display_bounds(_display_id: u32) -> Option<Rect> {
    None
}

pub fn release_all() {}

pub fn execute<R: Runtime>(
    _app: &AppHandle<R>,
    _display_id: u32,
    _mapping: &Mapping,
    _action: &Action,
    _interrupted: &AtomicBool,
) -> Result<ExecResult, String> {
    Err(UNSUPPORTED.into())
}
