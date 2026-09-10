#[cfg(target_os = "macos")]
use super::computer_use_capture::WindowGeometry;
use super::computer_use_capture::WindowTarget;
use super::computer_use_session::SessionScope;
use std::sync::Mutex;

static SELECTION: Mutex<Option<Selection>> = Mutex::new(None);

struct Selection {
    token: u64,
    target: WindowTarget,
}

impl Drop for Selection {
    fn drop(&mut self) {
        revoke(self.token);
    }
}

pub fn clear() {
    SELECTION.lock().unwrap_or_else(|e| e.into_inner()).take();
    revoke(0);
}

pub fn bind(selection_id: &str, scope: &SessionScope) -> Result<(), String> {
    let selected = SELECTION
        .lock()
        .map_err(|_| "Window sharing state unavailable")?;
    let selected = selected
        .as_ref()
        .ok_or("Select a window using the macOS picker")?;
    if selected.target.selection_id != selection_id {
        return Err("Window selection changed; grant fresh consent".into());
    }
    matches_scope(&selected.target, scope)?;
    snapshot(selected.token).map(|_| ())
}

fn matches_scope(target: &WindowTarget, scope: &SessionScope) -> Result<(), String> {
    if target.window_id != scope.window_id
        || target.process_id != scope.process_id
        || target.application != scope.application
        || scope.process_id == std::process::id()
    {
        return Err(
            "Window selection does not match the approved application/process/window".into(),
        );
    }
    Ok(())
}

#[derive(serde::Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Snapshot {
    pub window_id: u32,
    pub process_id: u32,
    pub application: String,
    #[cfg(target_os = "macos")]
    pub title: String,
    #[cfg(target_os = "macos")]
    pub geometry: WindowGeometry,
    #[cfg(target_os = "macos")]
    #[serde(deserialize_with = "lenient_bool")]
    pub frontmost: bool,
    #[cfg(any(target_os = "macos", test))]
    #[serde(deserialize_with = "lenient_bool")]
    pub focus_allowed: bool,
}

/// Accepts JSON `true`/`false` and also `1`/`0`. Objective-C boxes comparison
/// results as integers (`@(a == b)` is `numberWithInt:`), and a single such
/// slip in the native snapshot previously failed every `list_windows` with
/// "invalid type: integer `1`, expected a boolean".
fn lenient_bool<'de, D: serde::Deserializer<'de>>(deserializer: D) -> Result<bool, D::Error> {
    struct Visitor;
    impl serde::de::Visitor<'_> for Visitor {
        type Value = bool;
        fn expecting(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
            f.write_str("a boolean or 0/1")
        }
        fn visit_bool<E: serde::de::Error>(self, v: bool) -> Result<bool, E> {
            Ok(v)
        }
        fn visit_u64<E: serde::de::Error>(self, v: u64) -> Result<bool, E> {
            match v {
                0 => Ok(false),
                1 => Ok(true),
                _ => Err(E::invalid_value(serde::de::Unexpected::Unsigned(v), &self)),
            }
        }
        fn visit_i64<E: serde::de::Error>(self, v: i64) -> Result<bool, E> {
            match v {
                0 => Ok(false),
                1 => Ok(true),
                _ => Err(E::invalid_value(serde::de::Unexpected::Signed(v), &self)),
            }
        }
    }
    deserializer.deserialize_any(Visitor)
}

pub fn target(scope: &SessionScope) -> Result<Snapshot, String> {
    let selected = SELECTION
        .lock()
        .map_err(|_| "Window sharing state unavailable")?;
    let selected = selected
        .as_ref()
        .ok_or("Window sharing was revoked; select the window again")?;
    matches_scope(&selected.target, scope)?;
    let snapshot = snapshot(selected.token)?;
    if snapshot.window_id != scope.window_id
        || snapshot.process_id != scope.process_id
        || snapshot.application != scope.application
    {
        return Err("The shared window identity changed".into());
    }
    Ok(snapshot)
}

