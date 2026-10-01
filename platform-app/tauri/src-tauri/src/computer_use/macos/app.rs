//! AppKit: our own windows, hiding the app, screen names, Secure Event Input.
//! AppKit is main-thread only; everything here hops to the main thread.

use std::collections::HashMap;
use std::sync::mpsc;
use std::time::Duration;

use objc2::MainThreadMarker;
use objc2_app_kit::{NSApplication, NSScreen};
use objc2_core_graphics::{CGDisplayBounds, CGMainDisplayID};
use objc2_foundation::{ns_string, NSNumber};
use tauri::{AppHandle, Runtime};

#[link(name = "Carbon", kind = "framework")]
extern "C" {
    fn IsSecureEventInputEnabled() -> u8;
}

const MAIN_THREAD_TIMEOUT: Duration = Duration::from_secs(5);

pub fn on_main<R, T, F>(app: &AppHandle<R>, f: F) -> Result<T, String>
where
    R: Runtime,
    T: Send + 'static,
    F: FnOnce(MainThreadMarker) -> T + Send + 'static,
{
    if let Some(mtm) = MainThreadMarker::new() {
        return Ok(f(mtm));
    }
    let (tx, rx) = mpsc::channel();
    app.run_on_main_thread(move || {
        let mtm = MainThreadMarker::new().expect("run_on_main_thread runs on the main thread");
        let _ = tx.send(f(mtm));
    })
    .map_err(|e| format!("Main thread unavailable: {e}"))?;
    rx.recv_timeout(MAIN_THREAD_TIMEOUT)
        .map_err(|_| "The main thread did not respond".to_string())
}

pub fn secure_input_enabled() -> bool {
    unsafe { IsSecureEventInputEnabled() != 0 }
}

/// `NSScreen.localizedName` keyed by `CGDirectDisplayID`.
pub fn screen_names<R: Runtime>(app: &AppHandle<R>) -> Result<HashMap<u32, String>, String> {
    on_main(app, |mtm| {
        let mut names = HashMap::new();
        for screen in NSScreen::screens(mtm).iter() {
            let number = screen
                .deviceDescription()
                .objectForKey(ns_string!("NSScreenNumber"))
                .and_then(|value| value.downcast::<NSNumber>().ok());
            if let Some(number) = number {
                names.insert(
                    number.unsignedIntValue(),
                    screen.localizedName().to_string(),
                );
            }
        }
        names
    })
}

/// Hides our app when the pending input would land on it: any target point
/// inside one of our visible windows, or keyboard input while we are frontmost.
/// Hiding hands focus back to the previously active app. Returns whether it hid.
pub fn hide_if_in_the_way<R: Runtime>(
    app: &AppHandle<R>,
    targets: Vec<(f64, f64)>,
    keyboard: bool,
) -> Result<bool, String> {
    on_main(app, move |mtm| {
        let ns_app = NSApplication::sharedApplication(mtm);
        let in_the_way = if keyboard {
            ns_app.isActive()
        } else {
            // Cocoa frames are bottom-left based on the primary display; CG is top-left.
            let primary_height = CGDisplayBounds(CGMainDisplayID()).size.height;
            ns_app.windows().iter().any(|window| {
                if !window.isVisible() {
                    return false;
                }
                let frame = window.frame();
                let top = primary_height - (frame.origin.y + frame.size.height);
                targets.iter().any(|(x, y)| {
                    *x >= frame.origin.x
                        && *x < frame.origin.x + frame.size.width
                        && *y >= top
                        && *y < top + frame.size.height
                })
            })
        };
        if in_the_way {
            ns_app.hide(None);
        }
        in_the_way
    })
}
