import { useEffect, useState } from "react";
import { Check, Loader2 } from "lucide-react";

import { LiveDot } from "@/components/ui/live-dot";
import { normalizeRunStep } from "@/lib/runStatus";
import { cn } from "@/lib/utils";
import type { AgentRun, ChatMessage } from "@/rpc/platform/service_pb";

/**
 * True once the run produced its first agent output: any activity entry or
 * any non-user transcript message. The seeded initial user request alone
 * does not count, so the startup stepper stays visible until the agent
 * actually says or does something.
 */
export function hasFirstAgentOutput(
  activityEntryCount: number,
  deliveredMessages: ReadonlyArray<Pick<ChatMessage, "role">>,
): boolean {
  return activityEntryCount > 0 || deliveredMessages.some((message) => message.role !== "user");
}

export type StartupStageStatus = "complete" | "active" | "upcoming";

export type StartupStage = {
  id: "queued" | "sandbox" | "workspace" | "working";
  label: string;
  status: StartupStageStatus;
};

const STAGE_DEFS: ReadonlyArray<{ id: StartupStage["id"]; label: string }> = [
  { id: "queued", label: "Run queued" },
  { id: "sandbox", label: "Starting sandbox" },
  { id: "workspace", label: "Preparing workspace" },
  { id: "working", label: "Agent is working" },
];

// currentStep values reported while the worker is still cloning repositories
// and preparing the workspace (see runStatus STEP_LABELS).
const WORKSPACE_PREP_STEPS = new Set([
  "starting",
  "cloning-repository",
  "setting-up-workspace",
  "setup",
  "branch-setup",
]);

/**
 * Maps an AgentRun's startup status onto the ordered startup stages, or null
 * when the phase is not a known startup phase — callers must then fall back
 * to the generic progress copy instead of rendering a wrong stepper.
 */
export function deriveStartupStages(
  run: Pick<AgentRun, "phase" | "currentStep">,
): StartupStage[] | null {
  let activeIndex: number;
  switch (run.phase) {
    case "Pending":
      activeIndex = 0;
      break;
    case "Admitted":
    case "Provisioning":
      activeIndex = 1;
      break;
    case "Running":
      // currentStep can be pre-seeded before the worker starts, so it is only
      // trusted once the run actually reached Running.
      activeIndex = WORKSPACE_PREP_STEPS.has(normalizeRunStep(run.currentStep)) ? 2 : 3;
      break;
    default:
      return null;
  }
  return STAGE_DEFS.map((stage, index) => ({
    ...stage,
    status: index < activeIndex ? "complete" : index === activeIndex ? "active" : "upcoming",
  }));
}

export function formatStartupElapsed(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${seconds % 60}s`;
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

function StageIcon({ status }: { status: StartupStageStatus }) {
  if (status === "complete") {
    return <Check aria-hidden className="size-3.5 text-tone-success-fg" />;
  }
  if (status === "active") {
    return <Loader2 aria-hidden className="size-3.5 animate-spin text-primary" />;
  }
  return (
    <span aria-hidden className="flex size-3.5 items-center justify-center">
      <span className="size-1.5 rounded-full border border-muted-foreground/50" />
    </span>
  );
}

/**
 * Compact startup checklist shown while a run has produced no agent output
 * yet. Renders nothing for phases outside the known startup sequence.
 */
export function StartupProgress({
  run,
}: {
  run: Pick<AgentRun, "phase" | "currentStep" | "createdAtUnix">;
}) {
  const stages = deriveStartupStages(run);
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNowMs(Date.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  if (!stages) {
    return null;
  }
  const elapsedSeconds =
    run.createdAtUnix > 0n
      ? Math.max(0, Math.floor(nowMs / 1000) - Number(run.createdAtUnix))
      : null;
  return (
    <div className="rounded-lg border border-border/60 bg-muted/20 px-4 py-3">
      <div className="flex items-center justify-between gap-2">
        <p className="flex items-center gap-2 text-xs font-medium text-foreground">
          <LiveDot tone="running" pulse size="xs" />
          Starting up
        </p>
        {elapsedSeconds !== null && (
          <span className="text-2xs tabular-nums text-muted-foreground">
            {formatStartupElapsed(elapsedSeconds)}
          </span>
        )}
      </div>
      <ol className="mt-2 space-y-1.5">
        {stages.map((stage) => (
          <li
            key={stage.id}
            aria-current={stage.status === "active" ? "step" : undefined}
            className="flex items-center gap-2 text-2xs"
          >
            <StageIcon status={stage.status} />
            <span
              className={cn(
                stage.status === "active"
                  ? "font-medium text-foreground"
                  : "text-muted-foreground",
                stage.status === "upcoming" && "text-muted-foreground/60",
              )}
            >
              {stage.label}
            </span>
          </li>
        ))}
      </ol>
    </div>
  );
}
