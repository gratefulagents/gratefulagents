"""Run the production macOS destination/drag/scroll bodies against mocked AX/CG.

Linux cannot link Apple's frameworks. Extract only these bodies unchanged so the
regressions exercise their control flow without introducing production test seams.
Run with: python3 tests/computer_use_input_regression.py
"""

import os
from pathlib import Path
import subprocess
import tempfile

SOURCE = Path(__file__).resolve().parents[1] / "src/computer_use_input.rs"

HARNESS = r'''
#![allow(non_snake_case, dead_code)]
use std::{cell::RefCell, ffi::c_void, ptr, time::Duration};
type Ref = *const c_void;
#[derive(Clone, Copy)]
struct Point { x: f64, y: f64 }
struct DisplayBounds { x: i32, y: i32, width: u32, height: u32 }
struct SessionScope;
struct Frame;
impl Frame {
    fn point(&self, x: f64, y: f64) -> Result<(f64, f64), String> { Ok((x, y)) }
}
struct Owned(Ref);
impl Owned {
    fn new(value: Ref) -> Result<Self, String> {
        if value.is_null() { Err("Missing AX object".into()) } else { Ok(Self(value)) }
    }
}
enum MouseButton { Left }
enum Action {
    Drag { x: f64, y: f64, to_x: f64, to_y: f64 },
    Scroll { delta_x: f64, delta_y: f64, x: Option<f64>, y: Option<f64> },
}
#[derive(Default)]
struct State {
    hit_error: i32,
    null_hit: bool,
    owner_error: i32,
    pid: i32,
    password: bool,
    password_checks: usize,
    destinations: usize,
    cancel_at: usize,
    cancelled: bool,
    posts: Vec<usize>,
}
thread_local! { static STATE: RefCell<State> = RefCell::new(State::default()); }
fn reset() {
    STATE.with(|s| *s.borrow_mut() = State { pid: std::process::id() as i32 + 1, ..State::default() });
}
fn check() -> Result<(), String> {
    if STATE.with(|s| s.borrow().cancelled) { Err("Cancelled".into()) } else { Ok(()) }
}
unsafe fn AXUIElementCreateSystemWide() -> Ref { 100usize as Ref }
unsafe fn AXUIElementSetMessagingTimeout(_: Ref, _: f32) -> i32 { 0 }
unsafe fn AXUIElementCopyElementAtPosition(_: Ref, _: f32, _: f32, hit: *mut Ref) -> i32 {
    STATE.with(|s| {
        let mut s = s.borrow_mut();
        s.destinations += 1;
        if s.destinations == s.cancel_at { s.cancelled = true; }
        if !s.null_hit { *hit = 101usize as Ref; }
        s.hit_error
    })
}
unsafe fn AXUIElementGetPid(_: Ref, pid: *mut i32) -> i32 {
    STATE.with(|s| { let s = s.borrow(); *pid = s.pid; s.owner_error })
}
fn non_password(_: Ref) -> Result<(), String> {
    STATE.with(|s| {
        let mut s = s.borrow_mut();
        s.password_checks += 1;
        if s.password { Err("Password field".into()) } else { Ok(()) }
    })
}
unsafe fn CGEventPost(_: u32, event: Ref) {
    STATE.with(|s| s.borrow_mut().posts.push(event as usize));
}
unsafe fn CGEventCreateScrollWheelEvent(_: Ref, _: u32, _: u32, _: i32, _: i32) -> Ref { 22usize as Ref }
unsafe fn CGEventSetLocation(_: Ref, _: Point) {}
unsafe fn CGEventSetFlags(_: Ref, _: u64) {}
fn mouse_types(_: MouseButton) -> (u32, u32, u32) { (1, 2, 0) }
fn mouse_event(kind: u32, _: Point, _: u32, _: i64) -> Result<Owned, String> { Ok(Owned(kind as Ref)) }
const MOUSE_MOVED: u32 = 5;
const LEFT_DRAGGED: u32 = 6;
fn geometry() -> DisplayBounds { DisplayBounds { x: 0, y: 0, width: 100, height: 100 } }
/* DESTINATION */
/* RELEASE */
fn execute(action: &Action) -> Result<(), String> {
    let scope = &SessionScope;
    let geometry = geometry();
    let frame = Frame;
    let guard = check;
    let guard_with = |_: bool| check();
    match action {
        /* ACTIONS */
    }
}
fn drag() -> Action { Action::Drag { x: 10., y: 10., to_x: 20., to_y: 20. } }
fn scroll() -> Action { Action::Scroll { delta_x: 0., delta_y: 10., x: Some(10.), y: Some(10.) } }
#[test]
fn ax_lookup_and_owner_failures_deny_input_before_password_checks() {
    for action in [drag(), scroll()] {
        for failure in 0..6 {
            reset();
            STATE.with(|s| {
                let mut s = s.borrow_mut();
                match failure {
                    0 => s.hit_error = -25204,
                    1 => s.null_hit = true,
                    2 => s.owner_error = -25202,
                    3 => s.pid = 0,
                    4 => s.pid = -1,
                    _ => s.pid = std::process::id() as i32,
                }
            });
            assert!(execute(&action).is_err(), "failure {failure} allowed input");
            STATE.with(|s| {
                let s = s.borrow();
                assert!(s.posts.is_empty());
                assert_eq!(s.password_checks, 0);
            });
        }
    }
}
#[test]
fn known_external_owner_is_allowed_and_password_guard_is_separate() {
    reset();
    assert!(destination(&SessionScope, &geometry(), Point { x: 10., y: 10. }).is_ok());
    STATE.with(|s| { assert_eq!(s.borrow().password_checks, 1); s.borrow_mut().password = true; });
    assert_eq!(destination(&SessionScope, &geometry(), Point { x: 10., y: 10. }), Err("Password field".into()));
}
#[test]
fn cancellation_during_drag_destination_posts_only_prior_events_and_cleanup() {
    for cancel_at in 1..=15 {
        reset();
        STATE.with(|s| s.borrow_mut().cancel_at = cancel_at);
        assert_eq!(execute(&drag()), Err("Cancelled".into()));
        let expected = if cancel_at <= 2 {
            vec![]
        } else {
            let mut events = vec![1];
            events.extend(std::iter::repeat_n(6, cancel_at - 3));
            events.push(2);
            events
        };
        STATE.with(|s| assert_eq!(s.borrow().posts, expected, "destination {cancel_at}"));
    }
}
#[test]
fn cancellation_during_scroll_destination_posts_no_subsequent_ordinary_event() {
    for cancel_at in 1..=2 {
        reset();
        STATE.with(|s| s.borrow_mut().cancel_at = cancel_at);
        assert_eq!(execute(&scroll()), Err("Cancelled".into()));
        STATE.with(|s| assert_eq!(s.borrow().posts, if cancel_at == 1 { vec![] } else { vec![5] }));
    }
}
#[test]
fn uncancelled_drag_and_scroll_post_expected_events() {
    reset();
    assert!(execute(&drag()).is_ok());
    let mut expected = vec![1];
    expected.extend([6; 12]);
    expected.push(2);
    STATE.with(|s| assert_eq!(s.borrow().posts, expected));
    reset();
    assert!(execute(&scroll()).is_ok());
    STATE.with(|s| assert_eq!(s.borrow().posts, vec![5, 22]));
}
'''


def main():
    source = SOURCE.read_text()
    native = source[source.index("pub mod macos {"):]
    destination = native[native.index("    fn destination("):native.index("    fn open_url(")]
    release = native[native.index("    struct Release {"):native.index("    fn pair(")]
    execution = native[native.index("    pub fn execute("):]
    actions = execution[execution.index("            Action::Drag {"):execution.index("            Action::Type {")]
    harness = HARNESS.replace("/* DESTINATION */", destination).replace("/* RELEASE */", release).replace("/* ACTIONS */", actions)
    with tempfile.TemporaryDirectory(prefix="computer-use-input-") as directory:
        root = Path(directory)
        rust = root / "regression.rs"
        binary = root / "regression"
        rust.write_text(harness)
        subprocess.run([os.environ.get("RUSTC", "rustc"), "--edition=2021", "--test", "-Dwarnings", str(rust), "-o", str(binary)], check=True)
        subprocess.run([str(binary)], check=True)


if __name__ == "__main__":
    main()
