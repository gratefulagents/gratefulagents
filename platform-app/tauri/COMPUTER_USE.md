# Computer use (protocol v2)

An agent running in Kubernetes controls one display of the user's Mac through
the Grateful Agents desktop app. The design follows Anthropic's computer-use
tool and OpenAI's CUA loop:

- the model sees every screenshot directly, as an image attached to the tool result
- coordinates are pixels of the most recent screenshot
- every action returns a fresh screenshot, taken after the screen settles
- the relay needs one round trip per action

```
agent (pod)                       dashboard                    desktop app (Tauri)
computer_use tool ──► Broker ◄── unix socket ◄── pod exec ◄── ExchangeComputerUse RPC ◄── controller loop (TS)
                       ▲  (desktop-bridge binary)                                          │ invoke
                       └──────────────── result (screenshot) ◄─────────────────────────── native Rust (SCK + CGEvent)
```

## 1. Agent tool `computer_use`

The input is one JSON object. Integers are screenshot pixels, with origin
top-left.

| action | required | optional |
| --- | --- | --- |
| `screenshot` | – | – |
| `left_click`, `right_click`, `middle_click`, `double_click`, `triple_click` | `coordinate` | `text` (held modifiers, e.g. `"shift"`, `"cmd+shift"`) |
| `mouse_move` | `coordinate` | – |
| `left_click_drag` | `start_coordinate`, `coordinate` (end) | `text` (modifiers) |
| `left_mouse_down`, `left_mouse_up` | – | `coordinate` |
| `scroll` | `scroll_direction` (`up` / `down` / `left` / `right`) | `scroll_amount` (1–30, default 3), `coordinate`, `text` (modifiers) |
| `type` | `text` (1–4000 chars) | – |
| `key` | `text` (one chord, e.g. `cmd+c`, `Return`, `ctrl+shift+Tab`) | `repeat` (1–50) |
| `wait` | – | `duration` seconds (0.1–30, default 1) |
| `cursor_position` | – | – |
| `zoom` | `region` `[x0,y0,x1,y1]` (x1>x0, y1>y0) | – |
| `open_url` | `url` (absolute http/https, no credentials, ≤2048) | – |

Rules:

- A `coordinate` is `[x, y]`, with `0 ≤ x < width` and `0 ≤ y < height` of the last screenshot.
- A field that does not belong to the action is rejected.
- `wait` is executed by the Go tool: it sleeps, then requests `screenshot`.
- Results are text plus one image (`image/jpeg`, `Detail: "high"`). For example: `left_click at (512, 300) done. Screen 1183x768.`
- `cursor_position` returns the cursor position and a screenshot.
- `zoom` returns a magnified crop. Its coordinates are not valid for clicking.

**Key names.** Matching is case-insensitive, and both xdotool and OpenAI names are accepted.

- Modifiers:
  - `cmd` / `command` / `super` / `meta` / `win`
  - `ctrl` / `control`
  - `alt` / `option` / `opt`
  - `shift`
  - `fn`
- Keys:
  - `Return` / `Enter`, `Tab`, `space`, `BackSpace`, `Delete` (forward delete), `Escape` / `Esc`
  - `Up` / `Down` / `Left` / `Right` and `ArrowUp`…
  - `Home`, `End`, `Page_Up` / `PageUp` / `Prior`, `Page_Down` / `PageDown` / `Next`
  - `F1`–`F20`, `CapsLock`
  - letters and digits
  - punctuation, either as literal characters or as xdotool names: `minus`, `equal`, `bracketleft`, `bracketright`, `semicolon`, `apostrophe`, `comma`, `period`, `slash`, `backslash`, `grave`
- Reserved chords are rejected: `ctrl+alt+cmd+Escape` (emergency stop) and `cmd+alt+Escape` (force quit).

## 2. Relay wire format (JSON, UTF-8, ≤ 12 MiB)

**Exchange.** This is the dashboard → bridge stdin → broker message. Identity is
filled in by the dashboard, never by the desktop.

```json
{"namespace":"ns","run":"run","owner":"user-subject","sessionId":"<desktop session id>",
 "operation":"connect|next|result|disconnect","requestId":"<only for result>","result":{...only for result}}
```

**Response.**

```json
{"protocol":2,"active":true,"available":true,"reason":"","request":{"id":"<uuid>","action":{...tool action, snake_case, never wait}}}
```

- `available` means the run can actually use the tool: it is registered and the permission mode allows writes.
- `reason` is a short, human-readable string. It is only set when `active` is false or the operation was refused.

**Result.** The desktop sends this with `result`.

```json
{"requestId":"<uuid>","ok":true,"error":"","denied":false,
 "screenshot":{"mediaType":"image/jpeg","data":"<base64>","width":1183,"height":768},
 "cursor":{"x":10,"y":20}}
```

- When `ok` is false, `error` must be non-empty (≤1024 chars). `denied` is true only when the user refused the action.
- A screenshot is required for successful actions, and optional on failure.
- Screenshots are ≤ 8 MiB decoded, `image/jpeg` or `image/png`, with dimensions that match the image header.

### Broker semantics

