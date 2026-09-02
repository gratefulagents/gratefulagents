import { ModeInstructions } from "@/components/ModeInstructions";
import { RepositoriesPanel } from "@/components/run-session/RepositoriesPanel";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
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

interface RunContextSheetProps extends RunContextContentProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/**
 * RunContextContent is the body shared by the header sheet and the
 * inspector's Context tab: mode guidance first, then workspace repositories.
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

export function RunContextSheet({ open, onOpenChange, ...content }: RunContextSheetProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="!w-[min(92vw,30rem)] !max-w-[30rem] gap-0">
        <SheetHeader className="border-b">
          <SheetTitle>Run context</SheetTitle>
          <SheetDescription>
            Mode guidance and workspace repositories, available when you need them.
          </SheetDescription>
        </SheetHeader>
        <RunContextContent {...content} />
      </SheetContent>
    </Sheet>
  );
}
