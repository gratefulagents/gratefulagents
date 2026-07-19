// gratefulagents — Tauri v2 native bridge.
//
// Provides small, typed, platform-agnostic helpers for the features wired in
// `src-tauri/src`. Every helper no-ops cleanly when running outside Tauri (web
// preview / Vitest), so components can call them unconditionally.

import * as React from "react";
import { isTauri, platform } from "./platform";

type Unlisten = () => void;

async function listenTauri<T>(event: string, handler: (payload: T) => void): Promise<Unlisten> {
  if (!isTauri) return () => {};
  try {
    const { listen } = await import("@tauri-apps/api/event");
    return await listen<T>(event, (ev) => handler(ev.payload));
  } catch {
    return () => {};
  }
}

/* ────────────────────────────── Menu + Tray ─────────────────────────────── */

/**
 * Known native menu / tray action ids. Keep this in lock-step with
 * `src-tauri/src/menu.rs` and `src-tauri/src/tray.rs`.
 */
export type NativeMenuAction =
  | "new-run"
  | "command-palette"
  | "settings"
  | "toggle-theme"
  | "reload"
  | "reload-hard"
  | "open-diagnostics";

export function useNativeMenuActions(handler: (action: NativeMenuAction) => void) {
  const ref = React.useRef(handler);
  React.useEffect(() => {
    ref.current = handler;
  }, [handler]);
  React.useEffect(() => {
    let off: Unlisten = () => {};
    void listenTauri<string>("menu://action", (payload) => {
      ref.current(payload as NativeMenuAction);
    }).then((u) => {
      off = u;
    });
    return () => off();
  }, []);
}

/* ────────────────────────────── Badge count ─────────────────────────────── */

/**
 * Sets the dock / taskbar badge on supported platforms (macOS: numeric dock
 * badge; Windows 11: overlay; Linux: no-op). Pass 0 to clear.
 */
export async function setBadgeCount(count: number): Promise<void> {
  if (!isTauri) return;
  try {
    const mod = await import("@tauri-apps/api/app");
    const fn = (mod as unknown as { setBadgeCount?: (c: number) => Promise<void> }).setBadgeCount;
    if (typeof fn === "function") {
      await fn(count > 0 ? count : 0);
    }
  } catch {
    // no-op on older Tauri runtimes
  }
}

/**
 * Wraps setBadgeCount in a React effect that updates whenever the count
 * changes and clears on unmount.
 */
export function useDockBadge(count: number) {
  React.useEffect(() => {
    void setBadgeCount(count);
    return () => {
      void setBadgeCount(0);
    };
  }, [count]);
}

/* ────────────────────────────── Deep links ──────────────────────────────── */

export function useDeepLinks(handler: (urls: string[]) => void) {
  const ref = React.useRef(handler);
  React.useEffect(() => {
    ref.current = handler;
  }, [handler]);
  React.useEffect(() => {
    let off: Unlisten = () => {};
    void listenTauri<string[]>("deep-link://open", (payload) => {
      ref.current(payload ?? []);
    }).then((u) => {
      off = u;
    });
    return () => off();
  }, []);
}

/* ────────────────────────────── Drag + Drop ─────────────────────────────── */

export interface DragDropHandlers {
  onEnter?: () => void;
  onLeave?: () => void;
  onDrop?: (paths: string[]) => void;
}

export function useWindowDragDrop({ onEnter, onLeave, onDrop }: DragDropHandlers) {
  const ref = React.useRef({ onEnter, onLeave, onDrop });
  React.useEffect(() => {
    ref.current = { onEnter, onLeave, onDrop };
  }, [onEnter, onLeave, onDrop]);
  React.useEffect(() => {
    const offs: Unlisten[] = [];
    void Promise.all([
      listenTauri<void>("window://drag-enter", () => ref.current.onEnter?.()),
      listenTauri<void>("window://drag-leave", () => ref.current.onLeave?.()),
      listenTauri<string[]>("window://files-dropped", (paths) =>
        ref.current.onDrop?.(paths ?? []),
      ),
    ]).then((list) => offs.push(...list));
    return () => offs.forEach((o) => o());
  }, []);
}

/* ────────────────────────────── Notifications ───────────────────────────── */

export interface NotificationOptions {
  title: string;
  body?: string;
  icon?: string;
}

let notificationsGranted: boolean | null = null;

