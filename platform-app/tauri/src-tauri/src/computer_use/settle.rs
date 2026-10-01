//! Waiting for the screen to settle after input.

use std::time::{Duration, Instant};

use super::encode::Frame;

pub const THUMB_WIDTH: u32 = 64;
pub const THUMB_HEIGHT: u32 = 40;
pub const TIMEOUT: Duration = Duration::from_secs(2);
const POLL_INTERVAL: Duration = Duration::from_millis(60);
/// Luminance change (0–255) below which a thumbnail pixel counts as unchanged.
const PIXEL_TOLERANCE: u8 = 8;
const SETTLED_FRACTION: f64 = 0.01;

/// Box-averaged 64×40 luminance thumbnail.
pub fn thumbnail(frame: &Frame) -> Vec<u8> {
    let mut sums = vec![0u64; (THUMB_WIDTH * THUMB_HEIGHT) as usize];
    let mut counts = vec![0u64; sums.len()];
    let width = frame.width as usize;
    for (y, row) in frame.bgra.chunks_exact(width * 4).enumerate() {
        let ty = y * THUMB_HEIGHT as usize / frame.height as usize;
        for (x, px) in row.chunks_exact(4).enumerate() {
            let tx = x * THUMB_WIDTH as usize / width;
            let luma =
                (u64::from(px[2]) * 299 + u64::from(px[1]) * 587 + u64::from(px[0]) * 114) / 1000;
            let i = ty * THUMB_WIDTH as usize + tx;
            sums[i] += luma;
            counts[i] += 1;
        }
    }
    sums.iter()
        .zip(&counts)
        .map(|(sum, count)| if *count == 0 { 0 } else { (sum / count) as u8 })
        .collect()
}

/// Fraction of thumbnail pixels that changed noticeably.
pub fn difference(a: &[u8], b: &[u8]) -> f64 {
    if a.len() != b.len() || a.is_empty() {
        return 1.0;
    }
    let changed = a
        .iter()
        .zip(b)
        .filter(|(x, y)| x.abs_diff(**y) > PIXEL_TOLERANCE)
        .count();
    changed as f64 / a.len() as f64
}

/// Sleeps `min_wait`, then captures until two consecutive thumbnails differ by
/// less than 1% or `timeout` has passed, returning the latest frame.
pub fn settle(
    min_wait: Duration,
    timeout: Duration,
    mut capture: impl FnMut() -> Result<Frame, String>,
) -> Result<Frame, String> {
    std::thread::sleep(min_wait);
    let deadline = Instant::now() + timeout;
    let mut frame = capture()?;
    let mut thumb = thumbnail(&frame);
    loop {
        if Instant::now() >= deadline {
            return Ok(frame);
        }
        std::thread::sleep(POLL_INTERVAL);
        let next = capture()?;
        let next_thumb = thumbnail(&next);
        let settled = difference(&thumb, &next_thumb) < SETTLED_FRACTION;
        frame = next;
        thumb = next_thumb;
        if settled {
            return Ok(frame);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn solid(width: u32, height: u32, value: u8) -> Frame {
        Frame::new(
            width,
            height,
            [value, value, value, 255].repeat((width * height) as usize),
        )
        .unwrap()
    }

    #[test]
    fn thumbnail_is_64_by_40() {
        let t = thumbnail(&solid(1183, 768, 200));
        assert_eq!(t.len(), 64 * 40);
        assert!(t.iter().all(|v| *v == 200));
        // Smaller than the thumbnail still works.
        assert_eq!(thumbnail(&solid(10, 10, 9)).len(), 64 * 40);
    }

    #[test]
    fn difference_counts_changed_pixels() {
        let a = vec![100u8; 2560];
        let mut b = a.clone();
        assert_eq!(difference(&a, &b), 0.0);
        b[0] = 105; // within tolerance
        assert_eq!(difference(&a, &b), 0.0);
        for v in b.iter_mut().take(25) {
            *v = 0;
        }
        assert!(difference(&a, &b) < 0.01);
        for v in b.iter_mut().take(26) {
            *v = 0;
        }
        assert!(difference(&a, &b) >= 0.01);
    }

    #[test]
    fn a_small_change_in_a_large_frame_is_settled() {
        let a = solid(1000, 640, 50);
        let mut b = a.clone();
        // Blinking caret: a 2×16 px change.
        for y in 100..116 {
            for x in 300..302 {
                let i = (y * 1000 + x) * 4;
                b.bgra[i..i + 3].copy_from_slice(&[255, 255, 255]);
            }
        }
        assert!(difference(&thumbnail(&a), &thumbnail(&b)) < 0.01);
        let c = solid(1000, 640, 200);
        assert!(difference(&thumbnail(&a), &thumbnail(&c)) > 0.99);
    }

    #[test]
    fn settle_returns_once_stable() {
        let frames = [10u8, 120, 240, 240, 240];
        let mut calls = 0;
        let frame = settle(Duration::ZERO, Duration::from_secs(5), || {
            let f = solid(64, 40, frames[calls]);
            calls += 1;
            Ok(f)
        })
        .unwrap();
        assert_eq!(calls, 4);
        assert_eq!(frame.bgra[0], 240);
    }

    #[test]
    fn settle_gives_up_at_timeout() {
        let mut calls = 0u32;
        let started = Instant::now();
        let frame = settle(Duration::ZERO, Duration::from_millis(200), || {
            calls += 1;
            Ok(solid(64, 40, if calls.is_multiple_of(2) { 0 } else { 255 }))
        })
        .unwrap();
        assert!(started.elapsed() < Duration::from_secs(1));
        assert!(calls >= 2);
        assert_eq!(frame.bgra[0], if calls.is_multiple_of(2) { 0 } else { 255 });
    }

    #[test]
    fn settle_propagates_capture_errors() {
        assert!(settle(Duration::ZERO, TIMEOUT, || Err("boom".to_string())).is_err());
    }
}
