//! CGEvent input posted at the HID event tap.

use std::sync::atomic::{AtomicBool, Ordering};
use std::thread::sleep;
use std::time::Duration;

use objc2_core_foundation::{CFRetained, CGPoint};
use objc2_core_graphics::{
    CGEvent, CGEventField, CGEventFlags, CGEventSource, CGEventSourceStateID, CGEventTapLocation,
    CGEventType, CGMouseButton, CGScrollEventUnit, CGWarpMouseCursorPosition,
};

use super::super::action::{Action, Button, ScrollDirection};
use super::super::geometry::Mapping;
use super::super::keys::{type_segments, Chord, Modifier, Modifiers, TypeSegment};

const SCROLL_STEP_PIXELS: i32 = 100;

/// The left button stays down between `left_mouse_down` and `left_mouse_up`.
static LEFT_HELD: AtomicBool = AtomicBool::new(false);

pub fn cursor_location() -> (f64, f64) {
    let event = CGEvent::new(None);
    let point = CGEvent::location(event.as_deref());
    (point.x, point.y)
}

fn post(event: &CGEvent) {
    CGEvent::post(CGEventTapLocation::HIDEventTap, Some(event));
}

fn mouse_event(
    source: Option<&CGEventSource>,
    kind: CGEventType,
    at: CGPoint,
    button: CGMouseButton,
    flags: u64,
) -> Result<CFRetained<CGEvent>, String> {
    let event = CGEvent::new_mouse_event(source, kind, at, button)
        .ok_or("Could not create a mouse event")?;
    CGEvent::set_flags(Some(&event), CGEventFlags(flags));
    Ok(event)
}

/// Releases a left button left down by `left_mouse_down` (stop, pause, exit).
pub fn release_all() {
    if LEFT_HELD.swap(false, Ordering::SeqCst) {
        let (x, y) = cursor_location();
        if let Ok(event) = mouse_event(
            None,
            CGEventType::LeftMouseUp,
            CGPoint { x, y },
            CGMouseButton::Left,
            0,
        ) {
            post(&event);
        }
    }
}

fn buttons(button: Button) -> (CGEventType, CGEventType, CGMouseButton) {
    match button {
        Button::Left => (
            CGEventType::LeftMouseDown,
            CGEventType::LeftMouseUp,
            CGMouseButton::Left,
        ),
        Button::Right => (
            CGEventType::RightMouseDown,
            CGEventType::RightMouseUp,
            CGMouseButton::Right,
        ),
        Button::Middle => (
            CGEventType::OtherMouseDown,
            CGEventType::OtherMouseUp,
            CGMouseButton::Center,
        ),
    }
}

/// One action's input. Whatever it pressed is released on drop, so errors and
/// interruptions never leave keys or buttons down.
struct Input<'a> {
    source: Option<CFRetained<CGEventSource>>,
    interrupted: &'a AtomicBool,
    flags: u64,
    modifiers: Vec<Modifier>,
    key: Option<u16>,
    button: Option<(Button, CGPoint)>,
}

impl Drop for Input<'_> {
    fn drop(&mut self) {
        if let Some(key) = self.key.take() {
            let _ = self.key_event(key, false);
        }
        if let Some((button, at)) = self.button.take() {
            let (_, up, cg_button) = buttons(button);
            if let Ok(event) = mouse_event(self.source(), up, at, cg_button, self.flags) {
                post(&event);
            }
        }
        self.release_modifiers();
    }
}

