import path from "path";
import { execSync } from "node:child_process";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The shared React source lives in the sibling `frontend` package; this app is
// the plain-browser build target that the Go backend embeds (web/dist ->
// internal/dashboard/web_dist).
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

// Backend the dev server proxies to. Overridable so the selfdev harness
// (selfdev/) can point the UI at its fake backend on an ephemeral port.
const backendTarget = process.env.WEB_BACKEND_URL || "http://localhost:8090";

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
  server: {
    proxy: {
      "/platform.v1.PlatformService": {
        target: backendTarget,
        changeOrigin: true,
      },
      "/auth.v1.AuthService": {
        target: backendTarget,
        changeOrigin: true,
      },
      "/api": {
        target: backendTarget,
        changeOrigin: true,
      },
    },
  },
  build: {
    // The Go backend embeds this exact directory (web/dist).
    outDir: path.resolve(__dirname, "dist"),
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // Split stable vendor code out of the entry chunk so app-code changes
        // don't invalidate the (large, rarely-changing) vendor bundles in the
        // browser cache, and the initial parse cost is spread across
        // cacheable chunks.
        manualChunks(id: string) {
          if (!id.includes("node_modules")) return undefined;
          if (/[\\/]node_modules[\\/](react|react-dom|react-router|react-router-dom|scheduler)[\\/]/.test(id)) {
            return "react-vendor";
          }
          if (id.includes("framer-motion") || id.includes("motion-dom") || id.includes("motion-utils")) {
            return "motion";
          }
          if (/[\\/]node_modules[\\/](@bufbuild|@connectrpc)[\\/]/.test(id)) {
            return "connect";
          }
          return undefined;
        },
      },
    },
  },
});
