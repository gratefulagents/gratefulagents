//! Accessibility and Screen Recording (TCC) permissions.

use std::ffi::c_void;

use objc2::rc::Retained;
use objc2_foundation::{NSDictionary, NSNumber, NSString};
use tauri::{AppHandle, Runtime};
use tauri_plugin_opener::OpenerExt;

use super::super::{Permission, Permissions};

#[link(name = "ApplicationServices", kind = "framework")]
extern "C" {
    static kAXTrustedCheckOptionPrompt: *const c_void;
    fn AXIsProcessTrusted() -> u8;
    fn AXIsProcessTrustedWithOptions(options: *const c_void) -> u8;
}

#[link(name = "CoreGraphics", kind = "framework")]
extern "C" {
    fn CGPreflightScreenCaptureAccess() -> bool;
    fn CGRequestScreenCaptureAccess() -> bool;
}

const ACCESSIBILITY_PANE: &str =
    "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility";
const SCREEN_RECORDING_PANE: &str =
    "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture";

pub fn permissions() -> Permissions {
    Permissions {
        accessibility: unsafe { AXIsProcessTrusted() } != 0,
        screen_recording: unsafe { CGPreflightScreenCaptureAccess() },
    }
}

/// Registers this exact binary in System Settings → Accessibility (unchecked)
/// and shows the system prompt.
fn prompt_accessibility() -> bool {
    // SAFETY: kAXTrustedCheckOptionPrompt is a CFStringRef, toll-free bridged to NSString.
    let key: &NSString = unsafe { &*kAXTrustedCheckOptionPrompt.cast::<NSString>() };
    let value = NSNumber::new_bool(true);
    let options: Retained<NSDictionary<NSString, NSNumber>> =
        NSDictionary::from_slices(&[key], &[&*value]);
    unsafe { AXIsProcessTrustedWithOptions(Retained::as_ptr(&options).cast()) != 0 }
}

pub fn request_permission<R: Runtime>(
    app: &AppHandle<R>,
    permission: Permission,
) -> Result<(), String> {
    let pane = match permission {
        Permission::Accessibility => {
            prompt_accessibility();
            ACCESSIBILITY_PANE
        }
        Permission::ScreenRecording => {
            unsafe { CGRequestScreenCaptureAccess() };
            SCREEN_RECORDING_PANE
        }
    };
    app.opener()
        .open_url(pane, None::<&str>)
        .map_err(|e| format!("Could not open System Settings: {e}"))
}