- **Sessions.** There is one desktop session per run. `connect` creates it.
  - The same owner connecting with a new sessionId replaces the old session. Its in-flight request fails with "desktop reconnected".
  - A different owner is refused with reason `another user is controlling this run's desktop`.
- **Lease.** The lease is 30 s, renewed by every `connect`, `next` or `result`. It is not enforced while a request is delivered and unresolved.
- **`next`.** This is a long-poll that waits up to 20 s.
  - It returns immediately if a request is pending, and marks it delivered. A delivered but unresolved request is delivered again: the desktop only polls while idle, so this recovers a response that was lost in transit.
  - If the session ends during the wait, it returns `active:false`.
- **`result`.** It must match the delivered request id.
- **`disconnect`.** Ends the session and fails any pending request with `desktop disconnected`.
- **Concurrency.** Only one request can be in flight. A concurrent agent request gets a busy error.
- **Deadlines.**
  - An undelivered request fails after 30 s with `desktop did not pick up the request`.
  - A delivered request fails after 300 s. This covers approval in ask mode.
- **Timeouts.** The bridge has 30 s. The dashboard exec has 35 s. Frontend RPC timeouts are 40 s for `next` and 20 s for everything else.

The dashboard RPC `ExchangeComputerUse` is unchanged on the wire (proto):

- `operation` carries the operation
- `outcome_json` carries the Result
- `response_json` carries the Response

## 3. Native Tauri commands (macOS)

All commands are `camelCase` in JSON and return `Err(String)` with
user-readable messages.

| command | args | returns |
| --- | --- | --- |
| `computer_use_status` | – | `NativeStatus` |
| `computer_use_request_permission` | `permission: "accessibility" \| "screen_recording"` | `()`. It triggers the OS prompt and opens the matching System Settings pane. |
| `computer_use_relaunch` | – | `()` |
| `computer_use_start` | `displayId: u32, runKey: String` | `NativeSession` |
| `computer_use_stop` | `reason: Option<String>` | `()` |
| `computer_use_set_paused` | `paused: bool` | `NativeSession` |
| `computer_use_execute` | `action: serde_json::Value` (section 1 shape, no `wait`) | `ExecResult` |

```ts
NativeStatus = { supported: boolean; unsupportedReason?: string; accessibility: boolean; screenRecording: boolean;
                 emergencyStop: boolean; displays: Display[]; session: NativeSession }
Display = { id: number; name: string; width: number; height: number; scale: number; primary: boolean }  // width/height in points
NativeSession = { active: boolean; paused: boolean; displayId?: number; runKey?: string;
                  frameWidth?: number; frameHeight?: number; stoppedReason?: string }
ExecResult = { screenshot: { mediaType: "image/jpeg"; data: string /* base64, no data: prefix */; width: number; height: number };
               cursor?: { x: number; y: number } }
```

The event `computer-use://session` carries the `NativeSession` payload. It is
emitted on every native session change: start, stop, pause, emergency stop and
tray stop.

### Native behaviour

- **Frame size.** The display has `W×H` points. `k = min(1, 768/min(W,H), 1456/max(W,H))`, giving a frame of `round(W·k) × round(H·k)`. Points are `x_pt = ox + (x+0.5)·W/fw` (likewise for y).
- **Capture.** ScreenCaptureKit `SCScreenshotManager` (macOS 14+) captures at the frame size. The app's own windows are excluded and the cursor is hidden. The image is encoded as JPEG at quality 80.
- **Settle.** After input, the native layer waits for a per-action minimum. It then captures until two consecutive 64×40 thumbnails differ by less than 1%, or until 2 s have passed.
- **Getting out of the way.** Before pointer input that lands inside one of our windows, and before keyboard input while our app is frontmost, the app hides itself. This restores the previously active app.
- **Input.** Input uses CGEvent at the HID tap:
  - click-state for double and triple click
  - `LeftMouseDragged` steps for drag
  - pixel scroll of `amount × 100` px, at the cursor moved to `coordinate`
  - Unicode typing in chunks of 16 UTF-16 units, with `\n` sent as Return and `\t` as Tab
  - modifier keys pressed and released around chords and clicks
- Every button and modifier is released on exit, on error and on stop.
- **Refusals.**
  - `type` is refused while Secure Event Input is active.
  - Input is refused while the session is paused or stopped. Execute is also refused when Accessibility or Screen Recording is missing.
- **Emergency stop.** `Ctrl+Option+Cmd+Escape` stops the session, as does the tray item "Stop computer use".

## 4. Desktop UI

The **Computer** inspector tab shows:

- a setup checklist (permissions, display)
- a Start button
- a live view of the latest screenshot, with a marker for the last or pending action
- an action timeline with thumbnails
- Pause / Resume and Stop
- the approval mode: **Autonomous** (default) or **Ask before each action**

In ask mode, screenshots, zoom and cursor reads run without asking. Every other
action shows an Allow / Deny card with a preview overlay. The chat view keeps a
compact status pill. The controller loop is a module-level singleton, so
switching tabs never interrupts it.

## 5. Rollout

Protocol v2 is not compatible with v1. Ship the desktop app, the dashboard
image and new agent runs together. Runs that already exist must be recreated
before they can use computer use.
