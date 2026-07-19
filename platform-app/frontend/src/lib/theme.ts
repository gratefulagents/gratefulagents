import { useSyncExternalStore } from "react";

import { setNativeWindowTheme } from "./native";

export type ThemeName = "light" | "dark";
/** User-facing preference: an explicit theme, or "system" to follow the OS. */
export type ThemePreference = ThemeName | "system";

const THEME_STORAGE_KEY = "theme";
const THEME_CHANGE_EVENT = "gratefulagents-theme-change";

function readStoredTheme(): ThemeName | null {
  if (typeof window === "undefined") return null;
  const stored = localStorage.getItem(THEME_STORAGE_KEY);
  return stored === "dark" || stored === "light" ? stored : null;
}

function getSystemTheme(): ThemeName {
  if (typeof window === "undefined") return "light";
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

export function getPreferredTheme(): ThemeName {
  return readStoredTheme() ?? getSystemTheme();
}

export function getAppliedTheme(): ThemeName {
  if (typeof document === "undefined") return getPreferredTheme();
  return document.documentElement.classList.contains("dark") ? "dark" : "light";
}

export function applyTheme(
  theme: ThemeName,
  options: { persist?: boolean } = {},
): ThemeName {
  if (typeof document === "undefined" || typeof window === "undefined") return theme;

  const { persist = true } = options;
  const root = document.documentElement;

  root.classList.toggle("dark", theme === "dark");
  root.style.colorScheme = theme;

  if (persist) {
    localStorage.setItem(THEME_STORAGE_KEY, theme);
  }

  // Keep native window chrome (menus, dialogs, vibrancy appearance) in step:
  // follow the pinned theme, or hand control back to the OS when unpinned.
  void setNativeWindowTheme(readStoredTheme());

  window.dispatchEvent(
    new CustomEvent(THEME_CHANGE_EVENT, {
      detail: { theme },
    }),
  );

  return theme;
}

export function initializeTheme(): ThemeName {
  return applyTheme(getPreferredTheme(), { persist: false });
}

export function toggleTheme(): ThemeName {
  return applyTheme(getAppliedTheme() === "dark" ? "light" : "dark");
}

export function getThemePreference(): ThemePreference {
  return readStoredTheme() ?? "system";
}

/**
 * Pin an explicit theme, or clear the pin ("system") so the app tracks the
 * OS appearance (both the media-query listener here and the Tauri OS-theme
 * subscription in App.tsx only apply when no theme is stored).
 */
export function setThemePreference(preference: ThemePreference): ThemeName {
  if (preference === "system") {
    if (typeof window !== "undefined") {
      localStorage.removeItem(THEME_STORAGE_KEY);
    }
    return applyTheme(getSystemTheme(), { persist: false });
  }
  return applyTheme(preference);
}

function subscribe(callback: () => void): () => void {
  if (typeof window === "undefined") return () => {};

  const onThemeChange = () => callback();
  const onStorage = (event: StorageEvent) => {
    if (event.key !== THEME_STORAGE_KEY) return;

    const nextTheme =
      event.newValue === "dark" || event.newValue === "light"
        ? event.newValue
        : getSystemTheme();

    applyTheme(nextTheme, { persist: false });
  };
  const mediaQuery = window.matchMedia("(prefers-color-scheme: dark)");
  const onMediaChange = () => {
    if (readStoredTheme()) return;
    applyTheme(getSystemTheme(), { persist: false });
  };

  window.addEventListener(THEME_CHANGE_EVENT, onThemeChange);
  window.addEventListener("storage", onStorage);
  mediaQuery.addEventListener("change", onMediaChange);

  return () => {
    window.removeEventListener(THEME_CHANGE_EVENT, onThemeChange);
    window.removeEventListener("storage", onStorage);
    mediaQuery.removeEventListener("change", onMediaChange);
  };
}

export function useTheme(): ThemeName {
  return useSyncExternalStore(subscribe, getAppliedTheme, getPreferredTheme);
}

const getServerPreference = (): ThemePreference => "system";

export function useThemePreference(): ThemePreference {
  return useSyncExternalStore(subscribe, getThemePreference, getServerPreference);
}
