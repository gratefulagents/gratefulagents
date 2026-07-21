/* eslint-disable react-hooks/set-state-in-effect */
import * as React from "react";
import { Link } from "react-router-dom";
import {
  Activity,
  AlertTriangle,
  Bookmark,
  Check,
  ChevronDown,
  ChevronRight,
  Clock3,
  ExternalLink,
  GitBranch,
  GitPullRequest,
  Inbox,
  Layers3,
  MoreHorizontal,
  Network,
  Play,
  RotateCcw,
  Share2,
  Square,
  Trophy,
  X,
} from "lucide-react";

import { ShareDialog } from "@/components/ShareDialog";
import { StatusBadge } from "@/components/StatusBadge";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { ListSearchInput, filterByQuery } from "@/components/ui/list-search";
import { ListState, TableRowSkeleton } from "@/components/ui/list-state";
import {
  Table,
  TableBody,
  TableCaption,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { toast } from "@/components/ui/toaster";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { useNow } from "@/hooks/useNow";
import {
  canRunAction,
  getRunAttention,
  getRunBucket,
  latestRunActivity,
  runComparisonKey,
  runDurationSeconds,
  runModeLabel,
  runRepoLabel,
  runSourceLabel,
  runSourcePath,
  type OpsAction,
  type OpsBucket,
} from "@/lib/agentOps";
import { client } from "@/lib/client";
import { isRunComputing, runActivitySummary } from "@/lib/runStatus";
import { formatAge } from "@/lib/format";
import { runPullRequestUrls, pullRequestLabel } from "@/lib/pullRequests";
import { cn } from "@/lib/utils";
import { isDonePhase, toneSoft, toneText } from "@/lib/status";
import type { AgentRun } from "@/rpc/platform/service_pb";

type SortKey = "attention" | "newest" | "oldest-active" | "cost" | "name" | "status" | "mode" | "source" | "repo";
type GroupKey = "none" | "attention" | "status" | "source" | "repo" | "mode";
type AgeKey = "24h" | "7d" | "30d" | "all";
type CostKey = "all" | "under-1" | "1-5" | "over-5";
type BucketFilter = "all" | OpsBucket;

type OpsView = {
  name: string;
  query: string;
  bucket: BucketFilter;
  phases: string[];
  modes: string[];
  sources: string[];
  repos: string[];
  age: AgeKey;
  cost: CostKey;
  group: GroupKey;
  sort: SortKey;
};

type ConfirmState = { action: Exclude<OpsAction, "extend">; runs: AgentRun[] };
type ExtendState = { runs: AgentRun[]; duration: string };

const VIEW_STORAGE_KEY = "agent-ops:views:v1";
const TERMINAL_RECENCY_SECONDS: Record<AgeKey, number> = {
  "24h": 86_400,
  "7d": 7 * 86_400,
  "30d": 30 * 86_400,
  all: Number.POSITIVE_INFINITY,
};

const BUCKET_LABELS: Record<OpsBucket, string> = {
  attention: "Needs attention",
  active: "Active",
  queued: "Queued",
  completed: "Completed",
};

const GROUP_LABELS: Record<GroupKey, string> = {
  none: "No grouping",
  attention: "Operational state",
  status: "Status",
  source: "Project / source",
  repo: "Repository",
  mode: "Mode",
};

const BUCKET_DESCRIPTIONS: Record<OpsBucket, string> = {
  attention: "Action required",
  active: "Working now",
  queued: "Waiting to start",
  completed: "Finished recently",
};

function runKey(run: AgentRun): string {
  return `${run.namespace}/${run.name}`;
}

function loadViews(): OpsView[] {
  try {
    const parsed = JSON.parse(localStorage.getItem(VIEW_STORAGE_KEY) || "[]") as unknown;
    return Array.isArray(parsed) ? (parsed as OpsView[]) : [];
  } catch {
    return [];
  }
}

function persistViews(views: OpsView[]) {
  try {
    localStorage.setItem(VIEW_STORAGE_KEY, JSON.stringify(views));
  } catch {
    // Saved views are a convenience; storage can be unavailable in hardened webviews.
  }
}

function shortDuration(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86_400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86_400)}d`;
}

function costValue(run: AgentRun): number {
  const value = Number.parseFloat(run.costUsd || "0");
  return Number.isFinite(value) ? value : 0;
}

function groupValue(run: AgentRun, group: GroupKey): string {
  switch (group) {
    case "attention": {
      const attention = getRunAttention(run);
      return attention.kind === "none" ? BUCKET_LABELS[getRunBucket(run)] : attention.label;
    }
    case "status":
      return run.phase || "Unknown";
    case "source":
      return runSourceLabel(run);
    case "repo":
      return runRepoLabel(run);
    case "mode":
      return runModeLabel(run);
    default:
      return "All runs";
  }
}

function actionLabel(action: Exclude<OpsAction, "extend">, count: number): string {
  const noun = count === 1 ? "run" : "runs";
  if (action === "stop") return `Stop ${count} ${noun}`;
  if (action === "retry") return `Retry ${count} ${noun}`;
  return `Mark ${count} ${noun} succeeded`;
}

function matchesCost(run: AgentRun, cost: CostKey): boolean {
  const value = costValue(run);
  if (cost === "under-1") return value < 1;
  if (cost === "1-5") return value >= 1 && value <= 5;
  if (cost === "over-5") return value > 5;
  return true;
}

function latestTimestamp(run: AgentRun): bigint {
  return run.completedAtUnix || run.startedAtUnix || run.createdAtUnix;
}

export function AgentOpsConsole() {
  const { runs, loading, error, refetch } = useAgentRuns();
  const now = useNow();
  const [query, setQuery] = React.useState("");
  const [bucket, setBucket] = React.useState<BucketFilter>("active");
  const [phases, setPhases] = React.useState<Set<string>>(new Set());
  const [modes, setModes] = React.useState<Set<string>>(new Set());
  const [sources, setSources] = React.useState<Set<string>>(new Set());
  const [repos, setRepos] = React.useState<Set<string>>(new Set());
  const [age, setAge] = React.useState<AgeKey>("7d");
  const [cost, setCost] = React.useState<CostKey>("all");
  const [sort, setSort] = React.useState<SortKey>("attention");
  const [group, setGroup] = React.useState<GroupKey>("attention");
  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [expanded, setExpanded] = React.useState<Set<string>>(new Set());
  const [collapsed, setCollapsed] = React.useState<Set<string>>(new Set());
  const [confirm, setConfirm] = React.useState<ConfirmState | null>(null);
  const [extend, setExtend] = React.useState<ExtendState | null>(null);
  const [sharing, setSharing] = React.useState<AgentRun | null>(null);
  const [compareRuns, setCompareRuns] = React.useState<AgentRun[]>([]);
  const [busyRuns, setBusyRuns] = React.useState<Set<string>>(new Set());
  const [extendingRuntime, setExtendingRuntime] = React.useState(false);
  const extendingRuntimeRef = React.useRef(false);
  const [views, setViews] = React.useState<OpsView[]>(loadViews);
  const [activeView, setActiveView] = React.useState<string | null>(null);
  const [saveViewOpen, setSaveViewOpen] = React.useState(false);
  const [viewName, setViewName] = React.useState("");

  const options = React.useMemo(
    () => ({
      phases: [...new Set(runs.map((run) => run.phase).filter(Boolean))].sort(),
      modes: [...new Set(runs.map(runModeLabel))].sort(),
      sources: [...new Set(runs.map(runSourceLabel))].sort(),
      repos: [...new Set(runs.map(runRepoLabel))].sort(),
    }),
    [runs],
  );

  const counts = React.useMemo(() => {
    const result: Record<OpsBucket, number> = { attention: 0, active: 0, queued: 0, completed: 0 };
    for (const run of runs) result[getRunBucket(run)] += 1;
    return result;
  }, [runs]);
  const liveCount = React.useMemo(
    () => runs.filter(isRunComputing).length,
    [runs],
  );

  const filtered = React.useMemo(() => {
    const nowSeconds = Math.floor(now / 1000);
    let result = filterByQuery(runs, query, (run) => [
      run.displayName,
      run.intentTitle,
      run.name,
      run.namespace,
      run.repoUrl,
      runModeLabel(run),
      runSourceLabel(run),
      run.trigger?.externalIdentifier,
      run.owner?.name,
      run.owner?.email,
      run.currentStep,
      run.blockedReason,
      run.lastError,
      latestRunActivity(run)?.summary,
    ]);
    result = result.filter((run) => {
      if (bucket !== "all" && getRunBucket(run) !== bucket) return false;
      if (phases.size && !phases.has(run.phase)) return false;
      if (modes.size && !modes.has(runModeLabel(run))) return false;
      if (sources.size && !sources.has(runSourceLabel(run))) return false;
      if (repos.size && !repos.has(runRepoLabel(run))) return false;
      if (!matchesCost(run, cost)) return false;
      if (isDonePhase(run.phase) && age !== "all") {
        const timestamp = Number(latestTimestamp(run));
        if (!timestamp || nowSeconds - timestamp > TERMINAL_RECENCY_SECONDS[age]) return false;
      }
      return true;
    });
    return [...result].sort((a, b) => {
      if (sort === "name") return (a.displayName || a.name).localeCompare(b.displayName || b.name);
      if (sort === "status") return a.phase.localeCompare(b.phase) || Number(b.createdAtUnix - a.createdAtUnix);
      if (sort === "mode") return runModeLabel(a).localeCompare(runModeLabel(b)) || Number(b.createdAtUnix - a.createdAtUnix);
      if (sort === "source") return runSourceLabel(a).localeCompare(runSourceLabel(b)) || Number(b.createdAtUnix - a.createdAtUnix);
      if (sort === "repo") return runRepoLabel(a).localeCompare(runRepoLabel(b)) || Number(b.createdAtUnix - a.createdAtUnix);
      if (sort === "cost") return costValue(b) - costValue(a);
      if (sort === "oldest-active") return Number(a.createdAtUnix - b.createdAtUnix);
      if (sort === "newest") return Number(b.createdAtUnix - a.createdAtUnix);
      const rank = getRunAttention(a).rank - getRunAttention(b).rank;
      return rank || Number(b.createdAtUnix - a.createdAtUnix);
    });
  }, [age, bucket, cost, modes, now, phases, query, repos, runs, sort, sources]);

  const grouped = React.useMemo(() => {
    const result = new Map<string, AgentRun[]>();
    for (const run of filtered) {
      const key = groupValue(run, group);
      const current = result.get(key) || [];
      current.push(run);
      result.set(key, current);
    }
    return [...result.entries()];
  }, [filtered, group]);

  React.useEffect(() => {
    const available = new Set(runs.map(runKey));
    setSelected((current) => new Set([...current].filter((key) => available.has(key))));
  }, [runs]);

  const selectedRuns = React.useMemo(
    () => runs.filter((run) => selected.has(runKey(run))),
    [runs, selected],
  );
  const visibleSelectedCount = React.useMemo(
    () => filtered.filter((run) => selected.has(runKey(run))).length,
    [filtered, selected],
  );
  const selectAllRef = React.useRef<HTMLInputElement>(null);

  React.useEffect(() => {
    if (selectAllRef.current) {
      selectAllRef.current.indeterminate = visibleSelectedCount > 0 && visibleSelectedCount < filtered.length;
    }
  }, [filtered.length, visibleSelectedCount]);

  function toggleSet(value: string, setter: React.Dispatch<React.SetStateAction<Set<string>>>) {
    setter((current) => {
      const next = new Set(current);
      if (next.has(value)) next.delete(value);
      else next.add(value);
      return next;
    });
    setActiveView(null);
  }

  function applyView(view: OpsView) {
    setQuery(view.query || "");
    setBucket(view.bucket || "active");
    setPhases(new Set(view.phases || []));
    setModes(new Set(view.modes || []));
    setSources(new Set(view.sources || []));
    setRepos(new Set(view.repos || []));
    setAge(view.age || "7d");
    setCost(view.cost || "all");
    setGroup(view.group || "attention");
    setSort(view.sort || "attention");
    setActiveView(view.name);
  }

  function saveView() {
    const name = viewName.trim();
    if (!name) return;
    const view: OpsView = {
      name,
      query,
      bucket,
      phases: [...phases],
      modes: [...modes],
      sources: [...sources],
      repos: [...repos],
      age,
      cost,
      group,
      sort,
    };
    const next = [...views.filter((candidate) => candidate.name !== name), view];
    setViews(next);
    persistViews(next);
    setActiveView(name);
    setViewName("");
    setSaveViewOpen(false);
    toast.success(`Saved view “${name}”`);
  }

  function clearFilters() {
    setQuery("");
    setBucket("active");
    setPhases(new Set());
    setModes(new Set());
    setSources(new Set());
    setRepos(new Set());
    setAge("7d");
    setCost("all");
    setActiveView(null);
  }

  function eligible(action: OpsAction, candidates = selectedRuns): AgentRun[] {
    return candidates.filter((run) => canRunAction(run, action));
  }

  function requestAction(action: Exclude<OpsAction, "extend">, candidates: AgentRun[]) {
    const allowed = eligible(action, candidates);
    if (!allowed.length) {
      toast.info(`No selected runs can ${action}.`);
      return;
    }
    setConfirm({ action, runs: allowed });
  }

  async function executeAction(action: Exclude<OpsAction, "extend">, targets: AgentRun[]) {
    const keys = targets.map(runKey);
    setBusyRuns((current) => new Set([...current, ...keys]));
    const results = await Promise.allSettled(
      targets.map((run) => {
        const request = { namespace: run.namespace, name: run.name };
        if (action === "stop") return client.cancelAgentRun(request);
        if (action === "retry") return client.retryAgentRun({ ...request, idempotencyKey: crypto.randomUUID() });
        return client.promoteAgentRun(request);
      }),
    );
    setBusyRuns((current) => {
      const next = new Set(current);
      keys.forEach((key) => next.delete(key));
      return next;
    });
    const succeeded = results.filter((result) => result.status === "fulfilled").length;
    const failed = results.length - succeeded;
    const verb = action === "promote" ? "marked succeeded" : action === "retry" ? "retried" : "stopped";
    if (failed) {
      toast.warning(`${succeeded} ${verb} · ${failed} failed`, {
        description: "Open the affected runs for details.",
      });
    } else {
      toast.success(`${succeeded} ${succeeded === 1 ? "run" : "runs"} ${verb}`);
    }
    setSelected((current) => {
      const next = new Set(current);
      keys.forEach((key) => next.delete(key));
      return next;
    });
    void refetch();
  }

  async function executeExtend(targets: AgentRun[], duration: string) {
    if (extendingRuntimeRef.current) return;
    const allowed = eligible("extend", targets);
    if (!allowed.length || !duration.trim()) return;
    extendingRuntimeRef.current = true;
    setExtendingRuntime(true);
    const keys = allowed.map(runKey);
    setBusyRuns((current) => new Set([...current, ...keys]));
    const results = await Promise.allSettled(
      allowed.map((run) =>
        client.extendAgentRunRuntime({
          namespace: run.namespace,
          name: run.name,
          additionalRuntime: duration.trim(),
        }),
      ),
    );
    setBusyRuns((current) => {
      const next = new Set(current);
      keys.forEach((key) => next.delete(key));
      return next;
    });
    const succeeded = results.filter((result) => result.status === "fulfilled").length;
    const failed = results.length - succeeded;
    extendingRuntimeRef.current = false;
    setExtendingRuntime(false);
    if (failed) {
      toast.warning(`${succeeded} extended · ${failed} failed`, {
        description: "The dialog remains open so failed runs can be retried intentionally.",
      });
    } else {
      toast.success(`Extended ${succeeded} ${succeeded === 1 ? "run" : "runs"} by ${duration.trim()}`);
      setExtend(null);
    }
    void refetch();
  }

  const filterCount =
    phases.size + modes.size + sources.size + repos.size +
    (bucket === "active" ? 0 : 1) + (age === "7d" ? 0 : 1) + (cost === "all" ? 0 : 1);

  return (
    <div className="space-y-5 pb-8">
      <header className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h1 className="text-[24px] font-semibold leading-none tracking-[-0.025em]">Agent Ops</h1>
          <p className="mt-1 text-[12.5px] text-muted-foreground">Monitor live work, resolve blockers, and review recent outcomes.</p>
        </div>
        <div className="flex items-center gap-2">
          <span className="inline-flex h-7 items-center gap-1.5 rounded-full bg-[color-mix(in_oklch,var(--tone-running)_10%,transparent)] px-2.5 text-[11.5px] text-[color:var(--tone-running-fg)] ring-1 ring-inset ring-[color-mix(in_oklch,var(--tone-running)_25%,transparent)]">
            <span className="size-1.5 rounded-full bg-[color:var(--tone-running)] motion-safe:animate-pulse" />
            {liveCount} live
            <span className="text-muted-foreground">· auto-updating</span>
          </span>
          <Button size="sm" nativeButton={false} render={<Link to="/" />}>
            <Play className="size-3.5" /> New run
          </Button>
        </div>
      </header>

      <section aria-label="Run overview" className="flex items-stretch gap-1 overflow-x-auto border-b border-border/60 pb-px">
        {(["all", "attention", "active", "queued", "completed"] as BucketFilter[]).map((key) => {
          const active = bucket === key;
          const count = key === "all" ? runs.length : counts[key];
          const tone = key === "attention" ? "warning" : key === "active" ? "running" : key === "queued" ? "info" : key === "completed" ? "success" : null;
          const emphasize = key === "attention" && count > 0;
          return (
            <button
              key={key}
              type="button"
              aria-pressed={active}
              title={key === "all" ? "Every run loaded in this workspace" : BUCKET_DESCRIPTIONS[key as OpsBucket]}
              onClick={() => { setBucket(key); setActiveView(null); }}
              className={cn(
                "relative -mb-px flex shrink-0 items-baseline gap-2 rounded-t-md border-b-2 border-transparent px-3 py-2 text-left text-[12.5px] transition-colors hover:bg-muted/40",
                active
                  ? cn(
                      "font-medium text-foreground",
                      tone ? "border-b-[color:var(--tone-accent)]" : "border-b-primary",
                    )
                  : "text-muted-foreground",
                emphasize && !active && toneText.warning,
              )}
              style={tone ? ({ "--tone-accent": `var(--tone-${tone})` } as React.CSSProperties) : undefined}
            >
              <span className="whitespace-nowrap">{key === "all" ? "All runs" : BUCKET_LABELS[key as OpsBucket]}</span>
              <span
                className={cn(
                  "rounded-full px-1.5 py-px font-mono text-[11px] font-semibold tabular-nums",
                  emphasize
                    ? "bg-[color-mix(in_oklch,var(--tone-warning)_15%,transparent)] text-[color:var(--tone-warning-fg)]"
                    : "bg-muted/60 text-muted-foreground",
                )}
              >
                {count}
              </span>
            </button>
          );
        })}
        <span className="ml-auto hidden shrink-0 items-center self-center rounded-md bg-muted/40 px-2 py-1 text-[11px] text-muted-foreground tabular-nums sm:inline-flex">
          {filtered.length}{filtered.length !== runs.length ? ` of ${runs.length}` : ""} runs shown
        </span>
      </section>

      <div className="flex flex-wrap items-center gap-1.5">
        <ListSearchInput
          value={query}
          onChange={(value) => { setQuery(value); setActiveView(null); }}
          placeholder="Search runs…"
          className="w-full sm:w-[240px]"
        />
        <span className="mx-0.5 hidden h-5 w-px bg-border/70 sm:block" aria-hidden />
        <FilterMenu label="Status" values={options.phases} selected={phases} onToggle={(value) => toggleSet(value, setPhases)} />
        <FilterMenu label="Mode" values={options.modes} selected={modes} onToggle={(value) => toggleSet(value, setModes)} />
        <FilterMenu label="Source" values={options.sources} selected={sources} onToggle={(value) => toggleSet(value, setSources)} />
        <FilterMenu label="Repo" values={options.repos} selected={repos} onToggle={(value) => toggleSet(value, setRepos)} />
        <ChoiceMenu
          label={`Age: ${age === "all" ? "All" : age}`}
          choices={[
            ["24h", "Last 24 hours"], ["7d", "Last 7 days"], ["30d", "Last 30 days"], ["all", "Any age"],
          ]}
          value={age}
          onChange={(value) => { setAge(value as AgeKey); setActiveView(null); }}
        />
        <ChoiceMenu
          label={cost === "all" ? "Cost" : `Cost: ${cost === "under-1" ? "<$1" : cost === "1-5" ? "$1 to $5" : ">$5"}`}
          choices={[["all", "Any cost"], ["under-1", "Under $1"], ["1-5", "$1 to $5"], ["over-5", "Over $5"]]}
          value={cost}
          onChange={(value) => { setCost(value as CostKey); setActiveView(null); }}
        />
        <span className="mx-0.5 hidden h-5 w-px bg-border/70 sm:block" aria-hidden />
        <ChoiceMenu
          label={`Group: ${group === "attention" ? "State" : group === "none" ? "None" : group === "source" ? "Source" : GROUP_LABELS[group]}`}
          choices={(Object.entries(GROUP_LABELS) as [GroupKey, string][])}
          value={group}
          onChange={(value) => { setGroup(value as GroupKey); setCollapsed(new Set()); setActiveView(null); }}
        />
        <ChoiceMenu
          label={`Sort: ${sort === "attention" ? "Attention" : sort === "oldest-active" ? "Oldest" : sort === "newest" ? "Newest" : sort === "cost" ? "Cost" : sort === "status" ? "Status" : sort === "mode" ? "Mode" : sort === "source" ? "Source" : sort === "repo" ? "Repository" : "Name"}`}
          choices={[["attention", "Attention first"], ["newest", "Newest"], ["oldest-active", "Oldest first"], ["cost", "Highest cost"], ["status", "Status"], ["mode", "Mode"], ["source", "Project / source"], ["repo", "Repository"], ["name", "Name"]]}
          value={sort}
          onChange={(value) => { setSort(value as SortKey); setActiveView(null); }}
        />
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="ghost" size="sm" className="gap-1 text-muted-foreground" />}>
              <Bookmark className="size-3.5" /> {activeView || "Views"} <ChevronDown className="size-3" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start" className="min-w-[210px]">
              <DropdownMenuLabel>Saved operations views</DropdownMenuLabel>
              <DropdownMenuSeparator />
              {views.length === 0 && <p className="px-2 py-1.5 text-xs text-muted-foreground">No saved views</p>}
              {views.map((view) => (
                <div key={view.name} className="flex items-center gap-1 pr-1">
                  <DropdownMenuItem className="flex-1" onClick={() => applyView(view)}>
                    <span className="flex-1 truncate">{view.name}</span>
                    {activeView === view.name && <Check className="size-3" />}
                  </DropdownMenuItem>
                  <button
                    type="button"
                    className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground"
                    aria-label={`Delete view ${view.name}`}
                    onClick={(event) => {
                      event.preventDefault();
                      event.stopPropagation();
                      const next = views.filter((candidate) => candidate.name !== view.name);
                      setViews(next);
                      persistViews(next);
                      if (activeView === view.name) setActiveView(null);
                    }}
                  >
                    <X className="size-3" />
                  </button>
                </div>
              ))}
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => { setViewName(activeView || ""); setSaveViewOpen(true); }}>
                Save current view…
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          {(filterCount > 0 || query) && (
            <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={clearFilters}>Clear filters</Button>
          )}
      </div>

      {selectedRuns.length > 0 && (
        <div className="sticky top-0 z-20 flex flex-wrap items-center gap-2 rounded-xl border border-primary/25 bg-popover/95 px-3 py-2 shadow-md backdrop-blur">
          <span className="mr-1 text-[12.5px] font-medium">{selectedRuns.length} selected</span>
          <Button size="sm" variant="outline" disabled={!eligible("stop").length} onClick={() => requestAction("stop", selectedRuns)}>
            <Square className="size-3" /> Stop ({eligible("stop").length})
          </Button>
          <Button size="sm" variant="outline" disabled={!eligible("extend").length} onClick={() => setExtend({ runs: eligible("extend"), duration: "1h" })}>
            <Clock3 className="size-3" /> Extend ({eligible("extend").length})
          </Button>
          <Button size="sm" variant="outline" disabled={!eligible("promote").length} onClick={() => requestAction("promote", selectedRuns)}>
            <Trophy className="size-3" /> Succeed ({eligible("promote").length})
          </Button>
          <Button size="sm" variant="outline" disabled={!eligible("retry").length} onClick={() => requestAction("retry", selectedRuns)}>
            <RotateCcw className="size-3" /> Retry ({eligible("retry").length})
          </Button>
          <Button size="sm" variant="outline" disabled={selectedRuns.length < 2} onClick={() => setCompareRuns(selectedRuns)}>
            <Layers3 className="size-3" /> Compare
          </Button>
          <span className="text-[11px] text-muted-foreground">
            Mixed actions apply only to eligible runs.
          </span>
          <Button variant="ghost" size="icon-sm" className="ml-auto" aria-label="Clear selection" onClick={() => setSelected(new Set())}>
            <X className="size-3.5" />
          </Button>
        </div>
      )}

      <ListState
        loading={loading}
        error={error}
        onRetry={refetch}
        empty={!filtered.length}
        skeleton={<TableRowSkeleton rows={7} />}
        emptyIcon={<Inbox className="size-6" />}
        emptyTitle={runs.length ? "No runs match this view" : "No agent runs yet"}
        emptyDescription={runs.length ? "Clear filters or widen the completion age window." : "Start a run to see live fleet status here."}
      >
        <Table>
          <TableCaption className="sr-only">Live and recent agent runs</TableCaption>
          <TableHeader>
            <TableRow>
              <TableHead className="w-9">
                <input
                  ref={selectAllRef}
                  type="checkbox"
                  aria-label="Select visible runs"
                  aria-checked={visibleSelectedCount > 0 && visibleSelectedCount < filtered.length ? "mixed" : visibleSelectedCount === filtered.length && filtered.length > 0}
                  checked={filtered.length > 0 && visibleSelectedCount === filtered.length}
                  onChange={(event) => {
                    setSelected((current) => {
                      const next = new Set(current);
                      filtered.forEach((run) => event.currentTarget.checked ? next.add(runKey(run)) : next.delete(runKey(run)));
                      return next;
                    });
                  }}
                  className="size-3.5 accent-primary"
                />
              </TableHead>
              <TableHead>Run</TableHead>
              <TableHead>State</TableHead>
              <TableHead className="hidden xl:table-cell">Mode</TableHead>
              <TableHead className="min-w-[210px]">Latest activity</TableHead>
              <TableHead className="hidden lg:table-cell">Origin</TableHead>
              <TableHead className="hidden lg:table-cell">PR</TableHead>
              <TableHead className="text-right">Cost</TableHead>
              <TableHead className="text-right">Age</TableHead>
              <TableHead className="w-8"><span className="sr-only">Actions</span></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {grouped.map(([groupName, groupRuns]) => (
              <React.Fragment key={groupName}>
                {group !== "none" && (
                  <TableRow className="bg-muted/25 hover:bg-muted/35">
                    <TableCell colSpan={10} className="py-1.5">
                      <button
                        type="button"
                        className="flex w-full items-center gap-2 text-left"
                        onClick={() => setCollapsed((current) => {
                          const next = new Set(current);
                          if (next.has(groupName)) next.delete(groupName); else next.add(groupName);
                          return next;
                        })}
                      >
                        <ChevronRight className={cn("size-3.5 text-muted-foreground transition-transform", !collapsed.has(groupName) && "rotate-90")} />
                        <span className="text-[11.5px] font-medium">{groupName}</span>
                        <Badge variant="secondary" className="h-4 px-1.5 text-[10px]">{groupRuns.length}</Badge>
                        <span className="ml-auto font-mono text-[10.5px] text-muted-foreground">
                          ${groupRuns.reduce((sum, run) => sum + costValue(run), 0).toFixed(2)}
                        </span>
                      </button>
                    </TableCell>
                  </TableRow>
                )}
                {!collapsed.has(groupName) && groupRuns.map((run) => (
                  <RunRows
                    key={runKey(run)}
                    run={run}
                    now={now}
                    selected={selected.has(runKey(run))}
                    expanded={expanded.has(runKey(run))}
                    busy={busyRuns.has(runKey(run))}
                    allRuns={runs}
                    onSelect={(checked) => setSelected((current) => {
                      const next = new Set(current);
                      if (checked) next.add(runKey(run)); else next.delete(runKey(run));
                      return next;
                    })}
                    onExpand={() => setExpanded((current) => {
                      const next = new Set(current);
                      if (next.has(runKey(run))) next.delete(runKey(run)); else next.add(runKey(run));
                      return next;
                    })}
                    onAction={(action) => {
                      if (action === "extend") setExtend({ runs: [run], duration: "1h" });
                      else requestAction(action, [run]);
                    }}
                    onShare={() => setSharing(run)}
                    onCompare={(comparison) => setCompareRuns(comparison)}
                  />
                ))}
              </React.Fragment>
            ))}
          </TableBody>
        </Table>
      </ListState>

      {confirm && (
        <ConfirmDialog
          open
          onOpenChange={(open) => { if (!open) setConfirm(null); }}
          title={`${actionLabel(confirm.action, confirm.runs.length)}?`}
          description={
            <span>
              {confirm.action === "stop" && "Active workers are terminated, while history and diffs are preserved. "}
              {confirm.action === "retry" && "Stopped or failed sessions resume from their persisted state. "}
              {confirm.action === "promote" && "Anything still running is stopped and each run completes successfully. "}
              <span className="mt-2 block font-mono text-xs">{confirm.runs.slice(0, 4).map((run) => run.displayName || run.name).join(", ")}{confirm.runs.length > 4 ? ` +${confirm.runs.length - 4}` : ""}</span>
            </span>
          }
          confirmLabel={actionLabel(confirm.action, confirm.runs.length)}
          destructive={confirm.action === "stop"}
          onConfirm={async () => { await executeAction(confirm.action, confirm.runs); setConfirm(null); }}
        />
      )}

      <Dialog open={extend !== null} onOpenChange={(open) => { if (!open && !extendingRuntime) setExtend(null); }}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>Extend runtime</DialogTitle>
            <DialogDescription>
              Add runtime to {extend?.runs.length || 0} eligible {(extend?.runs.length || 0) === 1 ? "run" : "runs"}. Paused runs resume automatically.
            </DialogDescription>
          </DialogHeader>
          <label className="space-y-1 text-xs font-medium text-muted-foreground">
            Duration
            <Input
              value={extend?.duration || ""}
              disabled={extendingRuntime}
              onChange={(event) => setExtend((current) => current ? { ...current, duration: event.currentTarget.value } : null)}
              placeholder="1h"
            />
            <span className="block font-normal">Examples: 30m, 1h, 2h30m</span>
          </label>
          <DialogFooter>
            <Button variant="outline" disabled={extendingRuntime} onClick={() => setExtend(null)}>Cancel</Button>
            <Button disabled={extendingRuntime || !extend?.duration.trim()} onClick={() => extend && void executeExtend(extend.runs, extend.duration)}>
              {extendingRuntime ? "Extending…" : "Extend"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={saveViewOpen} onOpenChange={setSaveViewOpen}>
        <DialogContent className="sm:max-w-xs">
          <DialogHeader>
            <DialogTitle>Save operations view</DialogTitle>
            <DialogDescription>Filters, grouping, and sort order are stored on this device.</DialogDescription>
          </DialogHeader>
          <Input autoFocus value={viewName} onChange={(event) => setViewName(event.currentTarget.value)} placeholder="Needs review" />
          <DialogFooter>
            <Button variant="outline" onClick={() => setSaveViewOpen(false)}>Cancel</Button>
            <Button disabled={!viewName.trim()} onClick={saveView}>Save</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ComparisonDialog runs={compareRuns} onOpenChange={(open) => { if (!open) setCompareRuns([]); }} now={now} />

      {sharing && (
        <ShareDialog
          resourceType="agent_run"
          resourceId={sharing.name}
          resourceNamespace={sharing.namespace}
          open
          onOpenChange={(open) => { if (!open) setSharing(null); }}
        />
      )}
    </div>
  );
}

function FilterMenu({
  label,
  values,
  selected,
  onToggle,
}: {
  label: string;
  values: string[];
  selected: Set<string>;
  onToggle: (value: string) => void;
}) {
  if (!values.length) return null;
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="sm" className="gap-1 text-muted-foreground" />}>
        {label}
        {selected.size > 0 && <Badge variant="secondary" className="h-4 px-1 text-[10px]">{selected.size}</Badge>}
        <ChevronDown className="size-3" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" className="min-w-[170px]">
        <DropdownMenuLabel>{label}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {values.map((value) => (
          <DropdownMenuCheckboxItem key={value} checked={selected.has(value)} onCheckedChange={() => onToggle(value)}>
            {value}
          </DropdownMenuCheckboxItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function ChoiceMenu({
  label,
  choices,
  value,
  onChange,
}: {
  label: string;
  choices: [string, string][];
  value: string;
  onChange: (value: string) => void;
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="sm" className="gap-1 text-muted-foreground" />}>
        {label} <ChevronDown className="size-3" />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {choices.map(([key, choiceLabel]) => (
          <DropdownMenuCheckboxItem key={key} checked={value === key} onCheckedChange={() => onChange(key)}>
            {choiceLabel}
          </DropdownMenuCheckboxItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

function RunRows({
  run,
  now,
  selected,
  expanded,
  busy,
  allRuns,
  onSelect,
  onExpand,
  onAction,
  onShare,
  onCompare,
}: {
  run: AgentRun;
  now: number;
  selected: boolean;
  expanded: boolean;
  busy: boolean;
  allRuns: AgentRun[];
  onSelect: (checked: boolean) => void;
  onExpand: () => void;
  onAction: (action: OpsAction) => void;
  onShare: () => void;
  onCompare: (runs: AgentRun[]) => void;
}) {
  const attention = getRunAttention(run);
  const activity = latestRunActivity(run);
  const prs = runPullRequestUrls(run);
  const sourcePath = runSourcePath(run);
  const comparisonKey = runComparisonKey(run);
  const related = comparisonKey ? allRuns.filter((candidate) => runComparisonKey(candidate) === comparisonKey) : [];
  const hasChildren = run.children.length > 0;
  const presentation = runActivitySummary(run, now);
  const activitySummary = attention.detail || presentation.summary;
  const activityTimestamp = attention.kind === "none" ? presentation.timestamp : activity?.timestampUnix;
  const rowTone = attention.kind === "none" ? "" : attention.tone === "danger" ? "border-l-2 border-l-destructive bg-[color-mix(in_oklch,var(--destructive)_4%,transparent)]" : "border-l-2 border-l-[color:var(--tone-warning)] bg-[color-mix(in_oklch,var(--tone-warning)_4%,transparent)]";

  return (
    <>
      <TableRow data-state={selected ? "selected" : undefined} className={cn(rowTone, busy && "opacity-60")}>
        <TableCell>
          <input type="checkbox" aria-label={`Select ${run.displayName || run.name}`} checked={selected} onChange={(event) => onSelect(event.currentTarget.checked)} className="size-3.5 accent-primary" />
        </TableCell>
        <TableCell className="max-w-[260px]">
          <div className="flex min-w-0 items-center gap-2">
            <OwnerAvatar owner={run.owner} />
            <div className="min-w-0">
              <Link to={`/runs/${run.namespace}/${run.name}`} className="block truncate font-medium text-primary hover:underline" title={run.displayName || run.name}>
                {run.displayName || run.intentTitle || run.name}
              </Link>
              <div className="flex items-center gap-1.5 truncate font-mono text-[10.5px] text-muted-foreground">
                <span className="truncate">{run.namespace}/{run.name}</span>
                {related.length > 1 && (
                  <button type="button" onClick={() => onCompare(related)} className="shrink-0 text-primary hover:underline">
                    compare {related.length}
                  </button>
                )}
              </div>
            </div>
          </div>
        </TableCell>
        <TableCell>
          <div className="flex flex-col items-start gap-1">
            <StatusBadge phase={run.phase} run={run} />
            {attention.kind !== "none" && (
              <span title={attention.detail} className={cn("inline-flex max-w-[150px] items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-medium", toneSoft[attention.tone])}>
                <AlertTriangle className="size-2.5 shrink-0" />
                <span className="truncate">{attention.label}</span>
              </span>
            )}
          </div>
        </TableCell>
        <TableCell className="hidden xl:table-cell">
          <div className="text-[12px]">{runModeLabel(run)}</div>
        </TableCell>
        <TableCell className="max-w-[300px] whitespace-normal">
          <div className={cn("truncate text-[12.5px]", isDonePhase(run.phase) && attention.kind === "none" && "text-muted-foreground")} title={activitySummary}>
            {activitySummary}
          </div>
          <div className="mt-0.5 flex items-center gap-2 text-[10.5px] text-muted-foreground">
            {activityTimestamp ? <span>{formatAge(activityTimestamp, now)} ago</span> : <span>-</span>}
            {hasChildren && (
              <button type="button" onClick={onExpand} className="inline-flex items-center gap-1 text-primary hover:underline" aria-expanded={expanded}>
                <Network className="size-3" /> {run.children.length} child {run.children.length === 1 ? "run" : "runs"}
              </button>
            )}
          </div>
        </TableCell>
        <TableCell className="hidden max-w-[200px] lg:table-cell">
          <div className="flex items-center gap-1.5">
            {sourcePath ? (
              <Link to={sourcePath} className="truncate text-[12px] text-primary hover:underline">{runSourceLabel(run)}</Link>
            ) : (
              <span className="truncate text-[12px] text-muted-foreground">{runSourceLabel(run)}</span>
            )}
            {run.trigger?.externalUrl && (
              <a href={run.trigger.externalUrl} target="_blank" rel="noopener noreferrer" aria-label="Open source item" className="text-muted-foreground hover:text-foreground">
                <ExternalLink className="size-3" />
              </a>
            )}
          </div>
          <div className="flex items-center gap-1.5 truncate font-mono text-[10.5px] text-muted-foreground">
            {run.repoUrl && (
              <a href={run.repoUrl} target="_blank" rel="noopener noreferrer" className="truncate hover:text-foreground hover:underline" title={run.repoUrl}>{runRepoLabel(run)}</a>
            )}
            {run.trigger?.externalIdentifier && <span className="shrink-0 truncate">{run.trigger.externalIdentifier}</span>}
          </div>
        </TableCell>
        <TableCell className="hidden lg:table-cell">
          {prs.length ? (
            <div className="flex items-center gap-1">
              <a href={prs[prs.length - 1]} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-[12px] text-primary hover:underline">
                <GitPullRequest className="size-3" /> {pullRequestLabel(prs[prs.length - 1])}
              </a>
              {prs.length > 1 && <Badge variant="secondary" className="h-4 px-1 text-[9px]">+{prs.length - 1}</Badge>}
            </div>
          ) : <span className="text-muted-foreground">-</span>}
          {run.prLoop?.state && <div className="mt-0.5 text-[10.5px] capitalize text-muted-foreground">{run.prLoop.state.replace(/_/g, " ")}</div>}
        </TableCell>
        <TableCell className="text-right font-mono text-[12px] tabular-nums">{run.costUsd ? `$${costValue(run).toFixed(2)}` : "-"}</TableCell>
        <TableCell className="text-right">
          <div className="font-mono text-[12px] tabular-nums text-muted-foreground">{formatAge(run.createdAtUnix, now)}</div>
          <div className="font-mono text-[10px] text-muted-foreground/70">{shortDuration(runDurationSeconds(run, now))} runtime</div>
        </TableCell>
        <TableCell>
          <DropdownMenu>
            <DropdownMenuTrigger render={<Button variant="ghost" size="icon-xs" aria-label={`Actions for ${run.displayName || run.name}`} />}>
              <MoreHorizontal className="size-3.5" />
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-[190px]">
              <DropdownMenuItem render={<Link to={`/runs/${run.namespace}/${run.name}`} />}>
                <Activity className="size-3.5" /> Open session
              </DropdownMenuItem>
              {run.trigger?.externalUrl && (
                <DropdownMenuItem render={<a href={run.trigger.externalUrl} target="_blank" rel="noopener noreferrer" />}>
                  <ExternalLink className="size-3.5" /> Open source
                </DropdownMenuItem>
              )}
              {prs.length > 0 && (
                <DropdownMenuItem render={<a href={prs[prs.length - 1]} target="_blank" rel="noopener noreferrer" />}>
                  <GitPullRequest className="size-3.5" /> Open pull request
                </DropdownMenuItem>
              )}
              {run.repoUrl && (
                <DropdownMenuItem render={<a href={run.repoUrl} target="_blank" rel="noopener noreferrer" />}>
                  <GitBranch className="size-3.5" /> Open repository
                </DropdownMenuItem>
              )}
              {related.length > 1 && (
                <DropdownMenuItem onClick={() => onCompare(related)}>
                  <Layers3 className="size-3.5" /> Compare attempts
                </DropdownMenuItem>
              )}
              {(run.myPermission === "owner" || run.myPermission === "admin") && (
                <DropdownMenuItem onClick={onShare}>
                  <Share2 className="size-3.5" /> Share…
                </DropdownMenuItem>
              )}
              <DropdownMenuSeparator />
              {canRunAction(run, "extend") && (
                <DropdownMenuItem onClick={() => onAction("extend")}>
                  <Clock3 className="size-3.5" /> Extend runtime…
                </DropdownMenuItem>
              )}
              {canRunAction(run, "retry") && (
                <DropdownMenuItem onClick={() => onAction("retry")}>
                  <RotateCcw className="size-3.5" /> Retry run
                </DropdownMenuItem>
              )}
              {canRunAction(run, "promote") && (
                <DropdownMenuItem onClick={() => onAction("promote")}>
                  <Trophy className="size-3.5" /> Mark succeeded
                </DropdownMenuItem>
              )}
              {canRunAction(run, "stop") && (
                <DropdownMenuItem variant="destructive" onClick={() => onAction("stop")}>
                  <Square className="size-3.5" /> Stop run…
                </DropdownMenuItem>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </TableCell>
      </TableRow>
      {expanded && hasChildren && (
        <TableRow className="bg-muted/15 hover:bg-muted/15">
          <TableCell colSpan={10} className="py-3 pl-14">
            <ChildRunGraph run={run} />
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function ChildRunGraph({ run }: { run: AgentRun }) {
  const declaredTasks = (run.team?.steps || []).flatMap((step) =>
    step.tasks.map((task) => ({ task, step: step.name })),
  );
  const hasDependencies = declaredTasks.some(({ task }) => task.dependsOn.length > 0);

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2 text-[11.5px]">
        <Network className="size-3.5 text-primary" />
        <span className="font-medium">Team progress</span>
        {run.teamSummary?.blockedReason && <span className={cn("rounded-full px-2 py-0.5", toneSoft.warning)}>{run.teamSummary.blockedReason}</span>}
        <span className="text-muted-foreground">
          {run.teamSummary?.succeededChildren || 0} succeeded · {run.teamSummary?.runningChildren || 0} running · {run.teamSummary?.failedChildren || 0} failed
        </span>
      </div>

      {hasDependencies && (
        <div className="space-y-1.5">
          <div className="text-[10.5px] font-medium uppercase tracking-[0.05em] text-muted-foreground">Declared dependencies</div>
          <div className="flex flex-wrap gap-2">
            {declaredTasks.map(({ task, step }) => (
              <div key={`${step}/${task.name}`} className="min-w-[150px] max-w-[230px] rounded-lg border border-border/70 bg-card px-2.5 py-2">
                <div className="flex items-center gap-1.5">
                  <span className="truncate text-[11px] font-medium">{task.name}</span>
                  <span className="ml-auto shrink-0 font-mono text-[9px] text-muted-foreground">{step}</span>
                </div>
                <div className="mt-1 truncate text-[9.5px] text-muted-foreground">{task.role || "agent"}</div>
                {task.dependsOn.length > 0 ? (
                  <div className="mt-1.5 flex items-start gap-1 text-[9.5px] text-amber-500">
                    <GitBranch className="mt-px size-2.5 shrink-0" />
                    <span className="line-clamp-2">waits for {task.dependsOn.join(", ")}</span>
                  </div>
                ) : (
                  <div className="mt-1.5 text-[9.5px] text-muted-foreground/70">no prerequisites</div>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      <div className="flex min-w-max items-center gap-2 overflow-x-auto pb-1">
        <div className="rounded-lg border border-primary/30 bg-primary/8 px-3 py-2">
          <div className="text-[11px] font-medium">{run.displayName || run.name}</div>
          <div className="mt-0.5 font-mono text-[9.5px] text-muted-foreground">parent · {run.teamSummary?.currentStep || run.currentStep || "orchestrating"}</div>
        </div>
        <ChevronRight className="size-4 shrink-0 text-muted-foreground" />
        {run.children.map((child, index) => (
          <React.Fragment key={`${child.namespace}/${child.name}`}>
            {index > 0 && <span className="h-px w-3 shrink-0 bg-border" />}
            <Link
              to={`/runs/${child.namespace || run.namespace}/${child.name}`}
              className="rounded-lg border border-border/70 bg-card px-3 py-2 transition-colors hover:border-primary/40 hover:bg-muted/40"
            >
              <div className="flex items-center gap-1.5">
                <StatusBadge phase={child.phase} />
                <span className="max-w-[170px] truncate text-[11px] font-medium">{child.name}</span>
              </div>
              <div className="mt-1 font-mono text-[9.5px] text-muted-foreground">{child.role || "agent"} · {child.step || "task"}</div>
              {child.blockedReason && <div className="mt-1 max-w-[190px] truncate text-[9.5px] text-amber-500">{child.blockedReason}</div>}
            </Link>
          </React.Fragment>
        ))}
      </div>
    </div>
  );
}

function ComparisonDialog({ runs, onOpenChange, now }: { runs: AgentRun[]; onOpenChange: (open: boolean) => void; now: number }) {
  return (
    <Dialog open={runs.length > 0} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[900px]">
        <DialogHeader>
          <DialogTitle>Compare runs</DialogTitle>
          <DialogDescription>Compare related attempts or any selected runs by outcome, runtime, cost, activity, and artifacts.</DialogDescription>
        </DialogHeader>
        <div className="max-h-[60vh] overflow-auto rounded-lg border border-border/60">
          <table className="w-full min-w-[760px] text-sm">
            <thead className="sticky top-0 bg-muted/80 text-left text-[10.5px] uppercase tracking-[0.05em] text-muted-foreground backdrop-blur">
              <tr>
                <th className="px-3 py-2">Run</th><th className="px-3 py-2">Status</th><th className="px-3 py-2">Mode</th><th className="px-3 py-2">Runtime</th><th className="px-3 py-2 text-right">Cost</th><th className="px-3 py-2 text-right">Tokens</th><th className="px-3 py-2 text-right">Tools</th><th className="px-3 py-2">Result / attention</th><th className="px-3 py-2">Artifacts</th>
              </tr>
            </thead>
            <tbody>
              {[...runs].sort((a, b) => Number(a.createdAtUnix - b.createdAtUnix)).map((run, index) => {
                const attention = getRunAttention(run);
                const prs = runPullRequestUrls(run);
                return (
                  <tr key={runKey(run)} className="border-t border-border/50 align-top">
                    <td className="px-3 py-2">
                      <Link to={`/runs/${run.namespace}/${run.name}`} className="block max-w-[180px] truncate font-medium text-primary hover:underline">{run.displayName || run.name}</Link>
                      <div className="max-w-[180px] truncate font-mono text-[9.5px] text-muted-foreground">#{index + 1} · {run.namespace}/{run.name}</div>
                      <div className="font-mono text-[9.5px] text-muted-foreground">{formatAge(run.createdAtUnix, now)} ago</div>
                    </td>
                    <td className="px-3 py-2"><StatusBadge phase={run.phase} run={run} /></td>
                    <td className="px-3 py-2 text-[12px]">{runModeLabel(run)}</td>
                    <td className="px-3 py-2 font-mono text-[12px]">{shortDuration(runDurationSeconds(run, now))}</td>
                    <td className="px-3 py-2 text-right font-mono">${costValue(run).toFixed(2)}</td>
                    <td className="px-3 py-2 text-right font-mono">{Number(run.inputTokens + run.outputTokens).toLocaleString()}</td>
                    <td className="px-3 py-2 text-right font-mono">{run.toolCallCount}</td>
                    <td className="max-w-[220px] whitespace-normal px-3 py-2 text-[11.5px] text-muted-foreground">{attention.detail || runActivitySummary(run, now).summary}</td>
                    <td className="px-3 py-2">
                      <div className="flex items-center gap-2">
                        {prs.length > 0 && <a href={prs[prs.length - 1]} target="_blank" rel="noopener noreferrer" className="text-primary hover:underline">PR</a>}
                        {run.trigger?.externalUrl && <a href={run.trigger.externalUrl} target="_blank" rel="noopener noreferrer" className="text-primary hover:underline">Source</a>}
                        {!prs.length && !run.trigger?.externalUrl && <span className="text-muted-foreground">-</span>}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        <DialogFooter showCloseButton />
      </DialogContent>
    </Dialog>
  );
}
