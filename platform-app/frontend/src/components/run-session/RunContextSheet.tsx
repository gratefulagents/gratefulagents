import { ModeInstructions } from "@/components/ModeInstructions";
import { RepositoriesPanel } from "@/components/run-session/RepositoriesPanel";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import type { AgentRun } from "@/rpc/platform/service_pb";

interface RunContextSheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  namespace: string;
  name: string;
  run: AgentRun;
  showRepositories: boolean;
  canClone: boolean;
  sandboxReady: boolean;
  startupMessage: string;
}

export function RunContextSheet({
  open,
  onOpenChange,
  namespace,
  name,
  run,
  showRepositories,
  canClone,
  sandboxReady,
  startupMessage,
}: RunContextSheetProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="!w-[min(92vw,30rem)] !max-w-[30rem] gap-0">
        <SheetHeader className="border-b">
          <SheetTitle>Run context</SheetTitle>
          <SheetDescription>
            Mode guidance and workspace repositories, available when you need them.
          </SheetDescription>
        </SheetHeader>
        <div className="min-h-0 flex-1 overflow-y-auto">
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
      </SheetContent>
    </Sheet>
  );
}
