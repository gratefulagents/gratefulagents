import { useSyncExternalStore } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { isObservation } from "./describe";
import { executeAction, onNativeSession, setNativePaused, startNativeSession, stopNativeSession, type NativeSession } from "./native";
import { getDefaultApprovalMode, type ApprovalMode } from "./preferences";
import { exchange, RelayProtocolError, type RelayRequest, type RelayResponse, type RelayResult, type RunRef } from "./relay";
import type { ComputerAction } from "./native";

export type Phase = "idle" | "starting" | "active" | "paused" | "stopping" | "error";

export interface TimelineEntry {
  id: string;
  action: ComputerAction;
  status: "running" | "done" | "failed" | "denied";
  error?: string;
  startedAt: number;
  finishedAt?: number;
  thumbnail?: string;
}

export interface Frame {
  dataUrl: string;
  width: number;
  height: number;
  at: number;
  cursor?: { x: number; y: number };
}

export interface ComputerUseState {
  phase: Phase;
  run?: RunRef;
  displayId?: number;
  sessionId?: string;
  approvalMode: ApprovalMode;
  pending?: { request: RelayRequest; needsApproval: boolean };
  current?: RelayRequest;
  timeline: TimelineEntry[];
  latest?: Frame;
  /** False when the relay reports the run cannot use the tool (read-only or not registered). */
  available: boolean;
  error?: string;
  /** Why the last session ended when it was not stopped from this app (emergency stop, run ended, …). */
  stoppedReason?: string;
}

const TIMELINE_LIMIT = 50;
const BACKOFF_MIN = 250;
const BACKOFF_MAX = 5_000;
const GIVE_UP_AFTER = 45_000;
const FATAL_CODES = new Set([Code.PermissionDenied, Code.NotFound, Code.InvalidArgument, Code.Unimplemented]);
export const UNAVAILABLE_MESSAGE = "This run can't use the computer (read-only or tool unavailable).";
const DENIED_MESSAGE = "The user denied this action.";

let state: ComputerUseState = { phase: "idle", approvalMode: getDefaultApprovalMode(), timeline: [], available: true };
const listeners = new Set<() => void>();

function update(patch: Partial<ComputerUseState>) {
  state = { ...state, ...patch };
  for (const listener of listeners) listener();
}

export function getComputerUseState(): ComputerUseState {
  return state;
}

export function subscribeComputerUse(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useComputerUse(): ComputerUseState {
  return useSyncExternalStore(subscribeComputerUse, getComputerUseState, getComputerUseState);
}

interface Session {
  id: string;
  run: RunRef;
  abort: AbortController;
  unlisten?: () => void;
  decide?: (approved: boolean | null) => void;
  resume?: () => void;
}

let session: Session | null = null;
let teardownDone: Promise<void> = Promise.resolve();

const message = (error: unknown) => (error instanceof Error ? error.message : String(error)) || "Unknown error";

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => {
      clearTimeout(timer);
      resolve();
    }, { once: true });
  });
}

function teardown(s: Session, patch: Partial<ComputerUseState>, stopNative = true): Promise<void> {
  if (session !== s) return teardownDone;
  session = null;
  s.abort.abort();
  s.decide?.(null);
  s.resume?.();
  s.unlisten?.();
  update({ phase: "stopping", pending: undefined, current: undefined });
  teardownDone = Promise.allSettled([
    exchange(s.run, s.id, "disconnect"),
    stopNative ? stopNativeSession() : Promise.resolve(),
  ]).then(() => {
    update({ phase: "idle", error: undefined, ...patch });
  });
  return teardownDone;
}

const fail = (s: Session, error: string, available = true) => teardown(s, { phase: "error", error, available });

/** One relay round trip, retried with exponential backoff until it succeeds, the session ends, or ~45 s of failures. */
async function call(s: Session, operation: "next" | "result", result?: RelayResult): Promise<RelayResponse | null> {
  const failingSince = Date.now();
  let delay = BACKOFF_MIN;
  for (;;) {
    try {
      const response = await exchange(s.run, s.id, operation, { result, signal: s.abort.signal });
      return session === s ? response : null;
    } catch (error) {
      if (session !== s) return null;
      const fatal = error instanceof RelayProtocolError || FATAL_CODES.has(ConnectError.from(error).code);
      if (fatal || Date.now() - failingSince >= GIVE_UP_AFTER) {
        await fail(s, fatal ? message(error) : `Lost connection to the run: ${message(error)}`);
        return null;
      }
      await sleep(delay, s.abort.signal);
      delay = Math.min(delay * 2, BACKOFF_MAX);
    }
  }
}

async function apply(s: Session, response: RelayResponse): Promise<boolean> {
  if (session !== s) return false;
  if (state.available !== response.available) update({ available: response.available });
  if (response.active) return true;
  await teardown(s, { stoppedReason: response.reason || "The run ended the computer session." });
  return false;
}

async function untilResumed(s: Session): Promise<boolean> {
  while (session === s && state.phase === "paused") {
    await new Promise<void>((resolve) => { s.resume = resolve; });
    s.resume = undefined;
  }
  return session === s;
}

