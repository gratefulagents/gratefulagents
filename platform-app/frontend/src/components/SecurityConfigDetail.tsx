/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Pencil, Play, SquareArrowOutUpRight } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import { ListSearchInput } from "@/components/ui/list-search";
import {
  DetailHeader, DetailSection, StatBar, Stat, FactList, Fact,
} from "@/components/detail-page";
import { ReadyBadge } from "@/components/ReadyBadge";
import {
  SEVERITIES, SeverityBadge, SeverityCountBadges, severityTone,
} from "@/components/SecurityScanList";
import { FINDING_STATUSES, formatSeen, statusLabel } from "@/components/SecurityScanDetail";
import { SecurityScanFormDialog } from "@/components/SecurityScanFormDialog";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";
import { formatAge, formatScheduleTime } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import type {
  SecurityFinding,
  SecurityScan,
  SecurityScanConfig,
} from "@/rpc/platform/service_pb";

const filterSelectClass =
  "h-8 rounded-md border border-border/70 bg-background px-2 text-[12.5px] text-foreground capitalize focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60";

// The findings store caps an omitted limit at 200 rows; page explicitly so a
// configuration with more findings than one page can still show all of them.
export const FINDINGS_PAGE_SIZE = 200;

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

/**
 * Configuration-level security view: every deduplicated finding the scan has
 * ever reported (across all of its runs), plus the run history — so a single
 * click from the configurations list shows the whole picture without visiting
 * each run individually.
 */
