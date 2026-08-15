/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { Loader2, Settings2, ShieldAlert, ShieldCheck } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toaster";
import { ListRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { FilterBar, FilterSelect } from "@/components/ui/filter-bar";
import { DetailErrorState, classifyDetailError } from "@/components/ui/detail-state";
import { ResourceListPage } from "@/components/list-page";
import { DetailSection } from "@/components/detail-page";
import { SecurityConfigPostures } from "@/components/SecurityConfigPostures";
import { SecurityNav } from "@/components/SecurityNav";
import { SeverityCountBadges } from "@/components/SecurityScanList";
import { BaselineBadge, formatDurationSeconds } from "@/components/security-baseline";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneColor, toneSoft, toneText, type StatusTone } from "@/lib/status";
import { formatAge } from "@/lib/format";
import {
  SEVERITY_ORDER,
  TIME_RANGE_OPTIONS,
  optionsFrom,
  repoLabel,
  severityCountTotal,
  timestampMs,
  withinTimeRange,
} from "@/lib/securityFilters";
import { useNow } from "@/hooks/useNow";
import { useUrlFilters } from "@/hooks/useUrlFilters";
import type {
  GetSecurityOverviewResponse,
  SecurityScan,
  SecurityScanConfigIssue,
  SecuritySkillsStatus,
} from "@/rpc/platform/service_pb";

/** The lens the whole dashboard is read through; lives in the URL so a
 *  narrowed view is shareable and survives a reload. */
const FILTER_SPEC = { q: "", range: "all", repo: "all" } as const;

/** The severities that earn a headline tile; the rest roll into the total. */
const SEVERITY_TILES = SEVERITY_ORDER.slice(0, 3);

const SEVERITY_TONE: Record<string, StatusTone> = {
  critical: "danger",
  high: "warning",
  medium: "info",
};

function scanTone(status: string): StatusTone {
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

/** Completion wins over start: a finished run is "last scanned" when it ended. */
function scanMs(scan: SecurityScan): number {
  return timestampMs(scan.completedAt ?? scan.startedAt);
}

/**
 * Per-severity actionable count. The server has used three key shapes over
 * time (`actionable_<sev>`, the older `open_<sev>`, and a bare `<sev>`); the
 * shared helpers only know the first and last, so the legacy key is resolved
 * here rather than duplicating the whole vocabulary.
 */
function severityValue(counts: Record<string, number>, severity: string): number {
  return counts[`actionable_${severity}`] ?? counts[`open_${severity}`] ?? counts[severity] ?? 0;
}

function actionableTotal(counts: Record<string, number>): number {
  return counts["actionable"] ?? counts["open"] ?? severityCountTotal(counts);
}

/** Build a link into the scan-run list, dropping default/empty parameters. */
function runsHref(params: Record<string, string>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value && value !== "all") search.set(key, value);
  }
  const query = search.toString();
  return `/security/runs${query ? `?${query}` : ""}`;
}

/**
 * PostureTile is one figure of the headline strip: a small label over a large
 * tabular number, and always a link into the view that explains the number —
 * no metric on this page is a dead end. A tone paints an inset accent along
 * the top edge (and the number itself) so critical/high counts read at a
 * glance; zero counts stay quiet even when a tone is set.
 */
function PostureTile({
  label,
  value,
  to,
  hint,
  tone,
  live,
}: {
  label: string;
  value: number;
  to: string;
  /** One line of context under the number (what the link leads to). */
  hint?: string;
  tone?: StatusTone;
  /** Announce changes to this number (used for in-flight scan activity). */
  live?: boolean;
}) {
  const active = tone !== undefined && value > 0;
  return (
    <Link
      to={to}
      aria-label={`${label}: ${value}`}
      className="rounded-lg border border-border/70 bg-muted/20 px-3 py-2.5 transition-colors hover:border-border hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
      style={active ? { boxShadow: `inset 0 2px 0 0 ${toneColor[tone]}` } : undefined}
    >
      <p className="truncate text-[11.5px] text-muted-foreground">{label}</p>
      <p
        aria-live={live ? "polite" : undefined}
        className={cn(
          "mt-0.5 font-mono text-xl font-semibold leading-tight tabular-nums",
          active ? toneText[tone] : "text-foreground",
        )}
      >
        {value}
      </p>
      {hint && <p className="mt-0.5 truncate text-[11px] text-muted-foreground/70">{hint}</p>}
    </Link>
  );
}

