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
 * first, then the skills attached to the run, then workspace repositories.
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
  const skillRefs = run.skillRefs ?? [];
  const loadedSkills = new Set(run.resolvedSkills ?? []);
  return (
    <div className={cn("min-h-0 flex-1 overflow-y-auto", className)}>
      {run.modeInstructions && (
        <div className="border-b px-3 py-2">
          <ModeInstructions instructions={run.modeInstructions} defaultOpen />
        </div>
      )}
      {skillRefs.length > 0 && (
        <section aria-label="Skills" className="border-b px-3 py-2.5">
          <div className="flex items-baseline justify-between gap-2">
            <h3 className="text-[12px] font-medium">Skills</h3>
            <span className="text-[11px] text-muted-foreground">
              {loadedSkills.size} of {skillRefs.length} loaded
            </span>
          </div>
          <p className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
            Enabled skills are offered to the agent; a skill's instructions enter context only once it loads them.
          </p>
          <ul role="list" className="mt-2 space-y-1">
            {skillRefs.map((name) => (
              <li key={name} className="flex items-center gap-2 text-[12px]">
                <span className="font-mono">{name}</span>
                {loadedSkills.has(name) && (
                  <span className="inline-flex h-[16px] items-center rounded-full bg-muted/60 px-1.5 text-[10px] font-medium text-muted-foreground ring-1 ring-inset ring-border/70">
                    loaded
                  </span>
                )}
              </li>
            ))}
          </ul>
        </section>
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
      {!run.modeInstructions && skillRefs.length === 0 && !showRepositories && (
        <p className="p-4 text-sm text-muted-foreground">No additional run context is available.</p>
      )}
    </div>
  );
}
