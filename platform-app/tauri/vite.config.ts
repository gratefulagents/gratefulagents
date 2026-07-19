import path from "path";
import { execSync } from "node:child_process";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The shared React source lives in the sibling `frontend` package; this app is
// just a build target that wraps it in the native Tauri shell.
const frontendRoot = path.resolve(__dirname, "../frontend");

// Stamp the bundle with the commit it was built from (shown in the app
// sidebar; see frontend/src/lib/build-info.ts). Release builds pass
// VITE_BUILD_COMMIT explicitly; local dev servers and builds fall back to the
// checked-out git commit so the build line is always populated.
if (!process.env.VITE_BUILD_COMMIT) {
  try {
    process.env.VITE_BUILD_COMMIT = execSync("git rev-parse HEAD", {
      cwd: __dirname,
      stdio: ["ignore", "pipe", "ignore"],
    })
      .toString()
      .trim();
  } catch {
    // Not a git checkout — build-info.ts falls back to "dev".
  }
}

// Tauri exposes this env var to tell us which platform we're on
const host = process.env.TAURI_DEV_HOST;

export default defineConfig({
  root: frontendRoot,
  // Keep Vite's cache inside this app so concurrent web/tauri dev servers don't collide.
  cacheDir: path.resolve(__dirname, "node_modules/.vite"),
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      "@": path.resolve(frontendRoot, "src"),
    },
  },
  // Tauri expects a fixed port and will fail the dev command if it can't use it.
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    host: host || false,
    hmr: host
      ? { protocol: "ws", host, port: 1421 }
      : undefined,
    watch: { ignored: ["**/src-tauri/**"] },
    // Dev proxy to backend; in prod the TAURI_BACKEND_URL setting is used.
    proxy: {
      "/platform.v1.PlatformService": {
        target: process.env.TAURI_BACKEND_URL || "http://localhost:8090",
        changeOrigin: true,
      },
      "/auth.v1.AuthService": {
        target: process.env.TAURI_BACKEND_URL || "http://localhost:8090",
        changeOrigin: true,
      },
      "/api": {
        target: process.env.TAURI_BACKEND_URL || "http://localhost:8090",
        changeOrigin: true,
      },
    },
  },
  envPrefix: ["VITE_", "TAURI_ENV_*"],
  build: {
    // Emit into this app's own dist so `src-tauri` (frontendDist: ../dist) finds it.
    outDir: path.resolve(__dirname, "dist"),
    emptyOutDir: true,
    target:
      process.env.TAURI_ENV_PLATFORM === "windows"
        ? "chrome105"
        : "safari15",
    minify: !process.env.TAURI_ENV_DEBUG,
    sourcemap: !!process.env.TAURI_ENV_DEBUG,
  },
});
