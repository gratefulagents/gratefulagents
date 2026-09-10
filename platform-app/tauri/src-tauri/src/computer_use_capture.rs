#[derive(Clone, Debug, serde::Serialize)]
#[serde(rename_all = "camelCase")]
pub struct WindowTarget {
    pub selection_id: String,
    pub window_id: u32,
    pub process_id: u32,
    pub application: String,
    pub title: String,
}

#[derive(Clone, Debug, PartialEq, Eq, serde::Deserialize, serde::Serialize)]
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

pub fn available_target(
    scope: &super::computer_use_session::SessionScope,
) -> Result<super::computer_use_picker::Snapshot, String> {
    if scope.mode == super::computer_use_session::SessionMode::AgentChoice {
        super::computer_use_picker::agent_target(scope)
    } else {
        super::computer_use_picker::target(scope)
    }
}

pub fn validate_target(scope: &super::computer_use_session::SessionScope) -> Result<(), String> {
    available_target(scope).map(|_| ())
}

/// The approved window still exists, belongs to the approved process, and is
/// not minimized. Capture and pointer input work for a window behind other
/// windows, so this — not focus — is the availability condition.
pub fn validate_visible(scope: &super::computer_use_session::SessionScope) -> Result<(), String> {
    validate_target(scope)
}

#[cfg(any(target_os = "macos", test))]
pub fn validate_focus(scope: &super::computer_use_session::SessionScope) -> Result<(), String> {
    if available_target(scope)?.focus_allowed {
        Ok(())
    } else {
        Err("Focus left the approved application and supervisor".into())
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
    let geometry = available_target(scope)?.geometry;
    output_dimensions(geometry.width, geometry.height)?;
    Ok(geometry)
}

pub fn capture(scope: &super::computer_use_session::SessionScope) -> Result<WindowCapture, String> {
    #[cfg(target_os = "macos")]
    {
        use super::computer_use_input::macos;
        validate_visible(scope)?;
        macos::secure(scope)?;
        let before = target_geometry(scope)?;
        let display_before = macos::displays()?;
        let (pixel_width, pixel_height) = output_dimensions(before.width, before.height)?;
        let data_url = super::computer_use_picker::capture(scope, pixel_width, pixel_height)?;
        let after = target_geometry(scope)?;
        let displays = macos::displays()?;
        validate_visible(scope)?;
        macos::secure(scope)?;
        if before != after || display_before != displays {
            return Err(
                "The window/display changed during capture; request a fresh preview".into(),
            );
        }
        validate_visible(scope)?;
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
            data_url,
        })
    }
    #[cfg(not(target_os = "macos"))]
    {
        let _ = scope;
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
            mode: super::super::computer_use_session::SessionMode::SelectedWindow,
            backend: "https://operator.example".into(),
            user: "u".into(),
            namespace: "default".into(),
            run: "r".into(),
            application: "TextEdit".into(),
            window_id: 1,
            process_id: 2,
        };
        assert!(super::super::computer_use_picker::begin().is_err());
        assert!(validate_target(&scope).is_err());
        assert!(validate_focus(&scope).is_err());
        assert!(validate_visible(&scope).is_err());
        assert!(capture(&scope).is_err());
    }
}
