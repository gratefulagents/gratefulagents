//! The `computer_use` tool action (protocol v2, section 1), parsed and validated.

use serde::Deserialize;

use super::geometry::{Mapping, Point, Region};
use super::keys::{Chord, Modifiers};

const MAX_TYPE_CHARS: usize = 4000;
const MAX_URL_LEN: usize = 2048;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Button {
    Left,
    Right,
    Middle,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ScrollDirection {
    Up,
    Down,
    Left,
    Right,
}

#[derive(Clone, Debug, PartialEq)]
pub enum Action {
    Screenshot,
    Click {
        button: Button,
        count: u8,
        at: Point,
        modifiers: Modifiers,
    },
    MouseMove {
        at: Point,
    },
    Drag {
        from: Point,
        to: Point,
        modifiers: Modifiers,
    },
    MouseDown {
        at: Option<Point>,
    },
    MouseUp {
        at: Option<Point>,
    },
    Scroll {
        direction: ScrollDirection,
        amount: u32,
        at: Option<Point>,
        modifiers: Modifiers,
    },
    Type {
        text: String,
    },
    Key {
        chord: Chord,
        repeat: u32,
    },
    CursorPosition,
    Zoom {
        region: Region,
    },
    OpenUrl {
        url: String,
    },
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Raw {
    action: String,
    coordinate: Option<[i64; 2]>,
    start_coordinate: Option<[i64; 2]>,
    text: Option<String>,
    scroll_direction: Option<String>,
    scroll_amount: Option<i64>,
    repeat: Option<i64>,
    duration: Option<f64>,
    region: Option<[i64; 4]>,
    url: Option<String>,
}

impl Raw {
    fn present(&self) -> Vec<&'static str> {
        let mut fields = Vec::new();
        let mut add = |name, set: bool| {
            if set {
                fields.push(name)
            }
        };
        add("coordinate", self.coordinate.is_some());
        add("start_coordinate", self.start_coordinate.is_some());
        add("text", self.text.is_some());
        add("scroll_direction", self.scroll_direction.is_some());
        add("scroll_amount", self.scroll_amount.is_some());
        add("repeat", self.repeat.is_some());
        add("duration", self.duration.is_some());
        add("region", self.region.is_some());
        add("url", self.url.is_some());
        fields
    }
}

fn point([x, y]: [i64; 2]) -> Point {
    Point { x, y }
}

fn required<T>(value: Option<T>, field: &str, action: &str) -> Result<T, String> {
    value.ok_or_else(|| format!("`{action}` requires `{field}`"))
}

impl Action {
    pub fn parse(value: serde_json::Value) -> Result<Action, String> {
        let raw: Raw = serde_json::from_value(value).map_err(|e| format!("invalid action: {e}"))?;
        let name = raw.action.as_str();
        let allowed: &[&str] = match name {
            "screenshot" | "cursor_position" => &[],
            "left_click" | "right_click" | "middle_click" | "double_click" | "triple_click" => {
                &["coordinate", "text"]
            }
            "mouse_move" => &["coordinate"],
            "left_click_drag" => &["start_coordinate", "coordinate", "text"],
            "left_mouse_down" | "left_mouse_up" => &["coordinate"],
            "scroll" => &["scroll_direction", "scroll_amount", "coordinate", "text"],
            "type" => &["text"],
            "key" => &["text", "repeat"],
            "zoom" => &["region"],
            "open_url" => &["url"],
            "wait" => return Err("`wait` is executed by the agent tool, not the desktop".into()),
            _ => return Err(format!("unknown action `{name}`")),
        };
        if let Some(field) = raw.present().into_iter().find(|f| !allowed.contains(f)) {
            return Err(format!("`{field}` is not allowed for `{name}`"));
        }
        let modifiers = || Modifiers::parse(raw.text.as_deref().unwrap_or(""));

        Ok(match name {
            "screenshot" => Action::Screenshot,
            "cursor_position" => Action::CursorPosition,
            "left_click" | "right_click" | "middle_click" | "double_click" | "triple_click" => {
                let (button, count) = match name {
                    "left_click" => (Button::Left, 1),
                    "right_click" => (Button::Right, 1),
                    "middle_click" => (Button::Middle, 1),
                    "double_click" => (Button::Left, 2),
                    _ => (Button::Left, 3),
                };
                Action::Click {
                    button,
                    count,
                    at: point(required(raw.coordinate, "coordinate", name)?),
                    modifiers: modifiers()?,
                }
            }
            "mouse_move" => Action::MouseMove {
                at: point(required(raw.coordinate, "coordinate", name)?),
            },
            "left_click_drag" => Action::Drag {
                from: point(required(raw.start_coordinate, "start_coordinate", name)?),
                to: point(required(raw.coordinate, "coordinate", name)?),
                modifiers: modifiers()?,
            },
            "left_mouse_down" => Action::MouseDown {
                at: raw.coordinate.map(point),
            },
            "left_mouse_up" => Action::MouseUp {
                at: raw.coordinate.map(point),
            },
            "scroll" => {
                let direction =
                    match required(raw.scroll_direction.as_deref(), "scroll_direction", name)? {
                        "up" => ScrollDirection::Up,
                        "down" => ScrollDirection::Down,
                        "left" => ScrollDirection::Left,
                        "right" => ScrollDirection::Right,
                        other => {
                            return Err(format!(
                                "`scroll_direction` must be up, down, left or right, not `{other}`"
                            ))
                        }
                    };
                let amount = raw.scroll_amount.unwrap_or(3);
                if !(1..=30).contains(&amount) {
                    return Err("`scroll_amount` must be between 1 and 30".into());
                }
                Action::Scroll {
                    direction,
                    amount: amount as u32,
                    at: raw.coordinate.map(point),
                    modifiers: modifiers()?,
                }
            }
            "type" => {
                let text = required(raw.text, "text", name)?;
                let chars = text.chars().count();
                if chars == 0 || chars > MAX_TYPE_CHARS {
                    return Err(format!("`text` must be 1–{MAX_TYPE_CHARS} characters"));
                }
                Action::Type { text }
            }
            "key" => {
                let chord = Chord::parse(&required(raw.text, "text", name)?)?;
                let repeat = raw.repeat.unwrap_or(1);
                if !(1..=50).contains(&repeat) {
                    return Err("`repeat` must be between 1 and 50".into());
                }
                Action::Key {
                    chord,
                    repeat: repeat as u32,
                }
            }
            "zoom" => {
                let [x0, y0, x1, y1] = required(raw.region, "region", name)?;
                if x1 <= x0 || y1 <= y0 {
                    return Err("`region` must be [x0, y0, x1, y1] with x1 > x0 and y1 > y0".into());
                }
                Action::Zoom {
                    region: Region { x0, y0, x1, y1 },
                }
            }
            _ => Action::OpenUrl {
                url: validate_url(&required(raw.url, "url", name)?)?,
            },
        })
    }

    pub fn name(&self) -> &'static str {
        match self {
            Action::Screenshot => "screenshot",
            Action::Click {
                button: Button::Right,
                ..
            } => "right_click",
            Action::Click {
                button: Button::Middle,
                ..
            } => "middle_click",
            Action::Click { count: 2, .. } => "double_click",
            Action::Click { count: 3, .. } => "triple_click",
            Action::Click { .. } => "left_click",
            Action::MouseMove { .. } => "mouse_move",
            Action::Drag { .. } => "left_click_drag",
            Action::MouseDown { .. } => "left_mouse_down",
            Action::MouseUp { .. } => "left_mouse_up",
            Action::Scroll { .. } => "scroll",
            Action::Type { .. } => "type",
            Action::Key { .. } => "key",
            Action::CursorPosition => "cursor_position",
            Action::Zoom { .. } => "zoom",
            Action::OpenUrl { .. } => "open_url",
        }
    }

    /// Whether the action injects input (refused while paused).
    pub fn is_input(&self) -> bool {
        !matches!(
            self,
            Action::Screenshot | Action::CursorPosition | Action::Zoom { .. }
        )
    }

    /// Screenshot-pixel coordinates the action targets.
    pub fn points(&self) -> Vec<Point> {
        match self {
            Action::Click { at, .. } | Action::MouseMove { at } => vec![*at],
            Action::Drag { from, to, .. } => vec![*from, *to],
            Action::MouseDown { at } | Action::MouseUp { at } | Action::Scroll { at, .. } => {
                at.iter().copied().collect()
            }
            _ => Vec::new(),
        }
    }

    pub fn check_bounds(&self, mapping: &Mapping) -> Result<(), String> {
        for point in self.points() {
            mapping.check(point)?;
        }
        if let Action::Zoom { region } = self {
            region.check(mapping.frame_width, mapping.frame_height)?;
        }
        Ok(())
    }
}

