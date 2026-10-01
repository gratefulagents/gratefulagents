import { describe, expect, it } from "vitest";
import { actionMarker, chordGlyphs, describeAction, isObservation } from "./describe";

describe("chordGlyphs", () => {
  it.each([
    ["cmd+shift+t", "⇧⌘T"],
    ["ctrl+alt+cmd+Escape", "⌃⌥⌘⎋"],
    ["Return", "↩"],
    ["super+space", "⌘Space"],
    ["option+Page_Down", "⌥⇟"],
    ["cmd+bracketleft", "⌘["],
    ["cmd++", "⌘+"],
    ["F12", "F12"],
    ["ArrowUp", "↑"],
  ])("%s → %s", (chord, glyphs) => {
    expect(chordGlyphs(chord)).toBe(glyphs);
  });
});

describe("describeAction", () => {
  it.each([
    [{ action: "left_click", coordinate: [512, 300] }, "Click at 512, 300"],
    [{ action: "left_click", coordinate: [5, 6], text: "cmd+shift" }, "Click at 5, 6 holding ⇧⌘"],
    [{ action: "double_click", coordinate: [1, 2] }, "Double-click at 1, 2"],
    [{ action: "right_click", coordinate: [1, 2] }, "Right-click at 1, 2"],
    [{ action: "type", text: "hello" }, "Type “hello”"],
    [{ action: "type", text: "a\nb" }, "Type “a↵b”"],
    [{ action: "key", text: "cmd+c" }, "Press ⌘C"],
    [{ action: "key", text: "Tab", repeat: 3 }, "Press ⇥ ×3"],
    [{ action: "scroll", scroll_direction: "down" }, "Scroll down 3×"],
    [{ action: "scroll", scroll_direction: "up", scroll_amount: 5, coordinate: [10, 20] }, "Scroll up 5× at 10, 20"],
    [{ action: "left_click_drag", start_coordinate: [1, 2], coordinate: [3, 4] }, "Drag from 1, 2 to 3, 4"],
    [{ action: "mouse_move", coordinate: [7, 8] }, "Move pointer to 7, 8"],
    [{ action: "zoom", region: [0, 0, 100, 50] }, "Zoom into 0, 0 – 100, 50"],
    [{ action: "open_url", url: "https://example.com" }, "Open https://example.com"],
    [{ action: "screenshot" }, "Take a screenshot"],
    [{ action: "left_mouse_up" }, "Release mouse button"],
  ] as const)("%j", (action, label) => {
    expect(describeAction(action as never)).toBe(label);
  });

  it("truncates long text", () => {
    const label = describeAction({ action: "type", text: "x".repeat(200) });
    expect(label.length).toBeLessThan(60);
    expect(label).toContain("…");
  });
});

describe("isObservation", () => {
  it("covers screenshot, zoom and cursor reads only", () => {
    expect(isObservation({ action: "screenshot" })).toBe(true);
    expect(isObservation({ action: "zoom", region: [0, 0, 1, 1] })).toBe(true);
    expect(isObservation({ action: "cursor_position" })).toBe(true);
    expect(isObservation({ action: "mouse_move", coordinate: [0, 0] })).toBe(false);
  });
});

describe("actionMarker", () => {
  const frame = { width: 1000, height: 600 };

  it("marks clicks at the pixel centre", () => {
    expect(actionMarker({ action: "left_click", coordinate: [512, 300] }, frame)).toEqual({ kind: "point", x: 512.5, y: 300.5 });
  });

  it("falls back to the cursor for button presses without a coordinate", () => {
    expect(actionMarker({ action: "left_mouse_down" }, frame, { x: 4, y: 5 })).toEqual({ kind: "point", x: 4.5, y: 5.5 });
    expect(actionMarker({ action: "left_mouse_down" }, frame)).toBeNull();
  });

  it("draws drags, scrolls and zoom regions", () => {
    expect(actionMarker({ action: "left_click_drag", start_coordinate: [0, 0], coordinate: [10, 20] }, frame))
      .toEqual({ kind: "drag", x1: 0.5, y1: 0.5, x2: 10.5, y2: 20.5 });
    expect(actionMarker({ action: "scroll", scroll_direction: "left" }, frame)).toEqual({ kind: "scroll", x: 500, y: 300, dx: -1, dy: 0 });
    expect(actionMarker({ action: "zoom", region: [10, 20, 110, 70] }, frame)).toEqual({ kind: "rect", x: 10, y: 20, width: 100, height: 50 });
  });

  it("has no marker for keyboard actions", () => {
    expect(actionMarker({ action: "type", text: "hi" }, frame)).toBeNull();
  });
});
