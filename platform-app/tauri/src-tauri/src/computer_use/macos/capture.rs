//! ScreenCaptureKit screenshots of one display, excluding our own windows.

use std::ptr::NonNull;
use std::sync::mpsc;
use std::time::Duration;

use block2::RcBlock;
use objc2::rc::Retained;
use objc2::AllocAnyThread;
use objc2_core_foundation::CFRetained;
use objc2_core_graphics::{CGDataProvider, CGImage, CGImageAlphaInfo, CGImageByteOrderInfo};
use objc2_foundation::{NSArray, NSError};
use objc2_screen_capture_kit::{
    SCContentFilter, SCScreenshotManager, SCShareableContent, SCStreamConfiguration,
};

use super::super::encode::Frame;

const TIMEOUT: Duration = Duration::from_secs(5);

/// Moves an Objective-C object across the completion-handler thread boundary.
struct Handoff<T>(T);
// SAFETY: SCShareableContent is immutable once delivered and is only used by the receiver.
unsafe impl<T> Send for Handoff<T> {}

fn error_text(error: *mut NSError) -> String {
    unsafe { error.as_ref() }
        .map(|error| error.localizedDescription().to_string())
        .unwrap_or_else(|| "unknown error".into())
}

fn shareable_content() -> Result<Retained<SCShareableContent>, String> {
    let (tx, rx) = mpsc::channel();
    let handler = RcBlock::new(
        move |content: *mut SCShareableContent, error: *mut NSError| {
            let result = match unsafe { Retained::retain(content) } {
                Some(content) => Ok(Handoff(content)),
                None => Err(error_text(error)),
            };
            let _ = tx.send(result);
        },
    );
    unsafe {
        SCShareableContent::getShareableContentExcludingDesktopWindows_onScreenWindowsOnly_completionHandler(
            false, true, &handler,
        )
    };
    match rx.recv_timeout(TIMEOUT) {
        Ok(Ok(content)) => Ok(content.0),
        Ok(Err(error)) => Err(format!("ScreenCaptureKit could not list displays: {error}")),
        Err(_) => Err("ScreenCaptureKit did not list displays in time".into()),
    }
}

pub struct Capturer {
    filter: Retained<SCContentFilter>,
}

impl Capturer {
    pub fn new(display_id: u32) -> Result<Capturer, String> {
        let content = shareable_content()?;
        let display = unsafe { content.displays() }
            .iter()
            .find(|display| unsafe { display.displayID() } == display_id)
            .ok_or("The selected display is not available for capture")?;
        let pid = std::process::id() as i32;
        let ours: Vec<_> = unsafe { content.applications() }
            .iter()
            .filter(|app| unsafe { app.processID() } == pid)
            .collect();
        let filter = unsafe {
            SCContentFilter::initWithDisplay_excludingApplications_exceptingWindows(
                SCContentFilter::alloc(),
                &display,
                &NSArray::from_retained_slice(&ours),
                &NSArray::new(),
            )
        };
        Ok(Capturer { filter })
    }

    /// Captures the display scaled to `width × height` pixels, cursor hidden.
    pub fn capture(&self, width: u32, height: u32) -> Result<Frame, String> {
        let config = unsafe { SCStreamConfiguration::new() };
        unsafe {
            config.setWidth(width as usize);
            config.setHeight(height as usize);
            config.setShowsCursor(false);
        }
        let (tx, rx) = mpsc::channel();
        let handler = RcBlock::new(move |image: *mut CGImage, error: *mut NSError| {
            let result = match NonNull::new(image) {
                Some(image) => Ok(unsafe { CFRetained::retain(image) }),
                None => Err(error_text(error)),
            };
            let _ = tx.send(result);
        });
        unsafe {
            SCScreenshotManager::captureImageWithFilter_configuration_completionHandler(
                &self.filter,
                &config,
                Some(&handler),
            )
        };
        let image = match rx.recv_timeout(TIMEOUT) {
            Ok(Ok(image)) => image,
            Ok(Err(error)) => return Err(format!("Screen capture failed: {error}")),
            Err(_) => return Err("Screen capture timed out".into()),
        };
        frame_from_image(&image)
    }
}

fn frame_from_image(image: &CGImage) -> Result<Frame, String> {
    let image = Some(image);
    let width = CGImage::width(image);
    let height = CGImage::height(image);
    let stride = CGImage::bytes_per_row(image);
    if CGImage::bits_per_pixel(image) != 32 || stride < width * 4 {
        return Err("Unsupported screenshot pixel format".into());
    }
    let order = CGImage::byte_order_info(image);
    let alpha = CGImage::alpha_info(image);
    let alpha_first = matches!(
        alpha,
        CGImageAlphaInfo::First
            | CGImageAlphaInfo::PremultipliedFirst
            | CGImageAlphaInfo::NoneSkipFirst
    );
    let alpha_last = matches!(
        alpha,
        CGImageAlphaInfo::Last
            | CGImageAlphaInfo::PremultipliedLast
            | CGImageAlphaInfo::NoneSkipLast
    );
    let swap_red_blue = if order == CGImageByteOrderInfo::Order32Little && alpha_first {
        false
    } else if (order == CGImageByteOrderInfo::Order32Big
        || order == CGImageByteOrderInfo::OrderDefault)
        && alpha_last
    {
        true
    } else {
        return Err(format!(
            "Unsupported screenshot pixel layout (byte order {:#x}, alpha {})",
            order.0, alpha.0
        ));
    };
    let data = CGDataProvider::data(CGImage::data_provider(image).as_deref())
        .ok_or("Screenshot pixels are unavailable")?
        .to_vec();
    if data.len() < stride * (height - 1) + width * 4 {
        return Err("Screenshot pixel buffer is truncated".into());
    }
    let mut bgra = Vec::with_capacity(width * height * 4);
    for y in 0..height {
        let row = &data[y * stride..y * stride + width * 4];
        if swap_red_blue {
            bgra.extend(
                row.chunks_exact(4)
                    .flat_map(|px| [px[2], px[1], px[0], px[3]]),
            );
        } else {
            bgra.extend_from_slice(row);
        }
    }
    Frame::new(width as u32, height as u32, bgra)
}
