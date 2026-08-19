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

function entryKey(entry: SecurityCatalogEntry): string {
  return `${entry.resource?.kind ?? 0}:${entry.resource?.name ?? ""}`;
}

function stateVariant(state: SecurityCatalogInstallState): "outline" | "secondary" | "destructive" {
  if (state === SecurityCatalogInstallState.CONFLICT || state === SecurityCatalogInstallState.MODIFIED) {
    return "destructive";
  }
  return state === SecurityCatalogInstallState.NOT_INSTALLED ? "outline" : "secondary";
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

  const visibleEntries = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return (catalog?.entries ?? []).filter((entry) => {
      if (kind !== "all" && entry.resource?.kind !== Number(kind)) return false;
      if (!needle) return true;
      return [entry.title, entry.description, entry.resource?.name ?? "", kindLabel(entry.resource?.kind ?? 0)]
        .some((value) => value.toLowerCase().includes(needle));
    });
  }, [catalog, kind, query]);

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

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogTrigger
        render={
          <Button size="sm">
            <Boxes className="size-3.5" /> Add from shipped catalog
          </Button>
        }
      />
      <DialogContent className="flex max-h-[90vh] w-full max-w-4xl flex-col gap-0 overflow-hidden p-0 sm:max-w-4xl" showCloseButton>
        <DialogHeader className="space-y-1 border-b px-6 py-5">
          <DialogTitle>Add from shipped security catalog</DialogTitle>
          <DialogDescription>
            Choose reusable security resources, review their dependency-expanded plan, then apply it explicitly.
            Skills are managed later in <Link to="/settings/skills">Settings</Link>. Install Programs here before importing their scan targets on <Link to="/security/configs">Configurations</Link>.
          </DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-6 py-5">
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
              <div className="flex flex-col gap-2 sm:flex-row">
                <div className="relative min-w-0 flex-1">
                  <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
                  <Input
                    aria-label="Search catalog"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    placeholder="Search catalog…"
                    className="pl-8"
                  />
                </div>
                <select
                  aria-label="Catalog kind"
                  value={kind}
                  onChange={(event) => setKind(event.target.value)}
                  className="h-9 rounded-md border border-input bg-background px-3 text-sm"
                >
                  <option value="all">All kinds</option>
                  {availableKinds.map((value) => <option key={value} value={value}>{kindLabel(value)}</option>)}
                </select>
              </div>

              {catalog.entries.length === 0 ? (
                <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">The shipped catalog has no items.</p>
              ) : visibleEntries.length === 0 ? (
                <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">No catalog items match this search and kind.</p>
              ) : (
                <ul className="divide-y rounded-lg border" aria-label="Shipped security catalog">
                  {visibleEntries.map((entry) => {
                    const resource = entry.resource;
                    if (!resource) return null;
                    const key = entryKey(entry);
                    return (
                      <li key={key} className="p-3">
                        <label className="flex cursor-pointer items-start gap-3">
                          <input
                            type="checkbox"
                            className="mt-1 size-4"
                            checked={selected.has(key)}
                            onChange={() => toggle(entry)}
                            aria-label={`Select ${kindLabel(resource.kind)} ${entry.title || resource.name}`}
                          />
                          <span className="min-w-0 flex-1 space-y-1.5">
                            <span className="flex flex-wrap items-center gap-2">
                              <span className="font-medium">{entry.title || resource.name}</span>
                              <Badge variant="outline">{kindLabel(resource.kind)}</Badge>
                              <Badge variant={stateVariant(entry.installState)}>{installStateLabels[entry.installState] ?? "State unavailable"}</Badge>
                              <Badge variant={entry.ready ? "secondary" : "destructive"}>{entry.ready ? "Ready" : "Not ready"}</Badge>
                            </span>
                            <span className="block text-sm text-muted-foreground">{entry.description}</span>
                            <span className="block font-mono text-[11px] text-muted-foreground">{resource.name}</span>
                            {!entry.ready && entry.readinessMessage && <span className="block text-xs text-destructive">{entry.readinessMessage}</span>}
                            {entry.dependencies.length > 0 && (
                              <span className="block text-xs text-muted-foreground">
                                Depends on {entry.dependencies.map((dependency) => `${dependency.resource ? `${kindLabel(dependency.resource.kind)} / ${dependency.resource.name}` : "unknown"}${dependency.required ? " (required)" : " (optional)"}`).join(", ")}
                              </span>
                            )}
                          </span>
                        </label>
                      </li>
                    );
                  })}
                </ul>
              )}
            </>
          )}

          {!loading && catalog && review && (
            <div className="space-y-3">
              <div>
                <h3 className="font-medium">Dependency-expanded installation plan</h3>
                <p className="text-sm text-muted-foreground">Items are ordered so required dependencies are installed first.</p>
              </div>
              <ul className="divide-y rounded-lg border" aria-label="Catalog installation plan">
                {review.results.map((result, index) => {
                  const entry = result.entry;
                  const resource = entry?.resource;
                  return (
                    <li key={`${resource?.kind ?? 0}:${resource?.name ?? index}`} className="flex items-start justify-between gap-4 p-3">
                      <div className="min-w-0">
                        <p className="font-medium">{entry?.title || resource?.name || "Unknown item"}</p>
                        <p className="font-mono text-[11px] text-muted-foreground">{resource ? `${kindLabel(resource.kind)} / ${resource.name}` : "Unknown catalog resource"}</p>
                        {result.message && <p className={`mt-1 text-xs ${result.action === "blocked" ? "text-destructive" : "text-muted-foreground"}`}>{result.message}</p>}
                      </div>
                      <Badge variant={result.action === "blocked" ? "destructive" : result.action === "unchanged" ? "secondary" : "outline"}>
                        {result.action || "unknown"}
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

        <div className="flex justify-end gap-2 border-t px-6 py-4">
          {review ? (
            <Button type="button" variant="ghost" onClick={() => { setReview(null); setOperationError(""); }}>Back</Button>
          ) : (
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
          )}
          {review ? (
            <Button type="button" onClick={() => void applyInstall()} disabled={busy}>
              {busy ? "Applying…" : "Apply plan"}
            </Button>
          ) : (
            <Button type="button" onClick={() => void previewInstall()} disabled={busy || !catalog?.ready || selected.size === 0}>
              {busy ? "Building plan…" : `Review selection${selected.size ? ` (${selected.size})` : ""}`}
            </Button>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