impl<'a> Input<'a> {
    fn new(interrupted: &'a AtomicBool) -> Input<'a> {
        Input {
            source: CGEventSource::new(CGEventSourceStateID::HIDSystemState),
            interrupted,
            flags: 0,
            modifiers: Vec::new(),
            key: None,
            button: None,
        }
    }

    fn source(&self) -> Option<&CGEventSource> {
        self.source.as_deref()
    }

    fn check(&self) -> Result<(), String> {
        if self.interrupted.load(Ordering::SeqCst) {
            return Err("Computer use was stopped".into());
        }
        Ok(())
    }

    fn pause(&self, ms: u64) -> Result<(), String> {
        sleep(Duration::from_millis(ms));
        self.check()
    }

    fn key_event(&self, key: u16, down: bool) -> Result<(), String> {
        let event = CGEvent::new_keyboard_event(self.source(), key, down)
            .ok_or("Could not create a keyboard event")?;
        CGEvent::set_flags(Some(&event), CGEventFlags(self.flags));
        post(&event);
        Ok(())
    }

    fn press_modifiers(&mut self, modifiers: Modifiers) -> Result<(), String> {
        for modifier in modifiers.iter() {
            self.check()?;
            self.flags |= modifier.flag();
            self.modifiers.push(modifier);
            self.key_event(modifier.keycode(), true)?;
            sleep(Duration::from_millis(10));
        }
        Ok(())
    }

    fn release_modifiers(&mut self) {
        while let Some(modifier) = self.modifiers.pop() {
            self.flags &= !modifier.flag();
            let _ = self.key_event(modifier.keycode(), false);
        }
    }

    fn tap(&mut self, key: u16, key_modifier: Option<Modifier>) -> Result<(), String> {
        self.check()?;
        let flag = key_modifier.map_or(0, Modifier::flag);
        self.flags |= flag;
        self.key = Some(key);
        let down = self.key_event(key, true);
        self.flags &= !flag;
        down?;
        sleep(Duration::from_millis(15));
        self.key = None;
        self.key_event(key, false)
    }

    fn move_to(&self, at: CGPoint) -> Result<(), String> {
        self.check()?;
        let _ = CGWarpMouseCursorPosition(at);
        let (kind, button) = if LEFT_HELD.load(Ordering::SeqCst) {
            (CGEventType::LeftMouseDragged, CGMouseButton::Left)
        } else {
            (CGEventType::MouseMoved, CGMouseButton::Left)
        };
        let event = mouse_event(self.source(), kind, at, button, self.flags)?;
        post(&event);
        Ok(())
    }

    fn button_event(
        &mut self,
        button: Button,
        down: bool,
        at: CGPoint,
        clicks: i64,
    ) -> Result<(), String> {
        let (down_type, up_type, cg_button) = buttons(button);
        let kind = if down { down_type } else { up_type };
        let event = mouse_event(self.source(), kind, at, cg_button, self.flags)?;
        CGEvent::set_integer_value_field(Some(&event), CGEventField::MouseEventClickState, clicks);
        self.button = down.then_some((button, at));
        post(&event);
        Ok(())
    }

    fn click(
        &mut self,
        button: Button,
        count: u8,
        at: CGPoint,
        modifiers: Modifiers,
    ) -> Result<(), String> {
        self.move_to(at)?;
        self.pause(40)?;
        self.press_modifiers(modifiers)?;
        for clicks in 1..=i64::from(count) {
            self.check()?;
            self.button_event(button, true, at, clicks)?;
            sleep(Duration::from_millis(20));
            self.button_event(button, false, at, clicks)?;
            if clicks < i64::from(count) {
                sleep(Duration::from_millis(40));
            }
        }
        Ok(())
    }

    fn drag(&mut self, from: CGPoint, to: CGPoint, modifiers: Modifiers) -> Result<(), String> {
        self.move_to(from)?;
        self.pause(50)?;
        self.press_modifiers(modifiers)?;
        self.button_event(Button::Left, true, from, 1)?;
        self.pause(60)?;
        let distance = (to.x - from.x).hypot(to.y - from.y);
        let steps = ((distance / 15.0).ceil() as u32).clamp(10, 60);
        for step in 1..=steps {
            self.check()?;
            let t = f64::from(step) / f64::from(steps);
            let at = CGPoint {
                x: from.x + (to.x - from.x) * t,
                y: from.y + (to.y - from.y) * t,
            };
            let event = mouse_event(
                self.source(),
                CGEventType::LeftMouseDragged,
                at,
                CGMouseButton::Left,
                self.flags,
            )?;
            post(&event);
            self.button = Some((Button::Left, at));
            sleep(Duration::from_millis(12));
        }
        self.pause(60)?;
        self.button_event(Button::Left, false, to, 1)
    }

    fn scroll(
        &mut self,
        direction: ScrollDirection,
        amount: u32,
        at: Option<CGPoint>,
        modifiers: Modifiers,
    ) -> Result<(), String> {
        if let Some(at) = at {
            self.move_to(at)?;
            self.pause(40)?;
        }
        self.press_modifiers(modifiers)?;
        // Positive wheel 1 scrolls up; positive wheel 2 scrolls left.
        let (dy, dx) = match direction {
            ScrollDirection::Up => (SCROLL_STEP_PIXELS, 0),
            ScrollDirection::Down => (-SCROLL_STEP_PIXELS, 0),
            ScrollDirection::Left => (0, SCROLL_STEP_PIXELS),
            ScrollDirection::Right => (0, -SCROLL_STEP_PIXELS),
        };
        for _ in 0..amount {
            self.check()?;
            let event = CGEvent::new_scroll_wheel_event2(
                self.source(),
                CGScrollEventUnit::Pixel,
                2,
                dy,
                dx,
                0,
            )
            .ok_or("Could not create a scroll event")?;
            CGEvent::set_flags(Some(&event), CGEventFlags(self.flags));
            post(&event);
            sleep(Duration::from_millis(15));
        }
        Ok(())
    }

    fn type_text(&mut self, text: &str) -> Result<(), String> {
        for segment in type_segments(text) {
            self.check()?;
            match segment {
                TypeSegment::Key(key) => self.tap(key, None)?,
                TypeSegment::Text(units) => {
                    for down in [true, false] {
                        let event = CGEvent::new_keyboard_event(self.source(), 0, down)
                            .ok_or("Could not create a keyboard event")?;
                        CGEvent::set_flags(Some(&event), CGEventFlags(0));
                        unsafe {
                            CGEvent::keyboard_set_unicode_string(
                                Some(&event),
                                units.len() as _,
                                units.as_ptr(),
                            )
                        };
                        post(&event);
                    }
                }
            }
            sleep(Duration::from_millis(12));
        }
        Ok(())
    }

    fn chord(&mut self, chord: Chord, repeat: u32) -> Result<(), String> {
        self.press_modifiers(chord.modifiers)?;
        for i in 0..repeat {
            if i > 0 {
                self.pause(30)?;
            }
            self.tap(chord.key, chord.key_modifier)?;
        }
        Ok(())
    }
}

/// Performs an input action. Errors (including stop/pause) release everything.
pub fn perform(action: &Action, mapping: &Mapping, interrupted: &AtomicBool) -> Result<(), String> {
    let point = |p| {
        let (x, y) = mapping.to_points(p);
        CGPoint { x, y }
    };
    let mut input = Input::new(interrupted);
    match action {
        Action::Click {
            button,
            count,
            at,
            modifiers,
        } => input.click(*button, *count, point(*at), *modifiers),
        Action::MouseMove { at } => input.move_to(point(*at)),
        Action::Drag {
            from,
            to,
            modifiers,
        } => input.drag(point(*from), point(*to), *modifiers),
        Action::MouseDown { at } => {
            let at = match at {
                Some(at) => {
                    let at = point(*at);
                    input.move_to(at)?;
                    input.pause(40)?;
                    at
                }
                None => {
                    let (x, y) = cursor_location();
                    CGPoint { x, y }
                }
            };
            input.button_event(Button::Left, true, at, 1)?;
            // Intentionally held until left_mouse_up (or stop).
            input.button = None;
            LEFT_HELD.store(true, Ordering::SeqCst);
            Ok(())
        }
        Action::MouseUp { at } => {
            let at = match at {
                Some(at) => {
                    let at = point(*at);
                    input.move_to(at)?;
                    input.pause(40)?;
                    at
                }
                None => {
                    let (x, y) = cursor_location();
                    CGPoint { x, y }
                }
            };
            LEFT_HELD.store(false, Ordering::SeqCst);
            input.button_event(Button::Left, false, at, 1)
        }
        Action::Scroll {
            direction,
            amount,
            at,
            modifiers,
        } => input.scroll(*direction, *amount, at.map(point), *modifiers),
        Action::Type { text } => input.type_text(text),
        Action::Key { chord, repeat } => input.chord(*chord, *repeat),
        Action::Screenshot
        | Action::CursorPosition
        | Action::Zoom { .. }
        | Action::OpenUrl { .. } => Ok(()),
    }
}
