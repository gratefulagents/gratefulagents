import { useMemo, useState } from "react";
import { Code } from "@connectrpc/connect";
import { Link } from "react-router-dom";
import { Boxes, Loader2, Search } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { client } from "@/lib/client";
import { connectCodeOf } from "@/lib/rpc-errors";
import { cn } from "@/lib/utils";
import {
  SecurityCatalogInstallState,
  SecurityCatalogKind,
  type SecurityCatalog,
  type SecurityCatalogEntry,
  type SecurityCatalogInstallResponse,
} from "@/rpc/platform/service_pb";

const kindLabels: Record<number, string> = {
  [SecurityCatalogKind.SKILL]: "Skills",
  [SecurityCatalogKind.WORKFLOW]: "Workflows",
  [SecurityCatalogKind.RANKER]: "Rankers",
  [SecurityCatalogKind.POST_SCRIPT]: "Post-scripts",
  [SecurityCatalogKind.POLICY_PACK]: "Policy packs",
  [SecurityCatalogKind.PROGRAM]: "Programs",
};

const installStateLabels: Record<number, string> = {
  [SecurityCatalogInstallState.UNSPECIFIED]: "State unavailable",
  [SecurityCatalogInstallState.NOT_INSTALLED]: "Not installed",
  [SecurityCatalogInstallState.INSTALLED]: "Installed",
  [SecurityCatalogInstallState.UPDATE_AVAILABLE]: "Update available",
  [SecurityCatalogInstallState.MODIFIED]: "Locally modified",
  [SecurityCatalogInstallState.CONFLICT]: "Conflict",
};

function kindLabel(kind: SecurityCatalogKind): string {
  return kindLabels[kind] ?? "Other";
}

const statusFilterOrder: SecurityCatalogInstallState[] = [
  SecurityCatalogInstallState.UPDATE_AVAILABLE,
  SecurityCatalogInstallState.NOT_INSTALLED,
  SecurityCatalogInstallState.INSTALLED,
  SecurityCatalogInstallState.MODIFIED,
  SecurityCatalogInstallState.CONFLICT,
  SecurityCatalogInstallState.UNSPECIFIED,
];

/** selectableInBulk excludes items whose plan would only ever come back blocked. */
function selectableInBulk(entry: SecurityCatalogEntry): boolean {
  return Boolean(entry.resource) && entry.ready;
}

function entryKey(entry: SecurityCatalogEntry): string {
  return `${entry.resource?.kind ?? 0}:${entry.resource?.name ?? ""}`;
}

function stateVariant(state: SecurityCatalogInstallState): "outline" | "secondary" | "destructive" {
  if (state === SecurityCatalogInstallState.CONFLICT || state === SecurityCatalogInstallState.MODIFIED) {
    return "destructive";
  }
  return state === SecurityCatalogInstallState.NOT_INSTALLED ? "outline" : "secondary";
}

function actionLabel(action: string): string {
  switch (action) {
    case "create": return "Will create";
    case "refresh": return "Will update";
    case "unchanged": return "No change";
    case "blocked": return "Blocked";
    default: return action || "Unknown";
  }
}

function messageOf(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}

