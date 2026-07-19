// Dependency-free Tauri runtime detection. Lives in its own module so that
// low-level libs (secure-store, workspaces) can read it at module-eval time
// without importing platform.ts, whose import graph is cyclic through
// app-environment.ts → workspaces.ts (TDZ crash under Vite's dev module order).
export const isTauri =
  typeof (globalThis as { __TAURI_INTERNALS__?: unknown }).__TAURI_INTERNALS__ !== "undefined";
