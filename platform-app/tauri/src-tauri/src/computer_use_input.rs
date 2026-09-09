use super::computer_use_capture::{WindowCapture, WindowGeometry};
use super::computer_use_session::SessionScope;
use std::time::{Duration, Instant};

pub const FRAME_TTL: Duration = Duration::from_secs(30);

pub struct FocusTarget {
    checks: std::sync::mpsc::Sender<std::sync::mpsc::SyncSender<Result<(), String>>>,
}

impl FocusTarget {
    #[cfg(any(target_os = "macos", test))]
    pub(super) fn retain<F, V>(capture: F) -> Result<Self, String>
    where
        F: FnOnce() -> Result<V, String> + Send + 'static,
        V: Fn() -> Result<(), String> + 'static,
    {
        let (checks, requests) =
            std::sync::mpsc::channel::<std::sync::mpsc::SyncSender<Result<(), String>>>();
        let (ready, initialized) = std::sync::mpsc::sync_channel(1);
        std::thread::Builder::new()
            .name("desktop-focus-binding".into())
            .spawn(move || {
                // AX references are created, compared and released on this worker, never sent.
                let verify = match capture() {
                    Ok(verify) => verify,
                    Err(error) => {
                        let _ = ready.send(Err(error));
                        return;
                    }
                };
                if ready.send(Ok(())).is_err() {
                    return;
                }
                while let Ok(reply) = requests.recv_timeout(Duration::from_secs(130)) {
                    let result = verify();
                    let failed = result.is_err();
                    let _ = reply.send(result);
                    if failed {
                        break;
                    }
                }
            })
            .map_err(|_| "Cannot retain approved input target")?;
        initialized
            .recv_timeout(Duration::from_secs(2))
            .map_err(|_| "Cannot bind approved input target")??;
        Ok(Self { checks })
    }

    fn verify(&self) -> Result<(), String> {
        let (reply, result) = std::sync::mpsc::sync_channel(1);
        self.checks
            .send(reply)
            .map_err(|_| "Approved input target is unavailable")?;
        result
            .recv_timeout(Duration::from_secs(2))
            .map_err(|_| "Cannot verify approved input target")?
    }
}

pub fn bind_focus(scope: &SessionScope, action: &Action) -> Result<Option<FocusTarget>, String> {
    if !matches!(action, Action::Type { .. } | Action::Key { .. }) {
        return Ok(None);
    }
    #[cfg(target_os = "macos")]
    {
        macos::bind_focus(scope.clone()).map(Some)
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = scope;
        Err("Desktop input requires macOS".into())
    }
}

fn verify_focus(action: &Action, focus: Option<&FocusTarget>) -> Result<(), String> {
    if matches!(action, Action::Type { .. } | Action::Key { .. }) {
        focus.ok_or("No approved input target")?.verify()?;
    }
    Ok(())
}

#[derive(Clone, PartialEq, serde::Deserialize)]
#[serde(
    tag = "kind",
    rename_all = "lowercase",
    rename_all_fields = "camelCase",
    deny_unknown_fields
)]
pub enum Action {
    Observe { question: Option<String> },
    Click { x: f64, y: f64 },
    Scroll { delta_x: f64, delta_y: f64 },
    Type { text: String },
    Key { key: String },
    Activate {},
}

#[derive(Clone, PartialEq, serde::Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct QueuedRequest {
    pub request_id: String,
    pub frame_id: Option<String>,
    pub action: Action,
}

impl QueuedRequest {
    pub fn validate(&self) -> Result<(), String> {
        if self.request_id.is_empty()
            || self.request_id.len() > 128
            || self
                .frame_id
                .as_ref()
                .is_some_and(|id| id.is_empty() || id.len() > 128)
        {
            return Err("Invalid request or frame identifier".into());
        }
        match &self.action {
            Action::Observe { question } if question.as_ref().is_none_or(|q| q.len() <= 4096) => {
                Ok(())
            }
            Action::Click { x, y }
                if x.is_finite()
                    && y.is_finite()
                    && *x >= 0.0
                    && *y >= 0.0
                    && self.frame_id.is_some() =>
            {
                Ok(())
            }
            Action::Scroll { delta_x, delta_y }
                if delta_x.is_finite()
                    && delta_y.is_finite()
                    && delta_x.fract() == 0.0
                    && delta_y.fract() == 0.0
                    && delta_x.abs() <= 1000.0
                    && delta_y.abs() <= 1000.0
                    && (*delta_x != 0.0 || *delta_y != 0.0) =>
            {
                Ok(())
            }
            Action::Type { text }
                if !text.is_empty()
                    && text.len() <= 4000
                    && text.encode_utf16().count() <= 1000
                    && !text.chars().any(char::is_control) =>
            {
                Ok(())
            }
            Action::Key { key } if key_code(key).is_some() => Ok(()),
            Action::Activate {} => Ok(()),
            _ => Err("Unsupported or out-of-bounds desktop action".into()),
        }
    }
}