pub fn agent_windows() -> Result<Vec<WindowTarget>, String> {
    super::computer_use::require_agent_permission()?;
    #[cfg(target_os = "macos")]
    {
        let value =
            native::request(|reply, context| unsafe { native::ga_agent_windows(reply, context) })?;
        let snapshots: Vec<Snapshot> = serde_json::from_value(value).map_err(|e| e.to_string())?;
        Ok(snapshots
            .into_iter()
            .map(|s| WindowTarget {
                selection_id: String::new(),
                window_id: s.window_id,
                process_id: s.process_id,
                application: s.application,
                title: s.title,
            })
            .collect())
    }
    #[cfg(not(target_os = "macos"))]
    {
        Err("Window discovery requires macOS".into())
    }
}

pub fn agent_target(scope: &SessionScope) -> Result<Snapshot, String> {
    super::computer_use::require_agent_permission()?;
    #[cfg(target_os = "macos")]
    {
        let value = unsafe { native::ga_agent_window_snapshot(scope.window_id) };
        if value.is_null() {
            return Err("Discovered window is unavailable; list windows again".into());
        }
        let result: Result<Snapshot, _> =
            serde_json::from_slice(unsafe { std::ffi::CStr::from_ptr(value) }.to_bytes());
        unsafe {
            native::ga_window_sharing_free(value);
        }
        let snapshot = result.map_err(|e| e.to_string())?;
        if snapshot.window_id != scope.window_id
            || snapshot.process_id != scope.process_id
            || snapshot.application != scope.application
        {
            return Err("Discovered window identity changed".into());
        }
        Ok(snapshot)
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = scope;
        Err("Window discovery requires macOS".into())
    }
}

pub fn select_agent_target(scope: &SessionScope) -> Result<(), String> {
    let snapshot = agent_target(scope)?;
    #[cfg(target_os = "macos")]
    {
        let mut selected = SELECTION
            .lock()
            .map_err(|_| "Window sharing state unavailable")?;
        let token = next_token();
        if !unsafe { native::ga_agent_window_select(token, scope.window_id) } {
            return Err("Discovered window was revoked or is unavailable".into());
        }
        *selected = Some(Selection {
            token,
            target: WindowTarget {
                selection_id: String::new(),
                window_id: snapshot.window_id,
                process_id: snapshot.process_id,
                application: snapshot.application,
                title: snapshot.title,
            },
        });
        Ok(())
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = snapshot;
        Err("Window selection requires macOS".into())
    }
}

#[cfg(target_os = "macos")]
fn next_token() -> u64 {
    use std::sync::atomic::{AtomicU64, Ordering};
    static NEXT: AtomicU64 = AtomicU64::new(1);
    NEXT.fetch_add(1, Ordering::SeqCst)
}

#[cfg(target_os = "macos")]
pub fn supported() -> bool {
    unsafe { native::ga_window_sharing_supported() }
}

fn revoke(token: u64) {
    #[cfg(target_os = "macos")]
    unsafe {
        native::ga_window_sharing_revoke(token)
    };
    #[cfg(not(target_os = "macos"))]
    let _ = token;
}

fn snapshot(token: u64) -> Result<Snapshot, String> {
    #[cfg(target_os = "macos")]
    unsafe {
        let value = native::ga_window_sharing_snapshot(token);
        if value.is_null() {
            return Err("Window sharing was revoked or the window is unavailable".into());
        }
        let result = serde_json::from_slice(std::ffi::CStr::from_ptr(value).to_bytes())
            .map_err(|e| e.to_string());
        native::ga_window_sharing_free(value);
        result
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = token;
        Err("Computer use requires macOS 15.2 or later".into())
    }
}

pub struct Picked {
    token: u64,
    pub target: Option<WindowTarget>,
}
impl Drop for Picked {
    fn drop(&mut self) {
        if self.token != 0 {
            revoke(self.token);
        }
    }
}
impl Picked {
    pub fn install(mut self) -> Option<WindowTarget> {
        let target = self.target.take()?;
        *SELECTION.lock().unwrap_or_else(|e| e.into_inner()) = Some(Selection {
            token: self.token,
            target: target.clone(),
        });
        self.token = 0;
        Some(target)
    }
}

