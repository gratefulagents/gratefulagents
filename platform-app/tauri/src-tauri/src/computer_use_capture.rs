#[derive(Clone, Debug, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WindowTarget {
    pub window_id: u32,
    pub process_id: u32,
    pub application: String,
    pub title: String,
}

#[derive(Clone, Debug, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WindowGeometry {
    // Core Graphics bounds use desktop points, not the PNG's Retina pixel coordinates.
    pub x: i32,
    pub y: i32,
    pub width: u32,
    pub height: u32,
}

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WindowCapture {
    pub geometry: WindowGeometry,
    pub pixel_width: u32,
    pub pixel_height: u32,
    pub data_url: String,
}

#[cfg(target_os = "macos")]
fn window(scope: &super::computer_use_session::SessionScope) -> Result<xcap::Window, String> {
    if scope.process_id == std::process::id() {
        return Err("The supervisor application cannot be a computer-use target".into());
    }
    let window = xcap::Window::all()
        .map_err(|error| error.to_string())?
        .into_iter()
        .find(|window| window.id().ok() == Some(scope.window_id))
        .ok_or("The selected window is no longer available")?;
    if window.pid().map_err(|error| error.to_string())? != scope.process_id
        || window.app_name().map_err(|error| error.to_string())? != scope.application
    {
        return Err("The selected window no longer belongs to the approved application".into());
    }
    Ok(window)
}

pub fn validate_target(scope: &super::computer_use_session::SessionScope) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        window(scope).map(|_| ())
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = scope;
        Err("Window capture requires macOS".into())
    }
}

pub fn validate_focus(scope: &super::computer_use_session::SessionScope) -> Result<(), String> {
    #[cfg(target_os = "macos")]
    {
        if window(scope)?
            .is_focused()
            .map_err(|error| error.to_string())?
        {
            return Ok(());
        }
        let supervisor = xcap::Window::all()
            .map_err(|error| error.to_string())?
            .into_iter()
            .find(|window| window.pid().ok() == Some(std::process::id()));
        if let Some(supervisor) = supervisor {
            if supervisor.is_focused().map_err(|error| error.to_string())? {
                return Ok(());
            }
        }
        Err("Focus left the approved application and supervisor".into())
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = scope;
        Err("Window capture requires macOS".into())
    }
}

pub fn capture(scope: &super::computer_use_session::SessionScope) -> Result<WindowCapture, String> {
    #[cfg(target_os = "macos")]
    {
        use base64::Engine;
        use std::io::Cursor;

        fn geometry(window: &xcap::Window) -> Result<WindowGeometry, String> {
            Ok(WindowGeometry {
                x: window.x().map_err(|error| error.to_string())?,
                y: window.y().map_err(|error| error.to_string())?,
                width: window.width().map_err(|error| error.to_string())?,
                height: window.height().map_err(|error| error.to_string())?,
            })
        }

        validate_focus(scope)?;
        let selected = window(scope)?;
        let before = geometry(&selected)?;
        let image = selected
            .capture_image()
            .map_err(|error| error.to_string())?;
        let after = geometry(&window(scope)?)?;
        validate_focus(scope)?;
        if before != after || image.width() == 0 || image.height() == 0 {
            return Err("The window changed during capture; request a fresh preview".into());
        }
        let pixel_width = image.width();
        let pixel_height = image.height();
        let mut png = Cursor::new(Vec::new());
        image::DynamicImage::ImageRgba8(image)
            .write_to(&mut png, image::ImageFormat::Png)
            .map_err(|error| error.to_string())?;
        validate_focus(scope)?;
        Ok(WindowCapture {
            geometry: after,
            pixel_width,
            pixel_height,
            data_url: format!(
                "data:image/png;base64,{}",
                base64::engine::general_purpose::STANDARD.encode(png.into_inner())
            ),
        })
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = scope;
        Err("Window capture requires macOS".into())
    }
}

#[tauri::command]
pub fn computer_use_windows() -> Result<Vec<WindowTarget>, String> {
    if !super::computer_use::computer_use_permissions().screen_recording {
        return Err("macOS Screen Recording permission is required to select a window".into());
    }
    #[cfg(target_os = "macos")]
    {
        Ok(xcap::Window::all()
            .map_err(|error| error.to_string())?
            .into_iter()
            .filter_map(|window| {
                let process_id = window.pid().ok()?;
                if process_id == std::process::id() {
                    return None;
                }
                Some(WindowTarget {
                    window_id: window.id().ok()?,
                    process_id,
                    application: window.app_name().ok()?,
                    title: window.title().ok()?,
                })
            })
            .collect())
    }
    #[cfg(not(target_os = "macos"))]
    {
        Err("Window capture requires macOS".into())
    }
}

#[cfg(all(test, not(target_os = "macos")))]
mod tests {
    use super::*;

    #[test]
    fn unsupported_platform_cannot_list_or_capture_windows() {
        let scope = super::super::computer_use_session::SessionScope {
            backend: "https://operator.example".into(),
            user: "u".into(),
            namespace: "default".into(),
            run: "r".into(),
            application: "TextEdit".into(),
            window_id: 1,
            process_id: 2,
        };
        assert!(computer_use_windows().is_err());
        assert!(validate_target(&scope).is_err());
        assert!(validate_focus(&scope).is_err());
        assert!(capture(&scope).is_err());
    }
}
