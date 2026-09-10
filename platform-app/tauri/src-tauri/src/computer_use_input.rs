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
    Observe {
        question: Option<String>,
    },
    Click {
        x: f64,
        y: f64,
        #[serde(default)]
        button: Option<MouseButton>,
        #[serde(default)]
        count: Option<u8>,
    },
    Move {
        x: f64,
        y: f64,
    },
    Drag {
        x: f64,
        y: f64,
        to_x: f64,
        to_y: f64,
    },
    Scroll {
        delta_x: f64,
        delta_y: f64,
        #[serde(default)]
        x: Option<f64>,
        #[serde(default)]
        y: Option<f64>,
    },
    Type {
        text: String,
    },
    Key {
        key: String,
    },
    Activate {},
    /// Open a web URL in the approved browser without bringing it forward.
    #[serde(rename = "open_url")]
    OpenUrl {
        url: String,
    },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, serde::Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum MouseButton {
    Left,
    Right,
    Middle,
}

impl Action {
    /// Keyboard actions need the approved window to be key; they bring the
    /// approved application forward. Pointer actions and URL opening are
    /// delivered to the approved process in the background.
    pub fn needs_foreground(&self) -> bool {
        matches!(self, Action::Type { .. } | Action::Key { .. })
    }

    /// Frame pixel coordinates that must map inside the captured frame before
    /// the request is queued and again before execution.
    pub fn points(&self) -> Vec<(f64, f64)> {
        match self {
            Action::Click { x, y, .. } | Action::Move { x, y } => vec![(*x, *y)],
            Action::Drag { x, y, to_x, to_y } => vec![(*x, *y), (*to_x, *to_y)],
            Action::Scroll {
                x: Some(x),
                y: Some(y),
                ..
            } => vec![(*x, *y)],
            _ => Vec::new(),
        }
    }
}

fn pixel(n: f64) -> bool {
    n.is_finite() && n >= 0.0
}

/// Web URLs only: absolute http(s) with a host, no credentials, bounded length.
pub fn web_url(raw: &str) -> Result<url::Url, String> {
    if raw.is_empty()
        || raw.len() > 2048
        || raw.chars().any(|c| c.is_control() || c.is_whitespace())
    {
        return Err("URL must be a single http(s) address of at most 2048 characters".into());
    }
    let parsed = url::Url::parse(raw).map_err(|_| "Invalid URL")?;
    if !matches!(parsed.scheme(), "http" | "https")
        || parsed.host_str().is_none_or(str::is_empty)
        || !parsed.username().is_empty()
        || parsed.password().is_some()
    {
        return Err("Only http(s) URLs without credentials can be opened".into());
    }
    Ok(parsed)
}

/// Browsers that `open_url` may target, by bundle identifier. The approved
/// window's process must be one of these; other applications are refused.
pub const BROWSER_BUNDLES: &[&str] = &[
    "org.mozilla.firefox",
    "org.mozilla.firefoxdeveloperedition",
    "org.mozilla.nightly",
    "com.apple.Safari",
    "com.google.Chrome",
    "com.google.Chrome.canary",
    "com.microsoft.edgemac",
    "com.brave.Browser",
    "company.thebrowser.Browser",
    "com.vivaldi.Vivaldi",
    "com.operasoftware.Opera",
    "org.chromium.Chromium",
    "com.kagi.kagimacOS",
    "app.zen-browser.zen",
];

pub fn browser_bundle(bundle: &str) -> bool {
    BROWSER_BUNDLES.contains(&bundle)
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
            Action::Click { x, y, count, .. }
                if pixel(*x)
                    && pixel(*y)
                    && count.is_none_or(|c| (1..=3).contains(&c))
                    && self.frame_id.is_some() =>
            {
                Ok(())
            }
            Action::Move { x, y } if pixel(*x) && pixel(*y) && self.frame_id.is_some() => Ok(()),
            Action::Drag { x, y, to_x, to_y }
                if [*x, *y, *to_x, *to_y].iter().all(|n| pixel(*n))
                    && (x != to_x || y != to_y)
                    && self.frame_id.is_some() =>
            {
                Ok(())
            }
            Action::Scroll {
                delta_x,
                delta_y,
                x,
                y,
            } if delta_x.is_finite()
                && delta_y.is_finite()
                && delta_x.fract() == 0.0
                && delta_y.fract() == 0.0
                && delta_x.abs() <= 1000.0
                && delta_y.abs() <= 1000.0
                && (*delta_x != 0.0 || *delta_y != 0.0)
                && x.is_some() == y.is_some()
                && x.is_none_or(pixel)
                && y.is_none_or(pixel)
                && (x.is_none() || self.frame_id.is_some()) =>
            {
                Ok(())
            }
            Action::Type { text }
                if !text.is_empty()
                    && text.len() <= 4000
                    && text.encode_utf16().count() <= 1000
                    && !text.chars().any(hidden_character) =>
            {
                Ok(())
            }
            Action::Key { key } if parse_hotkey(key).is_some() => Ok(()),
            Action::Activate {} => Ok(()),
            Action::OpenUrl { url } => web_url(url).map(|_| ()),
            _ => Err("Unsupported or out-of-bounds desktop action".into()),
        }
    }
}

