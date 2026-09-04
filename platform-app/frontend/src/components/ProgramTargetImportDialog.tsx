import { useMemo, useState } from "react";
import { Search } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { scanConfigUsesSavedCredentials } from "@/components/SecurityScanFormDialog";
import { client } from "@/lib/client";
import { importableProgramTargets, type ProgramScanTarget } from "@/lib/programTargetCatalog";
import {
  buildImportedScanCreateRequest,
  buildImportedScanUpdateRequest,
  programTargetDrift,
  programTargetImportStatus,
  runDefaultsFromModelDefaults,
  type ProgramTargetImportStatus,
} from "@/lib/securityScanImport";
import { useOptionalAuth } from "@/contexts/AuthContext";
import { cn } from "@/lib/utils";
import type { ModelDefaults, SecurityProgramResource, SecurityScanConfig } from "@/rpc/platform/service_pb";

type StatusFilter = "all" | ProgramTargetImportStatus;

const statusLabels: Record<ProgramTargetImportStatus, string> = {
  "new": "New",
  "update-available": "Update available",
  "up-to-date": "Up to date",
};

type ClassifiedTarget = {
  target: ProgramScanTarget;
  status: ProgramTargetImportStatus;
  existing?: SecurityScanConfig;
  drift: string[];
};

export type ProgramTargetImportSummary = {
  created: number;
  updated: number;
  failed: number;
};

function plural(count: number, singular: string, pluralForm = `${singular}s`): string {
  return `${count} ${count === 1 ? singular : pluralForm}`;
}

