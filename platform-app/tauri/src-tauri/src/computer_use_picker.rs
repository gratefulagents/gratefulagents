use super::computer_use_capture::{DisplayBounds, DisplayTarget};
use super::computer_use_session::SessionScope;
use std::sync::Mutex;
static SELECTION: Mutex<Option<Selection>> = Mutex::new(None);
struct Selection {
    token: u64,
    target: DisplayTarget,
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
fn revoke(token: u64) {
    #[cfg(target_os = "macos")]
    unsafe {
        native::ga_display_revoke(token);
    }
    #[cfg(not(target_os = "macos"))]
    let _ = token;
}
#[cfg(target_os = "macos")]
pub fn supported() -> bool {
    unsafe { native::ga_display_supported() }
}
pub fn bind(selection_id: &str, scope: &SessionScope) -> Result<(), String> {
    {
        let selection = SELECTION
            .lock()
            .map_err(|_| "Display selection unavailable")?;
        let selection = selection
            .as_ref()
            .ok_or("Select a display and grant fresh desktop consent")?;
        if selection.target.selection_id != selection_id
            || selection.target.display_id != scope.display_id
        {
            return Err("Display selection changed; reconnect with fresh desktop consent".into());
        }
    }
    target(scope).map(|_| ())
}
pub fn target(scope: &SessionScope) -> Result<DisplayBounds, String> {
    #[cfg(target_os = "macos")]
    {
        super::computer_use::require_screen_permission()?;
        let selected = SELECTION
            .lock()
            .map_err(|_| "Display selection unavailable")?;
        let selected = selected.as_ref().ok_or("Display sharing was revoked")?;
        if selected.target.display_id != scope.display_id
            || !unsafe { native::ga_display_valid(selected.token, scope.display_id) }
        {
            return Err("Selected display disconnected or changed identity; reconnect".into());
        }
        super::computer_use_input::macos::displays()?
            .into_iter()
            .find(|d| d.id == scope.display_id)
            .map(|d| d.bounds)
            .ok_or_else(|| "Selected display disconnected".into())
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = scope;
        Err("Display capture requires macOS".into())
    }
}
pub struct Picked {
    token: u64,
    target: Option<DisplayTarget>,
}
impl Drop for Picked {
    fn drop(&mut self) {
        if self.token != 0 {
            revoke(self.token);
        }
    }
}
impl Picked {
    pub fn install(mut self) -> Option<DisplayTarget> {
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
        use std::sync::atomic::{AtomicU64, Ordering};
        static NEXT: AtomicU64 = AtomicU64::new(1);
        let token = NEXT.fetch_add(1, Ordering::SeqCst);
        let response = native::begin(|reply, context| unsafe {
            native::ga_display_pick(token, reply, context)
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
        Err("Display selection requires macOS".into())
    }
}
impl Picking {
    pub fn wait(self) -> Result<Picked, String> {
        #[cfg(target_os = "macos")]
        {
            let mut picked = self.picked;
            let value = native::wait(self.response)?;
            if !value.is_null() {
                picked.target = Some(DisplayTarget {
                    selection_id: super::computer_use_session::random_id()?,
                    display_id: value["displayId"].as_u64().ok_or("No display ID")? as u32,
                    name: value["name"].as_str().ok_or("No display name")?.into(),
                });
            }
            Ok(picked)
        }
        #[cfg(not(target_os = "macos"))]
        {
            let _ = self.picked;
            Err("Display selection requires macOS".into())
        }
    }
}
#[cfg(target_os = "macos")]
pub fn capture(scope: &SessionScope, width: u32, height: u32) -> Result<String, String> {
    target(scope)?;
    let token = SELECTION
        .lock()
        .map_err(|_| "Display selection unavailable")?
        .as_ref()
        .ok_or("Display sharing revoked")?
        .token;
    let value = native::request(|reply, context| unsafe {
        native::ga_display_capture(token, scope.display_id, width, height, reply, context)
    })?;
    target(scope)?;
    value["dataUrl"]
        .as_str()
        .map(str::to_owned)
        .ok_or_else(|| "No display capture received".into())
}

#[cfg(any(target_os = "macos", test))]
mod native {
    use std::ffi::{c_char, c_void, CStr};
    use std::sync::mpsc;
    pub type Reply = extern "C" fn(*mut c_void, *const c_char);
    #[cfg(target_os = "macos")]
    extern "C" {
        pub fn ga_display_supported() -> bool;
        pub fn ga_display_revoke(token: u64);
        pub fn ga_display_pick(token: u64, reply: Reply, context: *mut c_void);
        pub fn ga_display_valid(token: u64, display_id: u32) -> bool;
        pub fn ga_display_capture(
            token: u64,
            display_id: u32,
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
                let json = CString::new(r#"{"displayId":42}"#).unwrap();
                reply(context, json.as_ptr());
            });
            assert_eq!(wait(receiver).unwrap()["displayId"], 42);
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
