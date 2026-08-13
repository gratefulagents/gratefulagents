import { useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { featuredImmunefiTargets, type ImmunefiTarget } from "@/lib/immunefiTargetCatalog";
import type { SecurityProgramResource } from "@/rpc/platform/service_pb";

export function ImmunefiTargetImportDialog({
  programs,
  existingNames,
  trigger,
  onTargetSelected,
}: {
  programs: readonly SecurityProgramResource[];
  existingNames: ReadonlySet<string>;
  trigger: React.ReactElement;
  onTargetSelected: (target: ImmunefiTarget) => void;
}) {
  const [open, setOpen] = useState(false);
  const targets = featuredImmunefiTargets(programs);

  function selectTarget(target: ImmunefiTarget) {
    setOpen(false);
    onTargetSelected(target);
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={trigger} />
      <DialogContent className="flex max-h-[92vh] w-full max-w-3xl flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
        <DialogHeader className="space-y-1 border-b px-6 py-5">
          <DialogTitle>Choose an Immunefi target</DialogTitle>
          <DialogDescription>
            Select a bounty target to prefill a new scan. You can review the configuration,
            model, credentials, and all other options before creating it.
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">
          <p className="mb-4 rounded-lg border border-border bg-muted/40 px-3 py-2 text-sm font-medium">
            Nothing is created or run until you review the scan form and choose Create scan. New scans default
            to read-only access and unrestricted network egress.
          </p>
          {targets.length === 0 ? (
            <p className="rounded-lg border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
              No featured Immunefi targets are available.
            </p>
          ) : (
            <ul className="divide-y rounded-lg border" aria-label="Featured Immunefi targets">
              {targets.map((target) => {
                const exists = existingNames.has(target.name);
                return (
                  <li key={target.name} className="grid gap-3 px-3 py-2.5 sm:grid-cols-[1fr_auto] sm:items-center">
                    <div className="min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="font-medium">{target.displayName}</span>
                        {exists && (
                          <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">
                            Existing configuration
                          </span>
                        )}
                      </div>
                      <div className="truncate font-mono text-xs text-muted-foreground">
                        {target.repoUrl} · {target.baseBranch}
                      </div>
                      <div className="font-mono text-xs text-muted-foreground">{target.workflowRef}</div>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={exists}
                      aria-label={`Configure scan for ${target.displayName}`}
                      onClick={() => selectTarget(target)}
                    >
                      Configure scan
                    </Button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
        <div className="flex justify-end border-t px-6 py-4">
          <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
            Close
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
