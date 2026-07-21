/* eslint-disable react-hooks/set-state-in-effect */
import { useMemo, useState, useEffect } from "react";
import { Link } from "react-router-dom";
import { Activity, Inbox, Bookmark, Check, ChevronDown, X } from "lucide-react";

import { StatusBadge } from "@/components/StatusBadge";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCaption,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ListState, TableRowSkeleton } from "@/components/ui/list-state";
import { ListSearchInput, filterByQuery } from "@/components/ui/list-search";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { toast } from "@/components/ui/toaster";
import { cn } from "@/lib/utils";
import { formatAge, formatRepoShort } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import { prLoopTone, toneSoft } from "@/lib/status";
import { isRunComputing } from "@/lib/runStatus";
import type { AgentRun } from "@/rpc/platform/service_pb";

type SortKey = "age" | "name" | "cost";

function formatPRLoopBadge(run: AgentRun): string | null {
  const loop = run.prLoop;
  if (!loop?.state) return null;
  const maxRounds = loop.maxRounds || 3;
  const round = loop.reviewRound || 0;
  const state = loop.state.replace(/_/g, " ");
  return round > 0 ? `${state} ${round}/${maxRounds}` : state;
}

function displayMode(run: AgentRun): string {
  return run.modeName || run.workflowMode;
}

type AgentRunTableProps = {
  runs: AgentRun[];
  loading: boolean;
  emptyMessage: string;
  error?: string | null;
  onRetry?: () => void;
  sourceFallbackLabel?: string;
  sourceAriaLabel?: string;
  /** Shows the toolbar (search/filter/sort). Defaults to true. */
  showToolbar?: boolean;
  /** If set, enables per-table Saved Views (localStorage, scoped by key). */
  viewKey?: string;
};

type SavedView = {
  name: string;
  query: string;
  phases: string[];
  modes: string[];
  sort: SortKey;
};

