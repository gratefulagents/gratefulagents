/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { clone, create } from "@bufbuild/protobuf";
import {
  ArrowDown, ArrowUp, Check, Copy, FilterX, Pause, Pencil, Play, SquareArrowOutUpRight,
} from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import { ListSearchInput } from "@/components/ui/list-search";
import {
  FilterBar, FilterChips, FilterSelect, type FilterOption,
} from "@/components/ui/filter-bar";
import { DetailErrorState, classifyDetailError } from "@/components/ui/detail-state";
import {
  DetailHeader, DetailSection, StatBar, Stat, FactList, Fact,
} from "@/components/detail-page";
import { ReadyBadge } from "@/components/ReadyBadge";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { BASELINE_STATES } from "@/components/security-baseline";
import {
  EmptyCell, SEVERITIES, SeverityBadge, SeverityCountBadges, severityTone, STATUS_PILL,
} from "@/components/SecurityScanList";
import { FINDING_STATUSES, statusLabel } from "@/components/SecurityScanDetail";
import {
  SecurityScanFormDialog, scanConfigUsesSavedCredentials,
} from "@/components/SecurityScanFormDialog";
import { useUrlFilters } from "@/hooks/useUrlFilters";
import { SEVERITY_ORDER, SEVERITY_RANK, optionsFrom, repoLabel, timestampMs } from "@/lib/securityFilters";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft, toneText, type StatusTone } from "@/lib/status";
import { useNow } from "@/hooks/useNow";
import {
  SecurityScanConfigSpecSchema,
  UpdateSecurityScanRequestSchema,
  type SecurityFinding,
  type SecurityScan,
  type SecurityScanConfig,
} from "@/rpc/platform/service_pb";

const filterDividerClass = "mx-0.5 h-5 w-px shrink-0 self-center bg-border/60";

const filterInputClass =
  "h-7 w-28 rounded-lg border border-input bg-background px-2 text-[12px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 dark:bg-input/30";

// The findings store caps an omitted limit at 200 rows; page explicitly so a
// configuration with more findings than one page can still show all of them.
const FINDINGS_PAGE_SIZE = 200;

/** Runs shown inline; the rest are one click away in the scan runs list. */
const RUNS_PREVIEW = 8;

/**
 * Canonical finding-filter contract, shared with the scan detail and finding
 * detail pages, so a filtered view survives navigation between them.
 */
const FILTER_SPEC = {
  q: "",
  severity: "all",
  status: "actionable",
  category: "all",
  tool: "all",
  file: "",
  baseline: "all",
  assignee: "",
  suppressed: "exclude",
  dupes: "hide",
  selected: "",
} as const;

/** Keys "Clear" resets; `selected` marks a row, it does not filter. */
const FILTER_KEYS = [
  "q", "severity", "status", "category", "tool", "file", "baseline", "assignee",
  "suppressed", "dupes",
] as const;

const SEVERITY_CHIP_OPTIONS: FilterOption[] = SEVERITY_ORDER.map((severity) => ({
  value: severity,
  label: severity.charAt(0).toUpperCase() + severity.slice(1),
}));

const STATUS_FILTER_OPTIONS: FilterOption[] = [
  { value: "actionable", label: "Actionable" },
  { value: "all", label: "Any status" },
  ...FINDING_STATUSES.map((value) => ({ value, label: statusLabel(value) })),
];

const BASELINE_FILTER_OPTIONS: FilterOption[] = optionsFrom(BASELINE_STATES, "Any baseline");

const SUPPRESSED_FILTER_OPTIONS: FilterOption[] = [
  { value: "exclude", label: "Hidden" },
  { value: "include", label: "Included" },
  { value: "only", label: "Only suppressed" },
];

const DUPES_FILTER_OPTIONS: FilterOption[] = [
  { value: "hide", label: "Hidden" },
  { value: "include", label: "Included" },
];

const RECOVERY_LINKS = [
  { to: "/security/configs", label: "Configurations" },
  { to: "/security", label: "Security overview" },
];

function runStatusTone(status: string): StatusTone {
  switch (status.toLowerCase()) {
    case "completed":
    case "succeeded":
      return "success";
    case "running":
      return "running";
    case "failed":
    case "error":
      return "danger";
    default:
      return "neutral";
  }
}

function runTimeMs(run: SecurityScan): number {
  return timestampMs(run.completedAt ?? run.startedAt);
}

/**
 * One relative-time vocabulary for the whole page — "just now", "12m ago",
 * "in 9h", "8d ago" — so the header, the findings table, the run list and the
 * schedule facts never render the same instant three different ways.
 */
