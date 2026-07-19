// Tauri-sim: an addInitScript payload that fakes `window.__TAURI_INTERNALS__`
// before the app bundle evaluates, flipping `isTauri` to true in a plain
// browser. This unlocks desktop-only UI (TitleBar overlay, WorkspaceSwitcher,
// OAuth connect cards, ImportLocalCredentials, Connection settings) in
// screenshots without building the Rust shell.
//
// The stub is deliberately minimal: it implements the plugin commands the app
// actually calls (audited from frontend/src) and logs a console warning for
// anything unknown, so missing surface shows up in the captured console log.
//
// The store plugin is backed by localStorage (prefix __TAURI_STORE__.) so that
// Playwright storageState captures the auth session exactly like web mode.

import type { ScenarioLocalCredential } from "../scenario";

export interface TauriSimOptions {
  /** Reported OS platform. Drives macOS traffic-light spacing, iPad layout, … */
  platform?: "macos" | "ios" | "linux" | "windows";
  appVersion?: string;
  /** Result of the native detect_local_credentials command. */
  localCredentials?: ScenarioLocalCredential[];
}

export function buildTauriSimScript(options: TauriSimOptions = {}): string {
  const config = {
    platform: options.platform ?? "macos",
    appVersion: options.appVersion ?? "0.1.0-selfdev",
    localCredentials: options.localCredentials ?? [],
  };

  return `(() => {
  if (window.__TAURI_INTERNALS__) return;
  const cfg = ${JSON.stringify(config)};
  const STORE_PREFIX = "__TAURI_STORE__.";

  // Route the app's runtime fetch through window.fetch instead of the
  // plugin-http IPC protocol (see frontend/src/lib/runtime-fetch.ts).
  window.__OPERATOR_FORCE_WEB_FETCH__ = true;

  const osInternals = {
    platform: cfg.platform,
    arch: "aarch64",
    family: cfg.platform === "windows" ? "windows" : "unix",
    version: "15.3.1",
    os_type: cfg.platform,
    osType: cfg.platform,
    exe_extension: cfg.platform === "windows" ? "exe" : "",
    eol: cfg.platform === "windows" ? "\\r\\n" : "\\n",
  };
  window.__TAURI_OS_PLUGIN_INTERNALS__ = osInternals;

  let nextCallbackId = 1;
  function transformCallback(callback, once) {
    const id = nextCallbackId++;
    const prop = "_" + id;
    Object.defineProperty(window, prop, {
      value: (result) => {
        if (once) Reflect.deleteProperty(window, prop);
        return callback && callback(result);
      },
      writable: false,
      configurable: true,
    });
    return id;
  }

  function storeGet(key) {
    const raw = localStorage.getItem(STORE_PREFIX + key);
    if (raw === null) return [null, false];
    try { return [JSON.parse(raw), true]; } catch { return [null, false]; }
  }
  function storeKeys() {
    const keys = [];
    for (let i = 0; i < localStorage.length; i++) {
      const k = localStorage.key(i);
      if (k && k.startsWith(STORE_PREFIX)) keys.push(k.slice(STORE_PREFIX.length));
    }
    return keys;
  }

  async function invoke(cmd, args = {}, _options) {
    switch (cmd) {
      // ---- os ----
      case "plugin:os|platform": return osInternals.platform;
      case "plugin:os|arch": return osInternals.arch;
      case "plugin:os|family": return osInternals.family;
      case "plugin:os|version": return osInternals.version;
      case "plugin:os|os_type": return osInternals.os_type;
      case "plugin:os|locale": return "en-US";
      case "plugin:os|hostname": return "selfdev";
      case "plugin:os|exe_extension": return osInternals.exe_extension;

      // ---- app ----
      case "plugin:app|version": return cfg.appVersion;
      case "plugin:app|name": return "gratefulagents";
      case "plugin:app|tauri_version": return "2.9.0";
      case "plugin:app|app_show":
      case "plugin:app|app_hide": return null;
      case "plugin:app|set_dock_visibility": return null;
      case "set_badge_count":
      case "plugin:app|set_badge_count": return null;

      // ---- events ----
      case "plugin:event|listen": return nextCallbackId++;
      case "plugin:event|unlisten":
      case "plugin:event|emit":
      case "plugin:event|emit_to": return null;

      // ---- store (localStorage-backed so storageState persists auth) ----
      case "plugin:store|load": return 1;
      case "plugin:store|get_store": return 1;
      case "plugin:store|get": return storeGet(args.key);
      case "plugin:store|set": {
        localStorage.setItem(STORE_PREFIX + args.key, JSON.stringify(args.value));
        return null;
      }
      case "plugin:store|has": return storeGet(args.key)[1];
      case "plugin:store|delete": {
        const had = storeGet(args.key)[1];
        localStorage.removeItem(STORE_PREFIX + args.key);
        return had;
      }
      case "plugin:store|keys": return storeKeys();
      case "plugin:store|values": return storeKeys().map((k) => storeGet(k)[0]);
      case "plugin:store|entries": return storeKeys().map((k) => [k, storeGet(k)[0]]);
      case "plugin:store|length": return storeKeys().length;
      case "plugin:store|clear": {
        for (const k of storeKeys()) localStorage.removeItem(STORE_PREFIX + k);
        return null;
      }
      case "plugin:store|save":
      case "plugin:store|reload": return null;

      // ---- notifications ----
      case "plugin:notification|is_permission_granted": return true;
      case "plugin:notification|request_permission": return "granted";
      case "plugin:notification|permission_state": return "granted";
      case "plugin:notification|notify":
      case "plugin:notification|batch":
      case "plugin:notification|register_action_types":
      case "plugin:notification|register_listener": return null;

      // ---- window / webview ----
      case "plugin:window|theme": return null;
      case "plugin:window|is_focused": return true;
      case "plugin:window|is_maximized":
      case "plugin:window|is_fullscreen":
      case "plugin:window|is_minimized": return false;
      case "plugin:window|is_visible": return true;
      case "plugin:window|scale_factor": return 2;
      case "plugin:window|inner_size":
      case "plugin:window|outer_size":
        return { type: "Physical", data: { width: window.innerWidth * 2, height: window.innerHeight * 2 } };
      case "plugin:window|inner_position":
      case "plugin:window|outer_position":
        return { type: "Physical", data: { x: 0, y: 0 } };

      // ---- deep links / shortcuts / misc ----
      case "plugin:deep-link|get_current": return null;
      case "plugin:deep-link|register":
      case "plugin:deep-link|unregister": return null;
      case "plugin:global-shortcut|register":
      case "plugin:global-shortcut|register_all":
      case "plugin:global-shortcut|unregister":
      case "plugin:global-shortcut|unregister_all": return null;
      case "plugin:global-shortcut|is_registered": return false;
      case "plugin:opener|open_url":
      case "plugin:opener|open_path":
      case "plugin:shell|open": return null;
      case "plugin:clipboard-manager|write_text": return null;
      case "plugin:clipboard-manager|read_text": return "";
      case "plugin:log|log": return null;
      case "plugin:window-state|save_window_state":
      case "plugin:window-state|restore_state": return null;

      // ---- gratefulagents' custom Rust commands ----
      case "detect_local_credentials": return cfg.localCredentials;
      case "cancel_openai_oauth": return null;
      case "start_openai_oauth":
      case "start_anthropic_oauth":
      case "complete_anthropic_oauth":
      case "start_copilot_oauth":
      case "poll_copilot_oauth":
        throw new Error("selfdev tauri-sim: OAuth flows are not available in simulation");
      case "plugin:google-oauth|start_google_oauth":
        return { idToken: null, error: "cancelled" };

      default: {
        if (cmd.startsWith("plugin:window|") || cmd.startsWith("plugin:webview|")) return null;
        console.warn("selfdev tauri-sim: unhandled invoke:", cmd);
        return null;
      }
    }
  }

  window.__TAURI_INTERNALS__ = {
    invoke,
    transformCallback,
    convertFileSrc: (filePath) => filePath,
    metadata: {
      currentWindow: { label: "main" },
      currentWebview: { label: "main", windowLabel: "main" },
    },
    plugins: { path: { sep: "/", delimiter: ":" } },
  };
})();`;
}
