/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { clone, create } from "@bufbuild/protobuf";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { FilterX, Pause, Pencil, Play, SquareArrowOutUpRight } from "lucide-react";

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
  SEVERITIES, SeverityBadge, SeverityCountBadges, severityTone,
} from "@/components/SecurityScanList";
import { FINDING_STATUSES, formatSeen, statusLabel } from "@/components/SecurityScanDetail";
import {
  SecurityScanFormDialog, scanConfigUsesSavedCredentials,
} from "@/components/SecurityScanFormDialog";
import { useUrlFilters } from "@/hooks/useUrlFilters";
import { SEVERITY_ORDER, optionsFrom, repoLabel } from "@/lib/securityFilters";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";
import { formatAge, formatScheduleTime } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import {
  SecurityScanConfigSpecSchema,
  UpdateSecurityScanRequestSchema,
  type SecurityFinding,
  type SecurityScan,
  type SecurityScanConfig,
} from "@/rpc/platform/service_pb";

const filterInputClass =
  "h-7 w-32 rounded-lg border border-input bg-background px-2 text-[12px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 dark:bg-input/30";

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

function runTimeUnix(run: SecurityScan): bigint {
  const ts: Timestamp | undefined = run.completedAt ?? run.startedAt;
  if (!ts) return 0n;
  return BigInt(Math.floor(timestampDate(ts).getTime() / 1000));
}

function errorMessage(e: unknown, fallback: string): string {
  return e instanceof Error ? e.message : fallback;
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
  const schedule = config.spec?.schedule || "Runs once";
  const filtersActive = activeCount(["selected"]);
  const totalFindings = summary["total"] ?? 0;
  const scope = filtersActive > 0 ? "matching findings" : "findings";
  const loadedLabel = filtersActive === 0 && totalFindings > findings.length
    ? `Showing ${findings.length} of ${totalFindings} findings`
    : hasMoreFindings
      ? `Showing the first ${findings.length} ${scope}`
      : `Showing all ${findings.length} ${scope}`;

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
            <OwnerAvatar owner={config.owner} />
          </>
        }
        subtitle={
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-[12.5px] text-muted-foreground">
            <span className="max-w-[44ch] truncate font-mono" title={target || undefined}>
              {targetText || "—"}
            </span>
            <span aria-hidden className="text-muted-foreground/40">·</span>
            <span className="truncate font-mono" title={`Schedule: ${schedule}`}>
              {schedule}
            </span>
            <span aria-hidden className="text-muted-foreground/40">·</span>
            <span className="truncate font-mono" title={`Namespace: ${config.namespace}`}>
              {config.namespace}
            </span>
          </div>
        }
        actions={
          <div
            role="group"
            aria-label="Configuration actions"
            className="flex flex-wrap items-center gap-2"
          >
            {!suspended && (
              <Button
                variant="outline"
                size="sm"
                disabled={runNowPending}
                onClick={() => void handleRunNow()}
              >
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
            <Button
              variant="outline"
              size="sm"
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

      <StatBar>
        <Stat label="Total" value={totalFindings} />
        <Stat label="Actionable" value={summary["actionable"] ?? summary["open"] ?? 0} />
        {SEVERITIES.map((s) => (
          <Stat
            key={s}
            label={s}
            value={
              <span className={cn(toneSoft[severityTone(s)], "rounded-md px-2 py-0.5")}>
                {summary[`actionable_${s}`] ?? summary[s] ?? 0}
              </span>
            }
            mono={false}
          />
        ))}
      </StatBar>

      <div className="grid items-start gap-7 lg:grid-cols-[minmax(0,1fr)_minmax(240px,300px)]">
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
              resultLabel={`${visibleFindings.length} of ${findings.length} ${findings.length === 1 ? "finding" : "findings"}`}
            >
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
                aria-label="Filter by file path"
                type="text"
                value={values.file}
                onChange={(e) => set("file", e.target.value)}
                placeholder="File path…"
                className={filterInputClass}
              />
              <input
                aria-label="Filter by assignee"
                type="text"
                value={assignee}
                onChange={(e) => set("assignee", e.target.value)}
                placeholder="Assignee…"
                className={filterInputClass}
              />
            </FilterBar>
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
                        <TableHead>Title</TableHead>
                        <TableHead>Severity</TableHead>
                        <TableHead>Category</TableHead>
                        <TableHead>Status</TableHead>
                        <TableHead>Run</TableHead>
                        <TableHead className="hidden lg:table-cell">Last Seen</TableHead>
                        <TableHead className="text-right">Score</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {visibleFindings.map((finding) => {
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
                            <TableCell className="max-w-[44ch]">
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
                            <TableCell className="max-w-[16ch] truncate text-muted-foreground" title={finding.category}>
                              {finding.category || "—"}
                            </TableCell>
                            <TableCell className="capitalize text-muted-foreground">
                              {statusLabel(finding.status)}
                            </TableCell>
                            <TableCell>
                              <Link
                                to={`/security/${finding.namespace}/${findingRunName(finding)}`}
                                className="block max-w-[20ch] truncate font-mono text-[11.5px] text-primary hover:underline"
                                title={findingRunName(finding)}
                                onClick={(e) => e.stopPropagation()}
                              >
                                {findingRunName(finding)}
                              </Link>
                            </TableCell>
                            <TableCell className="hidden text-muted-foreground lg:table-cell">
                              {formatSeen(finding.lastSeenAt)}
                            </TableCell>
                            <TableCell className="text-right font-mono tabular-nums">
                              {finding.score.toFixed(1)}
                            </TableCell>
                          </TableRow>
                        );
                      })}
                    </TableBody>
                  </Table>
                  <div className="mt-3 flex flex-wrap items-center justify-center gap-3">
                    <span className="text-[12px] tabular-nums text-muted-foreground">
                      {loadedLabel}
                    </span>
                    {hasMoreFindings && (
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={loadingMore}
                        onClick={() => void loadMoreFindings()}
                      >
                        {loadingMore ? "Loading…" : "Load more"}
                      </Button>
                    )}
                  </div>
                </>
              </ListState>
            </div>
          )}
        </DetailSection>

        <div className="space-y-7">
          <DetailSection title="Configuration">
            <FactList>
              <Fact label="Schedule" mono value={schedule} />
              <Fact label="Phase" value={config.phase || "—"} />
              <Fact label="Last Scan" value={formatScheduleTime(config.lastScanTimeUnix, now)} />
              {config.nextScheduleTimeUnix > 0n && (
                <Fact
                  label="Next Scan"
                  value={formatScheduleTime(config.nextScheduleTimeUnix, now)}
                />
              )}
              <Fact label="Target" value={target || ""} />
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
                      <span className="shrink-0 text-[11.5px] tabular-nums text-muted-foreground">
                        {formatAge(runTimeUnix(run), now)}
                      </span>
                    </div>
                    <div className="mt-1.5 flex flex-wrap items-center gap-2">
                      <Badge
                        variant="outline"
                        className={cn(
                          "capitalize border-transparent",
                          toneSoft[runStatusTone(run.status)],
                        )}
                      >
                        {run.status || "unknown"}
                      </Badge>
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