fn validate_url(text: &str) -> Result<String, String> {
    if text.len() > MAX_URL_LEN {
        return Err(format!("`url` must be at most {MAX_URL_LEN} characters"));
    }
    let url = url::Url::parse(text).map_err(|e| format!("invalid `url`: {e}"))?;
    if !matches!(url.scheme(), "http" | "https") || url.host_str().is_none_or(str::is_empty) {
        return Err("`url` must be an absolute http or https URL".into());
    }
    if !url.username().is_empty() || url.password().is_some() {
        return Err("`url` must not contain credentials".into());
    }
    Ok(url.to_string())
}

#[cfg(test)]
mod tests {
    use super::super::geometry::Rect;
    use super::super::keys::Modifier;
    use super::*;
    use serde_json::json;

    fn parse(value: serde_json::Value) -> Result<Action, String> {
        Action::parse(value)
    }

    #[test]
    fn parses_every_action() {
        assert_eq!(
            parse(json!({"action": "screenshot"})),
            Ok(Action::Screenshot)
        );
        assert_eq!(
            parse(json!({"action": "cursor_position"})),
            Ok(Action::CursorPosition)
        );
        let Action::Click {
            button,
            count,
            at,
            modifiers,
        } = parse(json!({"action": "triple_click", "coordinate": [5, 6], "text": "cmd+shift"}))
            .unwrap()
        else {
            panic!("expected click");
        };
        assert_eq!((button, count, at), (Button::Left, 3, Point { x: 5, y: 6 }));
        assert!(modifiers.contains(Modifier::Cmd) && modifiers.contains(Modifier::Shift));
        for (name, button, count) in [
            ("left_click", Button::Left, 1),
            ("right_click", Button::Right, 1),
            ("middle_click", Button::Middle, 1),
            ("double_click", Button::Left, 2),
        ] {
            let action = parse(json!({"action": name, "coordinate": [1, 2]})).unwrap();
            assert_eq!(action.name(), name);
            assert!(
                matches!(action, Action::Click { button: b, count: c, .. } if b == button && c == count)
            );
        }
        assert_eq!(
            parse(json!({"action": "mouse_move", "coordinate": [1, 2]})),
            Ok(Action::MouseMove {
                at: Point { x: 1, y: 2 }
            })
        );
        assert!(matches!(
            parse(
                json!({"action": "left_click_drag", "start_coordinate": [1, 2], "coordinate": [3, 4]})
            ),
            Ok(Action::Drag {
                from: Point { x: 1, y: 2 },
                to: Point { x: 3, y: 4 },
                ..
            })
        ));
        assert_eq!(
            parse(json!({"action": "left_mouse_down"})),
            Ok(Action::MouseDown { at: None })
        );
        assert_eq!(
            parse(json!({"action": "left_mouse_up", "coordinate": [7, 8]})),
            Ok(Action::MouseUp {
                at: Some(Point { x: 7, y: 8 })
            })
        );
        assert!(matches!(
            parse(json!({"action": "scroll", "scroll_direction": "down"})),
            Ok(Action::Scroll {
                direction: ScrollDirection::Down,
                amount: 3,
                at: None,
                ..
            })
        ));
        assert!(matches!(
            parse(
                json!({"action": "scroll", "scroll_direction": "left", "scroll_amount": 30, "coordinate": [1, 1], "text": "shift"})
            ),
            Ok(Action::Scroll {
                direction: ScrollDirection::Left,
                amount: 30,
                at: Some(_),
                ..
            })
        ));
        assert_eq!(
            parse(json!({"action": "type", "text": "héllo\n"})),
            Ok(Action::Type {
                text: "héllo\n".into()
            })
        );
        assert!(matches!(
            parse(json!({"action": "key", "text": "cmd+c", "repeat": 3})),
            Ok(Action::Key { repeat: 3, chord }) if chord.key == 8
        ));
        assert_eq!(
            parse(json!({"action": "zoom", "region": [0, 0, 10, 10]})),
            Ok(Action::Zoom {
                region: Region {
                    x0: 0,
                    y0: 0,
                    x1: 10,
                    y1: 10
                }
            })
        );
        assert_eq!(
            parse(json!({"action": "open_url", "url": "https://example.com/a?b=c"})),
            Ok(Action::OpenUrl {
                url: "https://example.com/a?b=c".into()
            })
        );
    }