function loadViews(key: string): SavedView[] {
  try {
    const raw = localStorage.getItem(`runviews:${key}`);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}
function saveViews(key: string, views: SavedView[]) {
  try {
    localStorage.setItem(`runviews:${key}`, JSON.stringify(views));
  } catch { /* */ }
}

export function AgentRunTable({
  runs,
  loading,
  emptyMessage,
  error = null,
  onRetry,
  sourceFallbackLabel = "Source",
  sourceAriaLabel = "Source link (opens in new tab)",
  showToolbar = true,
  viewKey,
}: AgentRunTableProps) {
  const [query, setQuery] = useState("");
  const [phaseFilters, setPhaseFilters] = useState<Set<string>>(new Set());
  const [modeFilters, setModeFilters] = useState<Set<string>>(new Set());
  const [sortKey, setSortKey] = useState<SortKey>("age");
  const [views, setViews] = useState<SavedView[]>(() =>
    viewKey ? loadViews(viewKey) : [],
  );
  const [activeView, setActiveView] = useState<string | null>(null);
  const [saveViewOpen, setSaveViewOpen] = useState(false);
  const [saveViewName, setSaveViewName] = useState("");
  const [showAll, setShowAll] = useState(false);
  const now = useNow();

  useEffect(() => {
    // Clear active view name when user mutates filters away from the saved set.
    if (!activeView) return;
    const v = views.find((x) => x.name === activeView);
    if (!v) return;
    const same =
      v.query === query &&
      v.sort === sortKey &&
      v.phases.length === phaseFilters.size &&
      v.phases.every((p) => phaseFilters.has(p)) &&
      v.modes.length === modeFilters.size &&
      v.modes.every((m) => modeFilters.has(m));
    if (!same) setActiveView(null);
  }, [query, phaseFilters, modeFilters, sortKey, activeView, views]);

  const phaseOptions = useMemo(
    () => Array.from(new Set(runs.map((r) => r.phase).filter(Boolean))).sort(),
    [runs],
  );
  const modeOptions = useMemo(
    () =>
      Array.from(new Set(runs.map(displayMode).filter(Boolean))).sort(),
    [runs],
  );

  const filtered = useMemo(() => {
    let out = filterByQuery(runs, query, (r) => [
      r.displayName,
      r.name,
      r.namespace,
      r.repoUrl,
      displayMode(r),
      r.trigger?.externalIdentifier,
    ]);
    if (phaseFilters.size)
      out = out.filter((r) => phaseFilters.has(r.phase));
    if (modeFilters.size)
      out = out.filter((r) => modeFilters.has(displayMode(r)));
    out = [...out].sort((a, b) => {
      if (sortKey === "name") return a.name.localeCompare(b.name);
      if (sortKey === "cost")
        return parseFloat(b.costUsd || "0") - parseFloat(a.costUsd || "0");
      return Number(b.createdAtUnix - a.createdAtUnix);
    });
    return out;
  }, [runs, query, phaseFilters, modeFilters, sortKey]);

  const togglePhase = (p: string) => {
    setPhaseFilters((prev) => {
      const n = new Set(prev);
      if (n.has(p)) n.delete(p);
      else n.add(p);
      return n;
    });
  };
  const toggleMode = (m: string) => {
    setModeFilters((prev) => {
      const n = new Set(prev);
      if (n.has(m)) n.delete(m);
      else n.add(m);
      return n;
    });
  };

  const activeFilterCount = phaseFilters.size + modeFilters.size;
  const hasQueryOrFilters = query.length > 0 || activeFilterCount > 0;
  const visible = showAll ? filtered : filtered.slice(0, 200);
  const capped = visible.length < filtered.length;

  const saveCurrentView = (name: string) => {
    if (!viewKey) return;
    const trimmed = name.trim();
    if (!trimmed) return;
    const view: SavedView = {
      name: trimmed,
      query,
      phases: Array.from(phaseFilters),
      modes: Array.from(modeFilters),
      sort: sortKey,
    };
    const next = [...views.filter((v) => v.name !== trimmed), view];
    setViews(next);
    saveViews(viewKey, next);
    setActiveView(trimmed);
    setSaveViewOpen(false);
    setSaveViewName("");
    toast.success(`Saved view "${trimmed}"`);
  };

  return (
    <div className="space-y-3">
      {showToolbar && (
        <div className="flex flex-wrap items-center gap-2">
          <ListSearchInput
            value={query}
            onChange={setQuery}
            placeholder="Search runs…"
            className="w-full sm:w-[240px]"
          />
          {phaseOptions.length > 0 && (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button variant="ghost" size="sm" className="h-[28px] gap-1 text-[12px] text-muted-foreground hover:text-foreground data-[popup-open]:text-foreground" />}
              >
                Status
                {phaseFilters.size > 0 && (
                  <Badge variant="secondary" className="ml-0.5 h-4 px-1 text-[10px]">
                    {phaseFilters.size}
                  </Badge>
                )}
                <ChevronDown className="size-3 opacity-60" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="min-w-[160px]">
                <DropdownMenuLabel className="text-[11px]">Status</DropdownMenuLabel>
                <DropdownMenuSeparator />
                {phaseOptions.map((p) => (
                  <DropdownMenuCheckboxItem
                    key={p}
                    checked={phaseFilters.has(p)}
                    onCheckedChange={() => togglePhase(p)}
                  >
                    {p}
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          {modeOptions.length > 0 && (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button variant="ghost" size="sm" className="h-[28px] gap-1 text-[12px] text-muted-foreground hover:text-foreground data-[popup-open]:text-foreground" />}
              >
                Mode
                {modeFilters.size > 0 && (
                  <Badge variant="secondary" className="ml-0.5 h-4 px-1 text-[10px]">
                    {modeFilters.size}
                  </Badge>
                )}
                <ChevronDown className="size-3 opacity-60" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="min-w-[160px]">
                <DropdownMenuLabel className="text-[11px]">Mode</DropdownMenuLabel>
                <DropdownMenuSeparator />
                {modeOptions.map((m) => (
                  <DropdownMenuCheckboxItem
                    key={m}
                    checked={modeFilters.has(m)}
                    onCheckedChange={() => toggleMode(m)}
                  >
                    {m}
                  </DropdownMenuCheckboxItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          <DropdownMenu>
            <DropdownMenuTrigger
              render={<Button variant="ghost" size="sm" className="h-[28px] gap-1 text-[12px] text-muted-foreground hover:text-foreground data-[popup-open]:text-foreground" />}
            >
              Sort: {sortKey === "age" ? "Newest" : sortKey === "cost" ? "Cost" : "Name"}
              <ChevronDown className="size-3 opacity-60" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              <DropdownMenuCheckboxItem
                checked={sortKey === "age"}
                onCheckedChange={() => setSortKey("age")}
              >
                Newest first
              </DropdownMenuCheckboxItem>
              <DropdownMenuCheckboxItem
                checked={sortKey === "cost"}
                onCheckedChange={() => setSortKey("cost")}
              >
                Highest cost
              </DropdownMenuCheckboxItem>
              <DropdownMenuCheckboxItem
                checked={sortKey === "name"}
                onCheckedChange={() => setSortKey("name")}
              >
                Name (A–Z)
              </DropdownMenuCheckboxItem>
            </DropdownMenuContent>
          </DropdownMenu>
          {viewKey && (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<Button variant="ghost" size="sm" className="h-[28px] gap-1 text-[12px] text-muted-foreground hover:text-foreground data-[popup-open]:text-foreground" />}
              >
                <Bookmark className="size-3 mr-0.5" />
                {activeView ?? "Views"}
                <ChevronDown className="size-3 opacity-60" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="min-w-[200px]">
                <DropdownMenuLabel className="text-[11px]">Saved views</DropdownMenuLabel>
                <DropdownMenuSeparator />
                {views.length === 0 && (
                  <div className="px-2 py-1.5 text-[11px] text-muted-foreground">No saved views</div>
                )}
                {views.map((v) => (
                  <div
                    key={v.name}
                    className="flex items-center gap-1 pr-1"
                  >
                    <DropdownMenuItem
                      className="flex-1"
                      onClick={() => {
                        setQuery(v.query);
                        setPhaseFilters(new Set(v.phases));
                        setModeFilters(new Set(v.modes));
                        setSortKey(v.sort);
                        setActiveView(v.name);
                      }}
                    >
                      <span className="flex-1 truncate">{v.name}</span>
                      {activeView === v.name && <Check className="size-3 opacity-70" />}
                    </DropdownMenuItem>
                    <button
                      type="button"
                      aria-label={`Delete view ${v.name}`}
                      className="rounded-sm p-1 text-muted-foreground opacity-60 hover:bg-accent hover:opacity-100"
                      onClick={(e) => {
                        e.stopPropagation();
                        e.preventDefault();
                        const next = views.filter((x) => x.name !== v.name);
                        setViews(next);
                        saveViews(viewKey, next);
                        if (activeView === v.name) setActiveView(null);
                        toast.success(`Deleted view "${v.name}"`);
                      }}
                    >
                      <X className="size-3" />
                    </button>
                  </div>
                ))}
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  disabled={!hasQueryOrFilters && sortKey === "age"}
                  onClick={() => {
                    setSaveViewName(activeView ?? "");
                    setSaveViewOpen(true);
                  }}
                >
                  Save current as view…
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          {hasQueryOrFilters && (
            <Button
              variant="ghost"
              size="sm"
              className="h-[28px] text-[11.5px] text-muted-foreground"
              onClick={() => {
                setQuery("");
                setPhaseFilters(new Set());
                setModeFilters(new Set());
              }}
            >
              Clear
            </Button>
          )}
          <div className="ml-auto text-[11.5px] text-muted-foreground tabular-nums" aria-live="polite" aria-atomic="true">
            {filtered.length}
            {hasQueryOrFilters && runs.length !== filtered.length
              ? ` of ${runs.length}`
              : ""}{" "}
            run{filtered.length === 1 ? "" : "s"}
          </div>
        </div>
      )}

      <Dialog open={saveViewOpen} onOpenChange={setSaveViewOpen}>
        <DialogContent className="sm:max-w-xs">
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              saveCurrentView(saveViewName);
            }}
          >
            <DialogHeader>
              <DialogTitle>Name this view</DialogTitle>
            </DialogHeader>
            <Input
              autoFocus
              value={saveViewName}
              onChange={(e) => setSaveViewName(e.currentTarget.value)}
              placeholder="View name"
            />
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setSaveViewOpen(false)}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={!saveViewName.trim()}>
                Save
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      <ListState
        loading={loading}
        error={error}
        onRetry={onRetry}
        empty={!filtered.length}
        skeleton={<TableRowSkeleton rows={5} />}
        emptyIcon={<Inbox className="size-6" />}
        emptyTitle={query ? `No matches for "${query}"` : hasQueryOrFilters ? "No matches" : "No runs yet"}
        emptyDescription={
          hasQueryOrFilters
            ? "Clear the search or remove filters to see all runs."
            : emptyMessage
        }
      >
        <Table>
          <TableCaption className="sr-only">Agent runs</TableCaption>
          <TableHeader>
            <TableRow>
              <TableHead className="w-[22px]" aria-label="Live" />
              <TableHead>Name</TableHead>
              <TableHead>Status</TableHead>
              <TableHead className="hidden lg:table-cell">Mode</TableHead>
              <TableHead className="hidden lg:table-cell">Step</TableHead>
              <TableHead className="hidden md:table-cell">Repo</TableHead>
              <TableHead className="hidden text-right md:table-cell">Cost</TableHead>
              <TableHead className="hidden md:table-cell">Source</TableHead>
              <TableHead className="text-right">Age</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {visible.map((run) => {
              const live = isRunComputing(run);
              const prLoopBadge = formatPRLoopBadge(run);
              const modeLabel = displayMode(run);
              return (
                <TableRow key={`${run.namespace}/${run.name}`}>
                  <TableCell className="w-[22px] p-0 pl-2">
                    {live ? (
                      <span
                        aria-label="Live run"
                        title="Live"
                        className={cn(
                          "inline-block size-[7px] rounded-full bg-[oklch(0.74_0.13_160)]",
                          "motion-safe:animate-[pulse_1.6s_ease-in-out_infinite]",
                        )}
                      />
                    ) : (
                      <span className="inline-block size-[7px] rounded-full bg-transparent" />
                    )}
                  </TableCell>
                  <TableCell>
                    <span className="inline-flex flex-wrap items-center gap-1.5">
                      <Link
                        to={`/runs/${run.namespace}/${run.name}`}
                        className="font-medium text-primary hover:underline"
                        title={run.displayName ? run.name : undefined}
                      >
                        {run.displayName || run.name}
                      </Link>
                      {run.standingRunRole && (
                        <Badge
                          variant="secondary"
                          className="h-4 px-1.5 text-[10px] capitalize"
                          title="Standing supervisor run — stays attached to its source instead of finishing"
                        >
                          {run.standingRunRole}
                        </Badge>
                      )}
                    </span>
                  </TableCell>
                  <TableCell>
                    <div className="flex flex-wrap items-center gap-1.5">
                      <StatusBadge phase={run.phase} run={run} />
                      {prLoopBadge && (
                        <span
                          className={cn(
                            "inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium capitalize",
                            toneSoft[prLoopTone(run.prLoop?.state ?? "")],
                          )}
                        >
                          {prLoopBadge}
                        </span>
                      )}
                    </div>
                  </TableCell>
                  <TableCell className="hidden lg:table-cell">
                    {modeLabel ? (
                      <span className="inline-flex flex-wrap items-center gap-1.5 text-[12px] text-muted-foreground">
                        <span>{modeLabel}</span>
                        {run.modeCategory && (
                          <span className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] capitalize text-muted-foreground">
                            {run.modeCategory}
                          </span>
                        )}
                      </span>
                    ) : (
                      <span className="text-muted-foreground/50">—</span>
                    )}
                  </TableCell>
                  <TableCell className="hidden text-muted-foreground lg:table-cell">
                    {run.currentStep || "-"}
                  </TableCell>
                  <TableCell
                    className="hidden max-w-[200px] truncate text-sm text-muted-foreground md:table-cell"
                    title={run.repoUrl || undefined}
                  >
                    {run.repoUrl ? formatRepoShort(run.repoUrl) : "—"}
                  </TableCell>
                  <TableCell className="hidden text-right font-mono md:table-cell">
                    {run.costUsd ? `$${run.costUsd}` : "-"}
                  </TableCell>
                  <TableCell className="hidden md:table-cell">
                    {run.trigger?.externalUrl ? (
                      <a
                        href={run.trigger.externalUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="text-sm text-primary hover:underline"
                        aria-label={sourceAriaLabel}
                      >
                        {run.trigger.externalIdentifier || sourceFallbackLabel}
                      </a>
                    ) : (
                      "-"
                    )}
                  </TableCell>
                  <TableCell className="text-right text-muted-foreground">
                    {formatAge(run.createdAtUnix, now)}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
        {capped && (
          <button
            type="button"
            onClick={() => setShowAll(true)}
            className="w-full border-t border-border/40 py-2 text-[12px] text-muted-foreground hover:bg-muted/40"
          >
            Show all {filtered.length} runs
          </button>
        )}
      </ListState>
      {!loading && !filtered.length && hasQueryOrFilters && (
        <div className="flex items-center gap-2 px-1 text-[11.5px] text-muted-foreground">
          <Activity className="size-3" />
          {runs.length} total run{runs.length === 1 ? "" : "s"} available — try
          clearing filters.
        </div>
      )}
    </div>
  );
}