function finishEntry(id: string, patch: Partial<TimelineEntry>) {
  update({
    timeline: state.timeline.map((entry) => (entry.id === id ? { ...entry, ...patch, finishedAt: Date.now() } : entry)),
  });
}

async function handle(s: Session, request: RelayRequest): Promise<RelayResponse | null> {
  const asks = () => state.approvalMode === "ask" && !isObservation(request.action);
  update({ pending: { request, needsApproval: asks() } });
  if (!(await untilResumed(s))) return null;
  if (asks()) {
    update({ pending: { request, needsApproval: true } });
    const approved = await new Promise<boolean | null>((resolve) => { s.decide = resolve; });
    s.decide = undefined;
    if (approved === null || session !== s) return null;
    if (!approved) {
      const now = Date.now();
      update({
        pending: undefined,
        timeline: [{ id: request.id, action: request.action, status: "denied" as const, error: DENIED_MESSAGE, startedAt: now, finishedAt: now }, ...state.timeline].slice(0, TIMELINE_LIMIT),
      });
      return call(s, "result", { requestId: request.id, ok: false, denied: true, error: DENIED_MESSAGE });
    }
    if (!(await untilResumed(s))) return null;
  }

  update({
    pending: undefined,
    current: request,
    timeline: [{ id: request.id, action: request.action, status: "running" as const, startedAt: Date.now() }, ...state.timeline].slice(0, TIMELINE_LIMIT),
  });
  let result: RelayResult;
  try {
    const { screenshot, cursor } = await executeAction(request.action);
    const dataUrl = `data:${screenshot.mediaType};base64,${screenshot.data}`;
    finishEntry(request.id, { status: "done", thumbnail: dataUrl });
    // A zoom result is a magnified crop; keep the full-screen frame as the live view.
    if (request.action.action !== "zoom") update({ latest: { dataUrl, width: screenshot.width, height: screenshot.height, at: Date.now(), cursor } });
    result = { requestId: request.id, ok: true, screenshot, ...(cursor ? { cursor } : {}) };
  } catch (error) {
    finishEntry(request.id, { status: "failed", error: message(error) });
    result = { requestId: request.id, ok: false, error: message(error) };
  }
  if (session !== s) return null;
  update({ current: undefined });
  return call(s, "result", result);
}

async function loop(s: Session) {
  let response = await call(s, "next");
  while (response && (await apply(s, response))) {
    response = response.request ? await handle(s, response.request) : await call(s, "next");
  }
}

function onNative(s: Session, native: NativeSession) {
  if (session !== s || state.phase === "starting") return;
  if (!native.active) {
    void teardown(s, { stoppedReason: native.stoppedReason || "Computer use was stopped on this Mac." }, false);
    return;
  }
  update({ phase: native.paused ? "paused" : "active" });
  if (!native.paused) s.resume?.();
}

/** Starts controlling `displayId` for `run`. Any other session is stopped first. */
export async function startComputerUse(run: RunRef, displayId: number): Promise<void> {
  if (session) await stopComputerUse();
  else await teardownDone;
  const s: Session = { id: crypto.randomUUID(), run, abort: new AbortController() };
  session = s;
  update({
    phase: "starting", run, displayId, sessionId: s.id, approvalMode: getDefaultApprovalMode(), pending: undefined,
    current: undefined, timeline: [], latest: undefined, available: true, error: undefined, stoppedReason: undefined,
  });
  let paused: boolean;
  try {
    const unlisten = await onNativeSession((native) => onNative(s, native));
    if (session !== s) {
      unlisten();
      return;
    }
    s.unlisten = unlisten;
    paused = (await startNativeSession(displayId, `${run.namespace}/${run.name}`)).paused;
    if (session !== s) return;
    const response = await exchange(run, s.id, "connect", { signal: s.abort.signal });
    if (session !== s) return;
    if (!response.available) return fail(s, UNAVAILABLE_MESSAGE, false);
    if (!response.active) return fail(s, response.reason || "The run did not accept the desktop session.");
  } catch (error) {
    if (session === s) await fail(s, message(error));
    return;
  }
  update({ phase: paused ? "paused" : "active" });
  void loop(s);
}

export function stopComputerUse(): Promise<void> {
  return session ? teardown(session, { stoppedReason: undefined }) : teardownDone;
}

async function setPaused(paused: boolean) {
  const s = session;
  if (!s) return;
  try {
    const native = await setNativePaused(paused);
    if (session === s) onNative(s, native);
  } catch (error) {
    if (session === s) update({ error: message(error) });
  }
}

export const pauseComputerUse = () => setPaused(true);
export const resumeComputerUse = () => setPaused(false);

export function decideComputerUse(approved: boolean) {
  session?.decide?.(approved);
}

export function setSessionApprovalMode(mode: ApprovalMode) {
  update({ approvalMode: mode });
  const pending = state.pending;
  if (mode === "auto" && pending?.needsApproval) {
    update({ pending: { ...pending, needsApproval: false } });
    session?.decide?.(true);
  }
}
