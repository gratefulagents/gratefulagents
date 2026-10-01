import type { ComputerAction } from "./native";

const MODIFIER_GLYPHS: [RegExp, string][] = [
  [/^(ctrl|control)$/i, "⌃"],
  [/^(alt|option|opt)$/i, "⌥"],
  [/^shift$/i, "⇧"],
  [/^(cmd|command|super|meta|win)$/i, "⌘"],
  [/^fn$/i, "fn"],
];

const KEY_GLYPHS: Record<string, string> = {
  return: "↩", enter: "↩", tab: "⇥", space: "Space", backspace: "⌫", delete: "⌦", escape: "⎋", esc: "⎋",
  up: "↑", down: "↓", left: "←", right: "→", arrowup: "↑", arrowdown: "↓", arrowleft: "←", arrowright: "→",
  home: "↖", end: "↘", page_up: "⇞", pageup: "⇞", prior: "⇞", page_down: "⇟", pagedown: "⇟", next: "⇟",
  capslock: "⇪", minus: "-", equal: "=", bracketleft: "[", bracketright: "]", semicolon: ";", apostrophe: "'",
  comma: ",", period: ".", slash: "/", backslash: "\\", grave: "`",
};

function keyGlyph(key: string): string {
  const named = KEY_GLYPHS[key.toLowerCase()];
  if (named) return named;
  if (/^f\d{1,2}$/i.test(key)) return key.toUpperCase();
  return key.length === 1 ? key.toUpperCase() : key;
}

const modifierGlyphs = (parts: string[]) =>
  MODIFIER_GLYPHS.flatMap(([pattern, glyph]) => (parts.some((part) => pattern.test(part)) ? [glyph] : [])).join("");

/** Mac-style rendering of a key chord in Apple's modifier order, e.g. `cmd+shift+t` → `⇧⌘T`. */
export function chordGlyphs(chord: string): string {
  const parts = chord.split("+");
  let key = parts.pop() ?? "";
  // "cmd++" presses the plus key.
  if (key === "" && parts.at(-1) === "") {
    parts.pop();
    key = "+";
  }
  return modifierGlyphs(parts) + keyGlyph(key);
}

function quote(text: string, max = 48): string {
  const visible = text.replace(/\r?\n/g, "↵").replace(/\t/g, "⇥");
  const clipped = visible.length > max ? `${visible.slice(0, max - 1)}…` : visible;
  return `“${clipped}”`;
}

const at = (point?: [number, number]) => (point ? ` at ${point[0]}, ${point[1]}` : "");
const held = (text?: string) => (text ? ` holding ${modifierGlyphs(text.split("+"))}` : "");

const CLICK_VERBS: Partial<Record<ComputerAction["action"], string>> = {
  left_click: "Click", right_click: "Right-click", middle_click: "Middle-click", double_click: "Double-click", triple_click: "Triple-click",
};

/** Short human label for an action, e.g. `Click at 512, 300`. */
export function describeAction(action: ComputerAction): string {
  const verb = CLICK_VERBS[action.action];
  if (verb) return `${verb}${at(action.coordinate)}${held(action.text)}`;
  switch (action.action) {
    case "screenshot": return "Take a screenshot";
    case "mouse_move": return `Move pointer to ${action.coordinate?.join(", ")}`;
    case "left_click_drag":
      return `Drag from ${action.start_coordinate?.join(", ")} to ${action.coordinate?.join(", ")}${held(action.text)}`;
    case "left_mouse_down": return `Press mouse button${at(action.coordinate)}`;
    case "left_mouse_up": return `Release mouse button${at(action.coordinate)}`;
    case "scroll": return `Scroll ${action.scroll_direction} ${action.scroll_amount ?? 3}×${at(action.coordinate)}${held(action.text)}`;
    case "type": return `Type ${quote(action.text ?? "")}`;
    case "key": return `Press ${chordGlyphs(action.text ?? "")}${action.repeat && action.repeat > 1 ? ` ×${action.repeat}` : ""}`;
    case "wait": return `Wait ${action.duration ?? 1}s`;
    case "cursor_position": return "Read pointer position";
    case "zoom": {
      const [x0, y0, x1, y1] = action.region ?? [0, 0, 0, 0];
      return `Zoom into ${x0}, ${y0} – ${x1}, ${y1}`;
    }
    case "open_url": return `Open ${action.url}`;
    default: return action.action;
  }
}

/** Actions that only read the screen; ask mode runs them without asking. */
export function isObservation(action: ComputerAction): boolean {
  return action.action === "screenshot" || action.action === "zoom" || action.action === "cursor_position";
}

/** Overlay geometry in frame pixels (pixel centres), for an SVG with `viewBox="0 0 w h"`. */
export type Marker =
  | { kind: "point"; x: number; y: number }
  | { kind: "drag"; x1: number; y1: number; x2: number; y2: number }
  | { kind: "scroll"; x: number; y: number; dx: number; dy: number }
  | { kind: "rect"; x: number; y: number; width: number; height: number };

const SCROLL_VECTORS = { up: [0, -1], down: [0, 1], left: [-1, 0], right: [1, 0] } as const;

export function actionMarker(
  action: ComputerAction,
  frame: { width: number; height: number },
  cursor?: { x: number; y: number },
): Marker | null {
  const centre = (point: [number, number]) => ({ x: point[0] + 0.5, y: point[1] + 0.5 });
  const anchor = action.coordinate
    ? centre(action.coordinate)
    : cursor
      ? { x: cursor.x + 0.5, y: cursor.y + 0.5 }
      : undefined;
  switch (action.action) {
    case "left_click": case "right_click": case "middle_click": case "double_click": case "triple_click":
    case "mouse_move": case "left_mouse_down": case "left_mouse_up":
      return anchor ? { kind: "point", ...anchor } : null;
    case "left_click_drag": {
      if (!action.start_coordinate || !action.coordinate) return null;
      const from = centre(action.start_coordinate);
      const to = centre(action.coordinate);
      return { kind: "drag", x1: from.x, y1: from.y, x2: to.x, y2: to.y };
    }
    case "scroll": {
      const [dx, dy] = SCROLL_VECTORS[action.scroll_direction ?? "down"];
      const origin = anchor ?? { x: frame.width / 2, y: frame.height / 2 };
      return { kind: "scroll", ...origin, dx, dy };
    }
    case "zoom": {
      if (!action.region) return null;
      const [x0, y0, x1, y1] = action.region;
      return { kind: "rect", x: x0, y: y0, width: x1 - x0, height: y1 - y0 };
    }
    default:
      return null;
  }
}
