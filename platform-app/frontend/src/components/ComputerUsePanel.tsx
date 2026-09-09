import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import {
  AlertTriangle, AppWindow, ChevronDown, Eye, Keyboard, Monitor, MousePointerClick,
  MoveVertical, Pause, Play, RefreshCw, ShieldAlert, Square, Type as TypeIcon, Zap,
} from "lucide-react";
import { ApprovalModeBadge, ApprovalModeControl } from "@/components/ComputerUseApprovalMode";
import { useOptionalAuth } from "@/contexts/AuthContext";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { LiveDot } from "@/components/ui/live-dot";
import { Spinner } from "@/components/ui/spinner";
import { client } from "@/lib/client";
import { isDonePhase, toneSoft, toneText, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import { backendBaseUrl, isTauri } from "@/lib/platform";
import {
  computerUsePermissions, computerUseWindows, startDesktopSession,
  desktopSessionStatus, heartbeatDesktopSession, pauseDesktopSession,
  resumeDesktopSession, stopDesktopSession, captureDesktopWindow,
  queueDesktopRequest, armDesktopRequest, approveDesktopRequest,
  cancelDesktopRequest,
  type DesktopAction, type DesktopSession, type DesktopRequest, type DesktopOutcome,
  type WindowCapture, type WindowTarget,
} from "@/lib/computer-use";
import { exchangeDesktopRelay } from "@/lib/computer-use-relay";
import {
  APPROVAL_MODE_META, isReadOnlyAction, modeAutoApproves, setComputerUseApprovalMode, useComputerUseApprovalMode,
} from "@/lib/computer-use-preferences";

type ActionKind = DesktopAction["kind"];
type Activity = { id: string; kind: ActionKind; status: string; summary: string; at: number; auto: boolean };

const ACTION_META: Record<ActionKind, { label: string; icon: ReactNode }> = {
  observe: { label: "Observe", icon: <Eye /> },
  click: { label: "Click", icon: <MousePointerClick /> },
  scroll: { label: "Scroll", icon: <MoveVertical /> },
  type: { label: "Type text", icon: <TypeIcon /> },
  key: { label: "Key press", icon: <Keyboard /> },
  activate: { label: "Activate app", icon: <AppWindow /> },
};

const PHASE_TONE: Record<DesktopSession["phase"], StatusTone> = { stopped: "neutral", active: "running", paused: "warning" };
const OUTCOME_TONE: Record<string, StatusTone> = { completed: "success", failed: "danger", denied: "neutral" };

function describeAction(request: DesktopRequest): ReactNode {
  const { action } = request;
  switch (action.kind) {
    case "observe": return <>Share a fresh capture for analysis: {action.question || "Describe the approved window"}</>;
    case "click": return <>Click pixel ({action.x}, {action.y}) in frame {request.frameId}.</>;
    case "scroll": return <>Scroll horizontally {action.deltaX}, vertically {action.deltaY} pixels (positive: right/down).</>;
    case "key": return <>Press {action.key}.</>;
    case "activate": return <>Bring the approved application to the foreground.</>;
    case "type": return null;
  }
}

// Metadata-only narration for the timeline (Operator-style step list). Never
// includes the proposed text itself: the run history already carries it.
function summarizeAction(action: DesktopAction): string {
  switch (action.kind) {
    case "observe": return "Shared a capture for analysis";
    case "click": return `Clicked pixel (${action.x}, ${action.y})`;
    case "scroll": return `Scrolled ${action.deltaX}, ${action.deltaY}px`;
    case "type": return `Typed ${action.text.length} character${action.text.length === 1 ? "" : "s"}`;
    case "key": return `Pressed ${action.key}`;
    case "activate": return "Brought the approved app forward";
  }
}

const timeFormat = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });

