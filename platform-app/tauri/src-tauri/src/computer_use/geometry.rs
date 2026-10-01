//! Display points ↔ screenshot frame pixels.

const MAX_SHORT_SIDE: f64 = 768.0;
const MAX_LONG_SIDE: f64 = 1456.0;

/// Frame size for a `width × height` point display:
/// `k = min(1, 768/min(W,H), 1456/max(W,H))`, frame `round(W·k) × round(H·k)`.
pub fn frame_size(width: f64, height: f64) -> (u32, u32) {
    let k = 1f64
        .min(MAX_SHORT_SIDE / width.min(height))
        .min(MAX_LONG_SIDE / width.max(height));
    (
        ((width * k).round() as u32).max(1),
        ((height * k).round() as u32).max(1),
    )
}

/// A rectangle in global display points (top-left origin; may be negative).
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Rect {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

impl Rect {
    pub fn contains(&self, x: f64, y: f64) -> bool {
        x >= self.x && x < self.x + self.width && y >= self.y && y < self.y + self.height
    }
}

/// A pixel of the most recent screenshot.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Point {
    pub x: i64,
    pub y: i64,
}

#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Mapping {
    pub display: Rect,
    pub frame_width: u32,
    pub frame_height: u32,
}

impl Mapping {
    pub fn new(display: Rect) -> Mapping {
        let (frame_width, frame_height) = frame_size(display.width, display.height);
        Mapping {
            display,
            frame_width,
            frame_height,
        }
    }

    pub fn frame(&self) -> (u32, u32) {
        (self.frame_width, self.frame_height)
    }

    pub fn check(&self, point: Point) -> Result<(), String> {
        if point.x < 0
            || point.y < 0
            || point.x >= i64::from(self.frame_width)
            || point.y >= i64::from(self.frame_height)
        {
            return Err(format!(
                "coordinate [{}, {}] is outside the {}x{} screenshot",
                point.x, point.y, self.frame_width, self.frame_height
            ));
        }
        Ok(())
    }

    /// Centre of the frame pixel, in global points: `x_pt = ox + (x+0.5)·W/fw`.
    pub fn to_points(self, point: Point) -> (f64, f64) {
        let d = &self.display;
        (
            d.x + (point.x as f64 + 0.5) * d.width / f64::from(self.frame_width),
            d.y + (point.y as f64 + 0.5) * d.height / f64::from(self.frame_height),
        )
    }

    /// The frame pixel containing a global point, or `None` off this display.
    pub fn to_frame(self, x: f64, y: f64) -> Option<Point> {
        if !self.display.contains(x, y) {
            return None;
        }
        let d = &self.display;
        let fx = ((x - d.x) * f64::from(self.frame_width) / d.width).floor() as i64;
        let fy = ((y - d.y) * f64::from(self.frame_height) / d.height).floor() as i64;
        Some(Point {
            x: fx.min(i64::from(self.frame_width) - 1),
            y: fy.min(i64::from(self.frame_height) - 1),
        })
    }
}

/// Zoom region `[x0, y0, x1, y1]` in frame pixels (x1, y1 exclusive).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Region {
    pub x0: i64,
    pub y0: i64,
    pub x1: i64,
    pub y1: i64,
}

impl Region {
    pub fn check(&self, frame_width: u32, frame_height: u32) -> Result<(), String> {
        if self.x0 < 0
            || self.y0 < 0
            || self.x1 > i64::from(frame_width)
            || self.y1 > i64::from(frame_height)
        {
            return Err(format!(
                "region [{}, {}, {}, {}] is outside the {}x{} screenshot",
                self.x0, self.y0, self.x1, self.y1, frame_width, frame_height
            ));
        }
        Ok(())
    }

    /// The region scaled from frame pixels to a `width × height` image, as `(x, y, w, h)`.
    pub fn scaled(&self, frame: (u32, u32), width: u32, height: u32) -> (u32, u32, u32, u32) {
        let sx = f64::from(width) / f64::from(frame.0);
        let sy = f64::from(height) / f64::from(frame.1);
        let x0 = ((self.x0 as f64 * sx).floor() as u32).min(width - 1);
        let y0 = ((self.y0 as f64 * sy).floor() as u32).min(height - 1);
        let x1 = ((self.x1 as f64 * sx).ceil() as u32).clamp(x0 + 1, width);
        let y1 = ((self.y1 as f64 * sy).ceil() as u32).clamp(y0 + 1, height);
        (x0, y0, x1 - x0, y1 - y0)
    }