export function SecurityCatalogDialog({
  onInstalled,
}: {
  onInstalled: () => void | Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [catalog, setCatalog] = useState<SecurityCatalog | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [query, setQuery] = useState("");
  const [kind, setKind] = useState("all");
  const [status, setStatus] = useState("all");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [review, setReview] = useState<SecurityCatalogInstallResponse | null>(null);
  const [operationError, setOperationError] = useState("");
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState(false);
  const [success, setSuccess] = useState("");

  const availableKinds = useMemo(() => {
    const values = new Set(catalog?.entries.map((entry) => entry.resource?.kind).filter(
      (value): value is SecurityCatalogKind => value !== undefined,
    ));
    return [...values].sort((a, b) => a - b);
  }, [catalog]);

  const statusCounts = useMemo(() => {
    const counts = new Map<SecurityCatalogInstallState, number>();
    for (const entry of catalog?.entries ?? []) {
      counts.set(entry.installState, (counts.get(entry.installState) ?? 0) + 1);
    }
    return counts;
  }, [catalog]);

  const availableStatuses = statusFilterOrder.filter((value) => (statusCounts.get(value) ?? 0) > 0);

  const visibleEntries = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return (catalog?.entries ?? []).filter((entry) => {
      if (kind !== "all" && entry.resource?.kind !== Number(kind)) return false;
      if (status !== "all" && entry.installState !== Number(status)) return false;
      if (!needle) return true;
      return [entry.title, entry.description, entry.resource?.name ?? "", kindLabel(entry.resource?.kind ?? 0)]
        .some((value) => value.toLowerCase().includes(needle));
    });
  }, [catalog, kind, query, status]);

  const bulkSelectable = visibleEntries.filter(selectableInBulk);
  const visibleSelectedCount = bulkSelectable.filter((entry) => selected.has(entryKey(entry))).length;
  const allVisibleSelected = bulkSelectable.length > 0 && visibleSelectedCount === bulkSelectable.length;
  const someVisibleSelected = visibleSelectedCount > 0 && !allVisibleSelected;
  const visibleUpdates = bulkSelectable.filter((entry) => entry.installState === SecurityCatalogInstallState.UPDATE_AVAILABLE);
  const visibleNotInstalled = bulkSelectable.filter((entry) => entry.installState === SecurityCatalogInstallState.NOT_INSTALLED);
  const filtersActive = kind !== "all" || status !== "all" || query.trim() !== "";
  const allUpdates = (catalog?.entries ?? []).filter(
    (entry) => selectableInBulk(entry) && entry.installState === SecurityCatalogInstallState.UPDATE_AVAILABLE,
  );
  const allUpdatesSelected = allUpdates.length > 0 && allUpdates.every((entry) => selected.has(entryKey(entry)));

  async function loadCatalog() {
    setLoading(true);
    setLoadError("");
    setOperationError("");
    setStale(false);
    try {
      const response = await client.listSecurityCatalog({});
      setCatalog(response);
      setSelected(new Set());
      setReview(null);
    } catch (error: unknown) {
      setCatalog(null);
      setLoadError(`The shipped security catalog is unavailable. ${messageOf(error, "Failed to load the catalog.")}`);
    } finally {
      setLoading(false);
    }
  }

  function handleOpenChange(nextOpen: boolean) {
    setOpen(nextOpen);
    if (nextOpen) {
      setQuery("");
      setKind("all");
      setStatus("all");
      setSuccess("");
      void loadCatalog();
    }
  }

  function toggle(entry: SecurityCatalogEntry) {
    const key = entryKey(entry);
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
    setReview(null);
    setOperationError("");
  }

  function replaceSelection(entries: SecurityCatalogEntry[], mode: "add" | "remove" | "only") {
    setSelected((current) => {
      const next = mode === "only" ? new Set<string>() : new Set(current);
      for (const entry of entries) {
        if (mode === "remove") next.delete(entryKey(entry));
        else next.add(entryKey(entry));
      }
      return next;
    });
    setReview(null);
    setOperationError("");
  }

  function toggleSelectAllVisible() {
    replaceSelection(bulkSelectable, allVisibleSelected ? "remove" : "add");
  }

  function requestResources() {
    return (catalog?.entries ?? [])
      .filter((entry) => selected.has(entryKey(entry)) && entry.resource)
      .map((entry) => ({
        kind: entry.resource!.kind,
        name: entry.resource!.name,
      }));
  }

  function handleOperationError(error: unknown, fallback: string) {
    if (connectCodeOf(error) === Code.FailedPrecondition && /revision|stale/i.test(messageOf(error, ""))) {
      setStale(true);
      setReview(null);
      setOperationError("The shipped catalog changed while you were reviewing it. Refresh the catalog and review the new plan before applying.");
      return;
    }
    setOperationError(messageOf(error, fallback));
  }

  async function previewInstall() {
    if (!catalog) return;
    setBusy(true);
    setOperationError("");
    setStale(false);
    try {
      const response = await client.dryRunSecurityCatalogInstall({
        catalogRevision: catalog.revision,
        resources: requestResources(),
      });
      setReview(response);
    } catch (error: unknown) {
      handleOperationError(error, "Failed to build the installation plan.");
    } finally {
      setBusy(false);
    }
  }

  async function applyInstall() {
    if (!catalog || !review) return;
    setBusy(true);
    setOperationError("");
    setStale(false);
    try {
      const response = await client.applySecurityCatalogInstall({
        catalogRevision: catalog.revision,
        resources: requestResources(),
        planRevision: review.planRevision,
      });
      const successfulActions = new Set(["unchanged", "created", "refreshed", "adopted", "claimed"]);
      const changed = response.results.filter((result) => ["created", "refreshed", "adopted", "claimed"].includes(result.action)).length;
      const blockedCount = response.results.filter((result) => result.action === "blocked").length;
      const failedCount = response.results.filter((result) => result.action === "failed").length;
      const unknownCount = response.results.filter((result) => !successfulActions.has(result.action) && result.action !== "blocked" && result.action !== "failed").length;
      await Promise.all([onInstalled(), loadCatalog()]);
      if (blockedCount > 0 || failedCount > 0 || unknownCount > 0) {
        const issues = [
          blockedCount > 0 ? `${blockedCount} blocked` : "",
          failedCount > 0 ? `${failedCount} failed` : "",
          unknownCount > 0 ? `${unknownCount} returned an unknown result` : "",
        ].filter(Boolean).join(", ");
        const outcome = response.applied
          ? changed > 0
            ? `${changed} other item${changed === 1 ? " was" : "s were"} applied.`
            : "The server reports that some changes were applied before the issues occurred."
          : "No catalog changes were applied.";
        setOperationError(`Installation completed with issues: ${issues}. ${outcome}`);
      } else if (response.applied) {
        setSuccess(changed > 0 ? `Applied ${changed} catalog item${changed === 1 ? "" : "s"}.` : "The selected catalog items were applied.");
      } else if (changed > 0) {
        setOperationError("The server reported successful item changes but did not confirm that any changes were applied.");
      } else {
        setSuccess("The selected catalog items are already installed.");
      }
    } catch (error: unknown) {
      handleOperationError(error, "Failed to apply the installation plan.");
    } finally {
      setBusy(false);
    }
  }

  const blocked = review?.results.some((result) => result.action === "blocked") ?? false;
  const hasChanges = review?.results.some((result) => result.action === "create" || result.action === "refresh") ?? false;
  const planCreates = review?.results.filter((result) => result.action === "create").length ?? 0;
  const planUpdates = review?.results.filter((result) => result.action === "refresh").length ?? 0;
  const planUnchanged = review?.results.filter((result) => result.action === "unchanged").length ?? 0;
  const planChanges = planCreates + planUpdates;
  const displayedReviewResults = review
    ? [...review.results].sort((left, right) => {
        const leftSelected = left.entry ? selected.has(entryKey(left.entry)) : false;
        const rightSelected = right.entry ? selected.has(entryKey(right.entry)) : false;
        return Number(rightSelected) - Number(leftSelected);
      })
    : [];

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogTrigger
        render={
          <Button size="sm">
            <Boxes className="size-3.5" /> Add from shipped catalog
          </Button>
        }
      />
      <DialogContent className="flex h-[100dvh] max-h-[100dvh] w-full max-w-4xl flex-col gap-0 overflow-hidden rounded-none p-0 [&_[data-slot=dialog-close]]:size-11 sm:h-auto sm:max-h-[90vh] sm:w-[calc(100%-2rem)] sm:max-w-4xl sm:rounded-lg" showCloseButton>
        <DialogHeader className="space-y-1 border-b px-4 py-4 pr-12 sm:px-6 sm:py-5 sm:pr-12">
          <DialogTitle>Add from shipped security catalog</DialogTitle>
          <DialogDescription>
            Choose what to add. Required dependencies are included in the review before anything changes.
          </DialogDescription>
          <p className="text-xs text-muted-foreground">
            Manage installed skills in <Link className="font-medium underline underline-offset-2" to="/settings/skills">Settings</Link>. After adding a program, import its targets from <Link className="font-medium underline underline-offset-2" to="/security/configs">Configurations</Link>.
          </p>
        </DialogHeader>

        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-4 py-4 pb-8 sm:px-6 sm:py-5 sm:pb-8">
          {loading && (
            <div className="flex items-center gap-2 py-10 text-sm text-muted-foreground" role="status">
              <Loader2 className="size-4 animate-spin" /> Loading shipped catalog…
            </div>
          )}

          {!loading && loadError && (
            <div role="alert" className="space-y-3 rounded-lg border border-destructive/40 bg-destructive/5 p-4">
              <p className="font-medium text-destructive">Catalog unavailable</p>
              <p className="text-sm text-muted-foreground">{loadError}</p>
              <Button size="sm" variant="outline" onClick={() => void loadCatalog()}>Try again</Button>
            </div>
          )}

          {!loading && catalog && !review && (
            <>
              {!catalog.ready && (
                <div role="alert" className="rounded-lg border border-amber-500/40 bg-amber-500/5 p-3">
                  <p className="font-medium">Catalog unavailable for installation</p>
                  <p className="text-sm text-muted-foreground">{catalog.readinessMessage || "The shipped catalog is not ready."}</p>
                </div>
              )}
              {success && (
                <p role="status" className="rounded-lg border border-emerald-500/40 bg-emerald-500/5 p-3 text-sm text-emerald-700 dark:text-emerald-300">
                  {success} The library and catalog are now refreshed.
                </p>
              )}
              {allUpdates.length > 0 && catalog.ready && (
                <div role="status" className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-sky-500/40 bg-sky-500/5 p-3 text-sm">
                  <p>
                    <span className="font-medium">{allUpdates.length} installed {allUpdates.length === 1 ? "item has" : "items have"} an update available.</span>{" "}
                    <span className="text-muted-foreground">Select them to review what would change before applying.</span>
                  </p>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    disabled={allUpdatesSelected}
                    onClick={() => { replaceSelection(allUpdates, "add"); setStatus(String(SecurityCatalogInstallState.UPDATE_AVAILABLE)); }}
                  >
                    {allUpdatesSelected ? "All updates selected" : `Select all ${allUpdates.length} ${allUpdates.length === 1 ? "update" : "updates"}`}
                  </Button>
                </div>
              )}
              <div className="flex flex-col gap-2 sm:flex-row">
                <div className="relative min-w-0 flex-1">
                  <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    aria-label="Search catalog"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    placeholder="Search catalog…"
                    className="min-h-11 pl-8"
                  />
                </div>
                <select
                  aria-label="Catalog kind"
                  value={kind}
                  onChange={(event) => setKind(event.target.value)}
                  className="min-h-11 rounded-md border border-input bg-background px-3 text-sm"
                >
                  <option value="all">All kinds</option>
                  {availableKinds.map((value) => <option key={value} value={value}>{kindLabel(value)}</option>)}
                </select>
                <select
                  aria-label="Install status"
                  value={status}
                  onChange={(event) => setStatus(event.target.value)}
                  className="min-h-11 rounded-md border border-input bg-background px-3 text-sm"
                >
                  <option value="all">All statuses</option>
                  {availableStatuses.map((value) => (
                    <option key={value} value={value}>
                      {installStateLabels[value]} ({statusCounts.get(value) ?? 0})
                    </option>
                  ))}
                </select>
              </div>

              {catalog.entries.length === 0 ? (
                <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">The shipped catalog has no items.</p>
              ) : visibleEntries.length === 0 ? (
                <div className="space-y-3 rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
                  <p>No catalog items match this search, kind, and status.</p>
                  <Button size="sm" variant="outline" onClick={() => { setQuery(""); setKind("all"); setStatus("all"); }}>Clear filters</Button>
                </div>
              ) : (
                <div className="space-y-2">
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
                        disabled={bulkSelectable.length === 0}
                        onChange={toggleSelectAllVisible}
                      />
                      {filtersActive ? "Select all visible" : "Select all"}
                      <span className="font-normal text-muted-foreground">({bulkSelectable.length})</span>
                    </label>
                    <span className="hidden h-4 w-px bg-border sm:block" aria-hidden />
                    <span className="text-xs text-muted-foreground">Quick select:</span>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      className="h-8"
                      disabled={visibleUpdates.length === 0}
                      onClick={() => replaceSelection(visibleUpdates, "add")}
                    >
                      Updates available ({visibleUpdates.length})
                    </Button>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      className="h-8"
                      disabled={visibleNotInstalled.length === 0}
                      onClick={() => replaceSelection(visibleNotInstalled, "add")}
                    >
                      Not installed ({visibleNotInstalled.length})
                    </Button>
                    <span className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
                      <span>{visibleEntries.length} of {catalog.entries.length} items · {selected.size} selected</span>
                      {selected.size > 0 && (
                        <Button type="button" size="sm" variant="ghost" className="h-8" onClick={() => replaceSelection([], "only")}>
                          Clear selection
                        </Button>
                      )}
                    </span>
                  </div>
                  {bulkSelectable.length < visibleEntries.length && (
                    <p className="text-xs text-muted-foreground">
                      Items marked “Not ready” are skipped by bulk selection; they can still be selected individually.
                    </p>
                  )}
                <ul className="divide-y overflow-hidden rounded-lg border" aria-label="Shipped security catalog">
                  {visibleEntries.map((entry) => {
                    const resource = entry.resource;
                    if (!resource) return null;
                    const key = entryKey(entry);
                    return (
                      <li key={key} className={cn("transition-colors", selected.has(key) && "bg-primary/5")}>
                        <label className="flex min-h-11 cursor-pointer items-start gap-3 p-3 sm:p-4">
                          <input
                            type="checkbox"
                            className="mt-0.5 size-5 shrink-0 accent-primary"
                            checked={selected.has(key)}
                            onChange={() => toggle(entry)}
                            aria-label={`Select ${kindLabel(resource.kind)} ${entry.title || resource.name}`}
                          />
                          <span className="min-w-0 flex-1 space-y-1.5">
                            <span className="flex flex-wrap items-center gap-2">
                              <span className="font-medium">{entry.title || resource.name}</span>
                              <Badge variant="outline">{kindLabel(resource.kind)}</Badge>
                              <Badge variant={stateVariant(entry.installState)}>{installStateLabels[entry.installState] ?? "State unavailable"}</Badge>
                              {!entry.ready && <Badge variant="destructive">Not ready</Badge>}
                            </span>
                            <span className="block text-sm text-muted-foreground">{entry.description}</span>
                            <span className="block font-mono text-[11px] text-muted-foreground">{resource.name}</span>
                            {entry.installState === SecurityCatalogInstallState.INSTALLED && (
                              <span className="block text-xs text-muted-foreground">
                                Select again to verify its catalog dependencies.
                              </span>
                            )}
                            {!entry.ready && entry.readinessMessage && <span className="block text-xs text-destructive">{entry.readinessMessage}</span>}
                            {entry.dependencies.length > 0 && (
                              <details className="text-xs text-muted-foreground">
                                <summary className="w-fit cursor-pointer font-medium text-foreground/70">
                                  {entry.dependencies.length} {entry.dependencies.length === 1 ? "dependency" : "dependencies"}
                                </summary>
                                <span className="mt-1 block break-words">
                                  Depends on {entry.dependencies.map((dependency) => `${dependency.resource ? `${kindLabel(dependency.resource.kind)} / ${dependency.resource.name}` : "unknown"}${dependency.required ? " (required)" : " (optional)"}`).join(", ")}
                                </span>
                              </details>
                            )}
                          </span>
                        </label>
                      </li>
                    );
                  })}
                </ul>
                </div>
              )}
            </>
          )}

          {!loading && catalog && review && (
            <div className="space-y-3">
              <div>
                <h3 className="font-medium">Dependency-expanded installation plan</h3>
                <p className="text-sm text-muted-foreground">Your selected items appear first; required dependencies are included automatically.</p>
              </div>
              <ul className="divide-y rounded-lg border" aria-label="Catalog installation plan">
                {displayedReviewResults.map((result, index) => {
                  const entry = result.entry;
                  const resource = entry?.resource;
                  return (
                    <li key={`${resource?.kind ?? 0}:${resource?.name ?? index}`} className="flex items-start justify-between gap-4 p-3">
                      <div className="min-w-0">
                        <div className="flex flex-wrap items-center gap-2">
                          <p className="font-medium">{entry?.title || resource?.name || "Unknown item"}</p>
                          <Badge variant={entry && selected.has(entryKey(entry)) ? "secondary" : "outline"}>
                            {entry && selected.has(entryKey(entry)) ? "Selected item" : "Included dependency"}
                          </Badge>
                        </div>
                        <p className="font-mono text-[11px] text-muted-foreground">{resource ? `${kindLabel(resource.kind)} / ${resource.name}` : "Unknown catalog resource"}</p>
                        {result.message && <p className={`mt-1 text-xs ${result.action === "blocked" ? "text-destructive" : "text-muted-foreground"}`}>{result.message}</p>}
                      </div>
                      <Badge variant={result.action === "blocked" ? "destructive" : result.action === "unchanged" ? "secondary" : "outline"}>
                        {actionLabel(result.action)}
                      </Badge>
                    </li>
                  );
                })}
              </ul>
              {blocked && (
                <p role="alert" className="rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-sm text-amber-800 dark:text-amber-300">
                  Blocked items will be skipped. Independent items and optional branches can still be applied.
                </p>
              )}
              {!blocked && !hasChanges && (
                <p className="rounded-lg border bg-muted/30 p-3 text-sm text-muted-foreground">Everything in this plan is already installed.</p>
              )}
            </div>
          )}

          {operationError && (
            <div role="alert" className="space-y-2 rounded-lg border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
              <p>{operationError}</p>
              {stale && <Button size="sm" variant="outline" onClick={() => void loadCatalog()}>Refresh catalog</Button>}
            </div>
          )}
        </div>

        <div className="flex min-h-16 items-center justify-between gap-3 border-t bg-background px-4 py-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] sm:px-6">
          <p className="text-xs font-medium text-muted-foreground">
            {review
              ? `${planCreates} create · ${planUpdates} update · ${planUnchanged} unchanged`
              : `${selected.size} selected`}
          </p>
          <div className="flex items-center gap-2">
            {review ? (
              <Button className="min-h-11" type="button" variant="ghost" onClick={() => { setReview(null); setOperationError(""); }}>Edit selection</Button>
            ) : (
              <Button className="min-h-11" type="button" variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
            )}
            {review ? (
              <Button className="min-h-11" type="button" onClick={() => void applyInstall()} disabled={busy}>
                {busy ? "Applying…" : `Apply ${planChanges} ${planChanges === 1 ? "change" : "changes"}`}
              </Button>
            ) : (
              <Button className="min-h-11 disabled:bg-muted disabled:text-muted-foreground disabled:opacity-100" type="button" onClick={() => void previewInstall()} disabled={busy || !catalog?.ready || selected.size === 0}>
                {busy ? "Building plan…" : `Review selection (${selected.size})`}
              </Button>
            )}
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
