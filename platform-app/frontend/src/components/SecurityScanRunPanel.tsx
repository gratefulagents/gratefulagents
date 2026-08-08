import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { RotateCcw, Square, SquareArrowOutUpRight } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DetailSection, Fact, FactList } from "@/components/detail-page";
import { SubagentGraphView } from "@/components/SubagentGraphView";
import { RunUsageSummary } from "@/components/RunUsageSummary";
import { useAgentRun } from "@/hooks/useAgentRun";
import { useActivityLog } from "@/hooks/useActivityLog";
import { useAgentRunUsage } from "@/hooks/useAgentRunUsage";
import { useNow } from "@/hooks/useNow";
import { client } from "@/lib/client";
import { formatDuration } from "@/lib/activityGrouping";
import { isDonePhase } from "@/lib/status";

/**
 * Live progress, diagnostics, and controls for the AgentRun behind a security
 * scan. Reuses the run pages' data paths: the run watch stream (useAgentRun),
 * the activity-log stream's sub-agent graph (the scan workflow tasks execute
 * as sub-agents), and the usage ledger.
 */
export function SecurityScanRunPanel({
  namespace,
  runName,
  onRunSettled,
  hideWhenMissing = false,
}: {
  namespace: string;
  runName: string;
  /** Called once when the run transitions into a terminal phase. */
  onRunSettled?: (phase: string) => void;
  /**
   * Render nothing when no AgentRun of this name exists. Deterministic
   * executions persist ONE scan record keyed by a synthetic execution name
   * (not an AgentRun); their live progress is the execution DAG, so an error
   * panel here would be noise.
   */
  hideWhenMissing?: boolean;
}) {
  const { run, loading, error } = useAgentRun(namespace, runName);
  const phase = run?.phase ?? "";
  const { entries, subagentGraph } = useActivityLog(namespace, runName, phase);
  const { usage } = useAgentRunUsage(namespace, runName, run !== null);
  const now = useNow();

  const [pendingAction, setPendingAction] = useState<"cancel" | "retry" | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  // Notify the parent only on an OBSERVED transition into a terminal phase,
  // never when the run is already terminal on mount. Firing on mount created
  // a remount loop: the parent refreshed, showed a loading state that
  // unmounted this panel, the remount saw the still-terminal phase and fired
  // again — tearing down and reopening the watch streams several times per
  // second (visible as violent page flicker). A parent that mounts this panel
  // has just loaded fresh data anyway, so a mount-time notification is never
  // needed.
  const lastPhaseRef = useRef<string | null>(null);
  useEffect(() => {
    if (!phase) return;
    const last = lastPhaseRef.current;
    lastPhaseRef.current = phase;
    if (last !== null && !isDonePhase(last) && isDonePhase(phase)) {
      onRunSettled?.(phase);
    }
  }, [phase, onRunSettled]);

  if (error && !run) {
    if (hideWhenMissing) return null;
    return (
      <DetailSection title="Scan Run">
        <p role="alert" className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]">
          Failed to load the scan&apos;s agent run: {error}
        </p>
      </DetailSection>
    );
  }
  if (loading || !run) {
    return (
      <DetailSection title="Scan Run">
        <p className="text-[12.5px] text-muted-foreground">Loading run status…</p>
      </DetailSection>
    );
  }

  const canControl = run.myPermission === "owner" || run.myPermission === "admin";
  const canCancel = canControl && !isDonePhase(phase);
  const canRetry = canControl && (phase === "Failed" || phase === "Cancelled");

  async function handleCancel() {
    setActionError(null);
    setPendingAction("cancel");
    try {
      await client.cancelAgentRun({ namespace, name: runName });
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to stop the scan run");
    } finally {
      setPendingAction(null);
    }
  }

  async function handleRetry() {
    setActionError(null);
    setPendingAction("retry");
    try {
      await client.retryAgentRun({ namespace, name: runName, idempotencyKey: crypto.randomUUID() });
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to retry the scan run");
    } finally {
      setPendingAction(null);
    }
  }

  const model = run.resolvedModel || run.model;
  const startedMs = run.startedAtUnix > 0n ? Number(run.startedAtUnix) * 1000 : 0;
  const endMs = run.completedAtUnix > 0n ? Number(run.completedAtUnix) * 1000 : now;
  const duration = startedMs > 0 ? formatDuration(Math.max(endMs - startedMs, 0)) : "—";
  const tokens =
    run.inputTokens > 0n || run.outputTokens > 0n
      ? `${run.inputTokens.toLocaleString()} in / ${run.outputTokens.toLocaleString()} out`
      : "—";

  return (
    <DetailSection
      title="Scan Run"
      aside={
        <div className="flex flex-wrap items-center gap-2">
          {canCancel && (
            <Button
              variant="outline"
              size="sm"
              disabled={pendingAction !== null}
              onClick={() => void handleCancel()}
            >
              <Square />
              {pendingAction === "cancel" ? "Stopping…" : "Stop scan"}
            </Button>
          )}
          {canRetry && (
            <Button
              variant="outline"
              size="sm"
              disabled={pendingAction !== null}
              onClick={() => void handleRetry()}
            >
              <RotateCcw />
              {pendingAction === "retry" ? "Retrying…" : phase === "Cancelled" ? "Resume scan" : "Retry scan"}
            </Button>
          )}
        </div>
      }
    >
      <div className="space-y-4">
        {canRetry && (
          <p className="text-[12.5px] text-muted-foreground">
            Findings already recorded by this scan are preserved: retrying resumes from the
            persisted session, and re-observed findings update in place without losing triage
            decisions.
          </p>
        )}
        {actionError && (
          <p role="alert" className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]">
            {actionError}
          </p>
        )}
        {run.lastError && (
          <div role="alert" className="space-y-1 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]">
            <p className="font-medium">Most recent run error</p>
            <p className="text-muted-foreground">{run.lastError}</p>
            <p className="text-muted-foreground">
              {phase === "Failed"
                ? "Use Retry scan to resume from the persisted session, or inspect logs and traces on the "
                : "See logs, traces, and the full conversation on the "}
              <Link className="underline underline-offset-2" to={`/runs/${namespace}/${runName}`}>
                agent run page
              </Link>
              .
            </p>
          </div>
        )}

        <FactList>
          <Fact
            label="Phase"
            value={
              <Badge variant="outline" className="capitalize">
                {phase || "unknown"}
              </Badge>
            }
          />
          <Fact label="Duration" mono value={duration} />
          <Fact label="Model" mono value={model || "—"} />
          <Fact label="Retries" mono value={String(run.retryCount)} />
          {run.queueState && <Fact label="Queue" value={run.queueState} />}
          {run.blockedReason && <Fact label="Blocked" value={run.blockedReason} />}
          <Fact label="Cost" mono value={run.costUsd ? `$${run.costUsd}` : "—"} />
          <Fact label="Tokens" mono value={tokens} />
          <Fact
            label="Agent run"
            value={
              <Link
                className="inline-flex items-center gap-1 underline underline-offset-2"
                to={`/runs/${namespace}/${runName}`}
              >
                {runName}
                <SquareArrowOutUpRight className="size-3" />
              </Link>
            }
          />
        </FactList>

        {usage?.summary && <RunUsageSummary totals={usage.summary} />}

        <SubagentGraphView graph={subagentGraph} entries={entries} />
      </div>
    </DetailSection>
  );
}