/**
 * Checks (and requests, if needed) OS notification permission. Result is
 * cached for the session so repeated `notify()` calls don't re-prompt.
 */
export async function ensureNotificationPermission(): Promise<boolean> {
  if (!isTauri) return false;
  if (notificationsGranted !== null) return notificationsGranted;
  try {
    const plugin = await import("@tauri-apps/plugin-notification");
    let granted = await plugin.isPermissionGranted();
    if (!granted) {
      granted = (await plugin.requestPermission()) === "granted";
    }
    notificationsGranted = granted;
    return granted;
  } catch {
    return false;
  }
}

/**
 * One-time notification setup. Call once at app startup:
 *  - requests OS notification permission up front
 *  - wires click-to-focus: activating a notification brings the app window
 *    forward (via the plugin's action listener where supported; on macOS the
 *    OS also re-activates the app, which the Rust side handles via `Reopen`).
 */
export async function initNotifications(): Promise<void> {
  if (!isTauri) return;
  await ensureNotificationPermission();
  try {
    const plugin = await import("@tauri-apps/plugin-notification");
    const onAction = (plugin as { onAction?: (cb: (n: unknown) => void) => Promise<unknown> })
      .onAction;
    if (typeof onAction === "function") {
      await onAction(() => void focusMainWindow());
    }
  } catch {
    // action listener unsupported on this platform — OS-level activation still applies
  }
}

export async function notify(options: NotificationOptions): Promise<void> {
  if (!isTauri) return;
  try {
    if (await ensureNotificationPermission()) {
      const plugin = await import("@tauri-apps/plugin-notification");
      plugin.sendNotification(options);
    }
  } catch {
    // no-op
  }
}

/** True when the app window has focus. Falls back to `document.hasFocus()`. */
export async function isAppFocused(): Promise<boolean> {
  if (!isTauri) return document.hasFocus();
  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    return await getCurrentWindow().isFocused();
  } catch {
    return document.hasFocus();
  }
}

/** Shows, un-minimizes, and focuses the app window. */
export async function focusMainWindow(): Promise<void> {
  if (!isTauri) return;
  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    const win = getCurrentWindow();
    await win.show();
    await win.unminimize();
    await win.setFocus();
  } catch {
    // no-op
  }
}

/* ────────────────────────────── Clipboard ───────────────────────────────── */

