# Selected-display desktop control

This replaces both previous window modes. In the run’s **Computer** inspector tab,
the user selects a display locally, then clicks **Start desktop control** to
explicitly authorize capture of its entire visible content and **desktop-wide input**.
There are no separate sharing/input consent checkboxes. Selecting a display or
opening the tab never starts control. Chat keeps a compact status/Stop shortcut;
clicking the pending-approval indication opens Computer. Switching tabs or closing
the inspector does not stop the session, its heartbeat, or request handling.
Keyboard events follow OS focus and may affect other displays. Pointer events are bounded to the chosen display. There is no window
isolation or agent-selected target.

Screen Recording, Accessibility and a registered emergency-stop shortcut are
required. ScreenCaptureKit captures the display, including apps, menus, dialogs
and potentially the supervisor. CGEventPost delivers global input. Before
keyboard input the active supervisor hides itself so macOS restores foreground
focus; it does not activate or bind an approved application. Typing retains the
execution-time focused AX element to abort on mid-text focus changes. Pointer
hit tests reject the supervisor and password fields where macOS exposes them;
this is a practical protection, not an OS sandbox.

Frame pixels map linearly into CG display points, including negative origins,
Retina/downsampled dimensions. Every input requires a fresh captured frame.
Geometry/rotation/display configuration, permissions, lease, stop, pause and
cancellation are rechecked. Synthetic releases run even on cancellation to avoid
held keys/buttons. Secure Input and readable password fields reject input.
Emergency stop is Control+Option+Command+Escape or the tray. No cluster changes
are needed or performed by the desktop.

## Protocol/release migration

Required scope: `{mode:"selected_display", backend,user,namespace,run,displayId}`.
Native picker returns `{selectionId,displayId,name}`. Starting requires separate
`consentToScreenSharing` and `consentToDesktopInput` booleans. New attach operation:
`attach_desktop`; every relay response acknowledges `selected_display`. Old
attach operations, missing modes, legacy window fields and discovery actions
are rejected, never upgraded. Old approval preferences are not inherited.
Assisted mode auto-approves observation only, not scroll or hover.

Ship desktop, dashboard/backend and freshly created agent runs together. Old
running pods keep their old protocol and require replacement/reconnection.
Publish the SDK metadata change for consumers of removed discovery types; the
platform does not import those metadata types, so no unreleased version pin is
invented. Parent integrator handles commits, releases and deployment.

## Verification and real-Mac acceptance

Portable: `cargo test --lib --locked`, `cargo check --lib --tests --locked`.
Backend: tests of internal/computeruse, internal/tools and internal/dashboard;
frontend: computer-use relay, bridge, preferences, settings and panel tests,
typecheck, ESLint and production build. SDK: computeruse package tests.

A signed macOS app must still verify: permission grant/revoke; native picker
cancel/stop; primary and negative-origin secondary displays; Retina scaling and
rotation/hotplug; capture of menus/dialogs; click/drag/scroll on chosen display;
OS-focus keyboard effects on other displays; app switching and supervisor hiding;
mid-type focus changes; stop/pause/cancellation during held input; consent UI
hit rejection; default-browser URLs; stale app/backend/run reconnect rejection.
Linux policy tests are not evidence of actual macOS input delivery.

## Delivery failure regression

The removed agent-choice relay required native phase `active` even for failed
outcomes, while native failure pauses the session. This discarded the reason
into a generic unconfirmed-delivery error. The replacement forwards failed
outcomes for the same paused session while rejecting success after pause/stop.
This is source-level diagnosis, not a claim to have traced a production run.