// Virtual key codes are layout-independent only for these navigation/editing keys.
fn key_code(key: &str) -> Option<(u16, u64)> {
    Some(match key {
        "Enter" => (36, 0),
        "Tab" => (48, 0),
        "Escape" => (53, 0),
        "Backspace" => (51, 0),
        "Delete" => (117, 0),
        "ArrowLeft" => (123, 0),
        "ArrowRight" => (124, 0),
        "ArrowDown" => (125, 0),
        "ArrowUp" => (126, 0),
        "Home" => (115, 0),
        "End" => (119, 0),
        "PageUp" => (116, 0),
        "PageDown" => (121, 0),
        "Shift+Tab" => (48, 1 << 17),
        _ => return None,
    })
}

#[derive(Clone, Debug, PartialEq)]
pub struct DisplayGeometry {
    pub id: u32,
    pub bounds: WindowGeometry,
    pub pixel_width: usize,
    pub pixel_height: usize,
    pub rotation: f64,
}

#[derive(Clone)]
pub struct Frame {
    pub id: String,
    pub geometry: WindowGeometry,
    pub pixel_width: u32,
    pub pixel_height: u32,
    #[cfg_attr(not(target_os = "macos"), allow(dead_code))]
    pub displays: Vec<DisplayGeometry>,
    pub created: Instant,
}

impl Frame {
    pub fn from_capture(capture: &WindowCapture, now: Instant) -> Self {
        Self {
            id: capture.frame_id.clone(),
            geometry: capture.geometry.clone(),
            pixel_width: capture.pixel_width,
            pixel_height: capture.pixel_height,
            displays: capture.displays.clone(),
            created: now,
        }
    }

    #[cfg(any(target_os = "macos", test))]
    pub fn validate_snapshot(
        &self,
        geometry: &WindowGeometry,
        displays: &[DisplayGeometry],
        now: Instant,
    ) -> Result<(), String> {
        if now.duration_since(self.created) >= FRAME_TTL
            || &self.geometry != geometry
            || self.displays != displays
        {
            return Err("Preview expired or window/display changed".into());
        }
        Ok(())
    }

    pub fn point(&self, x: f64, y: f64) -> Result<(f64, f64), String> {
        if !x.is_finite()
            || !y.is_finite()
            || x < 0.0
            || y < 0.0
            || self.pixel_width == 0
            || self.pixel_height == 0
            || self.geometry.width == 0
            || self.geometry.height == 0
            || x >= f64::from(self.pixel_width)
            || y >= f64::from(self.pixel_height)
        {
            return Err("Click is outside the captured image".into());
        }
        Ok((
            f64::from(self.geometry.x)
                + x * f64::from(self.geometry.width) / f64::from(self.pixel_width),
            f64::from(self.geometry.y)
                + y * f64::from(self.geometry.height) / f64::from(self.pixel_height),
        ))
    }
}

pub fn snapshot(scope: &SessionScope) -> Result<Frame, String> {
    #[cfg(target_os = "macos")]
    {
        super::computer_use_capture::validate_focus(scope)?;
        let created = Instant::now();
        let geometry = super::computer_use_capture::target_geometry(scope)?;
        Ok(Frame {
            id: String::new(),
            pixel_width: geometry.width,
            pixel_height: geometry.height,
            geometry,
            displays: macos::displays()?,
            created,
        })
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = scope;
        Err("Desktop input requires macOS".into())
    }
}