export function SecurityConfigDetail() {
  const { namespace, name } = useParams<{ namespace: string; name: string }>();
  const [searchParams, setSearchParams] = useSearchParams();

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
  const [actionError, setActionError] = useState<string | null>(null);
  const [runNowPending, setRunNowPending] = useState(false);
  const now = useNow();

  // Filters live in the URL so a shared link reproduces the same view.
  const severity = searchParams.get("severity") ?? "";
  const status = searchParams.get("status") ?? "actionable";
  const category = searchParams.get("category") ?? "";
  const search = searchParams.get("q") ?? "";

  const setFilter = useCallback(
    (key: "severity" | "status" | "category" | "q", value: string) => {
      setSearchParams(
        (params) => {
          const next = new URLSearchParams(params);
          if (value) next.set(key, value);
          else next.delete(key);
          return next;
        },
        { replace: true },
      );
    },
    [setSearchParams],
  );

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
        setError(e instanceof Error ? e.message : "Failed to load the scan configuration");
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
          severity,
          status: status === "all" ? "" : status,
          category,
          search,
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
    } catch (e: unknown) {
      if (!background) {
        setError(e instanceof Error ? e.message : "Failed to load security findings");
      }
    } finally {
      if (!background) setFindingsLoading(false);
    }
  }, [namespace, name, severity, status, category, search]);

  useEffect(() => {
    void fetchConfig();
  }, [fetchConfig]);

  // Filter changes restart from the first page (fetchFindings is recreated).
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

  const categories = useMemo(() => {
    const set = new Set<string>();
    for (const finding of findings) {
      if (finding.category) set.add(finding.category);
    }
    if (category) set.add(category);
    return [...set].sort();
  }, [findings, category]);

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

  async function handleRunNow() {
    if (!namespace || !name) return;
    setActionError(null);
    setRunNowPending(true);
    try {
      await client.runSecurityScanNow({ namespace, name });
      await fetchConfig(true);
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to start the security scan");
    } finally {
      setRunNowPending(false);
    }
  }

  return (
    <ListState
      loading={loading && !config}
      error={error && !config ? error : ""}
      empty={!config}
      skeleton={<ListRowSkeleton rows={4} />}
      emptyTitle="Scan configuration not found"
      emptyDescription="This scan configuration may have been removed or you may not have access."
    >
      {config && (
        <div className="space-y-7">
          <DetailHeader
            parentLabel="Scan Configurations"
            parentTo="/security/configs"
            title={config.name}
            meta={
              config.spec?.suspend ? (
                <Badge variant="secondary">Suspended</Badge>
              ) : (
                <ReadyBadge status={config.conditionReady} />
              )
            }
            subtitle={
              <span className="font-mono text-[12.5px] text-muted-foreground">
                {config.spec?.repoUrl || "—"}
              </span>
            }
            actions={
              <>
                {!config.spec?.suspend && (
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
              </>
            }
          />

          {actionError && (
            <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]">
              {actionError}
            </div>
          )}

          <StatBar>
            <Stat label="Total" value={summary["total"] ?? 0} />
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

          <FactList>
            <Fact label="Schedule" mono value={config.spec?.schedule || "once"} />
            <Fact label="Phase" value={config.phase || "—"} />
            <Fact
              label="Last Scan"
              value={formatScheduleTime(config.lastScanTimeUnix, now)}
            />
            {config.nextScheduleTimeUnix > 0n && (
              <Fact
                label="Next Scan"
                value={formatScheduleTime(config.nextScheduleTimeUnix, now)}
              />
            )}
          </FactList>

          <DetailSection
            title="Findings"
            description="Every deduplicated finding this scan has reported, across all of its runs."
          >
            <div
              className="flex flex-wrap items-center gap-2 rounded-lg border border-border/60 bg-muted/20 px-2.5 py-2"
              role="group"
              aria-label="Finding filters"
            >
              <ListSearchInput
                value={search}
                onChange={(value) => setFilter("q", value)}
                placeholder="Search findings…"
              />
              <select
                aria-label="Filter by severity"
                className={filterSelectClass}
                value={severity}
                onChange={(e) => setFilter("severity", e.target.value)}
              >
                <option value="">All severities</option>
                {SEVERITIES.map((s) => (
                  <option key={s} value={s}>{s}</option>
                ))}
              </select>
              <select
                aria-label="Filter by status"
                className={filterSelectClass}
                value={status}
                onChange={(e) => setFilter("status", e.target.value)}
              >
                <option value="actionable">Actionable</option>
                <option value="all">All statuses</option>
                {FINDING_STATUSES.map((s) => (
                  <option key={s} value={s}>{statusLabel(s)}</option>
                ))}
              </select>
              <select
                aria-label="Filter by category"
                className={filterSelectClass}
                value={category}
                onChange={(e) => setFilter("category", e.target.value)}
              >
                <option value="">All categories</option>
                {categories.map((c) => (
                  <option key={c} value={c}>{c}</option>
                ))}
              </select>
            </div>
            <ListState
              loading={findingsLoading}
              empty={!findings.length}
              skeleton={<ListRowSkeleton rows={4} />}
              emptyTitle="No findings"
              emptyDescription={
                severity || status || category || search
                  ? "No findings match the current filters."
                  : "This scan has not reported any findings yet."
              }
            >
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
                    <TableHead>Last Seen</TableHead>
                    <TableHead className="text-right">Score</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {findings.map((finding) => (
                    <TableRow key={finding.id}>
                      <TableCell>
                        <Link
                          to={`/security/${finding.namespace}/${findingRunName(finding)}/findings/${finding.id}`}
                          className="font-medium text-primary hover:underline"
                        >
                          {finding.title}
                        </Link>
                        {finding.filePath && (
                          <div className="mt-0.5 truncate font-mono text-[11.5px] text-muted-foreground">
                            {finding.filePath}
                            {finding.startLine > 0 && `:${finding.startLine}`}
                          </div>
                        )}
                      </TableCell>
                      <TableCell>
                        <SeverityBadge severity={finding.severity} />
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {finding.category || "—"}
                      </TableCell>
                      <TableCell className="capitalize text-muted-foreground">
                        {statusLabel(finding.status)}
                      </TableCell>
                      <TableCell>
                        <Link
                          to={`/security/${finding.namespace}/${findingRunName(finding)}`}
                          className="font-mono text-[11.5px] text-primary hover:underline"
                        >
                          {findingRunName(finding)}
                        </Link>
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {formatSeen(finding.lastSeenAt)}
                      </TableCell>
                      <TableCell className="text-right font-mono tabular-nums">
                        {finding.score.toFixed(1)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              {hasMoreFindings && (
                <div className="mt-3 flex items-center justify-center gap-3">
                  <span className="text-[12px] text-muted-foreground tabular-nums">
                    Showing the first {findings.length} findings.
                  </span>
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
            </ListState>
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
              <Table>
                <TableCaption className="sr-only">Scan runs</TableCaption>
                <TableHeader>
                  <TableRow>
                    <TableHead>Run</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>Findings</TableHead>
                    <TableHead>Agent Run</TableHead>
                    <TableHead className="text-right">When</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {runs.map((run) => (
                    <TableRow key={`${run.namespace}/${run.runName}`}>
                      <TableCell>
                        <Link
                          to={`/security/${run.namespace}/${run.runName}`}
                          className="font-medium text-primary hover:underline"
                        >
                          {run.runName}
                        </Link>
                      </TableCell>
                      <TableCell>
                        <Badge
                          variant="outline"
                          className={cn(
                            "capitalize border-transparent",
                            toneSoft[runStatusTone(run.status)],
                          )}
                        >
                          {run.status || "unknown"}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <SeverityCountBadges counts={run.counts} />
                      </TableCell>
                      <TableCell>
                        <Link
                          to={`/runs/${run.namespace}/${run.runName}`}
                          className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
                          aria-label={`View agent run ${run.runName}`}
                        >
                          <SquareArrowOutUpRight className="size-3" aria-hidden />
                          View run
                        </Link>
                      </TableCell>
                      <TableCell className="text-right text-muted-foreground">
                        {formatAge(runTimeUnix(run), now)}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </DetailSection>
        </div>
      )}
    </ListState>
  );
}
