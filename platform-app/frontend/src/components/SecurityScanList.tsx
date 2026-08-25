/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  ArrowDown, ArrowUp, ArrowUpDown, FilterX, Settings2, ShieldAlert, SquareArrowOutUpRight,
} from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { FilterBar, FilterSelect, type FilterOption } from "@/components/ui/filter-bar";
import { ResourceListPage } from "@/components/list-page";
import { SecurityNav } from "@/components/SecurityNav";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";
import { formatAge } from "@/lib/format";
import {
  SCAN_STATUS_FILTER_OPTIONS, SEVERITY_FILTER_OPTIONS, SEVERITY_RANK, TIME_RANGE_OPTIONS,
  hasSeverityAtLeast, optionsFrom, repoLabel, severityCountTotal, timestampMs, topSeverity,
  withinTimeRange,
} from "@/lib/securityFilters";
import { useNow } from "@/hooks/useNow";
import { useUrlFilters } from "@/hooks/useUrlFilters";
import type { SecurityScan } from "@/rpc/platform/service_pb";

export const SEVERITIES = ["critical", "high", "medium", "low", "info"] as const;

const SEVERITY_TONES: Record<string, StatusTone> = {
  critical: "danger",
  high: "warning",
  medium: "purple",
  low: "info",
  info: "neutral",
};

export function severityTone(severity: string): StatusTone {
  return SEVERITY_TONES[severity.toLowerCase()] ?? "neutral";
}

/**
 * One pill geometry for every status-ish chip in a security row (severity,
 * scan status, suspended, ready) so a row reads as a single band of 20px
 * pills instead of three different heights. Meaning always lives in the text
 * label; the tone only reinforces it.
 */
export const STATUS_PILL =
  "inline-flex h-5 w-fit shrink-0 items-center gap-1 rounded-full px-2 "
  + "text-[11px] font-medium whitespace-nowrap";

/** Compact severity pill: "critical 3". */
export function SeverityBadge({
  severity,
  count,
  className,
}: {
  severity: string;
  count?: number;
  className?: string;
}) {
  return (
    <span
      className={cn(
        STATUS_PILL,
        "select-none capitalize tracking-tight",
        toneSoft[severityTone(severity)],
        className,
      )}
    >
      {severity}
      {count !== undefined && <span className="font-semibold tabular-nums">{count}</span>}
    </span>
  );
}

/** Severity count pills for a counts map; only non-zero severities render. */
export function SeverityCountBadges({ counts }: { counts: Record<string, number> }) {
  const countFor = (severity: string) => counts[`actionable_${severity}`] ?? counts[severity] ?? 0;
  const present = SEVERITIES.filter((severity) => countFor(severity) > 0);
  if (!present.length) return <EmptyCell meaning="No findings reported" />;
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {present.map((severity) => (
        <SeverityBadge key={severity} severity={severity} count={countFor(severity)} />
      ))}
    </span>
  );
}

/**
 * A bare "—" tells a sighted reader "nothing here" and tells a screen reader
 * nothing at all. Keep the dash as decoration and carry the meaning in text.
 */
export function EmptyCell({ meaning, className }: { meaning: string; className?: string }) {
  return (
    <span className={cn("text-sm text-muted-foreground", className)} title={meaning}>
      <span aria-hidden>—</span>
      <span className="sr-only">{meaning}</span>
    </span>
  );
}

