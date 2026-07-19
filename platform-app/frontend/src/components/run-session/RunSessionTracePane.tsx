import { AlertTriangle, CircleDot, Loader2 } from "lucide-react";

import { RunAttemptDetailsTable } from "@/components/RunAttemptDetailsTable";
import { RunUsageBreakdownTable } from "@/components/RunUsageBreakdownTable";
import { RunUsageSummary } from "@/components/RunUsageSummary";
import { TraceWaterfallView } from "@/components/TraceWaterfallView";
import { Separator } from "@/components/ui/separator";
import type { AgentRunUsageResponse, GetAgentTraceResponse } from "@/rpc/platform/service_pb";

interface RunSessionTracePaneProps {
  trace: GetAgentTraceResponse | null | undefined;
  traceError: string | null;
  traceLoading: boolean;
  usage: AgentRunUsageResponse | null;
  usageLoading: boolean;
  usageError: string | null;
}

export function RunSessionTracePane({
  trace,
  traceError,
  traceLoading,
  usage,
  usageLoading,
  usageError,
}: RunSessionTracePaneProps) {
  return (
    <div className="flex-1 min-h-0 min-w-0 overflow-y-auto p-3 md:p-4 space-y-6">
      {trace && trace.spans.length > 0 ? (
        <div className="h-full min-h-0 min-w-0">
          <TraceWaterfallView trace={trace} />
        </div>
      ) : traceError ? (
        <div className="flex flex-col items-center justify-center gap-1 py-16 text-center">
          <AlertTriangle className="size-5 text-destructive" />
          <p className="text-sm font-medium text-foreground">Trace unavailable</p>
          <p className="max-w-md text-xs text-muted-foreground">{traceError}</p>
        </div>
      ) : traceLoading || !trace ? (
        <div className="flex items-center justify-center gap-2 py-16 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          Loading trace…
        </div>
      ) : (
        <div className="flex flex-col items-center justify-center gap-1 py-16 text-center">
          <CircleDot className="size-5 animate-pulse text-muted-foreground" />
          <p className="text-sm font-medium text-foreground">No trace spans yet</p>
          <p className="max-w-md text-xs text-muted-foreground">
            Spans appear here once the agent emits OpenTelemetry tracing data.
          </p>
        </div>
      )}
      <Separator />
      <div className="space-y-4">
        <div>
          <h3 className="text-sm font-semibold">Usage</h3>
          {usageLoading ? (
            <p className="text-sm text-muted-foreground">Loading usage…</p>
          ) : usageError ? (
            <p className="text-sm text-muted-foreground">Usage unavailable.</p>
          ) : !usage?.isAvailable ? (
            <p className="text-sm text-muted-foreground">No LLM usage recorded.</p>
          ) : (
            <div className="space-y-4">
              <RunUsageSummary totals={usage.summary} />
              <RunUsageBreakdownTable title="Top-level" tasks={usage.topLevelTasks} />
              <RunUsageBreakdownTable title="Subagents" tasks={usage.subagentTasks} />
              <RunAttemptDetailsTable tasks={[...(usage.topLevelTasks ?? []), ...(usage.subagentTasks ?? [])]} />
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
