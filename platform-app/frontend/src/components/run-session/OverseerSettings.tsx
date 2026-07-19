import { useState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { client } from "@/lib/client";
import { isDonePhase } from "@/lib/status";
import type { AgentRun } from "@/rpc/platform/service_pb";

export function OverseerSettings({ run, canManage }: { run: AgentRun; canManage: boolean }) {
  const [mutationResult, setMutationResult] = useState<{ sourceRun: AgentRun; response: AgentRun } | null>(null);
  const [modeRefName, setModeRefName] = useState("");
  const [modeRefVersion, setModeRefVersion] = useState("");
  const [modeRefChannel, setModeRefChannel] = useState("");
  const [model, setModel] = useState("");
  const [authority, setAuthority] = useState<string | undefined>();
  const [intervalMinutes, setIntervalMinutes] = useState<string | undefined>();
  const [maxInterventions, setMaxInterventions] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Show a mutation response immediately, but only while it corresponds to
  // the current watch prop. A later watch snapshot is always authoritative.
  const current = mutationResult?.sourceRun === run ? mutationResult.response : run;
  const config = current.overseer;
  const summary = current.overseerSummary;
  const readOnly = !canManage || isDonePhase(current.phase);
  const attachBlocked = current.overseerDetaching || !!summary;
  const authorityValue = authority ?? config?.authority ?? "advise";
  const intervalValue = intervalMinutes ?? String(config?.intervalMinutes ?? 10);
  const maxInterventionsValue = maxInterventions ?? String(config?.maxInterventions ?? 5);

  async function perform(action: () => Promise<AgentRun>) {
    setBusy(true);
    setError(null);
    try {
      setMutationResult({ sourceRun: run, response: await action() });
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setBusy(false);
    }
  }

  function numbersValid() {
    const interval = Number(intervalValue);
    const cap = Number(maxInterventionsValue);
    if (!Number.isInteger(interval) || interval < 1 || interval > 1440) {
      setError("Interval must be a whole number between 1 and 1440 minutes.");
      return false;
    }
    if (!Number.isInteger(cap) || cap < 0 || cap > 100) {
      setError("Max interventions must be a whole number between 0 and 100.");
      return false;
    }
    return true;
  }

  function attach() {
    if (!numbersValid()) return;
    void perform(() => client.attachAgentRunOverseer({
      namespace: current.namespace,
      name: current.name,
      overseer: {
        modeRefName: modeRefName.trim(),
        modeRefVersion: modeRefVersion.trim(),
        modeRefChannel: modeRefChannel.trim(),
        model: model.trim(),
        authority: authorityValue,
        intervalMinutes: Number(intervalValue),
        maxInterventions: Number(maxInterventionsValue),
      },
    }));
  }

  function update() {
    if (!numbersValid()) return;
    void perform(() => client.updateAgentRunOverseer({
      namespace: current.namespace,
      name: current.name,
      authority: authorityValue,
      intervalMinutes: Number(intervalValue),
      maxInterventions: Number(maxInterventionsValue),
    }));
  }

  return (
    <section className="mt-4 border-t pt-4" aria-label="Overseer settings">
      <div className="mb-3 flex items-center justify-between">
        <h3 className="text-sm font-medium">Overseer</h3>
        <span className="text-xs text-muted-foreground">
          {current.overseerDetaching ? "Detaching" : summary?.state || (config ? "Attached" : "Not attached")}
        </span>
      </div>

      {config ? (
        <div className="space-y-3">
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-xs">
            <dt className="text-muted-foreground">Mode</dt><dd>{config.modeRefName || "overseer"}{config.modeRefVersion ? ` @ ${config.modeRefVersion}` : ""}{config.modeRefChannel ? ` (${config.modeRefChannel})` : ""}</dd>
            <dt className="text-muted-foreground">Model</dt><dd>{config.model || "default"}</dd>
          </dl>
          <div className="grid gap-3 sm:grid-cols-3">
            <label className="text-xs">Authority
              <select aria-label="Overseer authority" className="mt-1 h-9 w-full rounded-md border bg-background px-2" value={authorityValue} onChange={(event) => setAuthority(event.target.value)} disabled={readOnly || busy}>
                <option value="observe">Observe</option><option value="advise">Advise</option><option value="enforce">Enforce</option>
              </select>
            </label>
            <label className="text-xs">Interval (minutes)<Input aria-label="Overseer interval" className="mt-1" type="number" min={1} max={1440} value={intervalValue} onChange={(event) => setIntervalMinutes(event.target.value)} disabled={readOnly || busy} /></label>
            <label className="text-xs">Max interventions<Input aria-label="Overseer max interventions" className="mt-1" type="number" min={0} max={100} value={maxInterventionsValue} onChange={(event) => setMaxInterventions(event.target.value)} disabled={readOnly || busy} /></label>
          </div>
          {!readOnly && <div className="flex gap-2"><Button size="sm" type="button" onClick={update} disabled={busy}>Save overseer</Button><Button size="sm" variant="destructive" type="button" onClick={() => void perform(() => client.detachAgentRunOverseer({ namespace: current.namespace, name: current.name }))} disabled={busy}>Detach overseer</Button></div>}
        </div>
      ) : attachBlocked ? (
        <p className="text-xs text-muted-foreground">{current.overseerDetaching ? "Overseer detachment is in progress." : "This overseer attachment is no longer active."}</p>
      ) : !readOnly ? (
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="text-xs">Mode name<Input aria-label="Overseer mode name" className="mt-1" value={modeRefName} onChange={(e) => setModeRefName(e.target.value)} /></label>
          <label className="text-xs">Model<Input aria-label="Overseer model" className="mt-1" value={model} onChange={(e) => setModel(e.target.value)} /></label>
          <label className="text-xs">Mode version<Input aria-label="Overseer mode version" className="mt-1" value={modeRefVersion} onChange={(e) => setModeRefVersion(e.target.value)} /></label>
          <label className="text-xs">Mode channel<Input aria-label="Overseer mode channel" className="mt-1" value={modeRefChannel} onChange={(e) => setModeRefChannel(e.target.value)} /></label>
          <label className="text-xs">Authority<select aria-label="Overseer authority" className="mt-1 h-9 w-full rounded-md border bg-background px-2" value={authorityValue} onChange={(e) => setAuthority(e.target.value)}><option value="observe">Observe</option><option value="advise">Advise</option><option value="enforce">Enforce</option></select></label>
          <label className="text-xs">Interval (minutes)<Input aria-label="Overseer interval" className="mt-1" type="number" min={1} max={1440} value={intervalValue} onChange={(e) => setIntervalMinutes(e.target.value)} /></label>
          <label className="text-xs">Max interventions<Input aria-label="Overseer max interventions" className="mt-1" type="number" min={0} max={100} value={maxInterventionsValue} onChange={(e) => setMaxInterventions(e.target.value)} /></label>
          <div className="self-end"><Button size="sm" type="button" onClick={attach} disabled={busy}>Attach overseer</Button></div>
        </div>
      ) : <p className="text-xs text-muted-foreground">No overseer attached.</p>}

      {summary && <dl className="mt-3 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-xs">
        <dt className="text-muted-foreground">Overseer run</dt><dd>{summary.runName || "—"}</dd>
        <dt className="text-muted-foreground">Checkpoints</dt><dd>{String(summary.checkpointsHandled)}</dd>
        <dt className="text-muted-foreground">Interventions</dt><dd>{summary.interventionsUsed}</dd>
        <dt className="text-muted-foreground">Completion rejections</dt><dd>{summary.completionRejectionsUsed}</dd>
        <dt className="text-muted-foreground">Last verdict</dt><dd>{summary.lastVerdict || "—"}</dd>
        <dt className="text-muted-foreground">Last summary</dt><dd>{summary.lastSummary || "—"}</dd>
        <dt className="text-muted-foreground">Last verdict at</dt><dd>{summary.lastVerdictAtUnix ? new Date(Number(summary.lastVerdictAtUnix) * 1000).toLocaleString() : "—"}</dd>
      </dl>}
      {error && <p role="alert" className="mt-2 text-xs text-destructive">{error}</p>}
    </section>
  );
}
