import { ModeInstructions } from "@/components/ModeInstructions";
import { RepositoriesPanel } from "@/components/run-session/RepositoriesPanel";
import { cn } from "@/lib/utils";
import type { AgentRun } from "@/rpc/platform/service_pb";

interface RunContextContentProps {
  namespace: string;
  name: string;
  run: AgentRun;
  showRepositories: boolean;
  canClone: boolean;
  sandboxReady: boolean;
  startupMessage: string;
  className?: string;
}

/**
 * RunContextContent is the body of the inspector's Context tab: mode guidance
 * first, then workspace repositories.
 */
export function RunContextContent({
  namespace,
  name,
  run,
  showRepositories,
  canClone,
  sandboxReady,
  startupMessage,
  className,
}: RunContextContentProps) {
  return (
    <div className={cn("min-h-0 flex-1 overflow-y-auto", className)}>
      {run.modeInstructions && (
        <div className="border-b px-3 py-2">
          <ModeInstructions instructions={run.modeInstructions} defaultOpen />
        </div>
      )}
      {showRepositories && (
        <RepositoriesPanel
          namespace={namespace}
          name={name}
          resourceType="AgentRun"
          canClone={canClone}
          sandboxReady={sandboxReady}
          startupMessage={startupMessage}
          defaultExpanded
        />
      )}
      {!run.modeInstructions && !showRepositories && (
        <p className="p-4 text-sm text-muted-foreground">No additional run context is available.</p>
      )}
    </div>
  );
}
