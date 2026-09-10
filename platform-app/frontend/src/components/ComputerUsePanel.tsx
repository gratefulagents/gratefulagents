import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import {
  AlertTriangle, AppWindow, ChevronDown, Eye, Globe, Keyboard, Monitor, MousePointer2, MousePointerClick,
  Move, MoveVertical, Pause, Play, ShieldAlert, Square, Type as TypeIcon, Zap,
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
  computerUsePermissions, openComputerUsePermission, pickComputerUseWindow, hotkeyGlyphs, startDesktopSession,
  desktopSessionStatus, heartbeatDesktopSession, pauseDesktopSession,
  resumeDesktopSession, stopDesktopSession, captureDesktopWindow,
  queueDesktopRequest, armDesktopRequest, approveDesktopRequest,
  cancelDesktopRequest,
  type DesktopAction, type DesktopSession, type DesktopRequest, type DesktopOutcome,
  type WindowCapture, type WindowTarget, type WindowMetadata, type DesktopMode,
} from "@/lib/computer-use";
import { exchangeDesktopRelay } from "@/lib/computer-use-relay";
import {
  APPROVAL_MODE_META, isReadOnlyAction, modeAutoApproves, setComputerUseApprovalMode, useComputerUseApprovalMode,
} from "@/lib/computer-use-preferences";

type ActionKind = DesktopAction["kind"];
type Activity = { id: string; kind: ActionKind; status: string; summary: string; detail?: string; at: number; auto: boolean };

const ACTION_META: Record<ActionKind, { label: string; icon: ReactNode }> = {
  list_windows: { label: "List windows", icon: <AppWindow /> },
  select_window: { label: "Select window", icon: <AppWindow /> },
  observe: { label: "Observe", icon: <Eye /> },
  click: { label: "Click", icon: <MousePointerClick /> },
  move: { label: "Move pointer", icon: <MousePointer2 /> },
  drag: { label: "Drag", icon: <Move /> },
  scroll: { label: "Scroll", icon: <MoveVertical /> },
  type: { label: "Type text", icon: <TypeIcon /> },
  key: { label: "Key press", icon: <Keyboard /> },
  activate: { label: "Activate app", icon: <AppWindow /> },
  open_url: { label: "Open URL", icon: <Globe /> },
};

const PHASE_TONE: Record<DesktopSession["phase"], StatusTone> = { stopped: "neutral", active: "running", paused: "warning" };
const OUTCOME_TONE: Record<string, StatusTone> = { completed: "success", failed: "danger", denied: "neutral" };

const CLICK_LABEL: Record<string, string> = {
  "left-1": "Click", "left-2": "Double-click", "left-3": "Triple-click",
  "right-1": "Right-click", "right-2": "Double right-click", "right-3": "Triple right-click",
  "middle-1": "Middle-click", "middle-2": "Double middle-click", "middle-3": "Triple middle-click",
};
function clickLabel(action: Extract<DesktopAction, { kind: "click" }>): string {
  return CLICK_LABEL[`${action.button ?? "left"}-${action.count ?? 1}`] ?? "Click";
}

function HotkeyKeys({ value }: { value: string }) {
  return <span className="inline-flex items-center gap-0.5 align-middle">
    {hotkeyGlyphs(value).map((glyph, index) => <Kbd key={index}>{glyph}</Kbd>)}
  </span>;
}

function describeAction(request: DesktopRequest, windows: WindowMetadata[]): ReactNode {
  const { action } = request;
  switch (action.kind) {
    case "list_windows": return <>Share eligible window names and titles (no screenshots).</>;
    case "select_window": {
      const target = windows.find((window) => window.ref === action.targetRef);
      return <>Switch target to {target ? `${target.application} — ${target.title}` : "an expired target"}. Old frames and target-specific grants will be cleared.</>;
    }
    case "observe": return <>Share a fresh capture for analysis: {action.question || "Describe the approved window"}</>;
    case "click": return <>{clickLabel(action)} pixel ({action.x}, {action.y}) in frame {request.frameId}.</>;
    case "move": return <>Move the pointer to pixel ({action.x}, {action.y}) without clicking (hover).</>;
    case "drag": return <>Press the left button at ({action.x}, {action.y}), drag to ({action.toX}, {action.toY}), and release.</>;
    case "scroll": return <>Scroll horizontally {action.deltaX}, vertically {action.deltaY} pixels (positive: right/down){action.x !== undefined ? <> with the pointer at ({action.x}, {action.y})</> : null}.</>;
    case "key": return <><span>Press {action.key}.</span> <HotkeyKeys value={action.key} /></>;
    case "activate": return <>Bring the approved application to the foreground.</>;
    case "open_url": return <>Open <span className="break-all font-mono">{action.url}</span> in the approved browser without bringing it forward.</>;
    case "type": return null;
  }
}