pub struct Picking {
    picked: Picked,
    #[cfg(target_os = "macos")]
    response: std::sync::mpsc::Receiver<Result<serde_json::Value, String>>,
}

pub fn begin() -> Result<Picking, String> {
    #[cfg(target_os = "macos")]
    {
        let token = next_token();
        let response = native::begin(|reply, context| unsafe {
            native::ga_window_sharing_pick(token, reply, context)
        });
        Ok(Picking {
            picked: Picked {
                token,
                target: None,
            },
            response,
        })
    }
    #[cfg(not(target_os = "macos"))]
    {
        Err("Computer use requires macOS 15.2 or later".into())
    }
}

impl Picking {
    pub fn wait(self) -> Result<Picked, String> {
        #[cfg(target_os = "macos")]
        {
            let mut picked = self.picked;
            let value = native::wait(self.response)?;
            if !value.is_null() {
                let snapshot: Snapshot =
                    serde_json::from_value(value).map_err(|e| e.to_string())?;
                picked.target = Some(WindowTarget {
                    selection_id: super::computer_use_session::random_id()?,
                    window_id: snapshot.window_id,
                    process_id: snapshot.process_id,
                    application: snapshot.application,
                    title: snapshot.title,
                });
            }
            Ok(picked)
        }
        #[cfg(not(target_os = "macos"))]
        {
            let _ = self.picked;
            Err("Computer use requires macOS 15.2 or later".into())
        }
    }
}

#[cfg(target_os = "macos")]
pub fn capture(scope: &SessionScope, width: u32, height: u32) -> Result<String, String> {
    let token = {
        let selected = SELECTION
            .lock()
            .map_err(|_| "Window sharing state unavailable")?;
        let selected = selected.as_ref().ok_or("Window sharing was revoked")?;
        matches_scope(&selected.target, scope)?;
        selected.token
    };
    let value = native::request(|reply, context| unsafe {
        native::ga_window_sharing_capture(token, width, height, reply, context)
    })?;
    snapshot(token)?;
    value["dataUrl"]
        .as_str()
        .map(str::to_owned)
        .ok_or_else(|| "No window capture received".into())
}

#[cfg(any(target_os = "macos", test))]
mod native {
    use std::ffi::{c_char, c_void, CStr};
    use std::sync::mpsc;
    pub type Reply = extern "C" fn(*mut c_void, *const c_char);
    #[cfg(target_os = "macos")]
    extern "C" {
        pub fn ga_window_sharing_supported() -> bool;
        pub fn ga_agent_windows(reply: Reply, context: *mut c_void);
        pub fn ga_agent_window_snapshot(window_id: u32) -> *mut c_char;
        pub fn ga_agent_window_select(token: u64, window_id: u32) -> bool;
        pub fn ga_window_sharing_revoke(token: u64);
        pub fn ga_window_sharing_pick(token: u64, reply: Reply, context: *mut c_void);
        pub fn ga_window_sharing_snapshot(token: u64) -> *mut c_char;
        pub fn ga_window_sharing_free(value: *mut c_char);
        pub fn ga_window_sharing_capture(
            token: u64,
            width: u32,
            height: u32,
            reply: Reply,
            context: *mut c_void,
        );
    }
    extern "C" fn receive(context: *mut c_void, json: *const c_char) {
        // Native GACompletion invokes this exactly once, including cancellation and timeout.
        let sender = unsafe {
            Box::from_raw(context.cast::<mpsc::SyncSender<Result<serde_json::Value, String>>>())
        };
        let result = if json.is_null() {
            Err("Invalid native sharing response".into())
        } else {
            serde_json::from_slice(unsafe { CStr::from_ptr(json) }.to_bytes())
                .map_err(|e| e.to_string())
        };
        let _ = sender.send(result);
    }
    pub fn begin(
        start: impl FnOnce(Reply, *mut c_void),
    ) -> mpsc::Receiver<Result<serde_json::Value, String>> {
        let (sender, receiver) = mpsc::sync_channel::<Result<serde_json::Value, String>>(1);
        start(receive, Box::into_raw(Box::new(sender)).cast());
        receiver
    }
    pub fn request(start: impl FnOnce(Reply, *mut c_void)) -> Result<serde_json::Value, String> {
        wait(begin(start))
    }
    pub fn wait(
        receiver: mpsc::Receiver<Result<serde_json::Value, String>>,
    ) -> Result<serde_json::Value, String> {
        let value = receiver
            .recv()
            .map_err(|_| "Native sharing response unavailable")??;
        if let Some(error) = value.get("error").and_then(serde_json::Value::as_str) {
            return Err(error.into());
        }
        Ok(value)
    }