    #[test]
    fn rejects_fields_that_do_not_belong() {
        for value in [
            json!({"action": "screenshot", "coordinate": [1, 1]}),
            json!({"action": "type", "text": "x", "coordinate": [1, 1]}),
            json!({"action": "mouse_move", "coordinate": [1, 1], "text": "shift"}),
            json!({"action": "key", "text": "a", "scroll_amount": 2}),
            json!({"action": "zoom", "region": [0, 0, 1, 1], "url": "https://a.b"}),
            json!({"action": "left_click", "coordinate": [1, 1], "repeat": 2}),
        ] {
            let error = parse(value.clone()).expect_err(&value.to_string());
            assert!(error.contains("not allowed"), "{value}: {error}");
        }
        let error = parse(json!({"action": "screenshot", "bogus": 1})).unwrap_err();
        assert!(error.contains("unknown field"), "{error}");
    }

    #[test]
    fn rejects_invalid_values() {
        for value in [
            json!({}),
            json!({"action": "teleport"}),
            json!({"action": "wait", "duration": 1}),
            json!({"action": "left_click"}),
            json!({"action": "left_click", "coordinate": [1]}),
            json!({"action": "left_click", "coordinate": [1.5, 2]}),
            json!({"action": "left_click", "coordinate": [1, 2], "text": "cmd+c"}),
            json!({"action": "left_click_drag", "coordinate": [1, 2]}),
            json!({"action": "scroll"}),
            json!({"action": "scroll", "scroll_direction": "sideways"}),
            json!({"action": "scroll", "scroll_direction": "up", "scroll_amount": 0}),
            json!({"action": "scroll", "scroll_direction": "up", "scroll_amount": 31}),
            json!({"action": "type"}),
            json!({"action": "type", "text": ""}),
            json!({"action": "type", "text": "a".repeat(4001)}),
            json!({"action": "key", "text": "ctrl+alt+cmd+Escape"}),
            json!({"action": "key", "text": "cmd+alt+Escape"}),
            json!({"action": "key", "text": "a", "repeat": 0}),
            json!({"action": "key", "text": "a", "repeat": 51}),
            json!({"action": "zoom", "region": [10, 0, 10, 5]}),
            json!({"action": "zoom", "region": [0, 5, 10, 4]}),
            json!({"action": "open_url", "url": "file:///etc/passwd"}),
            json!({"action": "open_url", "url": "https://user:pw@example.com"}),
            json!({"action": "open_url", "url": "example.com"}),
            json!({"action": "open_url", "url": format!("https://e.com/{}", "a".repeat(2048))}),
        ] {
            assert!(parse(value.clone()).is_err(), "{value}");
        }
        assert!(parse(json!({"action": "type", "text": "😀".repeat(4000)})).is_ok());
    }

