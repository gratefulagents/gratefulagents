import { useMemo, useState } from "react";
import { AlertTriangle, CircleDot, Loader2 } from "lucide-react";

import { RunAttemptDetailsTable } from "@/components/RunAttemptDetailsTable";
import { RunUsageBreakdownTable } from "@/components/RunUsageBreakdownTable";
import { RunUsageSummary } from "@/components/RunUsageSummary";
import { TraceWaterfallSkeleton, TraceWaterfallView } from "@/components/TraceWaterfallView";
import { InspectorSubnav } from "@/components/run-session/InspectorSubnav";
import type { AgentRunUsageResponse, GetAgentTraceResponse } from "@/rpc/platform/service_pb";

interface RunSessionTracePaneProps {
  trace: GetAgentTraceResponse | null | undefined;
  traceError: string | null;
  traceLoading: boolean;
  usage: AgentRunUsageResponse | null;
  usageLoading: boolean;
  usageError: string | null;
}

type TraceSection = "timeline" | "usage";

function PaneMessage({
  icon,
  title,
  detail,
}: {
  icon: React.ReactNode;
  title: string;
  detail?: string;
}) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-1 px-6 py-16 text-center">
      {icon}
      <p className="text-sm font-medium text-foreground">{title}</p>
      {detail && <p className="max-w-md text-xs text-muted-foreground">{detail}</p>}
    </div>
  );
}

/**
 * The trace pane holds two unrelated readings of the same run: *when* things
 * happened (the waterfall) and *what they cost* (the usage ledger). Stacking
 * them in one scroller left the waterfall — the reason anyone opens this tab —
 * with no height and pushed the tables below the fold anyway. They are now
 * sibling sections: the timeline owns the full pane height and drives its own
 * internal scrolling, and usage is one click away with its own scroller.
 */
export function RunSessionTracePane({
  trace,
  traceError,
  traceLoading,
  usage,
  usageLoading,
  usageError,
}: RunSessionTracePaneProps) {
  const [section, setSection] = useState<TraceSection>("timeline");

  const spans = trace?.spans;
  const spanCount = spans?.length ?? 0;
  const errorCount = useMemo(() => (spans ?? []).reduce((total, span) => total + (span.isError ? 1 : 0), 0), [spans]);
  const usageTaskCount = (usage?.topLevelTasks?.length ?? 0) + (usage?.subagentTasks?.length ?? 0);

  return (
    <div className="@container flex h-full min-h-0 min-w-0 flex-col">
      <InspectorSubnav<TraceSection>
        items={[
          { id: "timeline", label: "Timeline", count: spanCount, alert: errorCount > 0 },
          { id: "usage", label: "Usage", count: usageTaskCount },
        ]}
        value={section}
        onChange={setSection}
      />

      {section === "timeline" ? (
        spanCount > 0 && trace ? (
          // The waterfall manages its own scrolling, sticky ruler and stat
          // header, so it takes the pane's height instead of sitting inside a
          // scroller, and drops its card chrome because the pane already frames it.
          <div className="min-h-0 min-w-0 flex-1 overflow-hidden">
            <TraceWaterfallView trace={trace} className="rounded-none border-0" />
          </div>
        ) : traceError ? (
          <PaneMessage
            icon={<AlertTriangle className="size-5 text-destructive" />}
            title="Trace unavailable"
            detail={traceError}
          />
        ) : traceLoading || !trace ? (
          <TraceWaterfallSkeleton className="flex-1" />
        ) : (
          <PaneMessage
            icon={<CircleDot className="size-5 animate-pulse text-muted-foreground" />}
            title="No trace spans yet"
            detail="Spans appear here once the agent emits OpenTelemetry tracing data."
          />
        )
      ) : (
        <div className="min-h-0 min-w-0 flex-1 overflow-y-auto p-3 @lg:p-4">
          {usageLoading && !usage ? (
            <div className="flex items-center justify-center gap-2 py-16 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              Loading usage…
            </div>
          ) : usageError ? (
            <PaneMessage
              icon={<AlertTriangle className="size-5 text-destructive" />}
              title="Usage unavailable"
              detail={usageError}
            />
          ) : !usage?.isAvailable ? (
            <PaneMessage
              icon={<CircleDot className="size-5 text-muted-foreground" />}
              title="No LLM usage recorded"
              detail="Token accounting appears once the agent completes a model call."
            />
          ) : (
            <div className="space-y-5">
              <RunUsageSummary totals={usage.summary} />
              {usage.topLevelTasks.length > 0 && (
                <RunUsageBreakdownTable title="Top-level" tasks={usage.topLevelTasks} />
              )}
              {usage.subagentTasks.length > 0 && (
                <RunUsageBreakdownTable title="Subagents" tasks={usage.subagentTasks} />
              )}
              <div className="space-y-2">
                <h4 className="text-sm font-medium">Attempts</h4>
                <div className="overflow-x-auto">
                  <RunAttemptDetailsTable tasks={[...usage.topLevelTasks, ...usage.subagentTasks]} />
                </div>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
