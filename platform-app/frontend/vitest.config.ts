import path from "path";
import { defineConfig } from "vitest/config";

// The shared frontend has no Vite app config of its own (the `web` and `tauri`
// packages each own one), so tests resolve the `@` alias and DOM environment
// from here.
export default defineConfig({
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  test: {
    environment: "jsdom",
    globals: false,
    setupFiles: ["./vitest.setup.ts"],
    include: ["src/**/*.{test,spec}.{ts,tsx}"],
  },
});
