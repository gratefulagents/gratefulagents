// Build metadata baked into the bundle at build time.
//
// Release builds stamp both the public app version and the source commit. The
// version is user-facing, while the commit remains internal metadata used to
// distinguish release builds from unstamped development builds.

const rawVersion = ((import.meta.env.VITE_APP_VERSION as string | undefined) ?? "").trim();
const rawCommit = ((import.meta.env.VITE_BUILD_COMMIT as string | undefined) ?? "").trim();

/** Public application version shown in the UI. */
export const APP_VERSION: string = rawVersion.replace(/^v/i, "") || "dev";

/** Full commit SHA of this build, or "dev" for unstamped builds. */
export const BUILD_COMMIT: string = rawCommit.length > 0 ? rawCommit : "dev";
