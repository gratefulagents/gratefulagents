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
import { client } from "@/lib/client";
import { importableProgramTargets, type ProgramScanTarget } from "@/lib/programTargetCatalog";
import {
  buildImportedScanCreateRequest,
  runDefaultsFromModelDefaults,
} from "@/lib/securityScanImport";
import { useOptionalAuth } from "@/contexts/AuthContext";
import type { ModelDefaults, SecurityProgramResource } from "@/rpc/platform/service_pb";

export function ProgramTargetImportDialog({
  programs,
  existingNames,
  trigger,
  onTargetSelected,
  onImported,
}: {
  programs: readonly SecurityProgramResource[];
  existingNames: ReadonlySet<string>;
  trigger: React.ReactElement;
  onTargetSelected: (target: ProgramScanTarget) => void;
  onImported?: (summary: { created: number; failed: number }) => void;
}) {
  const auth = useOptionalAuth();
  const canUseDockerInDocker = auth?.user?.role === "admin";
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);
  const [created, setCreated] = useState(0);
  const [failures, setFailures] = useState<{ name: string; error: string }[]>([]);
  const targets = importableProgramTargets(programs);
  const importable = targets.filter((target) => !existingNames.has(target.name));
  const running = progress !== null;

  function selectTarget(target: ProgramScanTarget) {
    setOpen(false);
    onTargetSelected(target);
  }

  function toggleSelected(name: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (!next.delete(name)) next.add(name);
      return next;
    });
  }

  function toggleSelectAll() {
    setSelected((prev) =>
      prev.size === importable.length ? new Set() : new Set(importable.map((t) => t.name)),
    );
  }

  async function importSelected() {
    const queue = importable.filter((target) => selected.has(target.name));
    if (!queue.length) return;
    setProgress({ done: 0, total: queue.length });
    setCreated(0);
    setFailures([]);
    // One defaults lookup for the whole batch; without saved defaults the
    // scans are created with the server's own.
    let modelDefaults: ModelDefaults | null = null;
    try {
      modelDefaults = await client.getMyModelDefaults({});
    } catch {
      // No defaults is always a safe answer; the import keeps the fallback.
    }
    const defaults = runDefaultsFromModelDefaults(modelDefaults);
    let done = 0;
    let succeeded = 0;
    const failed: { name: string; error: string }[] = [];
    for (const target of queue) {
      try {
        await client.createSecurityScan(buildImportedScanCreateRequest(target, {
          defaults,
          dockerInDocker: canUseDockerInDocker,
        }));
        succeeded += 1;
        setCreated(succeeded);
      } catch (err: unknown) {
        // Earlier successes stand; a failure only removes that one target.
        failed.push({
          name: target.name,
          error: err instanceof Error ? err.message : "Import failed",
        });
        setFailures([...failed]);
      }
      done += 1;
      setProgress({ done, total: queue.length });
    }
    setSelected(new Set(failed.map((f) => f.name)));
    setProgress(null);
    onImported?.({ created: succeeded, failed: failed.length });
  }

  return (
    <Dialog open={open} onOpenChange={(next) => !running && setOpen(next)}>
      <DialogTrigger render={trigger} />
      <DialogContent className="flex max-h-[92vh] w-full max-w-3xl flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl">
        <DialogHeader className="space-y-1 border-b px-6 py-5">
          <DialogTitle>Choose a scan target</DialogTitle>
          <DialogDescription>
            Import every available security-program target at once, or pick a single target to
            prefill a new scan and review it before creating.
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">
          <p className="mb-4 rounded-lg border border-border bg-muted/40 px-3 py-2 text-sm font-medium">
            Importing creates the selected scan configurations — no scan is run. They use your saved
            credentials and default model settings, a manual-only schedule, workspace-write access,
            and unrestricted network egress. Configure scan instead opens the form so you can review
            everything before creating.
          </p>
          {targets.length === 0 ? (
            <p className="rounded-lg border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
              No importable scan targets are available.
            </p>
          ) : (
            <>
              <div className="mb-3 flex flex-wrap items-center gap-3">
                <label className="flex items-center gap-2 text-sm font-medium">
                  <input
                    type="checkbox"
                    aria-label="Select all"
                    checked={importable.length > 0 && selected.size === importable.length}
                    disabled={running || importable.length === 0}
                    onChange={toggleSelectAll}
                  />
                  Select all
                </label>
                {selected.size > 0 && (
                  <div
                    role="toolbar"
                    aria-label="Bulk actions"
                    className="flex flex-wrap items-center gap-2 rounded-lg border border-border/70 bg-muted/20 px-3 py-1.5"
                  >
                    <span className="text-[12.5px] font-medium">{selected.size} selected</span>
                    <Button
                      type="button"
                      size="sm"
                      disabled={running}
                      onClick={() => void importSelected()}
                    >
                      {running ? "Importing…" : `Import ${selected.size} scans`}
                    </Button>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      disabled={running}
                      onClick={() => setSelected(new Set())}
                    >
                      Clear selection
                    </Button>
                  </div>
                )}
              </div>
              {running && (
                <p className="mb-3 text-sm text-muted-foreground" aria-live="polite">
                  Importing {Math.min(progress.done + 1, progress.total)} of {progress.total}…
                </p>
              )}
              {!running && created > 0 && (
                <p className="mb-3 text-sm font-medium">Imported {created} scans</p>
              )}
              {failures.length > 0 && (
                <div
                  role="alert"
                  className="mb-3 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]"
                >
                  <span className="font-medium">Some scans could not be imported.</span>
                  <ul className="mt-1 list-disc pl-5 text-muted-foreground">
                    {failures.map((failure) => (
                      <li key={failure.name} className="font-mono text-[11.5px]">
                        {failure.name}: {failure.error}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              <ul className="divide-y rounded-lg border" aria-label="Importable scan targets">
                {targets.map((target) => {
                  const exists = existingNames.has(target.name);
                  return (
                    <li
                      key={target.name}
                      className="grid gap-3 px-3 py-2.5 sm:grid-cols-[auto_1fr_auto] sm:items-center"
                    >
                      <input
                        type="checkbox"
                        aria-label={`Select ${target.displayName}`}
                        checked={selected.has(target.name)}
                        disabled={exists || running}
                        onChange={() => toggleSelected(target.name)}
                      />
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
                          {target.targetUrl || target.repoUrl}
                          {target.repoUrl && ` · ${target.baseBranch}`}
                        </div>
                        <div className="font-mono text-xs text-muted-foreground">{target.workflowRef}</div>
                      </div>
                      <Button
                        type="button"
                        size="sm"
                        variant="outline"
                        disabled={exists || running}
                        aria-label={`Configure scan for ${target.displayName}`}
                        onClick={() => selectTarget(target)}
                      >
                        Configure scan
                      </Button>
                    </li>
                  );
                })}
              </ul>
            </>
          )}
        </div>
        <div className="flex justify-end gap-2 border-t px-6 py-4">
          <Button type="button" variant="ghost" disabled={running} onClick={() => setOpen(false)}>
            Close
          </Button>
          <Button
            type="button"
            disabled={running || selected.size === 0}
            onClick={() => void importSelected()}
          >
            {running
              ? `Importing ${Math.min(progress.done + 1, progress.total)} of ${progress.total}…`
              : `Import ${selected.size} scans`}
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
