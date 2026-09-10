#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ComputerUsePermissions {
    pub supported: bool,
    pub accessibility: bool,
    pub agent_screen_recording: bool,
}

#[cfg(target_os = "macos")]
mod macos {
    use std::ffi::c_void;

    #[repr(C)]
    #[allow(dead_code)]
    pub struct DictionaryCallBacks {
        version: isize,
        retain: *const c_void,
        release: *const c_void,
        copy_description: *const c_void,
        equal: *const c_void,
        hash: *const c_void,
    }

    #[link(name = "CoreFoundation", kind = "framework")]
    extern "C" {
        pub static kCFBooleanTrue: *const c_void;
        pub static kCFTypeDictionaryKeyCallBacks: DictionaryCallBacks;
        pub static kCFTypeDictionaryValueCallBacks: DictionaryCallBacks;
        pub fn CFDictionaryCreate(
            allocator: *const c_void,
            keys: *const *const c_void,
            values: *const *const c_void,
            count: isize,
            key_callbacks: *const DictionaryCallBacks,
            value_callbacks: *const DictionaryCallBacks,
        ) -> *const c_void;
        pub fn CFRelease(value: *const c_void);
    }

    #[link(name = "ApplicationServices", kind = "framework")]
    extern "C" {
        pub static kAXTrustedCheckOptionPrompt: *const c_void;
        pub fn AXIsProcessTrusted() -> bool;
        pub fn CGPreflightScreenCaptureAccess() -> bool;
        pub fn CGRequestScreenCaptureAccess() -> bool;
        pub fn AXIsProcessTrustedWithOptions(options: *const c_void) -> bool;
    }

    /// Registers this exact running binary in System Settings → Accessibility
    /// (macOS lists it, unchecked, and shows its own prompt). Without this the
    /// user must locate the binary manually, and an entry for a differently
    /// signed build is silently ignored by `AXIsProcessTrusted`.
    pub fn request_accessibility() -> bool {
        unsafe {
            let keys = [kAXTrustedCheckOptionPrompt];
            let values = [kCFBooleanTrue];
            let options = CFDictionaryCreate(
                std::ptr::null(),
                keys.as_ptr(),
                values.as_ptr(),
                1,
                &kCFTypeDictionaryKeyCallBacks,
                &kCFTypeDictionaryValueCallBacks,
            );
            if options.is_null() {
                return AXIsProcessTrusted();
            }
            let trusted = AXIsProcessTrustedWithOptions(options);
            CFRelease(options);
            trusted
        }
    }
}

#[tauri::command]
pub fn computer_use_permissions() -> ComputerUsePermissions {
    #[cfg(target_os = "macos")]
    {
        ComputerUsePermissions {
            supported: super::computer_use_picker::supported(),
            accessibility: unsafe { macos::AXIsProcessTrusted() },
            agent_screen_recording: unsafe { macos::CGPreflightScreenCaptureAccess() },
        }
    }
    #[cfg(not(target_os = "macos"))]
    {
        ComputerUsePermissions {
            supported: false,
            accessibility: false,
            agent_screen_recording: false,
        }
    }
}

pub fn require_agent_permission() -> Result<(), String> {
    if !computer_use_permissions().agent_screen_recording {
        return Err("Agent chooses windows requires separate macOS Screen Recording permission; enable it in the connection panel and reconnect".into());
    }
    Ok(())
}

#[derive(serde::Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ComputerUsePermission {
    Accessibility,
    AgentScreenRecording,
}

#[tauri::command]
pub fn computer_use_open_permission(
    app: tauri::AppHandle,
    permission: ComputerUsePermission,
) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        use tauri_plugin_opener::OpenerExt;

        let url = match permission {
            ComputerUsePermission::AgentScreenRecording => {
                unsafe {
                    macos::CGRequestScreenCaptureAccess();
                }
                "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
            }
            ComputerUsePermission::Accessibility => {
                macos::request_accessibility();
                "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
            }
        };
        app.opener()
            .open_url(url, None::<&str>)
            .map_err(|error| error.to_string())
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = (app, permission);
        Err("Computer use requires the macOS desktop app".into())
    }
}

/// Re-signed builds may need a fresh process for Accessibility grants.
#[tauri::command]
pub fn computer_use_relaunch(app: tauri::AppHandle) {
    crate::computer_use_session::stop(&app, "Desktop app relaunching");
    app.restart()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn permission_targets_are_closed() {
        assert!(serde_json::from_str::<ComputerUsePermission>("\"screen_recording\"").is_err());
        assert!(serde_json::from_str::<ComputerUsePermission>("\"accessibility\"").is_ok());
        assert!(
            serde_json::from_str::<ComputerUsePermission>("\"agent_screen_recording\"").is_ok()
        );
        assert!(serde_json::from_str::<ComputerUsePermission>("\"https://example.com\"").is_err());
    }

    #[test]
    #[cfg(not(target_os = "macos"))]
    fn unsupported_platform_never_reports_permission() {
        let status = computer_use_permissions();
        assert!(!status.supported);
        assert!(!status.accessibility);
        assert!(!status.agent_screen_recording);
        assert!(require_agent_permission().is_err());
    }
}
