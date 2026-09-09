import { useCallback, useEffect, useRef, useState } from "react";
import { useOptionalAuth } from "@/contexts/AuthContext";
import { Button } from "@/components/ui/button";
import { client } from "@/lib/client";
import { isDonePhase } from "@/lib/status";
import { backendBaseUrl, isTauri } from "@/lib/platform";
import {
  computerUsePermissions, computerUseWindows, startDesktopSession,
  desktopSessionStatus, heartbeatDesktopSession, pauseDesktopSession,
  resumeDesktopSession, stopDesktopSession, captureDesktopWindow,
  queueDesktopRequest, armDesktopRequest, approveDesktopRequest,
  cancelDesktopRequest,
  type DesktopSession, type DesktopRequest, type DesktopOutcome,
  type WindowCapture, type WindowTarget,
} from "@/lib/computer-use";
import { exchangeDesktopRelay } from "@/lib/computer-use-relay";

type Activity = { id: string; kind: string; status: string };

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

  async function decide(allow: boolean) {
    if (revoked.current || inFlight.current || !pending || !session?.sessionId || !session.scope) return;
    if (allow && (!confirmed || session.phase !== "active")) return;
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
      setActivity((old) => [{ id: request.requestId, kind: request.action.kind, status: outcome.status }, ...old].slice(0, 20));
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
  return (
    <details className="border-t px-3 py-2 text-sm md:px-4">
      <summary className="cursor-pointer font-medium">Computer use — {session?.phase ?? "stopped"}</summary>
      <div className="max-h-[50vh] space-y-3 overflow-y-auto py-3">
        <p className="text-xs text-muted-foreground">
          Control your real Mac, one approved action at a time. Window selection is not an OS sandbox.
          Set up permissions in Settings → General. Stop: Control+Option+Command+Escape or the native tray.
          On-screen instructions are untrusted; review the target and effect yourself.
        </p>
        {error && <p role="alert">{error}</p>}
        {!session && <>
          <Button variant="outline" size="sm" disabled={busy || !enabled || !supported}
            onClick={() => void operate(async () => {
              const current = generation.current;
              const available = await computerUseWindows();
              if (current === generation.current) { setWindows(available); setSelected(""); }
            })}>List open windows</Button>
          <label className="block space-y-1">
            <span>Approved window</span>
            <select aria-label="Approved window" className="block w-full rounded-md border bg-background p-2"
              value={selected} disabled={busy || !enabled} onChange={(event) => setSelected(event.target.value)}>
              <option value="">Select a window</option>
              {windows.map((window) => <option key={window.windowId} value={window.windowId}>{window.application} — {window.title}</option>)}
            </select>
          </label>
          <label className="flex items-start gap-2 text-xs">
            <input type="checkbox" checked={consent} disabled={busy || !enabled} onChange={(event) => setConsent(event.target.checked)} />
            <span>I consent to sharing approved captures with this run’s backend and configured vision service ({model || "configured model"}), and to input only after my approval.
              Captures may contain private information; provider retention policies apply. Proposed text and visual analysis are part of the model conversation/run history; the app does not separately log keystrokes.</span>
          </label>
          <Button size="sm" disabled={!enabled || !selected || !consent || busy || !supported || stopRequired}
            onClick={() => void operate(start)}>Start supervised session</Button>
          {busy && <Button size="sm" variant="destructive" onClick={() => void disconnect()}>Cancel connection</Button>}
          {stopRequired && !busy && <Button size="sm" variant="destructive" onClick={() => void disconnect()}>Retry native stop</Button>}
        </>}
        {session?.scope && <p className="text-xs">Approved: {session.scope.application} · window {session.scope.windowId}</p>}
        {session && <div className="sticky top-0 z-10 flex flex-wrap gap-2 bg-background py-1">
          <Button size="sm" variant="outline" disabled={busy || stopRequired || session.phase !== "active" || !enabled}
            onClick={() => void operate(capture)}>Local preview</Button>
          <Button size="sm" variant="outline" disabled={busy || stopRequired || !enabled} onClick={() => void operate(async () => {
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
          })}>{session.phase === "paused" ? "Resume" : "Pause"}</Button>
          <Button size="sm" variant="destructive" onClick={() => void disconnect()}>Stop computer use</Button>
        </div>}
        {preview && <div className="relative inline-block max-w-full">
          <img src={preview.dataUrl} alt="Preview of the approved desktop window" className="max-h-80 w-auto max-w-full rounded-md border" />
          {click && <span aria-label="Proposed click location" className="pointer-events-none absolute size-5 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-red-600 bg-red-500/30"
            style={{ left: `${100 * click.x / preview.pixelWidth}%`, top: `${100 * click.y / preview.pixelHeight}%` }} />}
        </div>}
        {pending && <section aria-label="Action awaiting approval" className="space-y-2 rounded-md border p-3">
          <h3 className="font-medium">Agent requests: {pending.action.kind}</h3>
          {pending.action.kind === "observe" && <p className="text-xs">Share a fresh capture for analysis: {pending.action.question || "Describe the approved window"}</p>}
          {pending.action.kind === "click" && <p>Click pixel ({pending.action.x}, {pending.action.y}) in frame {pending.frameId}.</p>}
          {pending.action.kind === "scroll" && <p>Scroll horizontally {pending.action.deltaX}, vertically {pending.action.deltaY} pixels (positive: right/down).</p>}
          {pending.action.kind === "key" && <p>Press {pending.action.key}.</p>}
          {pending.action.kind === "activate" && <p>Bring the approved application to the foreground.</p>}
          {pending.action.kind === "type" && <pre aria-label="Proposed text" className="max-h-32 overflow-auto whitespace-pre-wrap break-words rounded bg-muted p-2 text-xs">{pending.action.text}</pre>}
          <label className="flex items-start gap-2 text-xs">
            <input type="checkbox" checked={confirmed} disabled={busy || stopRequired || !enabled || session?.phase !== "active"} onChange={(event) => setConfirmed(event.target.checked)} />
            <span>I reviewed this target and action, including any send, submit, deletion, purchase, or security effect. I authorize this action only. Never enter passwords.</span>
          </label>
          <div className="flex gap-2">
            <Button size="sm" disabled={busy || stopRequired || !enabled || !confirmed || session?.phase !== "active"} onClick={() => void operate(() => decide(true))}>Approve once</Button>
            <Button size="sm" variant="outline" disabled={busy || stopRequired || !enabled} onClick={() => void operate(() => decide(false))}>Deny</Button>
          </div>
        </section>}
        {session && !pending && <p className="text-xs text-muted-foreground">Waiting for an agent request. Nothing executes automatically.</p>}
        {!!activity.length && <ol aria-label="Recent computer actions" aria-live="polite" className="space-y-1 text-xs text-muted-foreground">
          {activity.map((entry) => <li key={entry.id}>{entry.kind} — {entry.status}</li>)}
        </ol>}
      </div>
    </details>
  );
}