function relativeTime(ms: number, nowMs: number): string {
  const seconds = Math.round((ms - nowMs) / 1000);
  const elapsed = Math.abs(seconds);
  if (elapsed < 45) return "just now";
  const amount =
    elapsed < 3600
      ? `${Math.round(elapsed / 60)}m`
      : elapsed < 86_400
        ? `${Math.round(elapsed / 3600)}h`
        : elapsed < 2_592_000
          ? `${Math.round(elapsed / 86_400)}d`
          : `${Math.round(elapsed / 2_592_000)}mo`;
  return seconds > 0 ? `in ${amount}` : `${amount} ago`;
}

/** Relative time with the absolute timestamp in the tooltip. */
function TimeAgo({ ms, now, className }: { ms: number; now: number; className?: string }) {
  if (!ms) return <EmptyCell meaning="Not recorded" className={cn("text-[12px]", className)} />;
  return (
    <span
      className={cn("whitespace-nowrap tabular-nums", className)}
      title={new Date(ms).toLocaleString()}
    >
      {relativeTime(ms, now)}
    </span>
  );
}

/** Copy affordance for values the rail has to truncate (URLs, cron strings). */
function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      aria-label={`Copy ${label}`}
      title={`Copy ${label}`}
      onClick={() => {
        void navigator.clipboard?.writeText(value).then(() => {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1_500);
        });
      }}
      className="inline-flex size-5 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
    >
      {copied ? <Check className="size-3" aria-hidden /> : <Copy className="size-3" aria-hidden />}
      <span className="sr-only" role="status">
        {copied ? `${label} copied` : ""}
      </span>
    </button>
  );
}

/** Labelled fragment of the header identity line ("Repo · demo/app"). */
function HeaderMeta({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="inline-flex min-w-0 items-baseline gap-1.5">
      <span className="shrink-0 text-[10.5px] font-medium uppercase tracking-[0.06em] text-muted-foreground">
        {label}
      </span>
      {children}
    </span>
  );
}

type SortKey = "severity" | "title" | "seen" | "score";
type SortDir = "asc" | "desc";

function compareFindings(a: SecurityFinding, b: SecurityFinding, key: SortKey): number {
  switch (key) {
    case "severity": {
      const rank = (f: SecurityFinding) => SEVERITY_RANK[f.severity.toLowerCase()] ?? 0;
      return rank(a) - rank(b) || a.score - b.score;
    }
    case "title":
      return a.title.localeCompare(b.title);
    case "seen":
      return timestampMs(a.lastSeenAt) - timestampMs(b.lastSeenAt);
    case "score":
      return a.score - b.score;
  }
}

/** Column header that also sorts the findings table. */
function SortHead({
  label,
  sortKey,
  sort,
  onSort,
  className,
}: {
  label: string;
  sortKey: SortKey;
  sort: { key: SortKey; dir: SortDir };
  onSort: (key: SortKey) => void;
  className?: string;
}) {
  const active = sort.key === sortKey;
  return (
    <TableHead
      className={className}
      aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : undefined}
    >
      <button
        type="button"
        onClick={() => onSort(sortKey)}
        className="inline-flex items-center gap-1 rounded-sm hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
      >
        {label}
        {active
          && (sort.dir === "asc" ? (
            <ArrowUp className="size-3" aria-hidden />
          ) : (
            <ArrowDown className="size-3" aria-hidden />
          ))}
      </button>
    </TableHead>
  );
}

function errorMessage(e: unknown, fallback: string): string {
  return e instanceof Error ? e.message : fallback;
}

function findingWord(count: number): string {
  return count === 1 ? "finding" : "findings";
}

/**
 * The single sentence that reconciles every finding number on this page: how
 * many rows match the current filters, how many are loaded while the list is
 * paged, and how many findings the configuration has recorded. The summary
 * strip, the toolbar and the table used to answer that question with three
 * different numbers ("5 actionable" beside "4 of 8").
 */
function findingsCountText(counts: {
  matching: number;
  loaded: number;
  recorded: number;
  hasMore: boolean;
  /** Recorded findings in the scope the status filter selects, when known. */
  scope: { count: number; noun: string } | null;
}): string {
  const { matching, loaded, recorded, hasMore, scope } = counts;
  if (hasMore) {
    const head = matching === loaded
      ? `${loaded} ${findingWord(loaded)} loaded`
      : `${matching} of ${loaded} loaded ${findingWord(loaded)} match these filters`;
    return `${head} · ${recorded} recorded — load more to see the rest`;
  }
  if (scope) {
    const head = matching === scope.count
      ? `All ${scope.count} ${scope.noun}`
      : `${matching} of ${scope.count} ${scope.noun} match these filters`;
    return `${head} · ${recorded} recorded in total`;
  }
  if (matching === recorded) return `All ${recorded} ${findingWord(recorded)}`;
  return `${matching} of ${recorded} ${findingWord(recorded)} match these filters`;
}

