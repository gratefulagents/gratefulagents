import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import App from "./App";
import { initializeTheme } from "./lib/theme";
import { initNotifications, installExternalLinkInterceptor, installDragRegionHandler } from "./lib/native";
import { removeLegacyUpdaterToken } from "./lib/desktop-updater";

initializeTheme();

// Public release assets no longer need the updater PAT stored by older builds.
void removeLegacyUpdaterToken();

// In the Tauri app, open all external links in the default system browser
// instead of navigating the webview.
installExternalLinkInterceptor();

// Tauri doesn't implement `-webkit-app-region: drag`, so wire `.drag-region`
// elements (titlebar, sidebar header) to native window dragging ourselves.
installDragRegionHandler();

// Request OS notification permission up front and wire click-to-focus.
// No-op outside Tauri.
void initNotifications();

// Disable native context menu + browser gestures inside the webview.
// We still allow it inside text inputs and editors.
window.addEventListener("contextmenu", (e) => {
  const t = e.target as HTMLElement | null;
  if (!t) return;
  if (t.closest("input, textarea, [contenteditable], .allow-context-menu")) return;
  e.preventDefault();
});

// Disable pinch-zoom and rubber-band scroll on iPad webview.
document.addEventListener(
  "gesturestart",
  (e) => e.preventDefault(),
  { passive: false },
);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