pub fn execute(
    scope: &SessionScope,
    action: &Action,
    frame: Option<&Frame>,
    focus: Option<&FocusTarget>,
    check: &dyn Fn() -> Result<(), String>,
) -> Result<(), String> {
    check()?;
    verify_focus(action, focus)?;
    #[cfg(target_os = "macos")]
    {
        macos::execute(scope, action, frame, focus, check)
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = (scope, action, frame, check);
        Err("Desktop input requires macOS".into())
    }
}

#[cfg(target_os = "macos")]
pub mod macos {
    use super::*;
    use std::ffi::{c_char, c_void};
    use std::ptr;
    type Ref = *const c_void;
    #[repr(C)]
    #[derive(Clone, Copy, Default)]
    pub struct Point {
        pub x: f64,
        pub y: f64,
    }
    #[repr(C)]
    #[derive(Clone, Copy, Default)]
    pub struct Size {
        pub width: f64,
        pub height: f64,
    }
    #[repr(C)]
    #[derive(Clone, Copy, Default)]
    pub struct Rect {
        pub origin: Point,
        pub size: Size,
    }

    #[link(name = "CoreFoundation", kind = "framework")]
    extern "C" {
        fn CFRelease(value: Ref);
        fn CFStringCreateWithCString(allocator: Ref, text: *const c_char, encoding: u32) -> Ref;
        fn CFEqual(a: Ref, b: Ref) -> bool;
        fn CFGetTypeID(value: Ref) -> usize;
    }
    #[link(name = "ApplicationServices", kind = "framework")]
    extern "C" {
        fn AXUIElementCreateApplication(pid: i32) -> Ref;
        fn AXUIElementGetTypeID() -> usize;
        fn AXUIElementCreateSystemWide() -> Ref;
        fn AXUIElementCopyAttributeValue(element: Ref, attribute: Ref, value: *mut Ref) -> i32;
        fn AXUIElementCopyElementAtPosition(element: Ref, x: f32, y: f32, value: *mut Ref) -> i32;
        fn AXUIElementGetPid(element: Ref, pid: *mut i32) -> i32;
        fn AXUIElementSetMessagingTimeout(element: Ref, timeout: f32) -> i32;
        fn AXValueGetValue(value: Ref, kind: u32, result: *mut c_void) -> bool;
    }
    #[link(name = "Carbon", kind = "framework")]
    extern "C" {
        fn IsSecureEventInputEnabled() -> bool;
    }
    #[link(name = "CoreGraphics", kind = "framework")]
    extern "C" {
        fn CGPreflightPostEventAccess() -> bool;
        fn CGEventCreateMouseEvent(source: Ref, kind: u32, point: Point, button: u32) -> Ref;
        fn CGEventCreateKeyboardEvent(source: Ref, key: u16, down: bool) -> Ref;
        fn CGEventKeyboardSetUnicodeString(event: Ref, length: usize, text: *const u16);
        fn CGEventSetFlags(event: Ref, flags: u64);
        fn CGEventSetLocation(event: Ref, point: Point);
        fn CGEventSetIntegerValueField(event: Ref, field: u32, value: i64);
        fn CGEventCreateScrollWheelEvent(source: Ref, units: u32, count: u32, ...) -> Ref;
        fn CGEventPostToPid(pid: i32, event: Ref);
        fn CGEventSourceFlagsState(state: i32) -> u64;
        fn CGEventSourceButtonState(state: i32, button: u32) -> bool;
        fn CGGetActiveDisplayList(max: u32, ids: *mut u32, count: *mut u32) -> i32;
        fn CGDisplayBounds(id: u32) -> Rect;
        fn CGDisplayPixelsWide(id: u32) -> usize;
        fn CGDisplayPixelsHigh(id: u32) -> usize;
        fn CGDisplayRotation(id: u32) -> f64;
        fn CGWindowListCreateImage(rect: Rect, option: u32, window: u32, flags: u32) -> Ref;
        fn CGImageGetWidth(image: Ref) -> usize;
        fn CGImageGetHeight(image: Ref) -> usize;
        fn CGColorSpaceCreateDeviceRGB() -> Ref;
        fn CGBitmapContextCreate(
            data: *mut c_void,
            width: usize,
            height: usize,
            bits: usize,
            row: usize,
            space: Ref,
            flags: u32,
        ) -> Ref;
        fn CGContextDrawImage(context: Ref, rect: Rect, image: Ref);
    }

