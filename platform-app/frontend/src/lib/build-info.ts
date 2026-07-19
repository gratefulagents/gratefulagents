// Build metadata baked into the bundle at build time.
//
// The local release script sets VITE_BUILD_COMMIT to the commit SHA being built
// (see platform-app/scripts/release.sh). Local dev servers and
// builds get it from the Vite configs (tauri/vite.config.ts,
// web/vite.config.ts), which fall back to the checked-out git commit. If
// neither source is available (e.g. building outside a git checkout), we
// still show "dev" so the build line in the sidebar is always visible.

const raw = ((import.meta.env.VITE_BUILD_COMMIT as string | undefined) ?? "").trim();

/** Full commit SHA of this build, or "dev" for unstamped builds. */
export const BUILD_COMMIT: string = raw.length > 0 ? raw : "dev";

/** Short (7-char) commit SHA of this build, or "dev" for unstamped builds. */
export const BUILD_COMMIT_SHORT: string = BUILD_COMMIT.slice(0, 7);