function scanStatusTone(status: string): StatusTone {
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
function lastScanMs(scan: SecurityScan): number {
  return timestampMs(scan.completedAt ?? scan.startedAt);
}

const FILTER_SPEC = {
  q: "",
  status: "all",
  severity: "all",
  repo: "all",
  config: "all",
  range: "all",
  sort: "recent",
} as const;

/** Filter keys "Clear" resets; `sort` is a view preference, not a filter. */
const FILTER_KEYS = ["q", "status", "severity", "repo", "config", "range"] as const;

type SortValue = "recent" | "oldest" | "severity" | "least-severe";
type SortField = "scanned" | "severity";

const SORTS: Record<SortValue, { field: SortField; descending: boolean }> = {
  recent: { field: "scanned", descending: true },
  oldest: { field: "scanned", descending: false },
  severity: { field: "severity", descending: true },
  "least-severe": { field: "severity", descending: false },
};

const SORT_VALUES: Record<SortField, { descending: SortValue; ascending: SortValue }> = {
  scanned: { descending: "recent", ascending: "oldest" },
  severity: { descending: "severity", ascending: "least-severe" },
};

function parseSort(value: string): SortValue {
  return Object.hasOwn(SORTS, value) ? (value as SortValue) : "recent";
}

/** Worst severity first, then volume, so "10 lows" never outranks "1 critical". */
function severityWeight(scan: SecurityScan): number {
  const rank = SEVERITY_RANK[topSeverity(scan.counts)] ?? 0;
  return rank * 1_000_000 + Math.min(severityCountTotal(scan.counts), 999_999);
}

function compareScans(a: SecurityScan, b: SecurityScan, sort: SortValue): number {
  const { field, descending } = SORTS[sort];
  const [left, right] = field === "severity"
    ? [severityWeight(a), severityWeight(b)]
    : [lastScanMs(a), lastScanMs(b)];
  if (left !== right) return descending ? right - left : left - right;
  // Ties fall back to recency so polling cannot reshuffle equal rows.
  return lastScanMs(b) - lastScanMs(a);
}

/**
 * A sortable column must look like a column, not like a link dropped into the
 * header: the button repeats the `TableHead` typography (11px, uppercase,
 * tracked, muted) so the only thing separating "FINDINGS" from "STATUS" is the
 * caret. `aria-sort` stays on the cell where assistive tech expects it.
 */
function SortableHead({
  label,
  field,
  sort,
  onSort,
  className,
  align = "start",
}: {
  label: string;
  field: SortField;
  sort: SortValue;
  onSort: (value: SortValue) => void;
  className?: string;
  align?: "start" | "end";
}) {
  const active = SORTS[sort].field === field;
  const descending = active && SORTS[sort].descending;
  const next = active && descending ? SORT_VALUES[field].ascending : SORT_VALUES[field].descending;
  const Icon = active ? (descending ? ArrowDown : ArrowUp) : ArrowUpDown;
  return (
    <TableHead
      className={className}
      aria-sort={active ? (descending ? "descending" : "ascending") : "none"}
    >
      <button
        type="button"
        onClick={() => onSort(next)}
        className={cn(
          "inline-flex items-center gap-1 rounded-sm text-[11px] font-medium uppercase",
          "tracking-[0.04em] text-muted-foreground/70 transition-colors hover:text-foreground",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
          align === "end" && "w-full justify-end",
        )}
      >
        {label}
        <Icon className={cn("size-3", active ? "opacity-100" : "opacity-40")} aria-hidden />
      </button>
    </TableHead>
  );
}

function ScanRow({ scan, now }: { scan: SecurityScan; now: number }) {
  const repo = repoLabel(scan.repository);
  const scanned = lastScanMs(scan);
  const statusPill = (
    <span className={cn(STATUS_PILL, "capitalize", toneSoft[scanStatusTone(scan.status)])}>
      {scan.status || "unknown"}
    </span>
  );
  return (
    <TableRow>
      <TableCell className="max-w-[420px] whitespace-normal">
        <div className="flex min-w-0 flex-col gap-0.5">
          <Link
            to={`/security/${scan.namespace}/${scan.runName}`}
            className="truncate font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
          >
            {scan.runName}
          </Link>
          {/* Provenance is triage-critical, so a run that lost its
              configuration or repository says so explicitly instead of
              rendering a blank line that looks like a rendering bug. */}
          <span className="flex min-w-0 items-center gap-1.5 text-[11.5px] text-muted-foreground">
            {!scan.scanName && !repo ? (
              <span className="italic">No configuration or repository recorded</span>
            ) : (
              <>
                {scan.scanName ? (
                  <span className="truncate">{scan.scanName}</span>
                ) : (
                  <span className="italic">No configuration recorded</span>
                )}
                <span aria-hidden>·</span>
                {repo ? (
                  <span className="truncate font-mono">{repo}</span>
                ) : (
                  <span className="italic">No repository recorded</span>
                )}
              </>
            )}
          </span>
          {/* Below `sm` the status, findings and age columns are hidden, so a
              phone reads the whole row here instead of scrolling the table
              sideways past a badge clipped at the card edge. The age says
              "Scanned …" because the column header that labelled it is gone. */}
          <div className="mt-1 flex flex-wrap items-center gap-1 sm:hidden" data-testid="scan-summary">
            {statusPill}
            <SeverityCountBadges counts={scan.counts} />
            <span
              className="text-[11.5px] tabular-nums text-muted-foreground"
              title={scanned ? new Date(scanned).toLocaleString() : undefined}
            >
              {scanned
                ? `Scanned ${formatAge(BigInt(Math.floor(scanned / 1000)), now)} ago`
                : "Never scanned"}
            </span>
          </div>
        </div>
      </TableCell>
      <TableCell className="hidden sm:table-cell">{statusPill}</TableCell>
      <TableCell className="hidden sm:table-cell">
        <SeverityCountBadges counts={scan.counts} />
      </TableCell>
      <TableCell className="hidden text-center sm:table-cell">
        {/* One icon per row instead of a repeated "View run" phrase: the column
            header already says what the target is. */}
        <Tooltip>
          <TooltipTrigger
            render={
              <Link
                to={`/runs/${scan.namespace}/${scan.runName}`}
                aria-label={`View agent run ${scan.runName}`}
                className="inline-flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
              >
                <SquareArrowOutUpRight className="size-3.5" aria-hidden />
              </Link>
            }
          />
          <TooltipContent>View agent run</TooltipContent>
        </Tooltip>
      </TableCell>
      <TableCell className="hidden pr-6 text-right text-muted-foreground tabular-nums sm:table-cell">
        {scanned ? (
          <span title={new Date(scanned).toLocaleString()}>
            {formatAge(BigInt(Math.floor(scanned / 1000)), now)}
          </span>
        ) : (
          <EmptyCell meaning="Never scanned" />
        )}
      </TableCell>
    </TableRow>
  );
}

export function SecurityScanList() {
  const [scans, setScans] = useState<SecurityScan[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const now = useNow();
  const { values, set, setMany, activeCount } = useUrlFilters(FILTER_SPEC);

  const fetchScans = useCallback(async (background = false) => {
    if (!background) {
      setLoading(true);
      setError("");
    }
    try {
      const resp = await client.listSecurityScans({ namespace: "" });
      setScans(resp.scans);
      setError("");
    } catch (e: unknown) {
      if (!background) setError(e instanceof Error ? e.message : "Failed to load security scans");
    } finally {
      if (!background) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchScans();
  }, [fetchScans]);

  // Scan rows change server-side while agents work; poll quietly so the list
  // does not go stale, skipping refreshes while the tab is hidden.
  useEffect(() => {
    const id = window.setInterval(() => {
      if (document.hidden) return;
      void fetchScans(true);
    }, 5_000);
    return () => window.clearInterval(id);
  }, [fetchScans]);

  const sort = parseSort(values.sort);

  // Repository and configuration choices are whatever the loaded rows contain:
  // the RPC only filters by namespace, so there is no server-side vocabulary.
  const repoOptions = useMemo<FilterOption[]>(
    () => optionsFrom(scans.map((scan) => repoLabel(scan.repository)), "All repositories"),
    [scans],
  );
  const configOptions = useMemo<FilterOption[]>(
    () => optionsFrom(scans.map((scan) => scan.scanName), "All configurations"),
    [scans],
  );

  const visible = useMemo(() => {
    const matched = filterByQuery(scans, values.q, (scan) => [
      scan.scanName,
      scan.runName,
      scan.namespace,
      scan.repository,
      repoLabel(scan.repository),
      scan.status,
    ]).filter((scan) => {
      if (values.status !== "all" && scan.status.toLowerCase() !== values.status) return false;
      if (values.severity !== "all" && !hasSeverityAtLeast(scan.counts, values.severity)) return false;
      if (values.repo !== "all" && repoLabel(scan.repository) !== values.repo) return false;
      if (values.config !== "all" && scan.scanName !== values.config) return false;
      return withinTimeRange(lastScanMs(scan), values.range, now);
    });
    return matched.sort((a, b) => compareScans(a, b, sort));
  }, [scans, values.q, values.status, values.severity, values.repo, values.config, values.range, now, sort]);

  const clearFilters = useCallback(() => {
    setMany(Object.fromEntries(FILTER_KEYS.map((key) => [key, FILTER_SPEC[key]])));
  }, [setMany]);

  const onQuery = useCallback((value: string) => set("q", value), [set]);
  const filtersActive = activeCount(["sort"]) > 0;
  const filteredEmpty = !visible.length && scans.length > 0;
  // Nothing loaded and nothing asked for: a search box and a filter strip over
  // an empty page imply the list is narrowed when it is simply empty.
  const nothingToSearch = !scans.length && !filtersActive;

  return (
    <ResourceListPage
      title="Security Scans"
      description="Security scan runs and the findings they reported."
      query={values.q}
      onQuery={onQuery}
      searchPlaceholder="Search scans…"
      hideSearch={nothingToSearch}
      loading={loading}
      error={error}
      onRetry={() => void fetchScans()}
      empty={!visible.length}
      skeleton={<TableRowSkeleton rows={5} />}
      emptyIcon={filteredEmpty ? <FilterX className="size-6" /> : <ShieldAlert className="size-6" />}
      emptyTitle={filteredEmpty ? "No scan runs match these filters" : "No security scans found"}
      emptyDescription={
        filteredEmpty
          ? `None of the ${scans.length} loaded scan runs match the current search and filters.`
          : "Create a scan configuration to scan a repository for vulnerabilities."
      }
      emptyAction={
        filteredEmpty ? (
          <Button variant="outline" size="sm" onClick={clearFilters}>
            <FilterX />
            Clear filters
          </Button>
        ) : undefined
      }
      actions={
        <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/security/configs" />}>
          <Settings2 />
          Configure scans
        </Button>
      }
      nav={<SecurityNav />}
      toolbar={
        !nothingToSearch && (
          <FilterBar
            label="Scan run filters"
            activeCount={activeCount(["sort"])}
            onClear={clearFilters}
            resultLabel={`${visible.length} of ${scans.length} ${scans.length === 1 ? "run" : "runs"}`}
          >
            <FilterSelect
              label="Status"
              value={values.status}
              onChange={(next) => set("status", next)}
              options={SCAN_STATUS_FILTER_OPTIONS}
            />
            <FilterSelect
              label="Severity"
              value={values.severity}
              onChange={(next) => set("severity", next)}
              options={SEVERITY_FILTER_OPTIONS}
            />
            <FilterSelect
              label="Repository"
              value={values.repo}
              onChange={(next) => set("repo", next)}
              options={repoOptions}
            />
            <FilterSelect
              label="Configuration"
              value={values.config}
              onChange={(next) => set("config", next)}
              options={configOptions}
            />
            <FilterSelect
              label="Scanned"
              value={values.range}
              onChange={(next) => set("range", next)}
              options={TIME_RANGE_OPTIONS}
            />
          </FilterBar>
        )
      }
    >
      <Table>
        <TableCaption className="sr-only">
          Security scan runs{filtersActive ? " matching the current filters" : ""}
        </TableCaption>
        <TableHeader>
          <TableRow>
            {/* The name column carries the run, its configuration and its
                repository, so it gets the slack the icon and age columns had.
                Below `sm` every other column folds into it, headers included. */}
            <TableHead className="sm:w-[34%]">Scan run</TableHead>
            <TableHead className="hidden sm:table-cell sm:w-[7.5rem]">Status</TableHead>
            <SortableHead
              label="Findings"
              field="severity"
              sort={sort}
              onSort={(next) => set("sort", next)}
              className="hidden sm:table-cell sm:w-[34%]"
            />
            <TableHead className="hidden w-[4.5rem] text-center sm:table-cell">Agent run</TableHead>
            <SortableHead
              label="Last scan"
              field="scanned"
              sort={sort}
              onSort={(next) => set("sort", next)}
              align="end"
              className="hidden pr-6 text-right sm:table-cell sm:w-[8rem]"
            />
          </TableRow>
        </TableHeader>
        <TableBody>
          {visible.map((scan) => (
            <ScanRow key={`${scan.namespace}/${scan.runName}`} scan={scan} now={now} />
          ))}
        </TableBody>
      </Table>
    </ResourceListPage>
  );
}
