//! macOS backend: ScreenCaptureKit capture, CGEvent input, AppKit integration.

mod app;
mod capture;
mod input;
mod permissions;

use std::sync::atomic::AtomicBool;
use std::time::Duration;

use objc2::runtime::AnyClass;
use objc2_core_graphics::{
    CGDisplayBounds, CGDisplayCopyDisplayMode, CGDisplayMode, CGGetActiveDisplayList,
    CGMainDisplayID,
};
use tauri::{AppHandle, Runtime};
use tauri_plugin_opener::OpenerExt;

use super::action::Action;
use super::geometry::{Mapping, Rect};
use super::{encode, settle, Cursor, Display, ExecResult};

pub use input::release_all;
pub use permissions::{permissions, request_permission};

/// Time for the hide animation and for the previous app to take focus.
const HIDE_SETTLE: Duration = Duration::from_millis(300);

pub fn unsupported_reason() -> Option<String> {
    // ScreenCaptureKit is weak-linked; SCScreenshotManager arrived in macOS 14.
    if AnyClass::get(c"SCScreenshotManager").is_none() {
        return Some("Computer use requires macOS 14 or later".into());
    }
    None
}

fn active_display_ids() -> Vec<u32> {
    let mut ids = [0u32; 32];
    let mut count = 0u32;
    let error = unsafe { CGGetActiveDisplayList(ids.len() as u32, ids.as_mut_ptr(), &mut count) };
    if error.0 != 0 {
        return Vec::new();
    }
    ids[..count as usize].to_vec()
}

/// Backing pixels of the display's current mode (falls back to points).
fn native_size(display_id: u32, points: &Rect) -> (u32, u32) {
    let mode = CGDisplayCopyDisplayMode(display_id);
    let (w, h) = (
        CGDisplayMode::pixel_width(mode.as_deref()),
        CGDisplayMode::pixel_height(mode.as_deref()),
    );
    if w == 0 || h == 0 {
        (points.width.round() as u32, points.height.round() as u32)
    } else {
        (w as u32, h as u32)
    }
}

pub fn display_bounds(display_id: u32) -> Option<Rect> {
    if !active_display_ids().contains(&display_id) {
        return None;
    }
    let bounds = CGDisplayBounds(display_id);
    Some(Rect {
        x: bounds.origin.x,
        y: bounds.origin.y,
        width: bounds.size.width,
        height: bounds.size.height,
    })
}

pub fn displays<R: Runtime>(app: &AppHandle<R>) -> Result<Vec<Display>, String> {
    let names = app::screen_names(app)?;
    let primary = CGMainDisplayID();
    Ok(active_display_ids()
        .into_iter()
        .filter_map(|id| {
            let bounds = display_bounds(id)?;
            let (pixel_width, _) = native_size(id, &bounds);
            Some(Display {
                id,
                name: names
                    .get(&id)
                    .cloned()
                    .unwrap_or_else(|| format!("Display {id}")),
                width: bounds.width.round() as u32,
                height: bounds.height.round() as u32,
                scale: f64::from(pixel_width) / bounds.width.max(1.0),
                primary: id == primary,
            })
        })
        .collect())
}

fn settle_minimum(action: &Action) -> Duration {
    Duration::from_millis(match action {
        Action::Click { count: 1, .. } => 150,
        Action::Click { .. } => 250,
        Action::MouseMove { .. } | Action::MouseDown { .. } | Action::MouseUp { .. } => 100,
        Action::Drag { .. } | Action::Scroll { .. } => 250,
        Action::Type { .. } => 150,
        Action::Key { .. } => 200,
        Action::OpenUrl { .. } => 1000,
        Action::Screenshot | Action::CursorPosition | Action::Zoom { .. } => 0,
    })
}