    /// Output size of the magnified crop: the region scaled to fit the frame.
    pub fn zoom_size(&self, frame: (u32, u32)) -> (u32, u32) {
        let w = (self.x1 - self.x0) as f64;
        let h = (self.y1 - self.y0) as f64;
        let k = (f64::from(frame.0) / w).min(f64::from(frame.1) / h);
        (
            ((w * k).round() as u32).max(1),
            ((h * k).round() as u32).max(1),
        )
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn frame_size_formula() {
        assert_eq!(frame_size(1512.0, 982.0), (1183, 768));
        assert_eq!(frame_size(1440.0, 900.0), (1229, 768));
        assert_eq!(frame_size(1920.0, 1080.0), (1365, 768));
        assert_eq!(frame_size(2560.0, 1440.0), (1365, 768));
        assert_eq!(frame_size(3440.0, 1440.0), (1456, 609));
        assert_eq!(frame_size(1080.0, 1920.0), (768, 1365));
        assert_eq!(frame_size(1024.0, 768.0), (1024, 768));
        assert_eq!(frame_size(800.0, 600.0), (800, 600));
    }

    #[test]
    fn maps_frame_pixels_to_points() {
        let m = Mapping::new(Rect {
            x: 0.0,
            y: 0.0,
            width: 1512.0,
            height: 982.0,
        });
        assert_eq!(m.frame(), (1183, 768));
        let (x, y) = m.to_points(Point { x: 0, y: 0 });
        assert!((x - 0.5 * 1512.0 / 1183.0).abs() < 1e-9);
        assert!((y - 0.5 * 982.0 / 768.0).abs() < 1e-9);
        let (x, y) = m.to_points(Point { x: 1182, y: 767 });
        assert!(x < 1512.0 && y < 982.0);
        for p in [
            Point { x: 0, y: 0 },
            Point { x: 512, y: 300 },
            Point { x: 1182, y: 767 },
        ] {
            let (x, y) = m.to_points(p);
            assert_eq!(m.to_frame(x, y), Some(p));
        }
    }

    #[test]
    fn handles_negative_origins() {
        // A display left of and above the primary one.
        let m = Mapping::new(Rect {
            x: -1920.0,
            y: -1080.0,
            width: 1920.0,
            height: 1080.0,
        });
        assert_eq!(m.frame(), (1365, 768));
        let (x, y) = m.to_points(Point { x: 0, y: 0 });
        assert!(x > -1920.0 && x < -1918.0, "{x}");
        assert!(y > -1080.0 && y < -1078.0, "{y}");
        let (x, y) = m.to_points(Point { x: 1364, y: 767 });
        assert!(x < 0.0 && y < 0.0);
        assert_eq!(m.to_frame(x, y), Some(Point { x: 1364, y: 767 }));
        assert_eq!(m.to_frame(10.0, 10.0), None);
        assert_eq!(m.to_frame(-1921.0, -500.0), None);
        assert_eq!(m.to_frame(-1920.0, -1080.0), Some(Point { x: 0, y: 0 }));
    }

    #[test]
    fn bounds_checks() {
        let m = Mapping::new(Rect {
            x: 0.0,
            y: 0.0,
            width: 1024.0,
            height: 768.0,
        });
        assert!(m.check(Point { x: 0, y: 0 }).is_ok());
        assert!(m.check(Point { x: 1023, y: 767 }).is_ok());
        assert!(m.check(Point { x: 1024, y: 0 }).is_err());
        assert!(m.check(Point { x: 0, y: 768 }).is_err());
        assert!(m.check(Point { x: -1, y: 0 }).is_err());

        let r = Region {
            x0: 0,
            y0: 0,
            x1: 1024,
            y1: 768,
        };
        assert!(r.check(1024, 768).is_ok());
        assert!(Region { x1: 1025, ..r }.check(1024, 768).is_err());
        assert!(Region { x0: -1, ..r }.check(1024, 768).is_err());
    }

    #[test]
    fn zoom_geometry() {
        let r = Region {
            x0: 100,
            y0: 50,
            x1: 300,
            y1: 200,
        };
        assert_eq!(r.zoom_size((1183, 768)), (1024, 768));
        // Frame 1183x768 captured at 3024x1964 native pixels.
        let (x, y, w, h) = r.scaled((1183, 768), 3024, 1964);
        assert_eq!((x, y), (255, 127));
        assert!(w >= 511 && h >= 255);
        assert!(x + w <= 3024 && y + h <= 1964);
        let full = Region {
            x0: 0,
            y0: 0,
            x1: 1183,
            y1: 768,
        };
        assert_eq!(full.scaled((1183, 768), 1183, 768), (0, 0, 1183, 768));
    }
}