export function ProgramTargetImportDialog({
  programs,
  existingConfigs,
  trigger,
  onTargetSelected,
  onImported,
}: {
  programs: readonly SecurityProgramResource[];
  /** Configurations in the caller's personal namespace, matched to targets by name. */
  existingConfigs: readonly SecurityScanConfig[];
  trigger: React.ReactElement;
  onTargetSelected: (target: ProgramScanTarget) => void;
  onImported?: (summary: ProgramTargetImportSummary) => void;
}) {
  const auth = useOptionalAuth();
  const canUseDockerInDocker = auth?.user?.role === "admin";
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [progress, setProgress] = useState<{ done: number; total: number } | null>(null);
  const [summary, setSummary] = useState<ProgramTargetImportSummary | null>(null);
  const [failures, setFailures] = useState<{ name: string; error: string }[]>([]);

  const classified = useMemo<ClassifiedTarget[]>(() => {
    const byName = new Map(existingConfigs.map((config) => [config.name, config]));
    return importableProgramTargets(programs).map((target) => {
      const existing = byName.get(target.name);
      return {
        target,
        existing,
        status: programTargetImportStatus(target, existing),
        drift: existing ? programTargetDrift(target, existing) : [],
      };
    });
  }, [programs, existingConfigs]);

  const counts = useMemo(() => {
    const result: Record<ProgramTargetImportStatus, number> = { "new": 0, "update-available": 0, "up-to-date": 0 };
    for (const item of classified) result[item.status] += 1;
    return result;
  }, [classified]);

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return classified.filter((item) => {
      if (statusFilter !== "all" && item.status !== statusFilter) return false;
      if (!needle) return true;
      const { target } = item;
      return [target.displayName, target.name, target.repoUrl, target.targetUrl, target.workflowRef, target.securityProgramRef]
        .some((value) => value.toLowerCase().includes(needle));
    });
  }, [classified, query, statusFilter]);

  const actionable = (items: ClassifiedTarget[]) => items.filter((item) => item.status !== "up-to-date");
  const visibleActionable = actionable(visible);
  const visibleSelectedCount = visibleActionable.filter((item) => selected.has(item.target.name)).length;
  const allVisibleSelected = visibleActionable.length > 0 && visibleSelectedCount === visibleActionable.length;
  const someVisibleSelected = visibleSelectedCount > 0 && !allVisibleSelected;
  const visibleNew = visible.filter((item) => item.status === "new");
  const visibleUpdates = visible.filter((item) => item.status === "update-available");
  const filtersActive = statusFilter !== "all" || query.trim() !== "";
  const running = progress !== null;

  const selectedItems = classified.filter((item) => item.status !== "up-to-date" && selected.has(item.target.name));
  const selectedCreates = selectedItems.filter((item) => item.status === "new").length;
  const selectedUpdates = selectedItems.length - selectedCreates;

  function importLabel(): string {
    if (selectedItems.length === 0) return "Import";
    if (selectedUpdates === 0) return `Import ${plural(selectedCreates, "scan")}`;
    if (selectedCreates === 0) return `Update ${plural(selectedUpdates, "scan")}`;
    return `Import ${selectedCreates} and update ${selectedUpdates}`;
  }

  function selectTarget(target: ProgramScanTarget) {
    setOpen(false);
    onTargetSelected(target);
  }

  function changeSelection(items: ClassifiedTarget[], mode: "add" | "remove" | "only") {
    setSelected((prev) => {
      const next = mode === "only" ? new Set<string>() : new Set(prev);
      for (const item of items) {
        if (mode === "remove") next.delete(item.target.name);
        else next.add(item.target.name);
      }
      return next;
    });
  }

  function toggleSelected(name: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (!next.delete(name)) next.add(name);
      return next;
    });
  }

  function toggleSelectAllVisible() {
    changeSelection(visibleActionable, allVisibleSelected ? "remove" : "add");
  }

  function handleOpenChange(next: boolean) {
    if (running) return;
    setOpen(next);
    if (next) {
      setQuery("");
      setStatusFilter("all");
      setSummary(null);
      setFailures([]);
    }
  }

  async function importSelected() {
    const queue = selectedItems;
    if (!queue.length) return;
    setProgress({ done: 0, total: queue.length });
    setSummary(null);
    setFailures([]);
    // One defaults lookup for the whole batch; without saved defaults the
    // scans are created with the server's own.
    let modelDefaults: ModelDefaults | null = null;
    if (queue.some((item) => item.status === "new")) {
      try {
        modelDefaults = await client.getMyModelDefaults({});
      } catch {
        // No defaults is always a safe answer; the import keeps the fallback.
      }
    }
    const defaults = runDefaultsFromModelDefaults(modelDefaults);
    let done = 0;
    let created = 0;
    let updated = 0;
    const failed: { name: string; error: string }[] = [];
    for (const item of queue) {
      try {
        if (item.existing) {
          await client.updateSecurityScan(buildImportedScanUpdateRequest(item.target, item.existing, {
            useSavedCredentials: scanConfigUsesSavedCredentials(item.existing),
          }));
          updated += 1;
        } else {
          await client.createSecurityScan(buildImportedScanCreateRequest(item.target, {
            defaults,
            dockerInDocker: canUseDockerInDocker,
          }));
          created += 1;
        }
      } catch (err: unknown) {
        // Earlier successes stand; a failure only removes that one target.
        failed.push({
          name: item.target.name,
          error: err instanceof Error ? err.message : "Import failed",
        });
        setFailures([...failed]);
      }
      done += 1;
      setProgress({ done, total: queue.length });
    }
    setSelected(new Set(failed.map((f) => f.name)));
    setProgress(null);
    const result = { created, updated, failed: failed.length };
    setSummary(result);
    onImported?.(result);
  }

  function summaryText(result: ProgramTargetImportSummary): string {
    const parts = [
      result.created > 0 ? `Imported ${plural(result.created, "scan")}` : "",
      result.updated > 0 ? `Updated ${plural(result.updated, "scan")}` : "",
    ].filter(Boolean);
    return parts.length ? parts.join(" · ") : "Nothing was imported";
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogTrigger render={trigger} />
      <DialogContent className="flex h-[100dvh] max-h-[100dvh] w-full max-w-3xl flex-col gap-0 overflow-hidden rounded-none p-0 sm:h-auto sm:max-h-[92vh] sm:w-[calc(100%-2rem)] sm:max-w-3xl sm:rounded-lg">
        <DialogHeader className="space-y-1 border-b px-4 py-4 pr-12 sm:px-6 sm:py-5">
          <DialogTitle>Import scan targets</DialogTitle>
          <DialogDescription>
            Import security-program targets as scan configurations, refresh existing ones whose
            program definition changed, or pick a single target to prefill a new scan and review it
            before creating.
          </DialogDescription>
        </DialogHeader>
        <div className="min-h-0 flex-1 space-y-3 overflow-y-auto px-4 py-4 sm:px-6 sm:py-5">
          <p className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-sm">
            <span className="font-medium">Importing creates the selected scan configurations — no scan is run.</span>{" "}
            New scans use your saved credentials and default model settings, a manual-only schedule,
            workspace-write access, and unrestricted network egress. Updates rewrite only the
            program-defined target fields (repository or URL, branch, workflow, policy pack, parameters)
            and keep everything else. Use Configure scan to review a new scan before creating it.
          </p>
          {classified.length === 0 ? (
            <p className="rounded-lg border border-dashed px-4 py-8 text-center text-sm text-muted-foreground">
              No importable scan targets are available.
            </p>
          ) : (
            <>
              {counts["update-available"] > 0 && (
                <div role="status" className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-sky-500/40 bg-sky-500/5 p-3 text-sm">
                  <p>
                    <span className="font-medium">
                      {counts["update-available"]} existing {counts["update-available"] === 1 ? "configuration" : "configurations"} no longer {counts["update-available"] === 1 ? "matches" : "match"} the program target.
                    </span>{" "}
                    <span className="text-muted-foreground">Select {counts["update-available"] === 1 ? "it" : "them"} to refresh the target fields.</span>
                  </p>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={running}
                    onClick={() => {
                      changeSelection(classified.filter((item) => item.status === "update-available"), "add");
                      setStatusFilter("update-available");
                    }}
                  >
                    Select all {plural(counts["update-available"], "update")}
                  </Button>
                </div>
              )}

              <div className="flex flex-col gap-2 sm:flex-row">
                <div className="relative min-w-0 flex-1">
                  <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    aria-label="Search scan targets"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    placeholder="Search targets…"
                    className="min-h-10 pl-8"
                  />
                </div>
                <select
                  aria-label="Target status"
                  value={statusFilter}
                  onChange={(event) => setStatusFilter(event.target.value as StatusFilter)}
                  className="min-h-10 rounded-md border border-input bg-background px-3 text-sm"
                >
                  <option value="all">All statuses ({classified.length})</option>
                  {(Object.keys(statusLabels) as ProgramTargetImportStatus[]).map((value) => (
                    <option key={value} value={value} disabled={counts[value] === 0}>
                      {statusLabels[value]} ({counts[value]})
                    </option>
                  ))}
                </select>
              </div>

              <div
                role="toolbar"
                aria-label="Selection"
                className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-border/70 bg-muted/20 px-3 py-2"
              >
                <label className="flex min-h-9 cursor-pointer items-center gap-2 text-sm font-medium">
                  <input
                    type="checkbox"
                    className="size-4 accent-primary"
                    aria-label={filtersActive ? "Select all visible" : "Select all"}
                    checked={allVisibleSelected}
                    ref={(node) => { if (node) node.indeterminate = someVisibleSelected; }}
                    disabled={running || visibleActionable.length === 0}
                    onChange={toggleSelectAllVisible}
                  />
                  {filtersActive ? "Select all visible" : "Select all"}
                  <span className="font-normal text-muted-foreground">({visibleActionable.length})</span>
                </label>
                <span className="hidden h-4 w-px bg-border sm:block" aria-hidden />
                <span className="text-xs text-muted-foreground">Quick select:</span>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  className="h-8"
                  disabled={running || visibleNew.length === 0}
                  onClick={() => changeSelection(visibleNew, "add")}
                >
                  New ({visibleNew.length})
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  className="h-8"
                  disabled={running || visibleUpdates.length === 0}
                  onClick={() => changeSelection(visibleUpdates, "add")}
                >
                  Updates available ({visibleUpdates.length})
                </Button>
                <span className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
                  <span>{visible.length} of {classified.length} targets · {selectedItems.length} selected</span>
                  {selectedItems.length > 0 && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      className="h-8"
                      disabled={running}
                      onClick={() => changeSelection([], "only")}
                    >
                      Clear selection
                    </Button>
                  )}
                </span>
              </div>

              {running && (
                <p className="text-sm text-muted-foreground" aria-live="polite">
                  Importing {Math.min(progress.done + 1, progress.total)} of {progress.total}…
                </p>
              )}
              {!running && summary && (
                <p role="status" className="rounded-lg border border-emerald-500/40 bg-emerald-500/5 px-3 py-2 text-sm font-medium text-emerald-700 dark:text-emerald-300">
                  {summaryText(summary)}
                </p>
              )}
              {failures.length > 0 && (
                <div
                  role="alert"
                  className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]"
                >
                  <span className="font-medium">Some scans could not be imported. They stay selected so you can retry.</span>
                  <ul className="mt-1 list-disc pl-5 text-muted-foreground">
                    {failures.map((failure) => (
                      <li key={failure.name} className="font-mono text-[11.5px]">
                        {failure.name}: {failure.error}
                      </li>
                    ))}
                  </ul>
                </div>
              )}

              {visible.length === 0 ? (
                <div className="space-y-3 rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
                  <p>No scan targets match this search and status.</p>
                  <Button size="sm" variant="outline" onClick={() => { setQuery(""); setStatusFilter("all"); }}>Clear filters</Button>
                </div>
              ) : (
                <ul className="divide-y rounded-lg border" aria-label="Importable scan targets">
                  {visible.map(({ target, status, drift }) => {
                    const upToDate = status === "up-to-date";
                    const isSelected = !upToDate && selected.has(target.name);
                    return (
                      <li
                        key={`${target.securityProgramRef}:${target.name}:${target.displayName}`}
                        className={cn(
                          "grid gap-3 px-3 py-2.5 transition-colors sm:grid-cols-[auto_1fr_auto] sm:items-center",
                          isSelected && "bg-primary/5",
                          upToDate && "text-muted-foreground",
                        )}
                      >
                        <input
                          type="checkbox"
                          className="size-4 accent-primary"
                          aria-label={`Select ${target.displayName}`}
                          checked={isSelected}
                          disabled={upToDate || running}
                          onChange={() => toggleSelected(target.name)}
                        />
                        <div className="min-w-0">
                          <div className="flex flex-wrap items-center gap-2">
                            <span className="font-medium text-foreground">{target.displayName}</span>
                            <Badge variant={status === "update-available" ? "default" : status === "new" ? "outline" : "secondary"}>
                              {statusLabels[status]}
                            </Badge>
                          </div>
                          <div className="truncate font-mono text-xs text-muted-foreground">
                            {target.targetUrl || target.repoUrl}
                            {target.repoUrl && ` · ${target.baseBranch}`}
                          </div>
                          <div className="font-mono text-xs text-muted-foreground">{target.workflowRef}</div>
                          {status === "update-available" && drift.length > 0 && (
                            <div className="mt-1 text-xs text-muted-foreground">
                              Changed: {drift.join(", ")}
                            </div>
                          )}
                        </div>
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          disabled={status !== "new" || running}
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
            </>
          )}
        </div>
        <div className="flex min-h-16 items-center justify-between gap-3 border-t bg-background px-4 py-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] sm:px-6">
          <p className="text-xs font-medium text-muted-foreground">
            {selectedItems.length === 0
              ? "Nothing selected"
              : [
                  selectedCreates > 0 ? `${selectedCreates} new` : "",
                  selectedUpdates > 0 ? `${selectedUpdates} to update` : "",
                ].filter(Boolean).join(" · ")}
          </p>
          <div className="flex items-center gap-2">
            <Button type="button" variant="ghost" disabled={running} onClick={() => setOpen(false)}>
              Close
            </Button>
            <Button
              type="button"
              disabled={running || selectedItems.length === 0}
              onClick={() => void importSelected()}
            >
              {running
                ? `Importing ${Math.min(progress.done + 1, progress.total)} of ${progress.total}…`
                : importLabel()}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