function ScanTable({
  caption,
  scans,
  now,
  timeLabel,
}: {
  caption: string;
  scans: SecurityScan[];
  now: number;
  timeLabel: string;
}) {
  return (
    <Table>
      <TableCaption className="sr-only">{caption}</TableCaption>
      <TableHeader>
        <TableRow>
          <TableHead>Scan run</TableHead>
          <TableHead>Configuration</TableHead>
          <TableHead>Repository</TableHead>
          <TableHead>Status</TableHead>
          <TableHead>Findings</TableHead>
          <TableHead className="text-right">{timeLabel}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {scans.map((scan) => (
          <TableRow key={`${scan.namespace}/${scan.runName}`}>
            <TableCell>
              <Link
                to={`/security/${scan.namespace}/${scan.runName}`}
                className="font-medium text-primary hover:underline"
              >
                {scan.runName}
              </Link>
            </TableCell>
            <TableCell className="text-sm text-muted-foreground">{scan.scanName || "—"}</TableCell>
            <TableCell className="font-mono text-sm text-muted-foreground">
              {repoLabel(scan.repository) || "—"}
            </TableCell>
            <TableCell>
              <Badge
                variant="outline"
                className={cn("capitalize border-transparent", toneSoft[scanTone(scan.status)])}
              >
                {scan.status || "unknown"}
              </Badge>
            </TableCell>
            <TableCell>
              <SeverityCountBadges counts={scan.counts} />
            </TableCell>
            <TableCell className="text-right tabular-nums text-muted-foreground">
              {formatAge(BigInt(Math.floor(scanMs(scan) / 1000)), now)}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function ConfigIssueRow({ issue }: { issue: SecurityScanConfigIssue }) {
  return (
    <li className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg border border-border/60 px-3 py-2 text-[12.5px]">
      <ShieldAlert className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
      <Link
        to={`/security/configs/${issue.namespace}/${issue.name}`}
        className="font-medium text-primary hover:underline"
      >
        {issue.name}
      </Link>
      <Badge
        variant="outline"
        className={cn("border-transparent capitalize", toneSoft[issue.suspended ? "neutral" : "danger"])}
      >
        {issue.readyReason || issue.phase || "NotReady"}
      </Badge>
      {issue.message && (
        <span className="min-w-0 flex-1 basis-64 truncate text-muted-foreground" title={issue.message}>
          {issue.message}
        </span>
      )}
    </li>
  );
}

export function SecurityOverview() {
  const [overview, setOverview] = useState<GetSecurityOverviewResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [skillsStatus, setSkillsStatus] = useState<SecuritySkillsStatus | null>(null);
  const [skillsLoading, setSkillsLoading] = useState(true);
  const [skillsError, setSkillsError] = useState("");
  const [skillsInstalling, setSkillsInstalling] = useState(false);
  const now = useNow();
  const { values, set, reset, activeCount } = useUrlFilters(FILTER_SPEC);

  const fetchOverview = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const resp = await client.getSecurityOverview({ namespace: "" });
      setOverview(resp);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to load the security overview");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchOverview();
  }, [fetchOverview]);

  const fetchSkillsStatus = useCallback(async () => {
    setSkillsLoading(true);
    setSkillsError("");
    try {
      setSkillsStatus(await client.getSecuritySkillsStatus({}));
    } catch (e: unknown) {
      setSkillsError(e instanceof Error ? e.message : "Security skill status is unavailable");
    } finally {
      setSkillsLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchSkillsStatus();
  }, [fetchSkillsStatus]);

  async function installSecuritySkills() {
    setSkillsInstalling(true);
    try {
      setSkillsStatus(await client.installSecuritySkills({}));
      setSkillsError("");
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : "Failed to install security skills");
    } finally {
      setSkillsInstalling(false);
    }
  }

  const activeScans = useMemo(() => overview?.activeScans ?? [], [overview]);
  const recentScans = useMemo(() => overview?.recentScans ?? [], [overview]);

  const repoOptions = useMemo(
    () =>
      optionsFrom(
        [...activeScans, ...recentScans].map((scan) => repoLabel(scan.repository)),
        "All repositories",
      ),
    [activeScans, recentScans],
  );

  const matchesLens = useCallback(
    (scan: SecurityScan) => values.repo === "all" || repoLabel(scan.repository) === values.repo,
    [values.repo],
  );

  const searched = useCallback(
    (scans: SecurityScan[]) =>
      filterByQuery(scans, values.q, (scan) => [
        scan.runName,
        scan.scanName,
        scan.namespace,
        scan.repository,
        scan.status,
      ]),
    [values.q],
  );

  // The repository lens applies to in-flight runs too; the time range does
  // not — an active scan is, by definition, happening now.
  const visibleActive = useMemo(
    () => searched(activeScans.filter(matchesLens)),
    [activeScans, matchesLens, searched],
  );
  const visibleRecent = useMemo(
    () =>
      searched(
        recentScans.filter(
          (scan) => matchesLens(scan) && withinTimeRange(scanMs(scan), values.range, now),
        ),
      ),
    [recentScans, matchesLens, searched, values.range, now],
  );

  const counts = overview?.findingCounts ?? {};
  const lens = { repo: values.repo, range: values.range };
  const latestRun = recentScans[0];
  // Without baseline data the "new" view has nothing to show, so the tile
  // falls back to the run it would have compared against.
  const baselineHref = latestRun
    ? `/security/${latestRun.namespace}/${latestRun.runName}${overview?.baselineAvailable ? "?baseline=new" : ""}`
    : runsHref(lens);

  const filtersActive = activeCount() > 0;
  const hasScans = activeScans.length > 0 || recentScans.length > 0;
  // First run: nothing has ever been configured or scanned. A filter that
  // hides every row must never look like an empty account.
  const firstRun =
    !!overview
    && !hasScans
    && overview.configCount === 0
    && overview.configIssues.length === 0;
  const failedHard = !overview && !!error;

  return (
    <ResourceListPage
      title="Security"
      description="Repository security posture: scan activity, actionable findings, and configurations that need attention."
      query={values.q}
      onQuery={(next) => set("q", next)}
      searchPlaceholder="Search scans, configurations, repositories…"
      nav={<SecurityNav />}
      toolbar={
        overview && hasScans ? (
          <FilterBar
            label="Security overview filters"
            activeCount={activeCount(["q"])}
            onClear={() => reset()}
            resultLabel={`${visibleRecent.length} of ${recentScans.length} recent scans`}
          >
            <FilterSelect
              label="Time range"
              value={values.range}
              onChange={(next) => set("range", next)}
              options={TIME_RANGE_OPTIONS}
            />
            <FilterSelect
              label="Repository"
              value={values.repo}
              onChange={(next) => set("repo", next)}
              options={repoOptions}
            />
          </FilterBar>
        ) : undefined
      }
      actions={
        <>
          <Badge
            variant="outline"
            title={skillsError || undefined}
            className={cn(
              "border-transparent",
              skillsStatus?.state === "installed" && toneSoft.success,
              skillsStatus?.state === "partially_installed" && toneSoft.warning,
            )}
          >
            <span role="status" aria-live="polite">
              {skillsLoading
                ? "Security skills · Checking…"
                : skillsError
                  ? "Security skills · Status unavailable"
                  : skillsStatus?.state === "installed"
                    ? "Security skills · Installed"
                    : skillsStatus?.state === "partially_installed"
                      ? `Security skills · ${skillsStatus.installedCount} of ${skillsStatus.availableCount} installed${skillsStatus.conflictCount > 0 ? ` · ${skillsStatus.conflictCount} conflict${skillsStatus.conflictCount === 1 ? "" : "s"}` : ""}`
                      : skillsStatus?.state === "unavailable"
                        ? "Security skills · Unavailable"
                        : "Security skills · Not installed"}
            </span>
          </Badge>
          {skillsError ? (
            <Button size="sm" variant="outline" onClick={() => void fetchSkillsStatus()}>
              Retry
            </Button>
          ) : skillsStatus
            && skillsStatus.state !== "installed"
            && skillsStatus.state !== "unavailable"
            && skillsStatus.installedCount + skillsStatus.conflictCount < skillsStatus.availableCount ? (
            <Button size="sm" disabled={skillsInstalling} onClick={() => void installSecuritySkills()}>
              {skillsInstalling && <Loader2 className="animate-spin" />}
              {skillsInstalling ? "Installing…" : "Install security skills"}
            </Button>
          ) : null}
        </>
      }
      loading={loading && !overview}
      // A failed refresh keeps the last good dashboard on screen; only a cold
      // failure replaces it (with DetailErrorState below).
      error={overview ? error : ""}
      onRetry={() => void fetchOverview()}
      empty={firstRun && !error}
      skeleton={<ListRowSkeleton rows={5} />}
      emptyIcon={<ShieldCheck className="size-6" />}
      emptyTitle="No security scans yet"
      emptyDescription="A security scan sends an agent through a repository to hunt for vulnerabilities — injection, authentication and access-control gaps, leaked secrets, risky dependencies — and reports what it finds as triageable findings. Create a scan configuration to run one once or on a schedule."
      emptyAction={
        <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/security/configs" />}>
          <Settings2 />
          Configure a scan
        </Button>
      }
    >
      {failedHard ? (
        <DetailErrorState
          kind={classifyDetailError(error)}
          title="Couldn't load the security overview"
          detail={error}
          onRetry={() => void fetchOverview()}
          links={[
            { to: "/security/configs", label: "Scan configurations" },
            { to: "/security/runs", label: "All scan runs" },
          ]}
        />
      ) : overview ? (
        <div className="space-y-7">
          {!overview.storeSupported && (
            <div className="rounded-lg border border-border/70 bg-muted/30 px-3 py-2 text-[12.5px] text-muted-foreground">
              Scan results and findings are not persisted: the configured state store does not support
              security findings (Postgres is required). Scan configurations below still work.
            </div>
          )}
          {overview.warnings.length > 0 && (
            <div
              role="alert"
              className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-[12.5px]"
            >
              <span className="font-medium">Partial data — some sources failed to load.</span>
              <ul className="mt-1 list-disc pl-5 text-muted-foreground">
                {overview.warnings.map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            </div>
          )}

          {overview.storeSupported && (
            <section aria-labelledby="security-posture-heading" data-testid="security-posture">
              <h2 id="security-posture-heading" className="sr-only">
                Security posture
              </h2>
              <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-4">
                {SEVERITY_TILES.map((severity) => (
                  <PostureTile
                    key={severity}
                    label={`${severity[0].toUpperCase()}${severity.slice(1)}`}
                    value={severityValue(counts, severity)}
                    tone={SEVERITY_TONE[severity]}
                    hint={`${severity} findings`}
                    to={runsHref({ ...lens, severity })}
                  />
                ))}
                <PostureTile
                  label="Actionable findings"
                  value={actionableTotal(counts)}
                  hint="across every severity"
                  to={runsHref(lens)}
                />
                <PostureTile
                  label="Total findings"
                  value={counts["total"] ?? 0}
                  hint="including triaged and resolved"
                  to={runsHref(lens)}
                />
                <PostureTile
                  label="Active scans"
                  value={visibleActive.length}
                  tone={visibleActive.length > 0 ? "running" : undefined}
                  hint="running right now"
                  live
                  to={runsHref({ ...lens, status: "running" })}
                />
                <PostureTile
                  label="Configs needing attention"
                  value={overview.configIssues.length}
                  tone={overview.configIssues.length > 0 ? "danger" : undefined}
                  hint={`of ${overview.configCount} configuration${overview.configCount === 1 ? "" : "s"}`}
                  to="/security/configs?status=attention"
                />
                <PostureTile
                  label="New since baseline"
                  value={overview.baselineAvailable ? overview.newFindings : 0}
                  tone={overview.baselineAvailable && overview.newFindings > 0 ? "warning" : undefined}
                  hint={overview.baselineAvailable ? "vs. the last baseline" : "no baseline yet"}
                  to={baselineHref}
                />
              </div>
            </section>
          )}

          {overview.storeSupported && (
            <div className="space-y-2">
              {overview.baselineAvailable ? (
                <div className="flex flex-wrap items-center gap-2 text-[12.5px]" aria-label="Baseline changes">
                  <span className="text-muted-foreground">Since the last baseline:</span>
                  {(
                    [
                      ["new", overview.newFindings],
                      ["recurring", overview.recurringFindings],
                      ["regressed", overview.regressedFindings],
                      ["reopened", overview.reopenedFindings],
                      ["resolved", overview.resolvedFindings],
                    ] as const
                  ).map(([state, count]) => {
                    const chip = (
                      <span className="inline-flex items-center gap-1">
                        <BaselineBadge state={state} />
                        <span className="font-medium tabular-nums">{count}</span>
                      </span>
                    );
                    return latestRun ? (
                      <Link
                        key={state}
                        to={`/security/${latestRun.namespace}/${latestRun.runName}?baseline=${state}`}
                        className="hover:opacity-80"
                        aria-label={`View ${state} findings`}
                      >
                        {chip}
                      </Link>
                    ) : (
                      <span key={state}>{chip}</span>
                    );
                  })}
                </div>
              ) : (
                <p className="text-[12px] text-muted-foreground/70">
                  New, recurring, and resolved finding counts appear here once baseline comparisons are available.
                </p>
              )}
            </div>
          )}

          {activeScans.length > 0 && (
            <DetailSection
              title="Active scans"
              description="Scan runs that are still in progress."
            >
              {visibleActive.length === 0 ? (
                <p className="text-[12.5px] text-muted-foreground">
                  {activeScans.length} active scan{activeScans.length === 1 ? " is" : "s are"} running
                  outside the current filters.
                </p>
              ) : (
                <ScanTable
                  caption="Active security scans"
                  scans={visibleActive}
                  now={now}
                  timeLabel="Started"
                />
              )}
            </DetailSection>
          )}

          {overview.storeSupported && (
            <DetailSection
              title="Recent scans"
              description="The newest completed scan runs."
              aside={
                <Link to={runsHref(lens)} className="text-primary hover:underline">
                  View all runs
                </Link>
              }
            >
              {recentScans.length === 0 ? (
                <p className="text-[12.5px] text-muted-foreground">No completed scan runs yet.</p>
              ) : visibleRecent.length === 0 ? (
                <div className="flex flex-wrap items-center gap-2 text-[12.5px] text-muted-foreground">
                  <span>No recent scans match the current filters.</span>
                  {filtersActive && (
                    <Button size="sm" variant="outline" onClick={() => reset()}>
                      Clear filters
                    </Button>
                  )}
                </div>
              ) : (
                <ScanTable
                  caption="Recent security scans"
                  scans={visibleRecent}
                  now={now}
                  timeLabel="Completed"
                />
              )}
              {overview.trends
                && (overview.trends.triagedCount > 0 || overview.trends.resolvedCount > 0) && (
                <p className="mt-3 text-[12px] text-muted-foreground" aria-label="Triage trends">
                  Time to triage: median {formatDurationSeconds(overview.trends.medianTimeToTriageSeconds)}
                  {" "}(avg {formatDurationSeconds(overview.trends.avgTimeToTriageSeconds)}, {overview.trends.triagedCount} triaged)
                  {" · "}Time to resolution: median {formatDurationSeconds(overview.trends.medianTimeToResolutionSeconds)}
                  {" "}(avg {formatDurationSeconds(overview.trends.avgTimeToResolutionSeconds)}, {overview.trends.resolvedCount} resolved)
                  {filtersActive && " · server-wide, not narrowed by the filters above"}
                </p>
              )}
            </DetailSection>
          )}

          {overview.storeSupported && <SecurityConfigPostures />}

          {(overview.configIssues.length > 0 || overview.configCount > 0) && (
            <DetailSection
              title="Issues needing attention"
              description="Scan configurations that are failing, blocked, or suspended."
              aside={
                <Link to="/security/configs?status=attention" className="text-primary hover:underline">
                  Manage configurations
                </Link>
              }
            >
              {overview.configIssues.length === 0 ? (
                <p className="text-[12.5px] text-muted-foreground">
                  All scan configurations are healthy.
                </p>
              ) : (
                <ul className="space-y-2">
                  {overview.configIssues.map((issue) => (
                    <ConfigIssueRow key={`${issue.namespace}/${issue.name}`} issue={issue} />
                  ))}
                </ul>
              )}
            </DetailSection>
          )}
        </div>
      ) : null}
    </ResourceListPage>
  );
}