// Metadata-only narration for the timeline (Operator-style step list). Never
// includes the proposed text itself: the run history already carries it.
function summarizeAction(action: DesktopAction): string {
  switch (action.kind) {
    case "list_windows": return "Shared eligible window metadata";
    case "select_window": return "Selected a new target; fresh observation required";
    case "observe": return "Shared a capture for analysis";
    case "click": return `${clickLabel(action)}ed pixel (${action.x}, ${action.y})`;
    case "move": return `Moved the pointer to (${action.x}, ${action.y})`;
    case "drag": return `Dragged from (${action.x}, ${action.y}) to (${action.toX}, ${action.toY})`;
    case "scroll": return `Scrolled ${action.deltaX}, ${action.deltaY}px${action.x !== undefined ? ` at (${action.x}, ${action.y})` : ""}`;
    case "type": return `Typed ${action.text.length} character${action.text.length === 1 ? "" : "s"}`;
    case "key": return `Pressed ${action.key}`;
    case "activate": return "Brought the approved app forward";
    case "open_url": return `Opened ${action.url.length > 80 ? `${action.url.slice(0, 77)}…` : action.url}`;
  }
}

const timeFormat = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });

export function ComputerUsePanel({ namespace, name, enabled, model }: {
  namespace: string; name: string; enabled: boolean; model: string;
}) {
  const auth = useOptionalAuth();
  const user = auth?.user?.id;
  const [supported, setSupported] = useState<boolean | null>(null);
  const [selected, setSelected] = useState<WindowTarget | null>(null);
  const sharingOwned = useRef(false);
  const panelRef = useRef<HTMLDetailsElement>(null);
  const [consent, setConsent] = useState(false);
  const [mode, setMode] = useState<DesktopMode>("selected_window");
  const [candidates, setCandidates] = useState<WindowMetadata[]>([]);
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
  useEffect(() => {
    if (panelRef.current && (pending || session?.phase === "paused" || error)) {
      panelRef.current.open = true;
    }
  }, [pending, session?.phase, error]);

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
    setCandidates([]);
    setSelected(null);
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
      sharingOwned.current = false;
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
    if (isTauri && (localSession.current || sharingOwned.current)) void disconnect();
    else invalidate();
    setSelected(null);
    setActivity([]);
  }, [namespace, name, user, enabled, model, disconnect, invalidate]);

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
          if (status.targetRevision !== localSession.current?.targetRevision) {
            setPreview(null);
            setSessionAllowed(new Set());
            setConfirmed(false);
            setCandidates([]);
          }
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
    const target = selected;
    if ((mode === "selected_window" && !target) || !user || !enabled || !consent || stopRequired) return;
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
      backend, user, namespace, run: name,
      ...(mode === "agent_choice" ? { mode, application: "", windowId: 0, processId: 0 } : { application: target!.application, windowId: target!.windowId, processId: target!.processId }),
    }, consent, observed.revision, mode === "selected_window" ? target!.selectionId : "");
    if (current !== generation.current) {
      // The panel was invalidated while native start was in flight: nothing
      // else holds this session, so revoke it rather than leaking it.
      await stopDesktopSession().catch(() => {});
      return;
    }
    localSession.current = started;
    try {
      if (!started.sessionId || !started.scope) throw new Error("Native session did not start");
      if (mode === "agent_choice" && (started.scope.mode !== mode || started.targetRevision !== 0)) throw new Error("Update the desktop app and reconnect to authorize agent-selected windows");
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
    if (allow && request.action.kind === "select_window" && !candidates.some((target) => target.ref === (request.action as { targetRef: string }).targetRef)) throw new Error("Target metadata expired; list windows again");
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
          // The native reason is forwarded so the agent can adapt instead of asking the user to look.
          outcome = { targetRevision: request.targetRevision, requestId: request.requestId, status: "failed", message: `Native validation rejected the request: ${String(localFailure).slice(0, 1024)}. Obtain a fresh observation and human approval.` };
          setError(String(localFailure));
          await cancelDesktopRequest(id, scope, request.requestId).catch(() => {});
        } else {
          mayHaveExecuted = true;
          outcome = await approveDesktopRequest(id, scope, request.requestId, permit);
        }
      } else {
        await exchangeDesktopRelay(id, scope, "claim", request.requestId);
        outcome = { targetRevision: request.targetRevision, requestId: request.requestId, status: "denied", message: "Denied by supervisor" };
      }
      if (!valid()) return;
      if (outcome.requestId !== request.requestId || (outcome.capture && request.action.kind !== "observe")) {
        throw new Error("Unexpected native response; stopped without sharing it");
      }
      if (request.action.kind === "observe" && outcome.status === "completed" && !outcome.capture) {
        // The broker rejects a completed observation without its capture.
        outcome = { ...outcome, status: "failed", message: "Native capture returned no image" };
      }
      if (scope.mode === "agent_choice") {
        const expected = (request.targetRevision ?? 0) + (request.action.kind === "select_window" && outcome.status === "completed" ? 1 : 0);
        const status = await desktopSessionStatus();
        if (!valid()) return;
        if (status.sessionId !== id || status.phase !== "active" || status.revision < (localSession.current?.revision ?? 0) || status.targetRevision !== expected || (outcome.targetRevision ?? 0) !== expected) throw new Error("Target authorization changed before delivery");
        if (outcome.target) {
          setPreview(null);
          setSessionAllowed(new Set());
          setCandidates([]);
          localSession.current = { ...status, scope };
          setSession(localSession.current);
        }
        if (request.action.kind === "list_windows" && outcome.status === "completed") setCandidates(outcome.windows ?? []);
      }
      if (outcome.capture) setPreview(outcome.capture);
      await exchangeDesktopRelay(id, scope, "resolve", request.requestId, outcome);
      if (!valid()) return;
      setActivity((old) => [{
        id: request.requestId, kind: request.action.kind, status: outcome.status, at: Date.now(), auto,
        summary: outcome.status === "completed" ? summarizeAction(request.action)
          : outcome.status === "denied" ? `Denied ${ACTION_META[request.action.kind].label.toLowerCase()}`
          : `${ACTION_META[request.action.kind].label} failed`,
        detail: outcome.status === "failed" ? outcome.message : undefined,
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

  if (!isTauri || !user) return null;
  // Viewers and finished runs get no controls unless a session or a pending
  // native stop still needs the operator's attention.
  if (!enabled && !session && !stopRequired && !busy) return null;
  if (supported === false && !session && !stopRequired) return <p className="border-t p-3 text-xs text-muted-foreground">
    Computer use requires macOS 15.2 or later. The rest of the app is unchanged.
  </p>;
  // Pointer markers are drawn only when the proposal targets the previewed frame.
  const inFrame = (x: number, y: number) => !!preview && x < preview.pixelWidth && y < preview.pixelHeight;
  const pointer = pending && preview && pending.frameId === preview.frameId &&
    (pending.action.kind === "click" || pending.action.kind === "move" || pending.action.kind === "drag" ||
      (pending.action.kind === "scroll" && pending.action.x !== undefined && pending.action.y !== undefined)) &&
    inFrame(pending.action.x!, pending.action.y!) &&
    (pending.action.kind !== "drag" || inFrame(pending.action.toX, pending.action.toY)) ? pending.action : null;
  const pointerTo = pointer?.kind === "drag" ? { x: pointer.toX, y: pointer.toY } : null;
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
    <details ref={panelRef} className="group/cu border-t text-sm">
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
            {session.scope.mode === "agent_choice" ? session.target ? `${session.target.application} — ${session.target.title}` : "Awaiting agent selection" : `${session.scope.application} · window ${session.scope.windowId}`}
          </span>
        )}
        <ApprovalModeBadge mode={approvalMode} />
        {pending && !pendingAuto && (
          <span className={cn("inline-flex h-5 items-center rounded-full px-2 text-[11px] font-medium", toneSoft.info)}>
            Needs your approval
          </span>
        )}
        {session && (
          <Button size="xs" variant="destructive" className="ml-auto"
            onClick={(event) => { event.preventDefault(); void disconnect(); }}>
            <Square data-icon="inline-start" /> Stop computer use
          </Button>
        )}
        <ChevronDown className="ml-auto size-4 shrink-0 text-muted-foreground transition-transform group-open/cu:rotate-180" />
      </summary>

      <div className="max-h-[50vh] space-y-3 overflow-y-auto px-3 pb-3 md:px-4">
        <p className="text-xs leading-relaxed text-muted-foreground">
          The agent works inside the current approved Mac window while you supervise. Window selection is not an OS sandbox, and
          on-screen content is untrusted: review the target and effect yourself. You remain responsible for every action taken.
          Emergency stop: <Kbd>⌃⌥⌘⎋</Kbd> or the native tray.
        </p>

        {session?.phase === "paused" && <p role="status" className="text-xs text-amber-700">Target unavailable or paused: {session.reason || "Paused by supervisor"}</p>}
        {busy && pending?.action.kind === "select_window" && <p role="status">Switching target under the local approval policy…</p>}

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
            <label className="block text-xs">Window access
              <select aria-label="Window access" value={mode} disabled={controlsLocked}
                className="mt-1 block h-8 w-full rounded-md border bg-background px-2 text-sm"
                onChange={(event) => { setMode(event.target.value as DesktopMode); setConsent(false); }}>
                <option value="selected_window">Selected window only</option>
                <option value="agent_choice">Agent chooses windows</option>
              </select>
            </label>
            {mode === "agent_choice" && <p className="text-xs text-muted-foreground">Connect without selecting a window. The agent may list eligible application/window names and choose or switch targets under your approval policy. Only the selected window is captured. This mode additionally requires broad macOS Screen Recording permission; selected-window-only mode does not. Use harmless test windows, not private content.</p>}
            {mode === "agent_choice" && <Button variant="outline" size="xs" disabled={controlsLocked || !consent}
              onClick={() => void operate(() => openComputerUsePermission("agent_screen_recording"))}>
              Enable Screen Recording for agent choice
            </Button>}
            {mode === "selected_window" && <div className="rounded-lg border p-3">
              <div className="mb-2 flex items-center justify-between gap-2">
                <span className="text-xs font-medium"><span className="mr-1.5 text-muted-foreground">1</span>Approved window</span>
                <Button variant="outline" size="xs" disabled={busy || !enabled || !supported || stopRequired}
                  onClick={() => void operate(async () => {
                    const current = generation.current;
                    sharingOwned.current = true;
                    setSelected(null);
                    setConsent(false);
                    const observed = await desktopSessionStatus();
                    if (current !== generation.current) return;
                    const target = await pickComputerUseWindow(observed.revision);
                    if (current === generation.current) setSelected(target);
                  })}>
                  <AppWindow data-icon="inline-start" />
                  Choose window with macOS
                </Button>
              </div>

              <p className="text-xs text-muted-foreground">
                {selected ? `${selected.application} — ${selected.title || `Window ${selected.windowId}`}` : "No window selected"}
              </p>
              <p className="mt-2 text-xs text-muted-foreground">
                macOS shares only the window you choose. The supervisor is excluded. No full-screen Screen Recording permission is needed.
              </p>
            </div>}


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
                  {mode === "agent_choice" && <>I explicitly allow the agent to discover eligible window names and titles, share that metadata with this run’s backend and configured model providers, and choose or switch the active window under my local approval policy. </>}
                  I consent to sharing approved captures with this run’s backend and configured vision service ({model || "configured model"}),
                  and to input {skipAll ? "executed automatically while approvals are skipped" : approvalMode === "assisted" ? "only after my approval, with read-only observations shared automatically" : "only after my approval"}.
                  Captures may contain private information; provider retention policies apply. Proposed text and visual analysis are part
                  of the model conversation/run history; the app does not separately log keystrokes.
                </span>
              </label>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" disabled={!enabled || (mode === "selected_window" && !selected) || !consent || busy || !supported || stopRequired}
                onClick={() => void operate(start)}>
                {busy ? <Spinner data-icon="inline-start" /> : <Play data-icon="inline-start" />}
                Start supervised session
              </Button>
              {selected && !busy && <Button size="sm" variant="outline" onClick={() => void disconnect()}>Clear selection</Button>}
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
                <span className="truncate">{session.scope.mode === "agent_choice" ? session.target ? `${session.target.application} — ${session.target.title}` : "Awaiting agent selection" : `${session.scope.application} · window ${session.scope.windowId}`}</span>
              </span>
            )}
            <ApprovalModeControl variant="compact" disabled={stopRequired || !enabled} className="ml-1" />
            <div className="ml-auto flex flex-wrap gap-1.5">
              <Button size="sm" variant="outline" disabled={controlsLocked || session.phase !== "active" || (session.scope?.mode === "agent_choice" && !session.target)} onClick={() => void operate(capture)}>
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
              {pointer && pointerTo && <svg aria-hidden="true" className="pointer-events-none absolute inset-0 size-full" viewBox={`0 0 ${preview.pixelWidth} ${preview.pixelHeight}`} preserveAspectRatio="none">
                <line x1={pointer.x!} y1={pointer.y!} x2={pointerTo.x} y2={pointerTo.y} stroke="var(--tone-danger)" strokeWidth={Math.max(2, preview.pixelWidth / 300)} strokeDasharray={`${preview.pixelWidth / 60} ${preview.pixelWidth / 120}`} vectorEffect="non-scaling-stroke" />
              </svg>}
              {pointer && <span aria-label={pointer.kind === "drag" ? "Proposed drag start" : pointer.kind === "click" ? "Proposed click location" : pointer.kind === "move" ? "Proposed pointer location" : "Proposed scroll location"}
                className={cn("pointer-events-none absolute size-5 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-[color:var(--tone-danger)] shadow-[0_0_0_2px_var(--color-background)]",
                  pointer.kind === "click" || pointer.kind === "drag" ? "bg-[color-mix(in_oklch,var(--tone-danger)_30%,transparent)]" : "border-dashed")}
                style={{ left: `${100 * pointer.x! / preview.pixelWidth}%`, top: `${100 * pointer.y! / preview.pixelHeight}%` }} />}
              {pointer && pointerTo && <span aria-label="Proposed drag destination"
                className="pointer-events-none absolute size-5 -translate-x-1/2 -translate-y-1/2 rounded-sm border-2 border-[color:var(--tone-danger)] bg-[color-mix(in_oklch,var(--tone-danger)_30%,transparent)] shadow-[0_0_0_2px_var(--color-background)]"
                style={{ left: `${100 * pointerTo.x / preview.pixelWidth}%`, top: `${100 * pointerTo.y / preview.pixelHeight}%` }} />}
            </div>
            <figcaption className="text-[11px] text-muted-foreground">
              Local preview, not shared · {preview.pixelWidth}×{preview.pixelHeight}px · frame {preview.frameId}
              {pointer?.kind === "click" ? " · red marker shows the proposed click" : pointer?.kind === "drag" ? " · red markers show the proposed drag path" : pointer?.kind === "move" ? " · dashed marker shows the proposed pointer position" : pointer?.kind === "scroll" ? " · dashed marker shows where scrolling is aimed" : ""}
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
                  : <p className="text-xs text-muted-foreground">{describeAction(pending, candidates)}</p>}
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
                    <span className="min-w-0 flex-1">
                      <span className="block truncate">
                        <span className="sr-only">{entry.kind} — {entry.status}</span>
                        {entry.summary}
                        {entry.auto && <span className="ml-1.5 text-muted-foreground">· auto-approved</span>}
                      </span>
                      {entry.detail && <span className="block truncate text-[11px] text-muted-foreground" title={entry.detail}>{entry.detail}</span>}
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
