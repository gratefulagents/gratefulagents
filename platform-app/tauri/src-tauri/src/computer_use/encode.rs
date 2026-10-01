//! BGRA frames → JPEG, and crop/resize for zoom.

use image::{codecs::jpeg::JpegEncoder, imageops, ExtendedColorType, ImageBuffer, Rgba};

pub const JPEG_QUALITY: u8 = 80;

/// A tightly packed BGRA image (stride = `width * 4`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Frame {
    pub width: u32,
    pub height: u32,
    pub bgra: Vec<u8>,
}

impl Frame {
    pub fn new(width: u32, height: u32, bgra: Vec<u8>) -> Result<Frame, String> {
        if width == 0 || height == 0 || bgra.len() != width as usize * height as usize * 4 {
            return Err(format!(
                "invalid {width}x{height} frame of {} bytes",
                bgra.len()
            ));
        }
        Ok(Frame {
            width,
            height,
            bgra,
        })
    }
}

pub fn jpeg(frame: &Frame) -> Result<Vec<u8>, String> {
    let rgb: Vec<u8> = frame
        .bgra
        .chunks_exact(4)
        .flat_map(|px| [px[2], px[1], px[0]])
        .collect();
    let mut out = Vec::new();
    JpegEncoder::new_with_quality(&mut out, JPEG_QUALITY)
        .encode(&rgb, frame.width, frame.height, ExtendedColorType::Rgb8)
        .map_err(|e| format!("JPEG encoding failed: {e}"))?;
    Ok(out)
}

/// Crops `(x, y, w, h)` out of `frame` and resizes it to `out_width × out_height`.
pub fn crop_resize(
    frame: &Frame,
    (x, y, w, h): (u32, u32, u32, u32),
    out_width: u32,
    out_height: u32,
) -> Result<Frame, String> {
    if w == 0 || h == 0 || x + w > frame.width || y + h > frame.height {
        return Err("zoom region is outside the captured image".into());
    }
    let stride = frame.width as usize * 4;
    let mut pixels = Vec::with_capacity(w as usize * h as usize * 4);
    for row in frame
        .bgra
        .chunks_exact(stride)
        .skip(y as usize)
        .take(h as usize)
    {
        pixels.extend_from_slice(&row[x as usize * 4..(x + w) as usize * 4]);
    }
    // Channel order is irrelevant to resampling, so BGRA passes through as "RGBA".
    let cropped =
        ImageBuffer::<Rgba<u8>, Vec<u8>>::from_raw(w, h, pixels).ok_or("invalid zoom crop")?;
    let resized = if (w, h) == (out_width, out_height) {
        cropped
    } else {
        imageops::resize(
            &cropped,
            out_width,
            out_height,
            imageops::FilterType::CatmullRom,
        )
    };
    Frame::new(out_width, out_height, resized.into_raw())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn gradient(width: u32, height: u32) -> Frame {
        let mut bgra = Vec::with_capacity((width * height * 4) as usize);
        for y in 0..height {
            for x in 0..width {
                bgra.extend_from_slice(&[
                    (x * 255 / width) as u8,
                    (y * 255 / height) as u8,
                    0,
                    255,
                ]);
            }
        }
        Frame::new(width, height, bgra).unwrap()
    }

    #[test]
    fn rejects_mismatched_buffers() {
        assert!(Frame::new(2, 2, vec![0; 15]).is_err());
        assert!(Frame::new(0, 2, vec![]).is_err());
    }

    #[test]
    fn encodes_jpeg_with_matching_dimensions() {
        let frame = gradient(64, 40);
        let bytes = jpeg(&frame).unwrap();
        assert_eq!(&bytes[..2], &[0xFF, 0xD8]);
        let decoded = image::load_from_memory_with_format(&bytes, image::ImageFormat::Jpeg)
            .unwrap()
            .to_rgb8();
        assert_eq!(decoded.dimensions(), (64, 40));
        // BGRA blue channel (x gradient) must land in RGB blue.
        let right = decoded.get_pixel(60, 2);
        assert!(right[2] > 200 && right[0] < 40, "{right:?}");
    }

    #[test]
    fn crops_and_magnifies() {
        let frame = gradient(100, 50);
        let zoomed = crop_resize(&frame, (50, 0, 50, 25), 200, 100).unwrap();
        assert_eq!((zoomed.width, zoomed.height), (200, 100));
        assert_eq!(zoomed.bgra.len(), 200 * 100 * 4);
        // Left edge of the crop starts mid-gradient.
        assert!(
            zoomed.bgra[0] >= 120 && zoomed.bgra[0] <= 135,
            "{}",
            zoomed.bgra[0]
        );

        let same = crop_resize(&frame, (0, 0, 100, 50), 100, 50).unwrap();
        assert_eq!(same, frame);
        assert!(crop_resize(&frame, (60, 0, 50, 10), 10, 10).is_err());
    }
}