    #[test]
    fn checks_bounds_against_the_frame() {
        let mapping = Mapping::new(Rect {
            x: 0.0,
            y: 0.0,
            width: 1024.0,
            height: 768.0,
        });
        let ok = parse(json!({"action": "left_click_drag", "start_coordinate": [0, 0], "coordinate": [1023, 767]})).unwrap();
        assert!(ok.check_bounds(&mapping).is_ok());
        for value in [
            json!({"action": "left_click", "coordinate": [1024, 0]}),
            json!({"action": "left_click", "coordinate": [-1, 0]}),
            json!({"action": "left_click_drag", "start_coordinate": [0, 0], "coordinate": [0, 768]}),
            json!({"action": "scroll", "scroll_direction": "up", "coordinate": [5000, 1]}),
            json!({"action": "zoom", "region": [0, 0, 1025, 10]}),
        ] {
            assert!(
                parse(value.clone())
                    .unwrap()
                    .check_bounds(&mapping)
                    .is_err(),
                "{value}"
            );
        }
    }

    #[test]
    fn classifies_input() {
        assert!(!Action::Screenshot.is_input());
        assert!(!Action::CursorPosition.is_input());
        assert!(parse(json!({"action": "type", "text": "x"}))
            .unwrap()
            .is_input());
        assert!(parse(json!({"action": "open_url", "url": "https://a.b"}))
            .unwrap()
            .is_input());
    }
}