    struct Owned(Ref);
    impl Owned {
        fn same(&self, other: &Self) -> bool {
            unsafe { CFEqual(self.0, other.0) }
        }
        fn new(value: Ref) -> Result<Self, String> {
            if value.is_null() {
                Err("macOS did not provide the required object".into())
            } else {
                Ok(Self(value))
            }
        }
    }
    impl Drop for Owned {
        fn drop(&mut self) {
            unsafe { CFRelease(self.0) };
        }
    }
    fn string(value: &str) -> Owned {
        let value = std::ffi::CString::new(value).expect("static AX name");
        Owned(unsafe { CFStringCreateWithCString(ptr::null(), value.as_ptr(), 0x08000100) })
    }
    fn attr(element: Ref, name: &str) -> Result<Owned, String> {
        let mut value = ptr::null();
        if unsafe { AXUIElementCopyAttributeValue(element, string(name).0, &mut value) } != 0 {
            return Err(format!("Cannot verify Accessibility attribute {name}"));
        }
        Owned::new(value)
    }
    fn equals(value: Ref, expected: &str) -> bool {
        unsafe { CFEqual(value, string(expected).0) }
    }
    fn app(scope: &SessionScope) -> Result<Owned, String> {
        let app = Owned::new(unsafe { AXUIElementCreateApplication(scope.process_id as i32) })?;
        if unsafe { AXUIElementSetMessagingTimeout(app.0, 0.2) } != 0 {
            return Err("Cannot bound Accessibility requests".into());
        }
        Ok(app)
    }
    pub fn secure(scope: &SessionScope) -> Result<(), String> {
        if unsafe { IsSecureEventInputEnabled() } {
            return Err("Secure input is active".into());
        }
        let app = app(scope)?;
        let focused = attr(app.0, "AXFocusedUIElement")?;
        non_password(focused.0)
    }
    fn focused_element(scope: &SessionScope) -> Result<Owned, String> {
        if unsafe { IsSecureEventInputEnabled() } {
            return Err("Secure input is active".into());
        }
        let app = app(scope)?;
        let focused = attr(app.0, "AXFocusedUIElement")?;
        if unsafe { CFGetTypeID(focused.0) != AXUIElementGetTypeID() }
            || unsafe { AXUIElementSetMessagingTimeout(focused.0, 0.2) } != 0
        {
            return Err("Cannot verify focused Accessibility element".into());
        }
        let mut pid = 0;
        if unsafe { AXUIElementGetPid(focused.0, &mut pid) } != 0 || pid as u32 != scope.process_id
        {
            return Err("Focused element is outside the approved process".into());
        }
        let window = attr(app.0, "AXFocusedWindow")?;
        let owner = attr(focused.0, "AXWindow")?;
        if !unsafe { CFEqual(window.0, owner.0) } {
            return Err("Focused element is outside the approved window".into());
        }
        non_password(focused.0)?;
        Ok(focused)
    }
    pub fn bind_focus(scope: SessionScope) -> Result<FocusTarget, String> {
        FocusTarget::retain(move || {
            super::super::computer_use_capture::validate_focus(&scope)?;
            let geometry = super::super::computer_use_capture::target_geometry(&scope)?;
            foreground(&scope, &geometry)?;
            let intended = focused_element(&scope)?;
            Ok(move || {
                let current = focused_element(&scope)?;
                if !intended.same(&current) {
                    return Err("Approved input target changed; queue a new request".into());
                }
                Ok(())
            })
        })
    }
    fn non_password(element: Ref) -> Result<(), String> {
        let role = attr(element, "AXRole")?;
        if equals(role.0, "AXTextField")
            || equals(role.0, "AXTextArea")
            || equals(role.0, "AXComboBox")
        {
            let subrole = attr(element, "AXSubrole")?;
            if equals(subrole.0, "AXSecureTextField") {
                return Err("Password fields are not supported".into());
            }
        }
        if equals(role.0, "AXSecureTextField") {
            return Err("Password fields are not supported".into());
        }
        Ok(())
    }
    pub fn displays() -> Result<Vec<DisplayGeometry>, String> {
        let mut ids = [0; 32];
        let mut count = 0;
        if unsafe { CGGetActiveDisplayList(32, ids.as_mut_ptr(), &mut count) } != 0
            || count == 0
            || count >= 32
        {
            return Err("Cannot verify display configuration".into());
        }
        let mut result = Vec::new();
        for id in &ids[..count as usize] {
            let rect = unsafe { CGDisplayBounds(*id) };
            result.push(DisplayGeometry {
                id: *id,
                bounds: WindowGeometry {
                    x: rect.origin.x as i32,
                    y: rect.origin.y as i32,
                    width: rect.size.width as u32,
                    height: rect.size.height as u32,
                },
                pixel_width: unsafe { CGDisplayPixelsWide(*id) },
                pixel_height: unsafe { CGDisplayPixelsHigh(*id) },
                rotation: unsafe { CGDisplayRotation(*id) },
            });
        }
        result.sort_by_key(|d| d.id);
        Ok(result)
    }
    pub fn capture_image(
        scope: &SessionScope,
        geometry: &WindowGeometry,
    ) -> Result<image::RgbaImage, String> {
        let (width, height) =
            super::super::computer_use_capture::output_dimensions(geometry.width, geometry.height)?;
        let rect = Rect {
            origin: Point {
                x: geometry.x.into(),
                y: geometry.y.into(),
            },
            size: Size {
                width: geometry.width.into(),
                height: geometry.height.into(),
            },
        };
        // Nominal resolution plus explicit bounds avoids Retina-sized/shadow allocations.
        let image = Owned::new(unsafe {
            CGWindowListCreateImage(rect, 1 << 3, scope.window_id, (1 << 0) | (1 << 4))
        })?;
        if unsafe { CGImageGetWidth(image.0) } != geometry.width as usize
            || unsafe { CGImageGetHeight(image.0) } != geometry.height as usize
        {
            return Err("Unexpected native capture dimensions".into());
        }
        let space = Owned::new(unsafe { CGColorSpaceCreateDeviceRGB() })?;
        let mut pixels = vec![0u8; width as usize * height as usize * 4];
        let context = Owned::new(unsafe {
            CGBitmapContextCreate(
                pixels.as_mut_ptr().cast(),
                width as usize,
                height as usize,
                8,
                width as usize * 4,
                space.0,
                1,
            )
        })?;
        unsafe {
            CGContextDrawImage(
                context.0,
                Rect {
                    origin: Point::default(),
                    size: Size {
                        width: width.into(),
                        height: height.into(),
                    },
                },
                image.0,
            )
        };
        drop(context);
        image::RgbaImage::from_raw(width, height, pixels)
            .ok_or_else(|| "Invalid image dimensions".into())
    }

