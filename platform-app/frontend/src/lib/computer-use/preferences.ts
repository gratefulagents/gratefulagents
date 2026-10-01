import { useSyncExternalStore } from "react";

/**
 * `auto` runs every action as it arrives; `ask` holds input actions for an
 * explicit Allow / Deny (screenshots, zoom and cursor reads never ask).
 * Stored per device only.
 */
export type ApprovalMode = "auto" | "ask";

const KEY = "computer-use-approval-mode-v2";
const LEGACY_KEY = "computer-use-desktop-approval-mode";
const CHANGE_EVENT = "gratefulagents-computer-use-approval-mode";

export function getDefaultApprovalMode(): ApprovalMode {
  try {
    localStorage.removeItem(LEGACY_KEY);
    return localStorage.getItem(KEY) === "ask" ? "ask" : "auto";
  } catch {
    return "auto";
  }
}

export function setDefaultApprovalMode(mode: ApprovalMode): void {
  try {
    if (mode === "auto") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, mode);
  } catch {
    // Storage can be unavailable; open views still update for this page.
  }
  window.dispatchEvent(new Event(CHANGE_EVENT));
}

function subscribe(callback: () => void): () => void {
  const onStorage = (event: StorageEvent) => {
    if (event.key === null || event.key === KEY) callback();
  };
  window.addEventListener(CHANGE_EVENT, callback);
  window.addEventListener("storage", onStorage);
  return () => {
    window.removeEventListener(CHANGE_EVENT, callback);
    window.removeEventListener("storage", onStorage);
  };
}

export function useDefaultApprovalMode(): ApprovalMode {
  return useSyncExternalStore(subscribe, getDefaultApprovalMode, () => "auto");
}
