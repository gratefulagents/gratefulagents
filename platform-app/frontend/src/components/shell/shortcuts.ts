import * as React from "react";
import { isTauri } from "@/lib/platform";
import { toggleTheme } from "@/lib/theme";

/** Registry of application shortcuts, rendered in the cheat-sheet overlay. */
export type ShortcutGroup = {
  group: string;
  items: Array<{
    keys: string[];
    label: string;
    hint?: string;
  }>;
};

export const APP_SHORTCUTS: ShortcutGroup[] = [
  {
    group: "Navigation",
    items: [
      { keys: ["⌘", "K"], label: "Open command palette" },
      { keys: ["⌘", "B"], label: "Toggle sidebar" },
      { keys: ["⌘", ","], label: "Settings" },
      { keys: ["⌘", "/"], label: "This cheat-sheet" },
      { keys: ["Esc"], label: "Close dialog / palette" },
    ],
  },
  {
    group: "Appearance",
    items: [
      { keys: ["⌘", "⇧", "L"], label: "Toggle light / dark" },
    ],
  },
  {
    group: "Runs",
    items: [
      { keys: ["↑", "↓"], label: "Move selection in palette / lists" },
      { keys: ["↩"], label: "Open focused item" },
      { keys: ["⌘", "↩"], label: "Send message" },
    ],
  },
  {
    group: "App",
    items: [
      { keys: ["⌘", "R"], label: "Reload" },
      { keys: ["⌘", "⇧", "R"], label: "Hard reload" },
    ],
  },
];

/**
 * Global shortcut handling. Minimal, keyboard-first, no library.
 * - ⌘K / Ctrl+K  → open palette
 * - ⌘,           → settings
 * - ⌘/           → shortcuts overlay
 * - ⌘⇧L          → toggle theme
 */
export function useGlobalShortcuts({
  onOpenPalette,
  onOpenSettings,
  onOpenShortcuts,
}: {
  onOpenPalette: () => void;
  onOpenSettings: () => void;
  onOpenShortcuts?: () => void;
}) {
  React.useEffect(() => {
    function onKey(e: KeyboardEvent) {
      const mod = e.metaKey || e.ctrlKey;
      if (!mod) return;

      const target = e.target as HTMLElement | null;
      const inField =
        !!target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.isContentEditable);

      if (e.key.toLowerCase() === "k" && !e.shiftKey && !e.altKey) {
        e.preventDefault();
        onOpenPalette();
        return;
      }
      if (e.key === "/" && !e.altKey) {
        e.preventDefault();
        onOpenShortcuts?.();
        return;
      }
      if (inField) return;
      if (e.key === "," && !e.shiftKey && !e.altKey) {
        e.preventDefault();
        onOpenSettings();
      }
      if (e.shiftKey && e.key.toLowerCase() === "l") {
        e.preventDefault();
        toggleTheme();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onOpenPalette, onOpenSettings, onOpenShortcuts]);
}

/**
 * iPad detection + responsive viewport hook.
 */
export function useViewport() {
  const [state, setState] = React.useState(() => ({
    width: typeof window === "undefined" ? 1400 : window.innerWidth,
    isTouch:
      typeof window !== "undefined" &&
      matchMedia("(hover: none) and (pointer: coarse)").matches,
  }));
  React.useEffect(() => {
    function onResize() {
      setState((s) => ({ ...s, width: window.innerWidth }));
    }
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);
  return {
    ...state,
    compact: state.width < 900,
    isTauri,
  };
}