/// A validated key press: the layout-independent virtual key code, Core
/// Graphics modifier flags, and the character an ANSI keyboard would produce
/// (set on the event so menu key equivalents match on non-ANSI layouts).
#[derive(Clone, Debug, PartialEq)]
pub struct Hotkey {
    pub code: u16,
    pub flags: u64,
    pub character: Option<char>,
    pub control: bool,
    pub option: bool,
    pub shift: bool,
    pub cmd: bool,
}

const FLAG_SHIFT: u64 = 1 << 17;
const FLAG_CONTROL: u64 = 1 << 18;
const FLAG_OPTION: u64 = 1 << 19;
const FLAG_CMD: u64 = 1 << 20;

// Virtual key codes are layout-independent only for these navigation/editing
// keys; letters and digits use ANSI positions and also carry their character.
fn named_key_code(key: &str) -> Option<u16> {
    Some(match key {
        "Enter" => 36,
        "Tab" => 48,
        "Space" => 49,
        "Escape" => 53,
        "Backspace" => 51,
        "Delete" => 117,
        "ArrowLeft" => 123,
        "ArrowRight" => 124,
        "ArrowDown" => 125,
        "ArrowUp" => 126,
        "Home" => 115,
        "End" => 119,
        "PageUp" => 116,
        "PageDown" => 121,
        _ => return None,
    })
}

fn ansi_key_code(character: char) -> Option<u16> {
    Some(match character {
        'A' => 0,
        'S' => 1,
        'D' => 2,
        'F' => 3,
        'H' => 4,
        'G' => 5,
        'Z' => 6,
        'X' => 7,
        'C' => 8,
        'V' => 9,
        'B' => 11,
        'Q' => 12,
        'W' => 13,
        'E' => 14,
        'R' => 15,
        'Y' => 16,
        'T' => 17,
        '1' => 18,
        '2' => 19,
        '3' => 20,
        '4' => 21,
        '6' => 22,
        '5' => 23,
        '9' => 25,
        '7' => 26,
        '8' => 28,
        '0' => 29,
        'O' => 31,
        'U' => 32,
        'I' => 34,
        'P' => 35,
        'L' => 37,
        'J' => 38,
        'K' => 40,
        'N' => 45,
        'M' => 46,
        _ => return None,
    })
}

/// Parses "Mod+...+Key" with Control/Ctrl, Option/Alt, Shift and
/// Cmd/Command/Meta in any order, mirroring the agent-side and relay
/// validators. Letters and digits need Control, Option or Cmd so a key press
/// cannot become a text channel that bypasses proposed-text review.
/// Combinations that quit, close, hide or minimize the approved window, switch
/// applications or spaces, open Spotlight, take screenshots, force quit,
/// toggle fullscreen, show the Dock, or match the emergency stop are rejected.
pub fn parse_hotkey(key: &str) -> Option<Hotkey> {
    if key.is_empty() || key.len() > 40 {
        return None;
    }
    let mut hotkey = Hotkey {
        code: 0,
        flags: 0,
        character: None,
        control: false,
        option: false,
        shift: false,
        cmd: false,
    };
    let parts: Vec<&str> = key.split('+').collect();
    let mut base = "";
    for (index, part) in parts.iter().enumerate() {
        if index + 1 == parts.len() {
            base = part;
            break;
        }
        let modifier = match *part {
            "Control" | "Ctrl" => &mut hotkey.control,
            "Option" | "Alt" => &mut hotkey.option,
            "Shift" => &mut hotkey.shift,
            "Cmd" | "Command" | "Meta" => &mut hotkey.cmd,
            _ => return None,
        };
        if *modifier {
            return None;
        }
        *modifier = true;
    }
    let mut chars = base.chars();
    match (chars.next(), chars.next()) {
        (Some(c), None) if c.is_ascii_alphanumeric() => {
            if !hotkey.control && !hotkey.option && !hotkey.cmd {
                return None;
            }
            let upper = c.to_ascii_uppercase();
            hotkey.code = ansi_key_code(upper)?;
            hotkey.character = Some(if hotkey.shift {
                upper
            } else {
                upper.to_ascii_lowercase()
            });
        }
        _ => hotkey.code = named_key_code(base)?,
    }
    let arrow = base.starts_with("Arrow");
    let upper = hotkey.character.map(|c| c.to_ascii_uppercase());
    let denied = if hotkey.cmd {
        matches!(upper, Some('Q' | 'W' | 'H' | 'M'))
            || matches!(base, "Tab" | "Space" | "Escape")
            || (hotkey.shift && matches!(upper, Some('3' | '4' | '5' | '6')))
            || (hotkey.option && upper == Some('D'))
            || (hotkey.control && upper == Some('F'))
    } else {
        false
    } || (hotkey.control && (arrow || base == "Space"));
    if denied {
        return None;
    }
    hotkey.flags = (if hotkey.shift { FLAG_SHIFT } else { 0 })
        | (if hotkey.control { FLAG_CONTROL } else { 0 })
        | (if hotkey.option { FLAG_OPTION } else { 0 })
        | (if hotkey.cmd { FLAG_CMD } else { 0 });
    Some(hotkey)
}