pub fn execute<R: Runtime>(
    app: &AppHandle<R>,
    display_id: u32,
    mapping: &Mapping,
    action: &Action,
    interrupted: &AtomicBool,
) -> Result<ExecResult, String> {
    log::debug!("computer use: {}", action.name());
    let keyboard = matches!(action, Action::Type { .. } | Action::Key { .. });
    if matches!(action, Action::Type { .. }) && app::secure_input_enabled() {
        return Err(
            "Secure Event Input is active (a password field may be focused); typing is refused"
                .into(),
        );
    }
    if action.is_input() && !matches!(action, Action::OpenUrl { .. }) {
        let mut targets: Vec<(f64, f64)> = action
            .points()
            .into_iter()
            .map(|p| mapping.to_points(p))
            .collect();
        if !keyboard && targets.is_empty() {
            targets.push(input::cursor_location());
        }
        if app::hide_if_in_the_way(app, targets, keyboard)? {
            std::thread::sleep(HIDE_SETTLE);
        }
    }

    match action {
        Action::OpenUrl { url } => app
            .opener()
            .open_url(url, None::<&str>)
            .map_err(|e| format!("Could not open the URL: {e}"))?,
        _ => input::perform(action, mapping, interrupted)?,
    }

    let capturer = capture::Capturer::new(display_id)?;
    let (fw, fh) = mapping.frame();
    let frame = match action {
        Action::Zoom { region } => {
            let (out_w, out_h) = region.zoom_size((fw, fh));
            // Capture just enough resolution for the magnified crop, up to native pixels.
            let k = f64::from(out_w) / (region.x1 - region.x0) as f64;
            let (native_w, native_h) = native_size(display_id, &mapping.display);
            let full = capturer.capture(
                ((f64::from(fw) * k).ceil() as u32).min(native_w).max(fw),
                ((f64::from(fh) * k).ceil() as u32).min(native_h).max(fh),
            )?;
            let crop = region.scaled((fw, fh), full.width, full.height);
            encode::crop_resize(&full, crop, out_w, out_h)?
        }
        _ if action.is_input() => settle::settle(settle_minimum(action), settle::TIMEOUT, || {
            capturer.capture(fw, fh)
        })?,
        _ => capturer.capture(fw, fh)?,
    };

    let (x, y) = input::cursor_location();
    Ok(ExecResult {
        screenshot: super::screenshot(&frame)?,
        cursor: mapping.to_frame(x, y).map(|p| Cursor { x: p.x, y: p.y }),
    })
}

#[cfg(test)]
mod tests {
    use super::super::geometry::{frame_size, Point};
    use super::*;

    /// Real-Mac smoke test: `cargo test --lib -- --ignored computer_use_smoke --nocapture`.
    #[test]
    #[ignore = "needs a real Mac session with Accessibility and Screen Recording granted"]
    fn computer_use_smoke() {
        if let Some(reason) = unsupported_reason() {
            println!("skipping computer_use_smoke: {reason}");
            return;
        }
        let granted = permissions();
        if !granted.accessibility || !granted.screen_recording {
            println!(
                "skipping computer_use_smoke: permissions missing (accessibility={}, screen_recording={})",
                granted.accessibility, granted.screen_recording
            );
            return;
        }

        let display_id = CGMainDisplayID();
        let bounds = display_bounds(display_id).expect("primary display bounds");
        let mapping = Mapping::new(bounds);
        let (fw, fh) = frame_size(bounds.width, bounds.height);
        assert_eq!(mapping.frame(), (fw, fh));
        assert!(fw <= 1456 && fh <= 1456 && fw.min(fh) <= 768);

        let frame = capture::Capturer::new(display_id)
            .and_then(|c| c.capture(fw, fh))
            .expect("capture primary display");
        assert_eq!((frame.width, frame.height), (fw, fh));
        let first = &frame.bgra[..4];
        assert!(
            frame.bgra.chunks_exact(4).any(|px| px[..3] != first[..3]),
            "screenshot is a single flat colour"
        );
        let jpeg = encode::jpeg(&frame).expect("encode JPEG");
        assert_eq!(&jpeg[..2], &[0xFF, 0xD8]);
        let decoded = image::load_from_memory_with_format(&jpeg, image::ImageFormat::Jpeg)
            .expect("decode JPEG")
            .to_luma8();
        assert_eq!(decoded.dimensions(), (fw, fh));
        let (min, max) = decoded
            .pixels()
            .fold((255u8, 0u8), |(lo, hi), p| (lo.min(p[0]), hi.max(p[0])));
        assert!(max - min > 16, "JPEG looks blank (luma {min}..{max})");
        println!("captured {fw}x{fh} frame, {} byte JPEG", jpeg.len());

        let original = input::cursor_location();
        let target = Point {
            x: i64::from(fw / 3),
            y: i64::from(fh / 3),
        };
        let interrupted = AtomicBool::new(false);
        input::perform(&Action::MouseMove { at: target }, &mapping, &interrupted)
            .expect("mouse move");
        std::thread::sleep(Duration::from_millis(150));
        let (x, y) = input::cursor_location();
        let (ex, ey) = mapping.to_points(target);
        assert!(
            (x - ex).abs() <= 1.0 && (y - ey).abs() <= 1.0,
            "cursor at ({x}, {y}), expected ({ex}, {ey})"
        );
        // The window server may round the cursor to whole points.
        let mapped = mapping
            .to_frame(x, y)
            .expect("cursor on the primary display");
        assert!(
            (mapped.x - target.x).abs() <= 1 && (mapped.y - target.y).abs() <= 1,
            "cursor maps to {mapped:?}, expected {target:?}"
        );
        println!("cursor mapped to frame ({}, {})", target.x, target.y);

        let _ = objc2_core_graphics::CGWarpMouseCursorPosition(objc2_core_foundation::CGPoint {
            x: original.0,
            y: original.1,
        });
    }
}
