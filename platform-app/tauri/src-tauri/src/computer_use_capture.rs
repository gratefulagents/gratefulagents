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
    pub frame_id: String,
    #[serde(skip)]
    pub displays: Vec<super::computer_use_input::DisplayGeometry>,
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

#[cfg(any(target_os = "macos", test))]
pub fn output_dimensions(width: u32, height: u32) -> Result<(u32, u32), String> {
    if width == 0
        || height == 0
        || width > 8192
        || height > 8192
        || u64::from(width) * u64::from(height) > 16_777_216
    {
        return Err("Window is too large or empty for bounded capture".into());
    }
    let scale = (1920.0 / f64::from(width))
        .min(1080.0 / f64::from(height))
        .min(1.0);
    Ok((
        (f64::from(width) * scale).floor().max(1.0) as u32,
        (f64::from(height) * scale).floor().max(1.0) as u32,
    ))
}

#[cfg(target_os = "macos")]
pub fn target_geometry(
    scope: &super::computer_use_session::SessionScope,
) -> Result<WindowGeometry, String> {
    let selected = window(scope)?;
    let geometry = WindowGeometry {
        x: selected.x().map_err(|e| e.to_string())?,
        y: selected.y().map_err(|e| e.to_string())?,
        width: selected.width().map_err(|e| e.to_string())?,
        height: selected.height().map_err(|e| e.to_string())?,
    };
    output_dimensions(geometry.width, geometry.height)?;
    Ok(geometry)
}

pub fn capture(scope: &super::computer_use_session::SessionScope) -> Result<WindowCapture, String> {
    #[cfg(target_os = "macos")]
    {
        use base64::Engine;
        use std::io::Cursor;

        use super::computer_use_input::macos;
        validate_focus(scope)?;
        macos::secure(scope)?;
        let before = target_geometry(scope)?;
        let display_before = macos::displays()?;
        let image = macos::capture_image(scope, &before)?;
        let after = target_geometry(scope)?;
        let displays = macos::displays()?;
        validate_focus(scope)?;
        macos::secure(scope)?;
        if before != after || display_before != displays {
            return Err(
                "The window/display changed during capture; request a fresh preview".into(),
            );
        }
        let pixel_width = image.width();
        let pixel_height = image.height();
        let mut png = Cursor::new(Vec::new());
        image::DynamicImage::ImageRgba8(image)
            .write_to(&mut png, image::ImageFormat::Png)
            .map_err(|error| error.to_string())?;
        validate_focus(scope)?;
        macos::secure(scope)?;
        if target_geometry(scope)? != after || macos::displays()? != displays {
            return Err("Window/display changed before capture delivery".into());
        }
        Ok(WindowCapture {
            frame_id: super::computer_use_session::random_id()?,
            displays,
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
    fn capture_wire_shape_excludes_native_display_metadata() {
        let capture = WindowCapture {
            frame_id: "frame".into(),
            geometry: WindowGeometry {
                x: -1,
                y: 2,
                width: 3,
                height: 4,
            },
            pixel_width: 3,
            pixel_height: 4,
            data_url: "data:image/png;base64,".into(),
            displays: vec![],
        };
        let json = serde_json::to_value(capture).unwrap();
        assert_eq!(json["frameId"], "frame");
        assert_eq!(json["pixelWidth"], 3);
        assert!(json.get("displays").is_none());
        assert_eq!(json.as_object().unwrap().len(), 5);
    }

    #[test]
    fn bounded_capture_dimensions() {
        assert_eq!(output_dimensions(3840, 2160).unwrap(), (1920, 1080));
        assert_eq!(output_dimensions(100, 100).unwrap(), (100, 100));
        assert!(output_dimensions(0, 100).is_err());
        assert!(output_dimensions(u32::MAX, 2).is_err());
        assert!(output_dimensions(8192, 8192).is_err());
    }

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