export function ComputerUsePanel({ namespace, name, enabled, model }: {
  namespace: string; name: string; enabled: boolean; model: string;
}) {
  const auth = useOptionalAuth();
  const user = auth?.user?.id;
  const [supported, setSupported] = useState(false);
  const [windows, setWindows] = useState<WindowTarget[]>([]);
  const [selected, setSelected] = useState("");
  const [consent, setConsent] = useState(false);
  const [session, setSession] = useState<DesktopSession | null>(null);
  const [preview, setPreview] = useState<WindowCapture | null>(null);
  const [pending, setPending] = useState<DesktopRequest | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [activity, setActivity] = useState<Activity[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [stopRequired, setStopRequired] = useState(false);
  const approvalMode = useComputerUseApprovalMode();
  // Copilot-style "allow for this session": kinds the supervisor approved for
  // the rest of this native session. Cleared whenever the session ends.
  const [sessionAllowed, setSessionAllowed] = useState<ReadonlySet<ActionKind>>(() => new Set());
  const generation = useRef(0);
  const requestVersion = useRef(0);
  const revoked = useRef(true);
  const localSession = useRef<DesktopSession | null>(null);
  const pendingRef = useRef<DesktopRequest | null>(null);
  const inFlight = useRef<string | null>(null);
  const sessionId = session?.sessionId;
  const sessionScope = session?.scope;
  const invalidate = useCallback(() => {
    generation.current++;
    revoked.current = true;
    inFlight.current = null;
  }, []);

  const disconnect = useCallback(async (message = "") => {
    const previous = localSession.current;
    invalidate();
    const current = generation.current;
    setBusy(true);
    setStopRequired(true);
    setPreview(null);
    pendingRef.current = null;
    setPending(null);
    setConfirmed(false);
    setConsent(false);
    setSessionAllowed(new Set());
    setError(message);
    // Native revocation is first and never waits for the network.
    const nativeStop = stopDesktopSession();
    if (previous?.sessionId && previous.scope) {
      void exchangeDesktopRelay(previous.sessionId, previous.scope, "stop").catch(() => {});
    }
    try {
      await nativeStop;
      if (current !== generation.current) return;
      setStopRequired(false);
      localSession.current = null;
      setSession(null);
    } catch {
      if (current !== generation.current) return;
      setError("Cannot confirm native stop. Press Control+Option+Command+Escape or use the native tray.");
    }
    setBusy(false);
  }, [invalidate]);

  useEffect(() => {
    if (!isTauri) return;
    let mounted = true;
    void computerUsePermissions().then((status) => {
      if (mounted) setSupported(status.supported);
    }).catch((cause: unknown) => {
      if (mounted) setError(String(cause));
    });
    return () => { mounted = false; };
  }, []);

  useEffect(() => () => {
    // Only a session that this panel actually holds needs native revocation;
    // stopping without one would surface a misleading emergency-stop alert.
    if (isTauri && localSession.current) void disconnect();
    setWindows([]);
    setSelected("");
    setActivity([]);
  }, [namespace, name, user, enabled, model, disconnect]);

  useEffect(() => {
    if (!sessionId || !sessionScope || !enabled) return;
    const scope = sessionScope;
    const current = generation.current;
    let alive = true;
    const valid = () => alive && current === generation.current && !revoked.current;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      if (!valid()) return;
      try {
        const version = requestVersion.current;
        if (backendBaseUrl() !== scope.backend || user !== scope.user) throw new Error("Desktop identity or backend changed");
        // Each exchange rechecks ownership and the actual agent pod server-side.
        const relay = await exchangeDesktopRelay(sessionId, scope, "poll");
        if (!valid()) return;
        if (backendBaseUrl() !== scope.backend) throw new Error("Desktop backend changed");
        if (!relay.active || !relay.visionAvailable) throw new Error(relay.reason || "Desktop agent connection ended");
        // The native lease is renewed only after the backend confirmed the run.
        await heartbeatDesktopSession(sessionId, scope);
        if (!valid()) return;
        const status = await desktopSessionStatus();
        if (!valid()) return;
        if (status.sessionId !== sessionId || status.phase === "stopped") {
          await disconnect(status.reason || "Native desktop session ended");
          return;
        }
        if (status.revision >= (localSession.current?.revision ?? 0)) {
          if (status.phase !== "active") {
            setPreview(null);
            setConfirmed(false);
          }
          localSession.current = { ...status, scope };
          setSession(localSession.current);
        }
        if (!inFlight.current && version === requestVersion.current) {
          const next = relay.pending ?? null;
          if (JSON.stringify(pendingRef.current) !== JSON.stringify(next)) {
            pendingRef.current = next;
            setPending(next);
            setConfirmed(false);
          }
        }
        timer = setTimeout(() => void poll(), 1500);
      } catch (cause) {
        if (valid()) await disconnect(String(cause));
      }
    };
    void poll();
    return () => { alive = false; clearTimeout(timer); };
  }, [sessionId, sessionScope, enabled, namespace, name, user, disconnect]);

  async function operate(work: () => Promise<void>) {
    const current = generation.current;
    setBusy(true);
    setError("");
    try { await work(); }
    catch (cause) {
      if (generation.current === current) setError(String(cause));
    } finally {
      if (generation.current === current) setBusy(false);
    }
  }

  async function start() {
    const target = windows.find((window) => String(window.windowId) === selected);
    if (!target || !user || !enabled || !consent || stopRequired) return;
    const current = generation.current;
    const backend = backendBaseUrl();
    const run = await client.getAgentRun({ namespace, name }, { timeoutMs: 4000 });
    if (run.namespace !== namespace || run.name !== name ||
        !["owner", "admin"].includes(run.myPermission) || isDonePhase(run.phase)) {
      throw new Error("Only a run owner or admin can start desktop supervision on an unfinished run");
    }
    if (current !== generation.current) return;
    const observed = await desktopSessionStatus();
    if (current !== generation.current) return;
    if (backend !== backendBaseUrl()) throw new Error("Backend changed; grant fresh consent");
    const started = await startDesktopSession({
      backend, user, namespace, run: name, application: target.application,
      windowId: target.windowId, processId: target.processId,
    }, consent, observed.revision);
    if (current !== generation.current) {
      // The panel was invalidated while native start was in flight: nothing
      // else holds this session, so revoke it rather than leaking it.
      await stopDesktopSession().catch(() => {});
      return;
    }
    localSession.current = started;
    try {
      if (!started.sessionId || !started.scope) throw new Error("Native session did not start");
      const relay = await exchangeDesktopRelay(started.sessionId, started.scope, "attach");
      if (current !== generation.current) {
        await exchangeDesktopRelay(started.sessionId, started.scope, "stop").catch(() => {});
        return;
      }
      if (!relay.active) throw new Error("The agent is not available for computer use");
      if (!relay.visionAvailable) throw new Error("This run has no supported vision analyzer. Select a vision-capable provider/model before connecting.");
      // The attach round trip consumed part of the ten-second native lease;
      // renew it now that the backend confirmed so the first poll has full margin.
      await heartbeatDesktopSession(started.sessionId, started.scope);
      if (current !== generation.current) return;
      const status = await desktopSessionStatus();
      if (current !== generation.current) return;
      if (status.sessionId !== started.sessionId || status.phase !== "active") throw new Error("Native authorization ended while connecting");
      revoked.current = false;
      localSession.current = { ...status, scope: started.scope };
      setSession(localSession.current);
      pendingRef.current = relay.pending ?? null;
      setPending(pendingRef.current);
      setActivity([]);
      setSessionAllowed(new Set());
      setPreview(null);
      setConfirmed(false);
    } catch (cause) {
      if (current === generation.current) await disconnect(String(cause));
      else if (started.sessionId && started.scope) {
        await exchangeDesktopRelay(started.sessionId, started.scope, "stop").catch(() => {});
      }
    }
  }

  async function capture() {
    if (revoked.current) throw new Error("Desktop session revoked; confirm stop before starting again");
    if (!session?.sessionId || !session.scope) return;
    const current = generation.current;
    const image = await captureDesktopWindow(session.sessionId, session.scope);
    if (current === generation.current && !revoked.current && backendBaseUrl() === session.scope.backend &&
        localSession.current?.phase === "active" && localSession.current.revision === session.revision) setPreview(image);
  }

  const autoApproves = (kind: ActionKind) => modeAutoApproves(approvalMode, kind) || sessionAllowed.has(kind);

  // Auto-approval: the moment a request lands while the session is active,
  // it is approved on the supervisor's behalf if the approval mode or a
  // session allowance covers its kind. The same native validation, claim, arm
  // and permit path runs; only the human confirmation is skipped.
  const autoDecide = useRef<() => void>(() => {});
  useEffect(() => {
    autoDecide.current = () => {
      if (!pending || !autoApproves(pending.action.kind)) return;
      if (busy || inFlight.current || revoked.current || stopRequired || !enabled || session?.phase !== "active") return;
      void operate(() => decide(true, { auto: true }));
    };
  });
  useEffect(() => { autoDecide.current(); }, [approvalMode, sessionAllowed, pending, busy, session, enabled, stopRequired]);

  async function decide(allow: boolean, { auto = false } = {}) {
    if (revoked.current || inFlight.current || !pending || !session?.sessionId || !session.scope) return;
    if (allow && ((!confirmed && !auto) || session.phase !== "active")) return;
    const request = pending;
    const id = session.sessionId;
    const scope = session.scope;
    const current = generation.current;
    const valid = () => current === generation.current && !revoked.current && backendBaseUrl() === scope.backend;
    inFlight.current = request.requestId;
    requestVersion.current++;
    setConfirmed(false);
    let mayHaveExecuted = false;
    try {
      let outcome: DesktopOutcome;
      if (allow) {
        // Native validation (queue) runs before the claim so a local rejection
        // can be reported without ever authorizing input. Arming comes after the
        // claim round trip so the short-lived permit covers only native execution.
        let localFailure: unknown = null;
        let permit: string | null = null;
        try { await queueDesktopRequest(id, scope, request); }
        catch (cause) { localFailure = cause; }
        if (!valid()) return;
        const relay = await exchangeDesktopRelay(id, scope, "claim", request.requestId);
        if (!valid()) return;
        if (!relay.active) throw new Error("The remote request expired");
        if (localFailure === null) {
          if (!relay.visionAvailable) throw new Error("The remote request expired or vision is unavailable");
          try { permit = (await armDesktopRequest(id, scope, request.requestId)).permit; }
          catch (cause) { localFailure = cause; }
          if (!valid()) return;
        }
        if (permit === null) {
          // Report local validation failure without ever authorizing an input.
          outcome = { requestId: request.requestId, status: "failed", message: "Native validation rejected the request; obtain a fresh observation and human approval." };
          setError(String(localFailure));
          await cancelDesktopRequest(id, scope, request.requestId).catch(() => {});
        } else {
          mayHaveExecuted = true;
          outcome = await approveDesktopRequest(id, scope, request.requestId, permit);
        }
      } else {
        await exchangeDesktopRelay(id, scope, "claim", request.requestId);
        outcome = { requestId: request.requestId, status: "denied", message: "Denied by supervisor" };
      }
      if (!valid()) return;
      if (outcome.requestId !== request.requestId || (outcome.capture && request.action.kind !== "observe")) {
        throw new Error("Unexpected native response; stopped without sharing it");
      }
      if (request.action.kind === "observe" && outcome.status === "completed" && !outcome.capture) {
        // The broker rejects a completed observation without its capture.
        outcome = { ...outcome, status: "failed", message: "Native capture returned no image" };
      }
      if (outcome.capture) setPreview(outcome.capture);
      await exchangeDesktopRelay(id, scope, "resolve", request.requestId, outcome);
      if (!valid()) return;
      setActivity((old) => [{
        id: request.requestId, kind: request.action.kind, status: outcome.status, at: Date.now(), auto,
        summary: outcome.status === "completed" ? summarizeAction(request.action)
          : outcome.status === "denied" ? `Denied ${ACTION_META[request.action.kind].label.toLowerCase()}`
          : `${ACTION_META[request.action.kind].label} failed`,
      }, ...old].slice(0, 20));
      if (outcome.status === "failed") setError(`${outcome.message} The action may be partially applied. Do not retry automatically.`);
      pendingRef.current = null;
      setPending(null);
    } catch {
      if (valid()) await disconnect(mayHaveExecuted
        ? "Action result could not be confirmed. It may already have happened. Session stopped; inspect the app before any retry."
        : "The request expired or the connection failed. Session stopped without authorizing further input.");
    } finally {
      if (current === generation.current && inFlight.current === request.requestId) {
        inFlight.current = null;
        requestVersion.current++;
      }
    }
  }

  if (!isTauri || !user || (!supported && !error)) return null;
  // Viewers and finished runs get no controls unless a session or a pending
  // native stop still needs the operator's attention.
  if (!enabled && !session && !stopRequired && !busy) return null;
  const click = pending?.action.kind === "click" && preview && pending.frameId === preview.frameId &&
    pending.action.x < preview.pixelWidth && pending.action.y < preview.pixelHeight ? pending.action : null;
  const phase = session?.phase ?? "stopped";
  const phaseLabel = phase.charAt(0).toUpperCase() + phase.slice(1);
  const controlsLocked = busy || stopRequired || !enabled;
  const canApprove = !controlsLocked && phase === "active";
  const pendingMeta = pending ? ACTION_META[pending.action.kind] : null;
  const pendingAuto = !!pending && autoApproves(pending.action.kind);
  const pendingAutoReason = pending && sessionAllowed.has(pending.action.kind) && !modeAutoApproves(approvalMode, pending.action.kind)
    ? `${ACTION_META[pending.action.kind].label} is allowed for this session` : APPROVAL_MODE_META[approvalMode].label;
  const skipAll = approvalMode === "auto";
  const allowedList = [...sessionAllowed].map((kind) => ACTION_META[kind].label);

  return (
    <details className="group/cu border-t text-sm">
      <summary className="flex cursor-pointer list-none items-center gap-2.5 px-3 py-2 select-none hover:bg-muted/40 md:px-4 [&::-webkit-details-marker]:hidden">
        <span className="grid size-6 shrink-0 place-items-center rounded-md bg-muted/60 text-muted-foreground ring-1 ring-inset ring-border/60 [&_svg]:size-3.5">
          <Monitor />
        </span>
        <span className="font-medium">Computer use</span>
        <span role="status" aria-label={`Session ${phase}`}
          className={cn("inline-flex h-5 items-center gap-1.5 rounded-full px-2 text-[11px] font-medium", toneSoft[PHASE_TONE[phase]])}>
          <LiveDot tone={phase === "active" ? "running" : phase === "paused" ? "waiting" : "idle"} pulse={phase === "active"} size="xs" />
          {phaseLabel}
        </span>
        {session?.scope && (
          <span className="hidden min-w-0 truncate text-xs text-muted-foreground sm:inline">
            {session.scope.application} · window {session.scope.windowId}
          </span>
        )}
        <ApprovalModeBadge mode={approvalMode} />
        {pending && !pendingAuto && (
          <span className={cn("inline-flex h-5 items-center rounded-full px-2 text-[11px] font-medium", toneSoft.info)}>
            Needs your approval
          </span>
        )}
        <ChevronDown className="ml-auto size-4 shrink-0 text-muted-foreground transition-transform group-open/cu:rotate-180" />
      </summary>

      <div className="max-h-[50vh] space-y-3 overflow-y-auto px-3 pb-3 md:px-4">
        <p className="text-xs leading-relaxed text-muted-foreground">
          The agent works inside one approved Mac window while you supervise. Window selection is not an OS sandbox, and
          on-screen content is untrusted: review the target and effect yourself. You remain responsible for every action taken.
          Emergency stop: <Kbd>⌃⌥⌘⎋</Kbd> or the native tray.
        </p>

        {error && (
          <p role="alert" className={cn("flex items-start gap-2 rounded-md px-3 py-2 text-xs", toneSoft.danger)}>
            <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
            <span>{error}</span>
          </p>
        )}

        {skipAll && (
          <div className={cn("flex flex-wrap items-center gap-2 rounded-md px-3 py-2 text-xs", toneSoft.warning)} role="note">
            <ShieldAlert className="size-3.5 shrink-0" />
            <span className="min-w-0 flex-1">
              <span className="font-medium">Skipping all approvals.</span> Every agent request — including clicks, typing, and key presses — runs in the approved window without a per-action review.
            </span>
            <Button size="xs" variant="outline" onClick={() => setComputerUseApprovalMode("manual")}>Switch to manual</Button>
          </div>
        )}

        {!session && (
          <div className="space-y-3">
            <div className="rounded-lg border p-3">
              <div className="mb-2 flex items-center justify-between gap-2">
                <span className="text-xs font-medium"><span className="mr-1.5 text-muted-foreground">1</span>Approved window</span>
                <Button variant="outline" size="xs" disabled={busy || !enabled || !supported}
                  onClick={() => void operate(async () => {
                    const current = generation.current;
                    const available = await computerUseWindows();
                    if (current === generation.current) { setWindows(available); setSelected(""); }
                  })}>
                  <RefreshCw data-icon="inline-start" />
                  List open windows
                </Button>
              </div>
              <select aria-label="Approved window"
                className="block h-8 w-full rounded-md border bg-background px-2 text-sm disabled:opacity-50"
                value={selected} disabled={busy || !enabled || !windows.length} onChange={(event) => setSelected(event.target.value)}>
                <option value="">{windows.length ? "Select a window" : "List open windows first"}</option>
                {windows.map((window) => <option key={window.windowId} value={window.windowId}>{window.application} — {window.title}</option>)}
              </select>
              <p className="mt-1.5 text-[11px] text-muted-foreground">The agent can only see and act inside this one window. Prefer a test document with no private data.</p>
            </div>

            <div className="rounded-lg border p-3">
              <div className="mb-1.5 flex flex-wrap items-center justify-between gap-2">
                <span className="text-xs font-medium"><span className="mr-1.5 text-muted-foreground">2</span>Approval mode</span>
                <ApprovalModeControl variant="compact" disabled={busy || !enabled} />
              </div>
              <p className="text-[11px] text-muted-foreground">{APPROVAL_MODE_META[approvalMode].description} You can change this at any time, including during a session.</p>
            </div>

            <div className="rounded-lg border p-3">
              <span className="mb-2 block text-xs font-medium"><span className="mr-1.5 text-muted-foreground">3</span>Sharing consent</span>
              <label className="flex items-start gap-2 text-xs leading-relaxed">
                <input type="checkbox" className="mt-0.5 shrink-0" checked={consent} disabled={busy || !enabled} onChange={(event) => setConsent(event.target.checked)} />
                <span className="text-muted-foreground">
                  I consent to sharing approved captures with this run’s backend and configured vision service ({model || "configured model"}),
                  and to input {skipAll ? "executed automatically while approvals are skipped" : approvalMode === "assisted" ? "only after my approval, with read-only observations shared automatically" : "only after my approval"}.
                  Captures may contain private information; provider retention policies apply. Proposed text and visual analysis are part
                  of the model conversation/run history; the app does not separately log keystrokes.
                </span>
              </label>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" disabled={!enabled || !selected || !consent || busy || !supported || stopRequired}
                onClick={() => void operate(start)}>
                {busy ? <Spinner data-icon="inline-start" /> : <Play data-icon="inline-start" />}
                Start supervised session
              </Button>
              {busy && <Button size="sm" variant="destructive" onClick={() => void disconnect()}>Cancel connection</Button>}
              {stopRequired && !busy && <Button size="sm" variant="destructive" onClick={() => void disconnect()}>Retry native stop</Button>}
            </div>
          </div>
        )}

        {session && (
          <div className="sticky top-0 z-10 -mx-3 flex flex-wrap items-center gap-2 border-y bg-background px-3 py-1.5 md:-mx-4 md:px-4">
            {session.scope && (
              <span className="flex min-w-0 items-center gap-1.5 text-xs">
                <AppWindow className="size-3.5 shrink-0 text-muted-foreground" />
                <span className="truncate"><span className="font-medium">{session.scope.application}</span><span className="text-muted-foreground"> · window {session.scope.windowId}</span></span>
              </span>
            )}
            <ApprovalModeControl variant="compact" disabled={stopRequired || !enabled} className="ml-1" />
            <div className="ml-auto flex flex-wrap gap-1.5">
              <Button size="sm" variant="outline" disabled={controlsLocked || session.phase !== "active"} onClick={() => void operate(capture)}>
                <Eye data-icon="inline-start" />
                Local preview
              </Button>
              <Button size="sm" variant="outline" disabled={controlsLocked}
                title={session.phase === "paused" ? "Let the agent act again" : "Take control: the agent cannot act until you resume"}
                onClick={() => void operate(async () => {
                if (revoked.current) throw new Error("Desktop session revoked");
                const current = generation.current;
                let status: DesktopSession;
                if (session.phase === "paused" && session.sessionId && session.scope) {
                  status = await resumeDesktopSession(session.sessionId, session.scope);
                } else {
                  await pauseDesktopSession();
                  status = await desktopSessionStatus();
                }
                if (current === generation.current && !revoked.current) {
                  setPreview(null); setConfirmed(false); setSession(status); localSession.current = status;
                }
              })}>
                {session.phase === "paused" ? <Play data-icon="inline-start" /> : <Pause data-icon="inline-start" />}
                {session.phase === "paused" ? "Resume" : "Pause"}
              </Button>
              <Button size="sm" variant="destructive" onClick={() => void disconnect()}>
                <Square data-icon="inline-start" />
                Stop computer use
              </Button>
            </div>
          </div>
        )}

        {session && allowedList.length > 0 && (
          <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
            <span>Allowed for this session:</span>
            {allowedList.map((label) => <span key={label} className={cn("inline-flex h-5 items-center rounded-full px-2 font-medium", toneSoft.info)}>{label}</span>)}
            <Button size="xs" variant="ghost" onClick={() => setSessionAllowed(new Set())}>Reset</Button>
          </div>
        )}

        {preview && (
          <figure className="space-y-1">
            <div className="relative inline-block max-w-full overflow-hidden rounded-md border bg-muted/30">
              <img src={preview.dataUrl} alt="Preview of the approved desktop window" className="max-h-80 w-auto max-w-full" />
              {click && <span aria-label="Proposed click location"
                className="pointer-events-none absolute size-5 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-[color:var(--tone-danger)] bg-[color-mix(in_oklch,var(--tone-danger)_30%,transparent)] shadow-[0_0_0_2px_var(--color-background)]"
                style={{ left: `${100 * click.x / preview.pixelWidth}%`, top: `${100 * click.y / preview.pixelHeight}%` }} />}
            </div>
            <figcaption className="text-[11px] text-muted-foreground">
              Local preview, not shared · {preview.pixelWidth}×{preview.pixelHeight}px · frame {preview.frameId}{click ? " · red marker shows the proposed click" : ""}
            </figcaption>
          </figure>
        )}

        {pending && pendingMeta && (
          <section aria-label="Action awaiting approval"
            className={cn("space-y-3 rounded-lg border p-3", pendingAuto ? "border-[color-mix(in_oklch,var(--tone-warning)_45%,transparent)]" : "border-[color-mix(in_oklch,var(--tone-info)_45%,transparent)]")}>
            <div className="flex items-start gap-2.5">
              <span className={cn("grid size-7 shrink-0 place-items-center rounded-md [&_svg]:size-4", toneSoft[pendingAuto ? "warning" : "info"])}>
                {pendingMeta.icon}
              </span>
              <div className="min-w-0 flex-1 space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <h3 className="text-[13px] font-medium leading-7">Agent requests: {pending.action.kind}</h3>
                  <span className={cn("inline-flex h-5 items-center rounded-full px-2 text-[11px] font-medium", isReadOnlyAction(pending.action.kind) ? toneSoft.neutral : toneSoft.warning)}>
                    {isReadOnlyAction(pending.action.kind) ? "Read-only" : "Enters input"}
                  </span>
                </div>
                {pending.action.kind === "type"
                  ? <>
                    <p className="text-xs text-muted-foreground">Type exactly the text below into the focused field:</p>
                    <pre aria-label="Proposed text" className="max-h-32 overflow-auto whitespace-pre-wrap break-words rounded-md border bg-muted/40 p-2 font-mono text-xs">{pending.action.text}</pre>
                  </>
                  : <p className="text-xs text-muted-foreground">{describeAction(pending)}</p>}
              </div>
            </div>

            {pendingAuto
              ? <div className={cn("flex items-center gap-2 rounded-md px-3 py-2 text-xs", toneSoft.warning)}>
                {busy ? <Spinner className="size-3.5" /> : <Zap className="size-3.5" />}
                <span className="flex-1">{busy ? "Approving automatically…" : phase === "active" ? "Will be approved automatically." : "Automatic approval waits until the session is resumed."} <span className="opacity-80">({pendingAutoReason})</span></span>
              </div>
              : <label className="flex items-start gap-2 text-xs leading-relaxed">
                <input type="checkbox" className="mt-0.5 shrink-0" checked={confirmed} disabled={!canApprove} onChange={(event) => setConfirmed(event.target.checked)} />
                <span className="text-muted-foreground">I reviewed this target and action, including any send, submit, deletion, purchase, or security effect. Never enter passwords.</span>
              </label>}

            <div className="flex flex-wrap gap-2">
              {!pendingAuto && <>
                <Button size="sm" disabled={!canApprove || !confirmed} onClick={() => void operate(() => decide(true))}>Allow once</Button>
                <Button size="sm" variant="outline" disabled={!canApprove || !confirmed}
                  title={`Approve every "${pendingMeta.label.toLowerCase()}" request until this session stops`}
                  onClick={() => {
                    const kind = pending.action.kind;
                    setSessionAllowed((old) => new Set([...old, kind]));
                    void operate(() => decide(true));
                  }}>Allow for this session</Button>
              </>}
              <Button size="sm" variant={pendingAuto ? "outline" : "ghost"} disabled={controlsLocked} onClick={() => void operate(() => decide(false))}>Deny</Button>
            </div>
          </section>
        )}

        {session && !pending && (
          <p className="flex items-center gap-2 text-xs text-muted-foreground">
            <LiveDot tone={phase === "active" ? "running" : "idle"} pulse={phase === "active"} size="xs" />
            {phase === "paused"
              ? "Paused — you have control. Resume to let the agent act again."
              : skipAll
                ? "Waiting for an agent request. Requests will run automatically while approvals are skipped."
                : approvalMode === "assisted"
                  ? "Waiting for an agent request. Read-only observations run automatically; input asks first."
                  : "Waiting for an agent request. Nothing executes without your approval."}
          </p>
        )}

        {!!activity.length && (
          <div className="space-y-1.5">
            <h4 className="text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">Recent actions</h4>
            <ol aria-label="Recent computer actions" aria-live="polite" className="divide-y rounded-md border text-xs">
              {activity.map((entry) => {
                const tone = OUTCOME_TONE[entry.status] ?? "neutral";
                return (
                  <li key={entry.id} className="flex items-center gap-2 px-2.5 py-1.5">
                    <span className={cn("grid size-5 shrink-0 place-items-center [&_svg]:size-3.5", toneText[tone])}>{ACTION_META[entry.kind].icon}</span>
                    <span className="min-w-0 flex-1 truncate">
                      <span className="sr-only">{entry.kind} — {entry.status}</span>
                      {entry.summary}
                      {entry.auto && <span className="ml-1.5 text-muted-foreground">· auto-approved</span>}
                    </span>
                    <time className="shrink-0 font-mono text-[10.5px] text-muted-foreground" dateTime={new Date(entry.at).toISOString()}>{timeFormat.format(entry.at)}</time>
                    <span className={cn("inline-flex h-4.5 shrink-0 items-center rounded-full px-1.5 text-[10.5px] font-medium", toneSoft[tone])}>{entry.status}</span>
                  </li>
                );
              })}
            </ol>
          </div>
        )}
      </div>
    </details>
  );
}
