#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ComputerUsePermissions {
    pub supported: bool,
    pub screen_recording: bool,
    pub accessibility: bool,
}

#[cfg(target_os = "macos")]
mod macos {
    #[link(name = "CoreGraphics", kind = "framework")]
    extern "C" {
        pub fn CGPreflightScreenCaptureAccess() -> bool;
        pub fn CGRequestScreenCaptureAccess() -> bool;
    }

    #[link(name = "ApplicationServices", kind = "framework")]
    extern "C" {
        pub fn AXIsProcessTrusted() -> bool;
    }
}

#[tauri::command]
pub fn computer_use_permissions() -> ComputerUsePermissions {
    #[cfg(target_os = "macos")]
    {
        ComputerUsePermissions {
            supported: true,
            screen_recording: unsafe { macos::CGPreflightScreenCaptureAccess() },
            accessibility: unsafe { macos::AXIsProcessTrusted() },
        }
    }
    #[cfg(not(target_os = "macos"))]
    {
        ComputerUsePermissions {
            supported: false,
            screen_recording: false,
            accessibility: false,
        }
    }
}

#[derive(serde::Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ComputerUsePermission {
    ScreenRecording,
    Accessibility,
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
            ComputerUsePermission::ScreenRecording => {
                unsafe { macos::CGRequestScreenCaptureAccess() };
                "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
            }
            ComputerUsePermission::Accessibility => {
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn permission_targets_are_closed() {
        assert!(serde_json::from_str::<ComputerUsePermission>("\"screen_recording\"").is_ok());
        assert!(serde_json::from_str::<ComputerUsePermission>("\"accessibility\"").is_ok());
        assert!(serde_json::from_str::<ComputerUsePermission>("\"https://example.com\"").is_err());
    }

    #[test]
    #[cfg(not(target_os = "macos"))]
    fn unsupported_platform_never_reports_permission() {
        let status = computer_use_permissions();
        assert!(!status.supported);
        assert!(!status.screen_recording);
        assert!(!status.accessibility);
    }
}