export async function copyText(text: string): Promise<boolean> {
  if (isTauri) {
    try {
      const plugin = await import("@tauri-apps/plugin-clipboard-manager");
      await plugin.writeText(text);
      return true;
    } catch {
      // fall through to navigator.clipboard
    }
  }
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/* ────────────────────────────── Opener ──────────────────────────────────── */

// Store global listener state on the document so repeated app bootstrap (for
// example after a desktop dev reload) cannot register another opener for the
// same click. Symbol.for keeps the guard stable across module re-evaluation.
const EXTERNAL_LINK_INTERCEPTOR = Symbol.for("gratefulagents.external-link-interceptor");
type ExternalLinkInterceptorDocument = Document & Partial<Record<symbol, () => void>>;

export const DIAGNOSTICS_ISSUE_URL =
  "https://github.com/gratefulagents/gratefulagents/issues/new";
export const DIAGNOSTICS_EMAIL_URL =
  "mailto:captaintrips@gratefulagents.dev?subject=gratefulagents%20diagnostic%20logs";

export async function openExternal(url: string): Promise<void> {
  if (isTauri) {
    try {
      const plugin = await import("@tauri-apps/plugin-opener");
      await plugin.openUrl(url);
      return;
    } catch {
      // fall through
    }
  }
  window.open(url, "_blank", "noopener,noreferrer");
}

/** Opens the desktop app's actual log directory; web and mobile show location docs. */
export async function openDiagnosticLogs(): Promise<void> {
  if (isTauri) {
    const os = await platform();
    if (os !== "ios" && os !== "android") {
      try {
        const { invoke } = await import("@tauri-apps/api/core");
        await invoke("open_log_directory");
        return;
      } catch {
        // Fall back to the location reference if the native folder cannot open.
      }
    }
  }
  await openExternal("https://v2.tauri.app/plugin/logging/#persisting-logs");
}

/**
 * Intercept clicks on `<a href="http(s)://…">` / `mailto:` links so they open
 * in the default system browser instead of navigating the Tauri webview.
 * Installation is idempotent, including across module re-evaluation. Returns
 * a cleanup function; no-ops outside Tauri.
 */
export function installExternalLinkInterceptor(): () => void {
  if (!isTauri) return () => {};

  const interceptorDocument = document as ExternalLinkInterceptorDocument;
  const installedCleanup = interceptorDocument[EXTERNAL_LINK_INTERCEPTOR];
  if (installedCleanup) return installedCleanup;

  const handler = (e: MouseEvent) => {
    if (e.defaultPrevented) return;
    const target = e.target as HTMLElement | null;
    const anchor = target?.closest<HTMLAnchorElement>("a[href]");
    if (!anchor) return;
    if (anchor.hasAttribute("download")) return;
    const href = anchor.href;
    if (!/^(https?|mailto):/i.test(href)) return;
    // In-app (same-origin) navigation stays inside the webview.
    if (href.startsWith(window.location.origin + "/")) return;
    e.preventDefault();
    void openExternal(href);
  };
  const auxClickHandler = (e: MouseEvent) => {
    if (e.button === 1) handler(e);
  };

  interceptorDocument.addEventListener("click", handler, true);
  interceptorDocument.addEventListener("auxclick", auxClickHandler, true);

  const cleanup = () => {
    interceptorDocument.removeEventListener("click", handler, true);
    interceptorDocument.removeEventListener("auxclick", auxClickHandler, true);
    if (interceptorDocument[EXTERNAL_LINK_INTERCEPTOR] === cleanup) {
      delete interceptorDocument[EXTERNAL_LINK_INTERCEPTOR];
    }
  };
  interceptorDocument[EXTERNAL_LINK_INTERCEPTOR] = cleanup;
  return cleanup;
}

/* ────────────────────────────── Window drag ─────────────────────────────── */

/** Elements that must keep receiving normal mouse interaction. */
const DRAG_EXEMPT =
  ".no-drag, button, a, input, textarea, select, [role='button'], [role='menuitem'], [contenteditable='true']";

/**
 * Makes `.drag-region` elements move the native window; a double-click
 * toggles maximize (macOS zoom). Descendants matching `.no-drag` or
 * interactive elements are exempt.
 *
 * Needed because Tauri's webviews do not implement the Electron-style
 * `-webkit-app-region: drag` CSS that `.drag-region` declares — without this
 * the window cannot be dragged from the titlebar overlay on macOS. Call once
 * at app startup; no-ops outside desktop Tauri.
 */
export function installDragRegionHandler(): void {
  if (!isTauri) return;
  void (async () => {
    const p = await platform();
    if (p === "ios" || p === "android" || p === "web") return;
    let win: Awaited<ReturnType<typeof import("@tauri-apps/api/window")["getCurrentWindow"]>>;
    try {
      const { getCurrentWindow } = await import("@tauri-apps/api/window");
      win = getCurrentWindow();
    } catch {
      return;
    }
    window.addEventListener("mousedown", (e) => {
      if (e.button !== 0) return;
      const target = e.target instanceof Element ? e.target : null;
      if (!target || target.closest(DRAG_EXEMPT)) return;
      if (!target.closest(".drag-region")) return;
      e.preventDefault();
      if (e.detail === 2) {
        void win.toggleMaximize();
      } else {
        void win.startDragging();
      }
    });
  })();
}

/* ────────────────────────────── Theme-follow ────────────────────────────── */

/**
 * Listen to OS theme changes from the Tauri window (e.g. macOS System
 * Settings → Appearance). Invokes `onChange` with the new theme. Returns an
 * unsubscribe function.
 */
export async function subscribeOsTheme(
  onChange: (theme: "light" | "dark") => void,
): Promise<Unlisten> {
  if (!isTauri) return () => {};
  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    const win = getCurrentWindow();
    const off = await win.onThemeChanged(({ payload }) => {
      if (payload === "light" || payload === "dark") onChange(payload);
    });
    return off;
  } catch {
    return () => {};
  }
}

/**
 * Align the native window chrome (menus, dialogs, vibrancy appearance) with
 * the in-app theme. Pass an explicit theme when the user pins one, or `null`
 * to follow the OS appearance again. No-op outside Tauri.
 */
export async function setNativeWindowTheme(theme: "light" | "dark" | null): Promise<void> {
  if (!isTauri) return;
  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    await getCurrentWindow().setTheme(theme);
  } catch {
    // Older shells without the set-theme permission: ignore.
  }
}
