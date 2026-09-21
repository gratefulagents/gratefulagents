# Desktop-control migration verification

## Delivered
- Window-only and agent-choice modes replaced by explicitly started selected-display desktop control.
- Selected display screenshots; pointer mapping restricted to that display; keyboard input follows OS focus, including other displays. Existing sessions must reconnect under the new protocol.
- Permissions, pause/stop, leases, action approvals, frame freshness, geometry validation and unconditional input-release cleanup retained.
- Desktop controller lives outside chat/inspector layouts; Computer inspector tab shows controls. Chat retains a compact status/open/Stop shortcut. Switching tabs does not stop heartbeat or approval processing.
- No setup consent checkboxes: clicking **Start desktop control**, beside scope/data-sharing disclosure, supplies the required capture/input authorization. Per-action approvals remain separate.
- Legacy scope/attach/discovery actions rejected with migration guidance. Tool/relay/backend/native/SDK/skill documentation updated.

## Validation
- Native Linux crate: 47 tests passed; cargo check and scoped Rust formatting passed.
- Additional native input regression harness: 5 tests passed, mutation checks caught all four reintroduced flaws. It exercises production destination/drag/scroll bodies with mocked AX/CG calls.
- Backend computeruse/tools/dashboard/configtest suites, relevant race tests, vet and builds passed.
- SDK metadata package tests/vet passed.
- Latest relevant frontend suites after tab changes: 227 tests passed across 7 suites; typecheck and production build passed; changed-file ESLint has zero errors and five existing Fast Refresh warnings.
- Earlier full frontend run: 1,632 passed, one failure in unchanged SecurityScanList query-string test. Its isolated 15-test suite passed. Full frontend is not claimed green.
- Strict Clippy has four existing findings in diagnostics/OAuth/updater files; no suppression added.
- Simulated Tauri Chat and Computer-tab screenshots inspected; not real Mac acceptance.

## Review corrections
- Supervisor pointer exclusion now fails closed on failed AX hit test, null element, unresolved owner or invalid PID.
- Drag and scroll recheck cancellation immediately after synchronous destination validation before ordinary events; releases remain unconditional.
- Native failure outcomes can be delivered for the same paused session; captures/success remain invalidated after pause/stop. This corrects a source-level delivery issue but does not prove the exact cause of the historical production failure.

## Release limitations
No macOS SDK/xcrun or logged-in GUI Mac was available. Objective-C compilation, framework linkage, signed app build, real ScreenCaptureKit capture and global event delivery remain unverified locally. Test negative-origin/Retina/rotated displays, hotplug, dialogs/menus, cross-display keyboard focus, supervisor exclusion, permissions and mid-action stop on Mac before release.

Deploy desktop, backend and fresh worker images together; reconnect existing sessions. SDK metadata API changes are breaking and require coordinated publication for consumers. Platform generic result transport does not need an unreleased SDK version pin.
