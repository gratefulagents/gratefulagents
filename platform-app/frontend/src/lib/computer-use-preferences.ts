import { useSyncExternalStore } from "react";
import type { DesktopAction } from "./computer-use";

/**
 * Per-device supervisor preferences for computer use.
 *
 * The approval mode mirrors the graduated trust levels users know from
 * Claude in Chrome ("Manually approve / Automatically approve / Skip all
 * approvals") and Copilot agent mode ("Manual / Assisted / Allow all"):
 *
 * - `manual`   every request waits for an explicit per-action approval (default).
 * - `assisted` read-only requests (observe, scroll, activate) run automatically;
 *              anything that enters input (click, type, key) still asks.
 * - `auto`     every request runs as soon as it arrives.
 *
 * It lives in localStorage on this machine only (never in the backend) so it
 * cannot follow the user to another device, and it defaults to manual.
 */
export type ComputerUseApprovalMode = "manual" | "assisted" | "auto";

export const APPROVAL_MODES: ComputerUseApprovalMode[] = ["manual", "assisted", "auto"];

export const APPROVAL_MODE_META: Record<ComputerUseApprovalMode, {
  label: string;
  short: string;
  description: string;
}> = {
  manual: {
    label: "Manually approve",
    short: "Manual",
    description: "Every observation and input waits for you to review and allow it. The safest option.",
  },
  assisted: {
    label: "Automatically approve read-only",
    short: "Assisted",
    description: "Screen observations, scrolling, and bringing the app forward run automatically. Clicks, typing, and key presses still ask you first.",
  },
  auto: {
    label: "Skip all approvals",
    short: "Skip approvals",
    description: "Every request — including clicks, typed text, and key presses — runs immediately without a per-action review.",
  },
};

/** Actions that never enter input; assisted mode approves these automatically. */
const READ_ONLY_KINDS: ReadonlySet<DesktopAction["kind"]> = new Set(["observe", "scroll", "activate"]);

export function isReadOnlyAction(kind: DesktopAction["kind"]): boolean {
  return READ_ONLY_KINDS.has(kind);
}

/** Whether the given mode approves this kind of request without asking. */
export function modeAutoApproves(mode: ComputerUseApprovalMode, kind: DesktopAction["kind"]): boolean {
  return mode === "auto" || (mode === "assisted" && isReadOnlyAction(kind));
}

const MODE_KEY = "computer-use-approval-mode";
const CHANGE_EVENT = "gratefulagents-computer-use-preferences";

function isMode(value: unknown): value is ComputerUseApprovalMode {
  return typeof value === "string" && (APPROVAL_MODES as string[]).includes(value);
}

export function getComputerUseApprovalMode(): ComputerUseApprovalMode {
  if (typeof window === "undefined") return "manual";
  try {
    const stored = localStorage.getItem(MODE_KEY);
    return isMode(stored) ? stored : "manual";
  } catch {
    return "manual";
  }
}

export function setComputerUseApprovalMode(mode: ComputerUseApprovalMode): void {
  if (typeof window === "undefined") return;
  try {
    if (mode === "manual") localStorage.removeItem(MODE_KEY);
    else localStorage.setItem(MODE_KEY, mode);
  } catch {
    // Storage may be unavailable (private mode); the in-memory event still
    // updates open panels for this page lifetime.
  }
  window.dispatchEvent(new CustomEvent(CHANGE_EVENT, { detail: { mode } }));
}

function subscribe(callback: () => void): () => void {
  if (typeof window === "undefined") return () => {};
  const onStorage = (event: StorageEvent) => {
    if (event.key === null || event.key === MODE_KEY) callback();
  };
  window.addEventListener(CHANGE_EVENT, callback);
  window.addEventListener("storage", onStorage);
  return () => {
    window.removeEventListener(CHANGE_EVENT, callback);
    window.removeEventListener("storage", onStorage);
  };
}

const getServerSnapshot = (): ComputerUseApprovalMode => "manual";

/** Reactive view of the approval mode; updates across components and tabs. */
export function useComputerUseApprovalMode(): ComputerUseApprovalMode {
  return useSyncExternalStore(subscribe, getComputerUseApprovalMode, getServerSnapshot);
}