    fn foreground(scope: &SessionScope, geometry: &WindowGeometry) -> Result<(), String> {
        let system = Owned::new(unsafe { AXUIElementCreateSystemWide() })?;
        unsafe { AXUIElementSetMessagingTimeout(system.0, 0.2) };
        let focused_app = attr(system.0, "AXFocusedApplication")?;
        let mut pid = 0;
        if unsafe { AXUIElementGetPid(focused_app.0, &mut pid) } != 0
            || pid as u32 != scope.process_id
        {
            return Err("Approved process is not foreground".into());
        }
        let focused_window = attr(app(scope)?.0, "AXFocusedWindow")?;
        let position = attr(focused_window.0, "AXPosition")?;
        let size = attr(focused_window.0, "AXSize")?;
        let mut point = Point::default();
        let mut dims = Size::default();
        if !unsafe { AXValueGetValue(position.0, 1, (&mut point as *mut Point).cast()) }
            || !unsafe { AXValueGetValue(size.0, 2, (&mut dims as *mut Size).cast()) }
            || point.x != f64::from(geometry.x)
            || point.y != f64::from(geometry.y)
            || dims.width != f64::from(geometry.width)
            || dims.height != f64::from(geometry.height)
        {
            return Err("Focused window geometry differs from the approved window".into());
        }
        // AX has no public window-ID accessor. Require the frontmost CG window to be the approved ID as well.
        let first = xcap::Window::all()
            .map_err(|e| e.to_string())?
            .into_iter()
            .find(|w| w.pid().ok() == Some(scope.process_id));
        if first.and_then(|w| w.id().ok()) != Some(scope.window_id) {
            return Err("Approved window is not the frontmost application window".into());
        }
        Ok(())
    }
    fn destination(scope: &SessionScope, point: Point) -> Result<(), String> {
        let system = Owned::new(unsafe { AXUIElementCreateSystemWide() })?;
        if unsafe { AXUIElementSetMessagingTimeout(system.0, 0.2) } != 0 {
            return Err("Cannot bound Accessibility hit testing".into());
        }
        let mut hit = ptr::null();
        if unsafe {
            AXUIElementCopyElementAtPosition(system.0, point.x as f32, point.y as f32, &mut hit)
        } != 0
        {
            return Err("Cannot verify pointer destination".into());
        }
        let mut hit = Owned::new(hit)?;
        let focused = attr(app(scope)?.0, "AXFocusedWindow")?;
        let mut pid = 0;
        if unsafe { AXUIElementGetPid(hit.0, &mut pid) } != 0 || pid as u32 != scope.process_id {
            return Err("Pointer destination is occluded by another process".into());
        }
        for _ in 0..16 {
            non_password(hit.0)?;
            if unsafe { CFEqual(hit.0, focused.0) } {
                return Ok(());
            }
            hit = attr(hit.0, "AXParent")?;
        }
        Err("Cannot bind pointer destination to the approved window".into())
    }
    fn activate(scope: &SessionScope) -> Result<(), String> {
        objc2::rc::autoreleasepool(|_| unsafe {
            let app: *mut objc2::runtime::AnyObject = objc2::msg_send![objc2::class!(NSRunningApplication), runningApplicationWithProcessIdentifier: scope.process_id as i32];
            if app.is_null() {
                return Err("Approved process is no longer running".into());
            }
            let ok: bool = objc2::msg_send![app, activateWithOptions: 2usize];
            if !ok {
                return Err("macOS refused approved-process activation".into());
            }
            Ok(())
        })
    }
    struct Release {
        event: Owned,
        pid: i32,
    }
    impl Drop for Release {
        fn drop(&mut self) {
            unsafe { CGEventPostToPid(self.pid, self.event.0) };
        }
    }
    fn pair(
        scope: &SessionScope,
        down: Owned,
        up: Owned,
        check: &dyn Fn() -> Result<(), String>,
    ) -> Result<(), String> {
        check()?;
        let release = Release {
            event: up,
            pid: scope.process_id as i32,
        };
        unsafe { CGEventPostToPid(release.pid, down.0) };
        // Never authorize key-up separately: stop must not leave a synthetic key held.
        drop(release);
        check()
    }
    pub fn execute(
        scope: &SessionScope,
        action: &Action,
        frame: Option<&Frame>,
        focus: Option<&FocusTarget>,
        check: &dyn Fn() -> Result<(), String>,
    ) -> Result<(), String> {
        check()?;
        if !unsafe { CGPreflightPostEventAccess() } {
            return Err("macOS input permission is unavailable".into());
        }
        super::super::computer_use_capture::validate_focus(scope)?;
        let geometry = super::super::computer_use_capture::target_geometry(scope)?;
        let display = displays()?;
        if let Some(frame) = frame {
            frame.validate_snapshot(&geometry, &display, Instant::now())?;
        }
        secure(scope)?;
        super::super::computer_use_capture::validate_focus(scope)?;
        check()?;
        activate(scope)?;
        let guard = || {
            check()?;
            if let Some(frame) = frame {
                frame.validate_snapshot(&geometry, &display, Instant::now())?;
            }
            let permissions = super::super::computer_use::computer_use_permissions();
            if !permissions.accessibility
                || !permissions.screen_recording
                || !unsafe { CGPreflightPostEventAccess() }
            {
                return Err("Required OS permission was revoked".into());
            }
            if unsafe { CGEventSourceFlagsState(0) }
                & ((1 << 17) | (1 << 18) | (1 << 19) | (1 << 20) | (1 << 23))
                != 0
                || (0..3).any(|button| unsafe { CGEventSourceButtonState(0, button) })
            {
                return Err("Release physical modifiers and mouse buttons first".into());
            }
            if super::super::computer_use_capture::target_geometry(scope)? != geometry
                || displays()? != display
            {
                return Err("Window or display changed during input".into());
            }
            foreground(scope, &geometry)?;
            secure(scope)?;
            verify_focus(action, focus)?;
            check()
        };
        // Activation is asynchronous. Fail closed if it has not settled within the permit.
        for _ in 0..10 {
            check()?;
            if foreground(scope, &geometry).is_ok() {
                break;
            }
            std::thread::sleep(Duration::from_millis(20));
        }
        guard()?;
        match action {
            Action::Activate {} => Ok(()),
            Action::Click { x, y } => {
                let (x, y) = frame
                    .ok_or("Click requires a captured frame")?
                    .point(*x, *y)?;
                let point = Point { x, y };
                let click_guard = || {
                    guard()?;
                    destination(scope, point)?;
                    check()
                };
                let down = Owned::new(unsafe {
                    CGEventCreateMouseEvent(ptr::null(), 1, Point { x, y }, 0)
                })?;
                let up = Owned::new(unsafe {
                    CGEventCreateMouseEvent(ptr::null(), 2, Point { x, y }, 0)
                })?;
                unsafe {
                    CGEventSetFlags(down.0, 0);
                    CGEventSetFlags(up.0, 0);
                    CGEventSetIntegerValueField(down.0, 1, 1);
                    CGEventSetIntegerValueField(up.0, 1, 1);
                }
                pair(scope, down, up, &click_guard)
            }
            Action::Scroll { delta_x, delta_y } => {
                let event = Owned::new(unsafe {
                    CGEventCreateScrollWheelEvent(
                        ptr::null(),
                        0,
                        2,
                        -*delta_y as i32,
                        -*delta_x as i32,
                    )
                })?;
                let point = Point {
                    x: f64::from(geometry.x) + f64::from(geometry.width) / 2.0,
                    y: f64::from(geometry.y) + f64::from(geometry.height) / 2.0,
                };
                unsafe {
                    CGEventSetLocation(event.0, point);
                    CGEventSetFlags(event.0, 0);
                }
                guard()?;
                destination(scope, point)?;
                check()?;
                unsafe { CGEventPostToPid(scope.process_id as i32, event.0) };
                guard()
            }
            Action::Type { text } => {
                for character in text.chars() {
                    let mut units = [0; 2];
                    let units = character.encode_utf16(&mut units);
                    let down =
                        Owned::new(unsafe { CGEventCreateKeyboardEvent(ptr::null(), 0, true) })?;
                    let up =
                        Owned::new(unsafe { CGEventCreateKeyboardEvent(ptr::null(), 0, false) })?;
                    unsafe {
                        CGEventSetFlags(down.0, 0);
                        CGEventSetFlags(up.0, 0);
                        CGEventKeyboardSetUnicodeString(down.0, units.len(), units.as_ptr());
                        CGEventKeyboardSetUnicodeString(up.0, units.len(), units.as_ptr());
                    }
                    pair(scope, down, up, &guard)?;
                }
                Ok(())
            }
            Action::Key { key } => {
                let (code, flags) = key_code(key).ok_or("Unsupported key")?;
                let down =
                    Owned::new(unsafe { CGEventCreateKeyboardEvent(ptr::null(), code, true) })?;
                let up =
                    Owned::new(unsafe { CGEventCreateKeyboardEvent(ptr::null(), code, false) })?;
                unsafe {
                    CGEventSetFlags(down.0, flags);
                    CGEventSetFlags(up.0, 0);
                }
                pair(scope, down, up, &guard)
            }
            Action::Observe { .. } => Err("Observe uses capture, not input".into()),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn focus_change_before_execution_or_between_characters_rejects_input() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        use std::sync::Arc;
        for action in [
            Action::Type { text: "abc".into() },
            Action::Key {
                key: "Enter".into(),
            },
        ] {
            assert!(verify_focus(&action, None).is_err());
            for change_after in [0, 1] {
                let current = Arc::new(AtomicUsize::new(1));
                let observed = current.clone();
                let target = FocusTarget::retain(move || {
                    let intended = std::rc::Rc::new(observed.load(Ordering::SeqCst));
                    Ok(move || {
                        if *intended == observed.load(Ordering::SeqCst) {
                            Ok(())
                        } else {
                            Err("Approved input target changed".into())
                        }
                    })
                })
                .unwrap();
                let mut sent = 0;
                for index in 0..3 {
                    if index == change_after {
                        current.store(2, Ordering::SeqCst);
                    }
                    if verify_focus(&action, Some(&target)).is_err() {
                        break;
                    }
                    sent += 1;
                }
                assert_eq!(sent, change_after);
                current.store(1, Ordering::SeqCst);
                assert!(verify_focus(&action, Some(&target)).is_err());
            }
        }
        assert!(verify_focus(&Action::Activate {}, None).is_ok());
    }

    #[test]
    fn focus_worker_releases_retained_identity_when_token_is_dropped() {
        struct Identity(std::sync::mpsc::Sender<()>);
        impl Drop for Identity {
            fn drop(&mut self) {
                self.0.send(()).unwrap();
            }
        }
        fn send_sync<T: Send + Sync>() {}
        send_sync::<FocusTarget>();
        let (released, release) = std::sync::mpsc::channel();
        let target = FocusTarget::retain(move || {
            let identity = std::rc::Rc::new(Identity(released));
            Ok(move || {
                let _retained = &identity;
                Ok(())
            })
        })
        .unwrap();
        target.verify().unwrap();
        assert!(release.try_recv().is_err());
        drop(target);
        release.recv_timeout(Duration::from_secs(2)).unwrap();
    }

    #[test]
    fn failed_focus_capture_cannot_produce_a_token() {
        let target = FocusTarget::retain(|| -> Result<fn() -> Result<(), String>, String> {
            Err("Cannot verify focused element".into())
        });
        assert!(target.is_err());
    }

    #[test]
    fn wire_and_bounds_are_closed() {
        for bad in [
            r#"{"requestId":"a","action":{"kind":"shell","text":"id"}}"#,
            r#"{"requestId":"a","action":{"kind":"activate","text":"x"}}"#,
            r#"{"requestId":"a","action":{"kind":"type"}}"#,
        ] {
            assert!(serde_json::from_str::<QueuedRequest>(bad).is_err());
        }
        for action in [
            Action::Click {
                x: f64::NAN,
                y: 0.0,
            },
            Action::Scroll {
                delta_x: 1001.0,
                delta_y: 0.0,
            },
            Action::Type {
                text: "a\nb".into(),
            },
            Action::Key {
                key: "Meta+Space".into(),
            },
        ] {
            assert!(QueuedRequest {
                request_id: "a".into(),
                frame_id: Some("f".into()),
                action
            }
            .validate()
            .is_err());
        }
        let scroll: QueuedRequest = serde_json::from_str(
            r#"{"requestId":"s","action":{"kind":"scroll","deltaX":0,"deltaY":100}}"#,
        )
        .unwrap();
        assert!(scroll.validate().is_ok());
        assert!(key_code("Shift+Tab").is_some());
    }
    #[test]
    fn maps_downsampled_pixels_to_negative_desktop_points() {
        let frame = Frame {
            id: "f".into(),
            geometry: WindowGeometry {
                x: -1200,
                y: 50,
                width: 2400,
                height: 1200,
            },
            pixel_width: 1200,
            pixel_height: 600,
            displays: vec![],
            created: Instant::now(),
        };
        assert_eq!(frame.point(600.0, 300.0).unwrap(), (0.0, 650.0));
        assert!(frame
            .validate_snapshot(&frame.geometry, &[], frame.created)
            .is_ok());
        assert!(frame
            .validate_snapshot(&frame.geometry, &[], frame.created + FRAME_TTL)
            .is_err());
        let mut moved = frame.geometry.clone();
        moved.x += 1;
        assert!(frame.validate_snapshot(&moved, &[], frame.created).is_err());
        let displays = vec![DisplayGeometry {
            id: 1,
            bounds: frame.geometry.clone(),
            pixel_width: 2400,
            pixel_height: 1200,
            rotation: 0.0,
        }];
        assert!(frame
            .validate_snapshot(&frame.geometry, &displays, frame.created)
            .is_err());
        for (x, y) in [
            (-1.0, 0.0),
            (1200.0, 0.0),
            (0.0, 600.0),
            (f64::INFINITY, 0.0),
        ] {
            assert!(frame.point(x, y).is_err());
        }
    }
}