    #[cfg(test)]
    mod tests {
        use super::*;
        use std::ffi::CString;

        fn response(json: &str) -> Result<serde_json::Value, String> {
            request(|reply, context| reply(context, CString::new(json).unwrap().as_ptr()))
        }

        #[test]
        fn callback_preserves_cancellation_and_native_errors() {
            assert_eq!(response("null").unwrap(), serde_json::Value::Null);
            assert_eq!(
                response(r#"{"error":"Sharing revoked"}"#).unwrap_err(),
                "Sharing revoked"
            );
            assert!(response("not json").is_err());
            assert!(request(|reply, context| reply(context, std::ptr::null())).is_err());
        }

        #[test]
        fn callback_copies_json_before_native_storage_is_released() {
            let receiver = begin(|reply, context| {
                let json = CString::new(r#"{"windowId":42}"#).unwrap();
                reply(context, json.as_ptr());
            });
            assert_eq!(wait(receiver).unwrap()["windowId"], 42);
        }

        #[test]
        fn a_dropped_request_can_receive_a_late_callback_safely() {
            let mut callback = None;
            let receiver = begin(|reply, context| callback = Some((reply, context)));
            drop(receiver);
            let (reply, context) = callback.unwrap();
            reply(context, c"null".as_ptr());
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn snapshot_accepts_boolean_and_integer_flags() {
        for (raw, expected) in [
            (r#"{"windowId":1,"processId":2,"application":"A","title":"t","geometry":{"x":0,"y":0,"width":10,"height":10},"frontmost":true,"focusAllowed":false}"#, (true, false)),
            (r#"{"windowId":1,"processId":2,"application":"A","title":"t","geometry":{"x":0,"y":0,"width":10,"height":10},"frontmost":1,"focusAllowed":0}"#, (true, false)),
        ] {
            let snapshot: Snapshot = serde_json::from_str(raw).expect(raw);
            assert_eq!(snapshot.focus_allowed, expected.1);
            #[cfg(target_os = "macos")]
            assert_eq!(snapshot.frontmost, expected.0);
            #[cfg(not(target_os = "macos"))]
            let _ = expected.0;
        }
        let rejected = r#"{"windowId":1,"processId":2,"application":"A","title":"t","geometry":{"x":0,"y":0,"width":10,"height":10},"frontmost":true,"focusAllowed":2}"#;
        assert!(serde_json::from_str::<Snapshot>(rejected).is_err());
    }

    #[test]
    fn picker_identity_binds_every_target_field_and_excludes_supervisor() {
        let target = WindowTarget {
            selection_id: "s".into(),
            window_id: 42,
            process_id: 99,
            application: "TextEdit".into(),
            title: "Notes".into(),
        };
        let scope = SessionScope {
            mode: super::super::computer_use_session::SessionMode::SelectedWindow,
            backend: "https://example.com".into(),
            user: "u".into(),
            namespace: "n".into(),
            run: "r".into(),
            application: target.application.clone(),
            window_id: 42,
            process_id: 99,
        };
        assert!(matches_scope(&target, &scope).is_ok());
        #[cfg(not(target_os = "macos"))]
        {
            assert!(agent_windows().is_err());
            assert!(agent_target(&scope).is_err());
            assert!(select_agent_target(&scope).is_err());
        }
        for field in 0..4 {
            let mut changed = scope.clone();
            match field {
                0 => changed.window_id += 1,
                1 => changed.process_id += 1,
                2 => changed.application = "Other".into(),
                _ => changed.process_id = std::process::id(),
            }
            assert!(matches_scope(&target, &changed).is_err());
        }
    }
}