/**
 * Configuration-level security view: every deduplicated finding the scan has
 * ever reported (across all of its runs), plus the run history — so a single
 * click from the configurations list shows the whole picture without visiting
 * each run individually.
 */
export function SecurityConfigDetail() {
  const { namespace, name } = useParams<{ namespace: string; name: string }>();
  const navigate = useNavigate();
  const { values, set, setMany, activeCount, queryString } = useUrlFilters(FILTER_SPEC);
  const {
    q: search, severity, status, category, baseline, assignee, suppressed,
  } = values;

  const [config, setConfig] = useState<SecurityScanConfig | null>(null);
  const [summary, setSummary] = useState<Record<string, number>>({});
  const [findings, setFindings] = useState<SecurityFinding[]>([]);
  const [runs, setRuns] = useState<SecurityScan[]>([]);
  const [loading, setLoading] = useState(true);
  const [findingsLoading, setFindingsLoading] = useState(true);
  const [loadedPages, setLoadedPages] = useState(1);
  const [hasMoreFindings, setHasMoreFindings] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState("");
  const [findingsError, setFindingsError] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [runNowPending, setRunNowPending] = useState(false);
  const [suspendPending, setSuspendPending] = useState(false);
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir }>({
    key: "severity",
    dir: "desc",
  });
  const now = useNow();

  const fetchConfig = useCallback(async (background = false) => {
    if (!namespace || !name) return;
    if (!background) {
      setLoading(true);
      setError("");
    }
    try {
      const [configResp, summaryResp, runsResp] = await Promise.all([
        client.getSecurityScanConfig({ namespace, name }),
        client.getSecurityFindingSummary({ namespace, scanName: name }),
        client.listSecurityScans({ namespace, scanName: name }),
      ]);
      setConfig(configResp);
      setSummary(summaryResp.counts);
      setRuns(runsResp.scans);
    } catch (e: unknown) {
      if (!background) {
        setError(errorMessage(e, "Failed to load the scan configuration"));
      }
    } finally {
      if (!background) setLoading(false);
    }
  }, [namespace, name]);

  const fetchFindings = useCallback(async (background = false, pageCount = 1) => {
    if (!namespace || !name) return;
    if (!background) setFindingsLoading(true);
    try {
      // Offset-paged accumulation: fetch up to pageCount pages, stopping at
      // the first short page (no further rows on the server).
      const accumulated: SecurityFinding[] = [];
      let more = false;
      for (let page = 0; page < pageCount; page++) {
        const resp = await client.listSecurityFindings({
          namespace,
          scanName: name,
          severity: severity === "all" ? "" : severity,
          status: status === "all" ? "" : status,
          category: category === "all" ? "" : category,
          search,
          baselineState: baseline === "all" ? "" : baseline,
          assignee,
          suppressed,
          includeDuplicates: values.dupes === "include",
          limit: FINDINGS_PAGE_SIZE,
          offset: page * FINDINGS_PAGE_SIZE,
        });
        accumulated.push(...resp.findings);
        more = resp.findings.length === FINDINGS_PAGE_SIZE;
        if (!more) break;
      }
      setFindings(accumulated);
      setHasMoreFindings(more);
      setLoadedPages(pageCount);
      setFindingsError("");
    } catch (e: unknown) {
      if (!background) {
        setFindingsError(errorMessage(e, "Failed to load security findings"));
      }
    } finally {
      if (!background) setFindingsLoading(false);
    }
  }, [
    namespace, name, severity, status, category, search, baseline, assignee,
    suppressed, values.dupes,
  ]);

  useEffect(() => {
    void fetchConfig();
  }, [fetchConfig]);

  // Any filter change recreates fetchFindings, which restarts paging at the
  // first page — a narrowed query must never keep the previous offsets.
  useEffect(() => {
    void fetchFindings();
  }, [fetchFindings]);

  async function loadMoreFindings() {
    setLoadingMore(true);
    try {
      await fetchFindings(true, loadedPages + 1);
    } finally {
      setLoadingMore(false);
    }
  }

  // While a run is executing, the summary, findings, and run list keep
  // changing server-side; poll quietly (no loading flicker) until every run
  // settles, skipping refreshes while the tab is hidden.
  const anyRunning = runs.some((run) => run.status.toLowerCase() === "running");
  useEffect(() => {
    if (!anyRunning) return;
    const id = window.setInterval(() => {
      if (document.hidden) return;
      void fetchConfig(true);
      void fetchFindings(true, loadedPages);
    }, 5_000);
    return () => window.clearInterval(id);
  }, [anyRunning, fetchConfig, fetchFindings, loadedPages]);

  // The category filter is server-side, so once it narrows the response the
  // active value has to be re-added to keep it selectable in the menu.
  const categoryOptions = useMemo(
    () => optionsFrom(
      [...findings.map((finding) => finding.category), category === "all" ? "" : category],
      "Any category",
    ),
    [findings, category],
  );

  const toolOptions = useMemo(
    () => optionsFrom(findings.map((finding) => finding.sourceAgent), "Any tool"),
    [findings],
  );

  // `tool` and `file` have no server-side equivalent on listSecurityFindings,
  // so they narrow the loaded page client-side.
  const visibleFindings = useMemo(() => {
    const file = values.file.trim().toLowerCase();
    return findings.filter((finding) => {
      if (values.tool !== "all" && finding.sourceAgent !== values.tool) return false;
      if (file && !finding.filePath.toLowerCase().includes(file)) return false;
      return true;
    });
  }, [findings, values.tool, values.file]);

  const sortedFindings = useMemo(() => {
    const rows = [...visibleFindings];
    rows.sort((a, b) => {
      const c = compareFindings(a, b, sort.key);
      return sort.dir === "asc" ? c : -c;
    });
    return rows;
  }, [visibleFindings, sort]);

  const onSort = useCallback((key: SortKey) => {
    setSort((prev) =>
      prev.key === key
        ? { key, dir: prev.dir === "desc" ? "asc" : "desc" }
        : { key, dir: key === "title" ? "asc" : "desc" },
    );
  }, []);

  // For deterministic multi-task executions, finding.runName is the task
  // AgentRun while only the shared execution's scan record exists in the run
  // routes. Resolve links through the persisted scan run (id == scanId) and
  // fall back to the reported run name.
  const runNameByScanId = useMemo(() => {
    const map = new Map<string, string>();
    for (const run of runs) map.set(run.id, run.runName);
    return map;
  }, [runs]);

  const findingRunName = useCallback(
    (finding: SecurityFinding) => runNameByScanId.get(finding.scanId) ?? finding.runName,
    [runNameByScanId],
  );

  // Findings keep the current filters in their link so the finding page can
  // offer the same narrowed view (and a way back to it).
  const findingHref = useCallback(
    (finding: SecurityFinding) =>
      `/security/${finding.namespace}/${findingRunName(finding)}/findings/${finding.id}${queryString}`,
    [findingRunName, queryString],
  );

  const clearFilters = useCallback(() => {
    setMany(Object.fromEntries(FILTER_KEYS.map((key) => [key, FILTER_SPEC[key]])));
  }, [setMany]);

  // Activating a row opens the finding; recording it as `selected` first means
  // the browser's back button returns to this list with the row still marked.
  const openFinding = useCallback(
    (finding: SecurityFinding) => {
      set("selected", finding.id);
      navigate(findingHref(finding));
    },
    [set, navigate, findingHref],
  );

  async function handleRunNow() {
    if (!namespace || !name) return;
    setActionError(null);
    setRunNowPending(true);
    try {
      await client.runSecurityScanNow({ namespace, name });
      await fetchConfig(true);
    } catch (e: unknown) {
      setActionError(errorMessage(e, "Failed to start the security scan"));
    } finally {
      setRunNowPending(false);
    }
  }

  async function toggleSuspend(current: SecurityScanConfig) {
    setActionError(null);
    setSuspendPending(true);
    const spec = current.spec
      ? clone(SecurityScanConfigSpecSchema, current.spec)
      : create(SecurityScanConfigSpecSchema, {});
    spec.suspend = !spec.suspend;
    try {
      await client.updateSecurityScan(
        create(UpdateSecurityScanRequestSchema, {
          namespace: current.namespace,
          name: current.name,
          spec,
          useSavedCredentials: scanConfigUsesSavedCredentials(current),
        }),
      );
      await fetchConfig(true);
    } catch (e: unknown) {
      setActionError(errorMessage(e, "Failed to update the security scan"));
    } finally {
      setSuspendPending(false);
    }
  }

  if (!config) {
    if (loading) {
      return (
        <div role="status" aria-live="polite">
          <ListRowSkeleton rows={4} />
        </div>
      );
    }
    const kind = error ? classifyDetailError(error) : "not-found";
    return (
      <DetailErrorState
        kind={kind}
        title={kind === "not-found" ? "Scan configuration not found" : undefined}
        description={
          kind === "not-found"
            ? `No scan configuration named "${name}" exists in ${namespace}. It may have been deleted, or the link may point at another namespace.`
            : undefined
        }
        detail={error || undefined}
        onRetry={() => {
          void fetchConfig();
          void fetchFindings();
        }}
        links={RECOVERY_LINKS}
      />
    );
  }

  const suspended = config.spec?.suspend ?? false;
  const repoUrl = config.spec?.repoUrl ?? "";
  const targetUrl = config.spec?.targetUrl ?? "";
  const target = repoUrl || targetUrl;
  const targetText = repoUrl ? repoLabel(repoUrl) : targetUrl;
  const cron = config.spec?.schedule ?? "";
  const schedule = cron || "Runs once";
  const filtersActive = activeCount(["selected"]);
  const totalFindings = summary["total"] ?? 0;
  const actionableFindings = summary["actionable"] ?? summary["open"] ?? 0;
  const shownFindings = sortedFindings.length;
  // A Run column that repeats the same name on every row is noise; it only
  // earns its width when the findings actually come from different runs.
  const showRunColumn = new Set(findings.map(findingRunName)).size > 1;
  const lastScanMs = Number(config.lastScanTimeUnix) * 1000;
  const nextScanMs = Number(config.nextScheduleTimeUnix) * 1000;
  const ownerLabel = config.owner
    ? `Owner: ${config.owner.name || config.owner.email || "unknown"}`
    : "";
  // The summary counts suppressed findings separately and leaves them out of
  // every other key, so the population the list draws from depends on the
  // suppressed filter.
  const suppressedFindings = summary["suppressed"] ?? 0;
  const recordedFindings =
    suppressed === "only"
      ? suppressedFindings
      : suppressed === "include"
        ? totalFindings + suppressedFindings
        : totalFindings;
  // With the default status filter the list is a subset of one summary stat;
  // naming that stat is what stops "5 actionable" and "4 shown" from reading
  // as a contradiction. Only trust it while the summary and the query cover
  // the same rows (it excludes suppressed findings, and duplicates are counted
  // by neither).
  const statusScope =
    suppressed !== "exclude" || values.dupes === "include"
      ? null
      : status === "actionable"
        ? { count: actionableFindings, noun: "actionable findings" }
        : status === "open"
          ? { count: summary["open"] ?? 0, noun: "open findings" }
          : null;
  const countScope =
    statusScope && statusScope.count >= shownFindings && statusScope.count !== recordedFindings
      ? statusScope
      : null;
  const countText = findingsLoading && findings.length === 0
    ? "Loading findings…"
    : findingsCountText({
      matching: shownFindings,
      loaded: findings.length,
      recorded: recordedFindings,
      hasMore: hasMoreFindings,
      scope: countScope,
    });
  // What the view is holding back, and the one click that brings it in.
  const hiddenSuppressed = suppressed === "exclude" && suppressedFindings > 0;
  const hiddenDupes = values.dupes === "hide";
  const hiddenParts = [
    hiddenSuppressed ? `${suppressedFindings} suppressed` : "",
    hiddenDupes ? "duplicates" : "",
  ].filter(Boolean);
  const hiddenLabel = hiddenParts.length > 0 ? `${hiddenParts.join(" and ")} hidden` : "";
  const includeHiddenLabel =
    hiddenParts.length === 2
      ? "Include both"
      : hiddenSuppressed
        ? "Include suppressed"
        : "Include duplicates";
  const includeHidden = () =>
    setMany({
      ...(hiddenSuppressed ? { suppressed: "include" } : {}),
      ...(hiddenDupes ? { dupes: "include" } : {}),
    });

  return (
    <div className="space-y-7">
      <DetailHeader
        parentLabel="Configurations"
        parentTo="/security/configs"
        title={config.name}
        meta={
          <>
            {suspended ? (
              <Badge variant="secondary">Suspended</Badge>
            ) : (
              <ReadyBadge status={config.conditionReady} />
            )}
            {config.owner && (
              // The bare avatar was an unexplained badge: name the person it
              // stands for for pointer, keyboard and screen-reader users.
              <span className="inline-flex items-center" title={ownerLabel}>
                <span className="sr-only">{ownerLabel}</span>
                <OwnerAvatar owner={config.owner} />
              </span>
            )}
          </>
        }
        subtitle={
          <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 text-[12.5px] text-muted-foreground">
            <HeaderMeta label="Repo">
              <span className="max-w-[40ch] truncate font-mono" title={target || undefined}>
                {targetText || "—"}
              </span>
            </HeaderMeta>
            <HeaderMeta label="Namespace">
              <span className="truncate font-mono">{config.namespace}</span>
            </HeaderMeta>
            {/* The cron expression itself lives (once) in the Configuration
                panel; here the schedule is stated as what it means next. */}
            <HeaderMeta label="Schedule">
              {suspended ? (
                <span>Paused</span>
              ) : nextScanMs > 0 ? (
                <span className="inline-flex items-baseline gap-1">
                  next run <TimeAgo ms={nextScanMs} now={now} />
                </span>
              ) : (
                <span>{cron ? "Scheduled" : "Manual"}</span>
              )}
            </HeaderMeta>
          </div>
        }
        actions={
          <div
            role="group"
            aria-label="Configuration actions"
            className="flex flex-wrap items-center gap-2"
          >
            {/* One primary action; the state-changing suspend/resume is set
                apart by a divider so it is never clicked by momentum. */}
            {!suspended && (
              <Button size="sm" disabled={runNowPending} onClick={() => void handleRunNow()}>
                <Play />
                {runNowPending ? "Starting…" : "Run now"}
              </Button>
            )}
            <SecurityScanFormDialog
              config={config}
              trigger={
                <Button variant="outline" size="sm">
                  <Pencil />
                  Edit
                </Button>
              }
              onSaved={() => void fetchConfig(true)}
            />
            <span aria-hidden className="mx-0.5 h-5 w-px shrink-0 bg-border/70" />
            <Button
              variant={suspended ? "default" : "ghost"}
              size="sm"
              className={suspended ? undefined : "text-muted-foreground hover:text-foreground"}
              disabled={suspendPending}
              onClick={() => void toggleSuspend(config)}
            >
              {suspended ? <Play /> : <Pause />}
              {suspended ? "Resume" : "Suspend"}
            </Button>
          </div>
        }
      />

      {actionError && (
        <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]">
          {actionError}
        </div>
      )}

      {/* One treatment for every metric: a plain number, tinted by severity,
          with zeros dropped back so the eye lands on what is actually there. */}
      <StatBar>
        <Stat label="Findings" value={totalFindings} sub="all runs" />
        <Stat
          label="Actionable"
          value={actionableFindings}
          sub={actionableFindings > 0 ? `of ${totalFindings} need triage` : "nothing to triage"}
        />
        {SEVERITIES.map((s) => {
          const count = summary[`actionable_${s}`] ?? summary[s] ?? 0;
          return (
            <Stat
              key={s}
              label={s}
              value={
                <span className={count > 0 ? toneText[severityTone(s)] : "text-muted-foreground/70"}>
                  {count}
                </span>
              }
            />
          );
        })}
      </StatBar>

      {/* The rail only takes a column once the findings table can still hold
          its own columns beside it; below that it sits underneath, two
          sections wide, instead of squeezing the table off-screen. */}
      <div className="grid items-start gap-7 xl:grid-cols-[minmax(0,1fr)_320px]">
        <DetailSection
          title="Findings"
          description="Every deduplicated finding this scan has reported, across all of its runs."
        >
          <div className="space-y-2">
            <ListSearchInput
              value={search}
              onChange={(value) => set("q", value)}
              placeholder="Search findings…"
            />
            <FilterBar
              label="Finding filters"
              activeCount={filtersActive}
              onClear={clearFilters}
            >
              {/* Triage first (what is wrong, how bad), then where it came
                  from, then the scope switches most people never touch —
                  hairlines keep the wrapped row readable as three groups. */}
              <div className="flex flex-wrap items-center gap-1.5">
                <FilterChips
                  label="Severity"
                  options={SEVERITY_CHIP_OPTIONS}
                  selected={severity === "all" ? [] : [severity]}
                  onToggle={(value) => set("severity", severity === value ? "all" : value)}
                />
                <FilterSelect
                  label="Status"
                  value={status}
                  defaultValue="actionable"
                  onChange={(next) => set("status", next)}
                  options={STATUS_FILTER_OPTIONS}
                />
              </div>
              <span aria-hidden className={filterDividerClass} />
              <div className="flex flex-wrap items-center gap-1.5">
                <FilterSelect
                  label="Category"
                  value={category}
                  onChange={(next) => set("category", next)}
                  options={categoryOptions}
                />
                <FilterSelect
                  label="Tool"
                  value={values.tool}
                  onChange={(next) => set("tool", next)}
                  options={toolOptions}
                />
                <input
                  aria-label="Filter by file path"
                  type="text"
                  value={values.file}
                  onChange={(e) => set("file", e.target.value)}
                  placeholder="File path…"
                  className={filterInputClass}
                />
              </div>
              <span aria-hidden className={filterDividerClass} />
              <div className="flex flex-wrap items-center gap-1.5">
                <FilterSelect
                  label="Baseline"
                  value={baseline}
                  onChange={(next) => set("baseline", next)}
                  options={BASELINE_FILTER_OPTIONS}
                />
                <FilterSelect
                  label="Suppressed"
                  value={suppressed}
                  defaultValue="exclude"
                  onChange={(next) => set("suppressed", next)}
                  options={SUPPRESSED_FILTER_OPTIONS}
                />
                <FilterSelect
                  label="Duplicates"
                  value={values.dupes}
                  defaultValue="hide"
                  onChange={(next) => set("dupes", next)}
                  options={DUPES_FILTER_OPTIONS}
                />
                <input
                  aria-label="Filter by assignee"
                  type="text"
                  value={assignee}
                  onChange={(e) => set("assignee", e.target.value)}
                  placeholder="Assignee…"
                  className={filterInputClass}
                />
              </div>
            </FilterBar>
            {/* One place answers "how many?" — matching, loaded, recorded, and
                what the default filters are holding back, with the switch that
                brings it in. */}
            <p
              role="status"
              className="flex flex-wrap items-center gap-x-1.5 gap-y-1 px-0.5 text-[12px] text-muted-foreground"
            >
              <span className="tabular-nums">{countText}</span>
              {hiddenLabel && (
                <>
                  <span aria-hidden>·</span>
                  <span className="tabular-nums">{hiddenLabel}</span>
                  <button
                    type="button"
                    onClick={includeHidden}
                    className="ml-0.5 rounded-sm font-medium text-primary underline underline-offset-2 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                  >
                    {includeHiddenLabel}
                  </button>
                </>
              )}
            </p>
          </div>

          {findingsError && (
            // Non-blocking: the configuration, its stats, and its runs stay on
            // screen when only the findings query fails.
            <div
              role="alert"
              className="mt-3 flex flex-wrap items-center gap-2 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]"
            >
              <span className="min-w-0 flex-1">Couldn't load findings. {findingsError}</span>
              <Button variant="outline" size="sm" onClick={() => void fetchFindings()}>
                Retry
              </Button>
            </div>
          )}

          {!(findingsError && findings.length === 0) && (
            <div className="mt-3">
              <ListState
                loading={findingsLoading}
                empty={!visibleFindings.length}
                skeleton={<ListRowSkeleton rows={4} />}
                emptyIcon={filtersActive > 0 ? <FilterX className="size-6" /> : undefined}
                emptyTitle={filtersActive > 0 ? "No findings match these filters" : "No findings"}
                emptyDescription={
                  filtersActive > 0
                    ? "Clear the filters to see everything this configuration has reported."
                    : "This scan has not reported any findings yet."
                }
                emptyAction={
                  filtersActive > 0 ? (
                    <Button variant="outline" size="sm" onClick={clearFilters}>
                      <FilterX />
                      Clear filters
                    </Button>
                  ) : undefined
                }
              >
                <>
                  <Table>
                    <TableCaption className="sr-only">
                      Security findings across all runs
                    </TableCaption>
                    <TableHeader>
                      <TableRow>
                        <SortHead label="Title" sortKey="title" sort={sort} onSort={onSort} />
                        <SortHead label="Severity" sortKey="severity" sort={sort} onSort={onSort} />
                        <TableHead className="hidden 2xl:table-cell">Category</TableHead>
                        <TableHead>Status</TableHead>
                        {showRunColumn && (
                          <TableHead className="hidden lg:table-cell">Run</TableHead>
                        )}
                        <SortHead label="Last seen" sortKey="seen" sort={sort} onSort={onSort} />
                        <SortHead
                          label="Score"
                          sortKey="score"
                          sort={sort}
                          onSort={onSort}
                          className="text-right"
                        />
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {sortedFindings.map((finding) => {
                        const href = findingHref(finding);
                        const selected = values.selected === finding.id;
                        return (
                          // The row itself is the control: focusable and
                          // activated with Enter/Space, with the title link
                          // kept for middle-click and copy-link.
                          <TableRow
                            key={finding.id}
                            tabIndex={0}
                            aria-selected={selected}
                            data-state={selected ? "selected" : undefined}
                            className="cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/60"
                            onClick={() => openFinding(finding)}
                            onKeyDown={(e) => {
                              if (e.key !== "Enter" && e.key !== " ") return;
                              e.preventDefault();
                              openFinding(finding);
                            }}
                          >
                            <TableCell className="max-w-[24ch] lg:max-w-[30ch] 2xl:max-w-[40ch]">
                              <Link
                                to={href}
                                title={finding.title}
                                className="block truncate font-medium text-primary hover:underline"
                                onClick={(e) => e.stopPropagation()}
                              >
                                {finding.title}
                              </Link>
                              {finding.filePath && (
                                <div
                                  className="mt-0.5 truncate font-mono text-[11.5px] text-muted-foreground"
                                  title={finding.filePath}
                                >
                                  {finding.filePath}
                                  {finding.startLine > 0 && `:${finding.startLine}`}
                                </div>
                              )}
                            </TableCell>
                            <TableCell>
                              <SeverityBadge severity={finding.severity} />
                            </TableCell>
                            <TableCell
                              className="hidden max-w-[16ch] truncate text-muted-foreground 2xl:table-cell"
                              title={finding.category}
                            >
                              {finding.category || <EmptyCell meaning="No category" />}
                            </TableCell>
                            <TableCell>
                              <span
                                className={cn(
                                  STATUS_PILL,
                                  "bg-muted/70 capitalize tracking-tight text-muted-foreground",
                                )}
                              >
                                {statusLabel(finding.status)}
                              </span>
                            </TableCell>
                            {showRunColumn && (
                              <TableCell className="hidden lg:table-cell">
                                <Link
                                  to={`/security/${finding.namespace}/${findingRunName(finding)}`}
                                  className="block max-w-[18ch] truncate font-mono text-[11.5px] text-primary hover:underline"
                                  title={findingRunName(finding)}
                                  onClick={(e) => e.stopPropagation()}
                                >
                                  {findingRunName(finding)}
                                </Link>
                              </TableCell>
                            )}
                            <TableCell className="text-muted-foreground">
                              <TimeAgo ms={timestampMs(finding.lastSeenAt)} now={now} />
                            </TableCell>
                            <TableCell className="text-right font-mono tabular-nums">
                              {finding.score.toFixed(1)}
                            </TableCell>
                          </TableRow>
                        );
                      })}
                    </TableBody>
                  </Table>
                  {hasMoreFindings && (
                    // The count lives in the filter bar; here there is only the
                    // one thing left to do about it.
                    <div className="mt-3 flex justify-center">
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={loadingMore}
                        onClick={() => void loadMoreFindings()}
                      >
                        {loadingMore ? "Loading…" : "Load more"}
                      </Button>
                    </div>
                  )}
                </>
              </ListState>
            </div>
          )}
        </DetailSection>

        <div className="grid items-start gap-7 md:grid-cols-2 xl:grid-cols-1">
          <DetailSection title="Configuration">
            {/* A narrow rail cannot afford a 160px label column: labels get
                just enough room so the values (URLs, cron) keep theirs. */}
            <FactList className="sm:grid-cols-[minmax(0,76px)_minmax(0,1fr)]">
              <Fact
                label="Target"
                value={
                  target ? (
                    <span className="flex min-w-0 items-center gap-1">
                      <a
                        href={target}
                        target="_blank"
                        rel="noopener noreferrer"
                        title={target}
                        className="min-w-0 truncate font-mono text-[12.5px] text-primary hover:underline"
                      >
                        {target}
                      </a>
                      <CopyButton value={target} label={repoUrl ? "repository URL" : "target URL"} />
                    </span>
                  ) : (
                    ""
                  )
                }
              />
              <Fact
                label="Schedule"
                value={
                  <span className="flex min-w-0 items-center gap-1">
                    <span className="min-w-0 truncate font-mono text-[12.5px]" title={schedule}>
                      {schedule}
                    </span>
                    {cron && <CopyButton value={cron} label="schedule" />}
                  </span>
                }
              />
              <Fact
                label="Next run"
                value={
                  suspended ? (
                    <span className="text-muted-foreground">Paused</span>
                  ) : (
                    <TimeAgo ms={nextScanMs} now={now} />
                  )
                }
              />
              <Fact label="Last run" value={<TimeAgo ms={lastScanMs} now={now} />} />
              <Fact label="Phase" value={config.phase || ""} />
            </FactList>
          </DetailSection>

          <DetailSection
            title="Runs"
            aside={
              runs.length > 0 ? <span className="tabular-nums">{runs.length} total</span> : undefined
            }
          >
            {runs.length === 0 ? (
              <p className="text-[12.5px] text-muted-foreground">
                This configuration has not run yet.
              </p>
            ) : (
              <ul className="space-y-2">
                {runs.slice(0, RUNS_PREVIEW).map((run) => (
                  <li
                    key={`${run.namespace}/${run.runName}`}
                    className="rounded-lg border border-border/60 px-2.5 py-2"
                  >
                    <div className="flex items-center justify-between gap-2">
                      <Link
                        to={`/security/${run.namespace}/${run.runName}`}
                        title={run.runName}
                        className="min-w-0 truncate font-mono text-[12px] text-primary hover:underline"
                      >
                        {run.runName}
                      </Link>
                      <TimeAgo
                        ms={runTimeMs(run)}
                        now={now}
                        className="shrink-0 text-[11.5px] text-muted-foreground"
                      />
                    </div>
                    <div className="mt-1.5 flex flex-wrap items-center gap-2">
                      <span
                        className={cn(
                          STATUS_PILL,
                          "capitalize tracking-tight",
                          toneSoft[runStatusTone(run.status)],
                        )}
                      >
                        {run.status || "unknown"}
                      </span>
                      <SeverityCountBadges counts={run.counts} />
                      <Link
                        to={`/runs/${run.namespace}/${run.runName}`}
                        aria-label={`View agent run ${run.runName}`}
                        className="ml-auto inline-flex shrink-0 items-center gap-1 text-[11.5px] text-primary hover:underline"
                      >
                        <SquareArrowOutUpRight className="size-3" aria-hidden />
                        Agent run
                      </Link>
                    </div>
                  </li>
                ))}
              </ul>
            )}
            <Link
              to={`/security/runs?q=${encodeURIComponent(config.name)}`}
              className="mt-3 inline-block text-[12px] text-primary hover:underline"
            >
              View all runs for this configuration
            </Link>
          </DetailSection>
        </div>
      </div>
    </div>
  );
}
