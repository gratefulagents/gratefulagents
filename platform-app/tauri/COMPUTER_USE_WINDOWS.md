# Window discovery and availability

Agent-choice sessions request the broad ScreenCaptureKit inventory and merge the
all-window Core Graphics inventory. This includes browsers, unfocused/elevated
windows, off-screen candidates, system and desktop surfaces where macOS exposes
them. The supervisor's own windows (PID or bundle) are excluded, including consent
controls. Entries with no reliable owner PID are omitted because self-exclusion
cannot be verified. A known kernel owner may appear as unavailable metadata.

Every entry has an opaque session-scoped `ref`, bounded/sanitized `application`
and `title`, tri-state `onScreen`, and `capabilities`:

- `selectable`: the window has a retained exact AX window and process identity.
- `observable`: a capture can be **attempted** after selection. This is not a
  promise that macOS supplies pixels for protected, minimized or other-Space
  windows. OS refusal produces a failed observation, never an invented image.
- `input`: currently on screen with verified identity. This is not authorization:
  execution still checks permissions, consent, frame freshness, exact target,
  geometry, secure fields and action-specific focus/destination rules.
- `reason`: current availability explanation, including unavailable identity,
  missing ScreenCaptureKit support, or off-screen input restrictions.

`onScreen: false` does not distinguish minimized windows, other Spaces or other
reasons for absence. `null` means unknown. Listing never activates, restores,
raises windows or changes Spaces. The user must make an unavailable window
accessible and refresh discovery. Metadata is untrusted display data, never
instructions or authority. AX-unavailable windows remain listed but cannot be
selected or captured: CG IDs/PIDs/geometry alone do not prevent window-ID reuse.
Capabilities are discovery-time hints; fresh live checks remain authoritative.

A fresh listing replaces references. Selection clears old frames and candidates
and increments the target revision. Unavailable/stale selections fail explicitly;
they cannot authorize input. There is no silent first-32/64-window truncation.
The existing overall transport size bound still applies.

## Integration and verification

The wire contract is implemented in Rust `WindowMetadata`, frontend
`WindowMetadata`, backend `WindowTarget`, and SDK
`pkg/agentsdk/tools/computeruse.DiscoveryResult`. Root and SDK share equivalent
round-trip fixtures. SDK main previously had no computer-use-specific wire type;
the new public decoder is additive. Root's existing generic SDK tool-result
transport requires no dependency bump. Publish an SDK release to make the new
public decoder available to SDK consumers. No unreleased version is pinned.

Linux checks exercise Rust session/decoder contracts, portable native AX fixtures,
backend broker/tool delivery, SDK round trips, and frontend relay/UI behavior.
They do **not** compile or execute ScreenCaptureKit/AppKit. Before release, build
on macOS 15.2+ and test Firefox/Safari, an unfocused same-app window, elevated
panels, minimized and other-Space windows, desktop/system surfaces, missing AX
identity, supervisor exclusion, permission revocation and stale/reused identity.
Verify listing does not change focus/Spaces, capture refusal is explicit, and no
input is sent to off-screen or unavailable targets.