// Code points that render invisibly or reorder visible text would let the
// delivered keystrokes differ from what the supervisor reviewed: controls,
// Unicode format characters (zero-width space, BOM, bidi overrides/isolates,
// tags), line/paragraph separators, private-use planes and noncharacters.
// Zero-width joiner/non-joiner (U+200C/U+200D) and variation selectors stay
// allowed because emoji sequences and several scripts need them.
pub fn hidden_character(c: char) -> bool {
    c.is_control()
        || matches!(
            c,
            '\u{00AD}'
                | '\u{0600}'..='\u{0605}'
                | '\u{061C}'
                | '\u{06DD}'
                | '\u{070F}'
                | '\u{0890}'..='\u{0891}'
                | '\u{08E2}'
                | '\u{180E}'
                | '\u{200B}'
                | '\u{200E}'..='\u{200F}'
                | '\u{2028}'..='\u{202E}'
                | '\u{2060}'..='\u{206F}'
                | '\u{E000}'..='\u{F8FF}'
                | '\u{FDD0}'..='\u{FDEF}'
                | '\u{FEFF}'
                | '\u{FFF0}'..='\u{FFFF}'
                | '\u{110BD}'
                | '\u{110CD}'
                | '\u{13430}'..='\u{1343F}'
                | '\u{1BCA0}'..='\u{1BCA3}'
                | '\u{1D173}'..='\u{1D17A}'
                | '\u{E0000}'..='\u{E0FFF}'
                | '\u{F0000}'..='\u{10FFFF}'
        )
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

    /// Maps every coordinate the action targets, failing if any is outside the frame.
    pub fn points(&self, action: &Action) -> Result<Vec<(f64, f64)>, String> {
        action
            .points()
            .into_iter()
            .map(|(x, y)| self.point(x, y))
            .collect()
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
            return Err("Pointer target is outside the captured image".into());
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
        super::computer_use_capture::validate_visible(scope)?;
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
        // Some applications (Firefox before its Accessibility tree is
        // instantiated) report no focused element; pointer actions verify the
        // element under the pointer instead, so only a readable secure field
        // is refused here.
        match attr(app.0, "AXFocusedUIElement") {
            Ok(focused) => non_password(focused.0),
            Err(_) => Ok(()),
        }
    }
    /// Touches the application's Accessibility tree so lazily instantiated
    /// implementations (Firefox) start answering, then retries the focused
    /// element a few times.
    fn focused_element_warm(scope: &SessionScope) -> Result<Owned, String> {
        let mut last = Err("Cannot verify focused Accessibility element".to_string());
        for attempt in 0..4 {
            if attempt > 0 {
                if let Ok(app) = app(scope) {
                    let _ = attr(app.0, "AXWindows");
                    let _ = attr(app.0, "AXFocusedWindow");
                }
                std::thread::sleep(Duration::from_millis(120));
            }
            last = focused_element(scope);
            if last.is_ok() {
                break;
            }
        }
        last
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
            // Queueing happens while the supervisor is usually frontmost; the
            // approved window only needs to be its application's key window.
            super::super::computer_use_capture::validate_visible(&scope)?;
            let geometry = super::super::computer_use_capture::target_geometry(&scope)?;
            approved_window(&scope, &geometry)?;
            let intended = focused_element_warm(&scope)?;
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
            // A missing subrole is an ordinary field; only an explicit secure subrole is refused.
            if attr(element, "AXSubrole")
                .is_ok_and(|subrole| equals(subrole.0, "AXSecureTextField"))
            {
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
    fn window_geometry_matches(window: Ref, geometry: &WindowGeometry) -> bool {
        let (Ok(position), Ok(size)) = (attr(window, "AXPosition"), attr(window, "AXSize")) else {
            return false;
        };
        let mut point = Point::default();
        let mut dims = Size::default();
        let decoded = unsafe { AXValueGetValue(position.0, 1, (&mut point as *mut Point).cast()) }
            && unsafe { AXValueGetValue(size.0, 2, (&mut dims as *mut Size).cast()) };
        decoded
            && point.x == f64::from(geometry.x)
            && point.y == f64::from(geometry.y)
            && dims.width == f64::from(geometry.width)
            && dims.height == f64::from(geometry.height)
    }
    /// The approved window must be its application's focused window and the
    /// application's frontmost CG window. This holds whether or not the
    /// application itself is active, so background delivery stays bound to
    /// the reviewed window.
    fn approved_window(scope: &SessionScope, geometry: &WindowGeometry) -> Result<(), String> {
        let focused_window = attr(app(scope)?.0, "AXFocusedWindow")?;
        if !window_geometry_matches(focused_window.0, geometry) {
            return Err("Focused window geometry differs from the approved window".into());
        }
        // AX has no public window-ID accessor; require the approved ID to be
        // the process's frontmost window as well as matching AX geometry.
        if !super::super::computer_use_picker::target(scope)?.frontmost {
            return Err("Approved window is not the frontmost application window".into());
        }
        Ok(())
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
        approved_window(scope, geometry)
    }
    /// Hit-tests within the approved application only, so another window on
    /// top (typically the supervisor) does not fail the check: events are
    /// posted to the approved process, never to whatever is visually in front.
    fn destination(
        scope: &SessionScope,
        geometry: &WindowGeometry,
        point: Point,
    ) -> Result<(), String> {
        let application = app(scope)?;
        let mut hit = ptr::null();
        if unsafe {
            AXUIElementCopyElementAtPosition(
                application.0,
                point.x as f32,
                point.y as f32,
                &mut hit,
            )
        } != 0
        {
            return Err("Cannot verify pointer destination inside the approved application".into());
        }
        let mut hit = Owned::new(hit)?;
        let focused = attr(application.0, "AXFocusedWindow").ok();
        let mut pid = 0;
        if unsafe { AXUIElementGetPid(hit.0, &mut pid) } != 0 || pid as u32 != scope.process_id {
            return Err("Pointer destination is outside the approved process".into());
        }
        for _ in 0..16 {
            non_password(hit.0)?;
            if focused
                .as_ref()
                .is_some_and(|window| unsafe { CFEqual(hit.0, window.0) })
                || (attr(hit.0, "AXRole").is_ok_and(|role| equals(role.0, "AXWindow"))
                    && window_geometry_matches(hit.0, geometry))
            {
                return Ok(());
            }
            hit = attr(hit.0, "AXParent")?;
        }
        Err("Cannot bind pointer destination to the approved window".into())
    }
    fn bundle_identifier(scope: &SessionScope) -> Result<String, String> {
        objc2::rc::autoreleasepool(|_| unsafe {
            let app: *mut objc2::runtime::AnyObject = objc2::msg_send![objc2::class!(NSRunningApplication), runningApplicationWithProcessIdentifier: scope.process_id as i32];
            if app.is_null() {
                return Err("Approved process is no longer running".into());
            }
            let bundle: *mut objc2::runtime::AnyObject = objc2::msg_send![app, bundleIdentifier];
            if bundle.is_null() {
                return Err("Approved application has no bundle identifier".into());
            }
            let utf8: *const c_char = objc2::msg_send![bundle, UTF8String];
            if utf8.is_null() {
                return Err("Approved application has no bundle identifier".into());
            }
            Ok(std::ffi::CStr::from_ptr(utf8)
                .to_string_lossy()
                .into_owned())
        })
    }
    /// `open -g -b <bundle> <url>`: the running approved browser loads the URL
    /// (new tab per its own setting) without being brought to the foreground.
    fn open_url(
        scope: &SessionScope,
        url: &str,
        check: &dyn Fn() -> Result<(), String>,
    ) -> Result<(), String> {
        let parsed = web_url(url)?;
        let bundle = bundle_identifier(scope)?;
        if !browser_bundle(&bundle) {
            return Err(format!(
                "open_url requires the approved window to belong to a web browser (got {bundle})"
            ));
        }
        super::super::computer_use_capture::validate_visible(scope)?;
        check()?;
        let status = std::process::Command::new("/usr/bin/open")
            .arg("-g")
            .arg("-b")
            .arg(&bundle)
            .arg(parsed.as_str())
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .status()
            .map_err(|error| format!("Cannot run open: {error}"))?;
        if !status.success() {
            return Err(format!("open refused the URL for {bundle} ({status})"));
        }
        check()
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
        event: Option<Owned>,
        pid: i32,
    }
    impl Drop for Release {
        fn drop(&mut self) {
            if let Some(event) = self.event.take() {
                unsafe { CGEventPostToPid(self.pid, event.0) };
            }
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
            event: Some(up),
            pid: scope.process_id as i32,
        };
        unsafe { CGEventPostToPid(release.pid, down.0) };
        // Never authorize key-up separately: stop must not leave a synthetic key held.
        drop(release);
        check()
    }
    // Core Graphics mouse event types and button numbers.
    const MOUSE_MOVED: u32 = 5;
    const LEFT_DRAGGED: u32 = 6;
    fn mouse_types(button: MouseButton) -> (u32, u32, u32) {
        match button {
            MouseButton::Left => (1, 2, 0),
            MouseButton::Right => (3, 4, 1),
            MouseButton::Middle => (25, 26, 2),
        }
    }
    fn mouse_event(
        kind: u32,
        point: Point,
        button: u32,
        click_state: i64,
    ) -> Result<Owned, String> {
        let event =
            Owned::new(unsafe { CGEventCreateMouseEvent(ptr::null(), kind, point, button) })?;
        unsafe {
            CGEventSetFlags(event.0, 0);
            if click_state > 0 {
                // kCGMouseEventClickState: 2 and 3 make the target treat the
                // sequence as a double or triple click.
                CGEventSetIntegerValueField(event.0, 1, click_state);
            }
        }
        Ok(event)
    }
    fn physical_modifiers_released() -> Result<(), String> {
        if unsafe { CGEventSourceFlagsState(0) }
            & ((1 << 17) | (1 << 18) | (1 << 19) | (1 << 20) | (1 << 23))
            != 0
        {
            return Err("Release physical modifiers first".into());
        }
        Ok(())
    }
    fn physical_input_released() -> Result<(), String> {
        physical_modifiers_released()?;
        if (0..3).any(|button| unsafe { CGEventSourceButtonState(0, button) }) {
            return Err("Release physical modifiers and mouse buttons first".into());
        }
        Ok(())
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
        if let Action::OpenUrl { url } = action {
            return open_url(scope, url, check);
        }
        let foreground_needed = action.needs_foreground();
        let present = |scope: &SessionScope| {
            if foreground_needed {
                super::super::computer_use_capture::validate_focus(scope)
            } else {
                super::super::computer_use_capture::validate_visible(scope)
            }
        };
        present(scope)?;
        let geometry = super::super::computer_use_capture::target_geometry(scope)?;
        let display = displays()?;
        if let Some(frame) = frame {
            frame.validate_snapshot(&geometry, &display, Instant::now())?;
        }
        secure(scope)?;
        present(scope)?;
        check()?;
        if foreground_needed {
            // Keyboard events need a key window; pointer events are posted to
            // the approved process without bringing it forward.
            activate(scope)?;
        }
        // While a synthetic drag holds the left button, the session button
        // state may report it as pressed; that guard checks modifiers only.
        let guard_with = |buttons_released: bool| {
            check()?;
            if let Some(frame) = frame {
                frame.validate_snapshot(&geometry, &display, Instant::now())?;
            }
            let permissions = super::super::computer_use::computer_use_permissions();
            if !permissions.accessibility
                || !permissions.supported
                || !unsafe { CGPreflightPostEventAccess() }
            {
                return Err("Required OS permission was revoked".into());
            }
            if buttons_released {
                physical_input_released()?;
            } else {
                physical_modifiers_released()?;
            }
            if super::super::computer_use_capture::target_geometry(scope)? != geometry
                || displays()? != display
            {
                return Err("Window or display changed during input".into());
            }
            if foreground_needed {
                foreground(scope, &geometry)?;
            } else {
                approved_window(scope, &geometry)?;
            }
            secure(scope)?;
            verify_focus(action, focus)?;
            check()
        };
        let guard = || guard_with(true);
        if foreground_needed {
            // Activation is asynchronous. Fail closed if it has not settled within the permit.
            for _ in 0..10 {
                check()?;
                if foreground(scope, &geometry).is_ok() {
                    break;
                }
                std::thread::sleep(Duration::from_millis(20));
            }
        }
        guard()?;
        match action {
            Action::Activate {} => Ok(()),
            Action::Click {
                x,
                y,
                button,
                count,
                ..
            } => {
                let (x, y) = frame
                    .ok_or("Click requires a captured frame")?
                    .point(*x, *y)?;
                let point = Point { x, y };
                let click_guard = || {
                    guard()?;
                    destination(scope, &geometry, point)?;
                    check()
                };
                let (down_kind, up_kind, number) = mouse_types(button.unwrap_or(MouseButton::Left));
                let count = count.unwrap_or(1).clamp(1, 3);
                for state in 1..=i64::from(count) {
                    if state > 1 {
                        std::thread::sleep(Duration::from_millis(30));
                    }
                    let down = mouse_event(down_kind, point, number, state)?;
                    let up = mouse_event(up_kind, point, number, state)?;
                    pair(scope, down, up, &click_guard)?;
                }
                Ok(())
            }
            Action::Move { x, y } => {
                let (x, y) = frame
                    .ok_or("Pointer move requires a captured frame")?
                    .point(*x, *y)?;
                let point = Point { x, y };
                let event = mouse_event(MOUSE_MOVED, point, 0, 0)?;
                guard()?;
                destination(scope, &geometry, point)?;
                check()?;
                unsafe { CGEventPostToPid(scope.process_id as i32, event.0) };
                guard()
            }
            Action::Drag { x, y, to_x, to_y } => {
                let frame = frame.ok_or("Drag requires a captured frame")?;
                let (sx, sy) = frame.point(*x, *y)?;
                let (ex, ey) = frame.point(*to_x, *to_y)?;
                let start = Point { x: sx, y: sy };
                let end = Point { x: ex, y: ey };
                guard()?;
                destination(scope, &geometry, start)?;
                destination(scope, &geometry, end)?;
                check()?;
                let (down_kind, up_kind, number) = mouse_types(MouseButton::Left);
                let down = mouse_event(down_kind, start, number, 1)?;
                // Until the destination is reached, stop or a failed guard
                // releases the button where it was pressed, which undoes most
                // drags instead of dropping at an unreviewed position.
                let mut release = Release {
                    event: Some(mouse_event(up_kind, start, number, 1)?),
                    pid: scope.process_id as i32,
                };
                unsafe { CGEventPostToPid(release.pid, down.0) };
                const STEPS: u32 = 12;
                for step in 1..=STEPS {
                    check()?;
                    physical_modifiers_released()?;
                    let t = f64::from(step) / f64::from(STEPS);
                    let point = Point {
                        x: sx + (ex - sx) * t,
                        y: sy + (ey - sy) * t,
                    };
                    let dragged = mouse_event(LEFT_DRAGGED, point, number, 1)?;
                    unsafe { CGEventPostToPid(release.pid, dragged.0) };
                    std::thread::sleep(Duration::from_millis(12));
                }
                guard_with(false)?;
                destination(scope, &geometry, end)?;
                check()?;
                let up = mouse_event(up_kind, end, number, 1)?;
                unsafe { CGEventPostToPid(release.pid, up.0) };
                release.event = None;
                guard()
            }
            Action::Scroll {
                delta_x,
                delta_y,
                x,
                y,
            } => {
                let event = Owned::new(unsafe {
                    CGEventCreateScrollWheelEvent(
                        ptr::null(),
                        0,
                        2,
                        -*delta_y as i32,
                        -*delta_x as i32,
                    )
                })?;
                let point = match (x, y) {
                    (Some(x), Some(y)) => {
                        let (x, y) = frame
                            .ok_or("Positioned scroll requires a captured frame")?
                            .point(*x, *y)?;
                        Point { x, y }
                    }
                    _ => Point {
                        x: f64::from(geometry.x) + f64::from(geometry.width) / 2.0,
                        y: f64::from(geometry.y) + f64::from(geometry.height) / 2.0,
                    },
                };
                unsafe {
                    CGEventSetLocation(event.0, point);
                    CGEventSetFlags(event.0, 0);
                }
                guard()?;
                destination(scope, &geometry, point)?;
                check()?;
                unsafe { CGEventPostToPid(scope.process_id as i32, event.0) };
                guard()
            }
            Action::Type { text } => {
                // The full guard enumerates windows and displays and round-trips
                // through Accessibility; running it twice per character cannot
                // finish realistic text inside the permit. Every key pair still
                // re-checks cancellation, deadline, revision, permissions, physical
                // modifiers and the retained focused element; the full guard runs
                // again periodically and after the last character.
                let key_guard = || {
                    check()?;
                    if !unsafe { CGPreflightPostEventAccess() } {
                        return Err("macOS input permission is unavailable".into());
                    }
                    physical_input_released()?;
                    verify_focus(action, focus)?;
                    check()
                };
                for (index, character) in text.chars().enumerate() {
                    if index > 0 && index % 16 == 0 {
                        guard()?;
                    }
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
                    pair(scope, down, up, &key_guard)?;
                }
                guard()
            }
            Action::Key { key } => {
                let hotkey = parse_hotkey(key).ok_or("Unsupported key")?;
                let down = Owned::new(unsafe {
                    CGEventCreateKeyboardEvent(ptr::null(), hotkey.code, true)
                })?;
                let up = Owned::new(unsafe {
                    CGEventCreateKeyboardEvent(ptr::null(), hotkey.code, false)
                })?;
                unsafe {
                    CGEventSetFlags(down.0, hotkey.flags);
                    CGEventSetFlags(up.0, 0);
                    if let Some(character) = hotkey.character {
                        // Carry the ANSI character so menu key equivalents match
                        // on keyboard layouts where this position differs.
                        let mut units = [0; 2];
                        let units = character.encode_utf16(&mut units);
                        CGEventKeyboardSetUnicodeString(down.0, units.len(), units.as_ptr());
                        CGEventKeyboardSetUnicodeString(up.0, units.len(), units.as_ptr());
                    }
                }
                pair(scope, down, up, &guard)
            }
            Action::Observe { .. } => Err("Observe uses capture, not input".into()),
            Action::OpenUrl { .. } => unreachable!("handled before pointer setup"),
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
                button: None,
                count: None,
            },
            Action::Click {
                x: 1.0,
                y: 1.0,
                button: Some(MouseButton::Right),
                count: Some(4),
            },
            Action::Click {
                x: 1.0,
                y: 1.0,
                button: None,
                count: Some(0),
            },
            Action::Move { x: -1.0, y: 0.0 },
            Action::Drag {
                x: 1.0,
                y: 1.0,
                to_x: 1.0,
                to_y: 1.0,
            },
            Action::Drag {
                x: 1.0,
                y: 1.0,
                to_x: f64::INFINITY,
                to_y: 1.0,
            },
            Action::Scroll {
                delta_x: 1001.0,
                delta_y: 0.0,
                x: None,
                y: None,
            },
            Action::Scroll {
                delta_x: 0.0,
                delta_y: 10.0,
                x: Some(1.0),
                y: None,
            },
            Action::Scroll {
                delta_x: 0.0,
                delta_y: 10.0,
                x: Some(-1.0),
                y: Some(1.0),
            },
            Action::Type {
                text: "a\nb".into(),
            },
            Action::Key {
                key: "Meta+Space".into(),
            },
            Action::Key { key: "A".into() },
            Action::Key {
                key: "Cmd+Q".into(),
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
        assert!(parse_hotkey("Shift+Tab").is_some());
        for bad in [
            r#"{"requestId":"a","frameId":"f","action":{"kind":"wait","seconds":1}}"#,
            r#"{"requestId":"a","frameId":"f","action":{"kind":"click","x":1,"y":1,"count":1.5}}"#,
            r#"{"requestId":"a","frameId":"f","action":{"kind":"click","x":1,"y":1,"button":"back"}}"#,
            r#"{"requestId":"a","frameId":"f","action":{"kind":"click","x":1,"y":1,"question":"q"}}"#,
            r#"{"requestId":"a","frameId":"f","action":{"kind":"drag","x":1,"y":1,"toX":2}}"#,
        ] {
            assert!(serde_json::from_str::<QueuedRequest>(bad).is_err(), "{bad}");
        }
        for (raw, frame_required) in [
            (
                r#"{"kind":"click","x":1,"y":1,"button":"right","count":2}"#,
                true,
            ),
            (r#"{"kind":"click","x":1,"y":1,"button":"middle"}"#, true),
            (r#"{"kind":"move","x":1,"y":1}"#, true),
            (r#"{"kind":"drag","x":1,"y":1,"toX":30,"toY":40}"#, true),
            (
                r#"{"kind":"scroll","deltaX":0,"deltaY":100,"x":5,"y":6}"#,
                true,
            ),
            (r#"{"kind":"scroll","deltaX":0,"deltaY":100}"#, false),
            (r#"{"kind":"key","key":"Cmd+Shift+Z"}"#, false),
        ] {
            let with_frame: QueuedRequest = serde_json::from_str(&format!(
                r#"{{"requestId":"a","frameId":"f","action":{raw}}}"#
            ))
            .unwrap();
            assert!(with_frame.validate().is_ok(), "{raw}");
            let without_frame: QueuedRequest =
                serde_json::from_str(&format!(r#"{{"requestId":"a","action":{raw}}}"#)).unwrap();
            assert_eq!(without_frame.validate().is_err(), frame_required, "{raw}");
        }
    }

    #[test]
    fn open_url_accepts_only_web_addresses_for_browsers_and_needs_no_frame() {
        for good in [
            "https://www.google.com",
            "http://example.com/path?q=1#frag",
            "https://user-less.example:8443/a%20b",
        ] {
            assert!(web_url(good).is_ok(), "{good}");
            let request: QueuedRequest = serde_json::from_str(&format!(
                r#"{{"requestId":"a","action":{{"kind":"open_url","url":"{good}"}}}}"#
            ))
            .unwrap();
            assert!(request.validate().is_ok(), "{good}");
            assert!(!request.action.needs_foreground());
            assert!(request.action.points().is_empty());
        }
        for bad in [
            "",
            "google.com",
            "file:///etc/passwd",
            "javascript:alert(1)",
            "ftp://example.com",
            "https://",
            "https://user:pw@example.com",
            "https://user@example.com",
            "https://example.com/a b",
            "https://example.com/\n",
            &format!("https://example.com/{}", "a".repeat(2048)),
        ] {
            assert!(web_url(bad).is_err(), "{bad:?}");
            assert!(QueuedRequest {
                request_id: "a".into(),
                frame_id: None,
                action: Action::OpenUrl {
                    url: bad.to_string()
                },
            }
            .validate()
            .is_err());
        }
        assert!(serde_json::from_str::<QueuedRequest>(
            r#"{"requestId":"a","action":{"kind":"open_url","url":"https://x.y","x":1}}"#
        )
        .is_err());
        assert!(browser_bundle("org.mozilla.firefox"));
        assert!(browser_bundle("com.apple.Safari"));
        assert!(!browser_bundle("com.apple.TextEdit"));
        assert!(!browser_bundle(""));
    }

    #[test]
    fn only_keyboard_actions_bring_the_application_forward() {
        for background in [
            Action::Click {
                x: 1.0,
                y: 1.0,
                button: Some(MouseButton::Right),
                count: None,
            },
            Action::Move { x: 1.0, y: 1.0 },
            Action::Drag {
                x: 1.0,
                y: 1.0,
                to_x: 2.0,
                to_y: 2.0,
            },
            Action::Scroll {
                delta_x: 0.0,
                delta_y: 1.0,
                x: None,
                y: None,
            },
            Action::Activate {},
            Action::Observe { question: None },
            Action::OpenUrl {
                url: "https://example.com".into(),
            },
        ] {
            assert!(!background.needs_foreground());
        }
        assert!(Action::Type { text: "a".into() }.needs_foreground());
        assert!(Action::Key {
            key: "Cmd+L".into()
        }
        .needs_foreground());
    }

    #[test]
    fn hotkeys_follow_the_shared_grammar_and_deny_list() {
        for key in [
            "Enter",
            "Space",
            "Shift+Tab",
            "Shift+Enter",
            "Cmd+A",
            "Cmd+Shift+Z",
            "Shift+Cmd+z",
            "Option+ArrowLeft",
            "Ctrl+A",
            "Alt+Backspace",
            "Command+S",
            "Cmd+1",
            "Cmd+Shift+7",
        ] {
            assert!(parse_hotkey(key).is_some(), "{key}");
        }
        for key in [
            "a",
            "A",
            "1",
            "Shift+A",
            "Shift+1",
            "Cmd+Cmd+A",
            "Cmd+",
            "+A",
            "Cmd+Shift",
            "Cmd+AB",
            "Cmd+F1",
            "Cmd+,",
            "Cmd+`",
            "Meta+Space",
            "Fn+A",
            "cmd+a",
            "",
            "Cmd+Q",
            "Cmd+Shift+Q",
            "Control+Cmd+Q",
            "Cmd+W",
            "Cmd+Shift+W",
            "Cmd+H",
            "Cmd+Option+H",
            "Cmd+M",
            "Cmd+Tab",
            "Cmd+Shift+Tab",
            "Cmd+Space",
            "Cmd+Option+Escape",
            "Control+Option+Cmd+Escape",
            "Cmd+Shift+3",
            "Cmd+Shift+4",
            "Cmd+Shift+5",
            "Cmd+Shift+6",
            "Cmd+Option+D",
            "Control+Cmd+F",
            "Control+ArrowLeft",
            "Control+Shift+ArrowUp",
            "Control+Space",
        ] {
            assert!(parse_hotkey(key).is_none(), "{key}");
        }
        let redo = parse_hotkey("Shift+Cmd+z").unwrap();
        assert_eq!(redo.code, 6);
        assert_eq!(redo.flags, FLAG_SHIFT | FLAG_CMD);
        assert_eq!(redo.character, Some('Z'));
        let select_all = parse_hotkey("Cmd+A").unwrap();
        assert_eq!(
            (select_all.code, select_all.flags, select_all.character),
            (0, FLAG_CMD, Some('a'))
        );
        let back_tab = parse_hotkey("Shift+Tab").unwrap();
        assert_eq!(
            (back_tab.code, back_tab.flags, back_tab.character),
            (48, FLAG_SHIFT, None)
        );
        let word_left = parse_hotkey("Alt+ArrowLeft").unwrap();
        assert_eq!((word_left.code, word_left.flags), (123, FLAG_OPTION));
        assert_eq!(parse_hotkey("Ctrl+A").unwrap().flags, FLAG_CONTROL);
    }

    #[test]
    fn pointer_actions_map_every_target_into_the_frame() {
        let frame = Frame {
            id: "f".into(),
            geometry: WindowGeometry {
                x: 100,
                y: 200,
                width: 400,
                height: 300,
            },
            pixel_width: 800,
            pixel_height: 600,
            displays: vec![],
            created: Instant::now(),
        };
        assert_eq!(
            frame
                .points(&Action::Drag {
                    x: 0.0,
                    y: 0.0,
                    to_x: 400.0,
                    to_y: 300.0,
                })
                .unwrap(),
            vec![(100.0, 200.0), (300.0, 350.0)]
        );
        assert!(frame
            .points(&Action::Drag {
                x: 0.0,
                y: 0.0,
                to_x: 800.0,
                to_y: 0.0,
            })
            .is_err());
        assert_eq!(
            frame
                .points(&Action::Scroll {
                    delta_x: 0.0,
                    delta_y: 5.0,
                    x: Some(799.0),
                    y: Some(599.0),
                })
                .unwrap(),
            vec![(499.5, 499.5)]
        );
        assert!(frame
            .points(&Action::Scroll {
                delta_x: 0.0,
                delta_y: 5.0,
                x: None,
                y: None,
            })
            .unwrap()
            .is_empty());
        assert!(frame.points(&Action::Move { x: 800.0, y: 0.0 }).is_err());
        assert!(frame
            .points(&Action::Click {
                x: 10.0,
                y: 10.0,
                button: Some(MouseButton::Middle),
                count: Some(2),
            })
            .is_ok());
        assert!(frame
            .points(&Action::Key {
                key: "Enter".into(),
            })
            .unwrap()
            .is_empty());
    }
    #[test]
    fn proposed_text_must_render_exactly_as_typed() {
        let request = |text: &str| QueuedRequest {
            request_id: "a".into(),
            frame_id: Some("f".into()),
            action: Action::Type { text: text.into() },
        };
        for visible in [
            "plain text",
            "non-breaking\u{00A0}space",
            "family \u{1F468}\u{200D}\u{1F469}\u{200D}\u{1F467}",
            "zwnj a\u{200C}b",
            "heart \u{2764}\u{FE0F}",
        ] {
            assert!(request(visible).validate().is_ok(), "{visible:?}");
        }
        for hidden in [
            "zero\u{200B}width",
            "\u{FEFF}bom",
            "abc\u{202E}fed",
            "a\u{2066}b\u{2069}",
            "line\u{2028}separator",
            "para\u{2029}graph",
            "soft\u{00AD}hyphen",
            "private\u{E000}use",
            "tag\u{E0041}",
            "plane16\u{100000}",
            "del\u{7F}",
        ] {
            assert!(request(hidden).validate().is_err(), "{hidden:?}");
        }
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
