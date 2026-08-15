/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  ArrowDown, ArrowUp, FilterX, Settings2, ShieldAlert, SquareArrowOutUpRight,
} from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
        "inline-flex items-center gap-1 h-[20px] px-2 rounded-full",
        "text-[11px] font-medium tracking-tight whitespace-nowrap select-none capitalize",
        toneSoft[severityTone(severity)],
        className,
      )}
    >
      {severity}
      {count !== undefined && <span className="font-semibold">{count}</span>}
    </span>
  );
}

/** Severity count pills for a counts map; only non-zero severities render. */
export function SeverityCountBadges({ counts }: { counts: Record<string, number> }) {
  const countFor = (severity: string) => counts[`actionable_${severity}`] ?? counts[severity] ?? 0;
  const present = SEVERITIES.filter((severity) => countFor(severity) > 0);
  if (!present.length) {
    return <span className="text-sm text-muted-foreground">—</span>;
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {present.map((severity) => (
        <SeverityBadge key={severity} severity={severity} count={countFor(severity)} />
      ))}
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

function SortableHead({
  label,
  field,
  sort,
  onSort,
  className,
}: {
  label: string;
  field: SortField;
  sort: SortValue;
  onSort: (value: SortValue) => void;
  className?: string;
}) {
  const active = SORTS[sort].field === field;
  const descending = active && SORTS[sort].descending;
  const next = active && descending ? SORT_VALUES[field].ascending : SORT_VALUES[field].descending;
  return (
    <TableHead
      className={className}
      aria-sort={active ? (descending ? "descending" : "ascending") : "none"}
    >
      <button
        type="button"
        onClick={() => onSort(next)}
        className={cn(
          "inline-flex items-center gap-1 rounded-sm hover:text-foreground",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
          active && "text-foreground",
        )}
      >
        {label}
        {active && (descending
          ? <ArrowDown className="size-3" aria-hidden />
          : <ArrowUp className="size-3" aria-hidden />)}
      </button>
    </TableHead>
  );
}

function ScanRow({ scan, now }: { scan: SecurityScan; now: number }) {
  const repo = repoLabel(scan.repository);
  const scanned = lastScanMs(scan);
  return (
    <TableRow>
      <TableCell className="max-w-[420px]">
        <div className="flex min-w-0 flex-col gap-0.5">
          <Link
            to={`/security/${scan.namespace}/${scan.runName}`}
            className="truncate font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
          >
            {scan.runName}
          </Link>
          <span className="flex min-w-0 items-center gap-1.5 text-[11.5px] text-muted-foreground">
            {scan.scanName && <span className="truncate">{scan.scanName}</span>}
            {scan.scanName && repo && <span aria-hidden>·</span>}
            {repo && <span className="truncate font-mono">{repo}</span>}
          </span>
        </div>
      </TableCell>
      <TableCell>
        <Badge
          variant="outline"
          className={cn("capitalize border-transparent", toneSoft[scanStatusTone(scan.status)])}
        >
          {scan.status || "unknown"}
        </Badge>
      </TableCell>
      <TableCell>
        <SeverityCountBadges counts={scan.counts} />
      </TableCell>
      <TableCell>
        <Link
          to={`/runs/${scan.namespace}/${scan.runName}`}
          className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
          aria-label={`View agent run ${scan.runName}`}
        >
          <SquareArrowOutUpRight className="size-3" aria-hidden />
          View run
        </Link>
      </TableCell>
      <TableCell className="text-right text-muted-foreground">
        <span title={scanned ? new Date(scanned).toLocaleString() : "Never scanned"}>
          {formatAge(BigInt(Math.floor(scanned / 1000)), now)}
        </span>
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

  return (
    <ResourceListPage
      title="Security Scans"
      description="Security scan runs and the findings they reported."
      query={values.q}
      onQuery={onQuery}
      searchPlaceholder="Search security scans…"
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
        scans.length > 0 && (
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
            <TableHead>Scan run</TableHead>
            <TableHead>Status</TableHead>
            <SortableHead
              label="Findings"
              field="severity"
              sort={sort}
              onSort={(next) => set("sort", next)}
            />
            <TableHead>Agent run</TableHead>
            <SortableHead
              label="Last scan"
              field="scanned"
              sort={sort}
              onSort={(next) => set("sort", next)}
              className="text-right [&>button]:justify-end"
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
