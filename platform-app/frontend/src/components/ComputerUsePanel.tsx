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
  type DesktopSession, type WindowCapture, type WindowTarget,
} from "@/lib/computer-use";

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
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const revoked = useRef(true);
  const sessionId = session?.sessionId;
  const sessionScope = session?.scope;
  const revokeLocalSession = useCallback(() => {
    generation.current++;
    revoked.current = true;
  }, []);

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

  useEffect(() => {
    return () => {
      revokeLocalSession();
      if (isTauri) void stopDesktopSession().catch(() => {});
    };
  }, [namespace, name, user, enabled, revokeLocalSession]);

  useEffect(() => {
    if (!sessionId || !sessionScope || !enabled) return;
    const scope = sessionScope;
    let alive = true;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      if (!alive || revoked.current) return;
      try {
        if (backendBaseUrl() !== scope.backend || user !== scope.user) {
          throw new Error("Desktop session identity or backend changed");
        }
        const run = await client.getAgentRun({ namespace, name });
        if (run.namespace !== namespace || run.name !== name ||
            !["owner", "admin"].includes(run.myPermission) || isDonePhase(run.phase)) {
          throw new Error("This run is no longer available for desktop supervision");
        }
        if (!alive || revoked.current) return;
        await heartbeatDesktopSession(sessionId, scope);
        const status = await desktopSessionStatus();
        if (!alive || revoked.current) return;
        if (status.sessionId !== sessionId || status.phase !== "active") {
          setPreview(null);
        }
        setSession(status.phase === "stopped" ? null : { ...status, scope });
        if (status.phase === "stopped") {
          revoked.current = true;
          setConsent(false);
          setError(status.reason);
          return;
        }
        timer = setTimeout(() => void poll(), 3000);
      } catch (cause) {
        if (!alive) return;
        revokeLocalSession();
        setPreview(null);
        setConsent(false);
        setError(String(cause));
        try {
          await stopDesktopSession();
          if (alive) setSession(null);
        } catch {
          if (alive) setError("Cannot confirm native stop. Press Control+Option+Command+Escape or use the native tray.");
        }
      }
    };
    void poll();
    return () => { alive = false; clearTimeout(timer); };
  }, [sessionId, sessionScope, enabled, namespace, name, user, revokeLocalSession]);

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
    if (!target || !user || !enabled || !consent) return;
    const current = generation.current;
    const backend = backendBaseUrl();
    const run = await client.getAgentRun({ namespace, name });
    if (run.namespace !== namespace || run.name !== name ||
        !["owner", "admin"].includes(run.myPermission) || isDonePhase(run.phase)) {
      throw new Error("Only a run owner or admin can start desktop supervision");
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
      await stopDesktopSession();
      return;
    }
    revoked.current = false;
    setSession(started);
    setPreview(null);
  }

  async function stop() {
    setPreview(null);
    revokeLocalSession();
    setConsent(false);
    setBusy(true);
    try {
      await stopDesktopSession();
      setSession(null);
      setError("");
    } catch {
      setError("Cannot confirm native stop. Press Control+Option+Command+Escape or use the native tray.");
    } finally { setBusy(false); }
  }

  async function capture() {
    if (revoked.current) throw new Error("Desktop session revoked; confirm stop before starting again");
    if (!session?.sessionId || !session.scope) return;
    const current = generation.current;
    const image = await captureDesktopWindow(session.sessionId, session.scope);
    if (current === generation.current && !revoked.current) setPreview(image);
  }

  if (!isTauri || !user || (!supported && !error)) return null;
  return (
    <details className="border-t px-3 py-2 text-sm md:px-4">
      <summary className="cursor-pointer font-medium">Desktop preview — {session?.phase ?? "stopped"}</summary>
      <div className="max-h-[40vh] space-y-3 overflow-y-auto py-3">
        <p className="text-xs text-muted-foreground">
          Preview-only implementation: agent control and screen sharing are not connected yet.
          Captures stay in memory on this Mac. Application selection is not an OS sandbox.
          Set up macOS permissions in Settings → General before selecting a window.
          Native stop: Control+Option+Command+Escape, or Stop computer use in the tray.
        </p>
        {error && <p role="alert">{error}</p>}
        {!session && <>
          <Button variant="outline" size="sm" disabled={busy || !enabled || !supported}
            onClick={() => void operate(async () => { setWindows(await computerUseWindows()); setSelected(""); })}>
            List open windows
          </Button>
          <label className="block space-y-1">
            <span>Approved window</span>
            <select aria-label="Approved window" className="block w-full rounded-md border bg-background p-2"
              value={selected} disabled={busy || !enabled} onChange={(event) => setSelected(event.target.value)}>
              <option value="">Select a window</option>
              {windows.map((window) => <option key={window.windowId} value={window.windowId}>
                {window.application} — {window.title}
              </option>)}
            </select>
          </label>
          <label className="flex items-start gap-2 text-xs">
            <input type="checkbox" checked={consent} disabled={busy || !enabled}
              onChange={(event) => setConsent(event.target.checked)} />
            <span>I consent to capturing this window and sharing captures with this run’s backend and model ({model || "configured model"}).
              Captures may contain private information. Sharing is not connected in this preview version.</span>
          </label>
          <Button size="sm" disabled={!enabled || !selected || !consent || busy || !supported}
            onClick={() => void operate(start)}>Start preview session</Button>
        </>}
        {session?.scope && <p className="text-xs">Approved: {session.scope.application} · window {session.scope.windowId}</p>}
        {session && <div className="sticky top-0 z-10 flex flex-wrap gap-2 bg-background py-1">
          <Button size="sm" variant="outline" disabled={busy || session.phase !== "active" || !enabled}
            onClick={() => void operate(capture)}>Capture preview</Button>
          <Button size="sm" variant="outline" disabled={busy || !enabled}
            onClick={() => void operate(async () => {
              if (revoked.current) throw new Error("Desktop session revoked; confirm stop before starting again");
              const current = generation.current;
              let status: DesktopSession;
              if (session.phase === "paused" && session.sessionId && session.scope) {
                status = await resumeDesktopSession(session.sessionId, session.scope);
              } else {
                await pauseDesktopSession();
                status = await desktopSessionStatus();
              }
              if (current === generation.current && !revoked.current) {
                setPreview(null);
                setSession(status);
              }
            })}>{session.phase === "paused" ? "Resume" : "Pause"}</Button>
          <Button size="sm" variant="destructive" onClick={() => void stop()}>Stop desktop session</Button>
        </div>}
        {preview && <img src={preview.dataUrl} alt="Preview of the approved desktop window"
          className="max-h-80 w-auto max-w-full rounded-md border" />}
      </div>
    </details>
  );
}
