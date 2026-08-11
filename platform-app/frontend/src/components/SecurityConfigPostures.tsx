/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { ArrowDown, ArrowUp } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ListRowSkeleton } from "@/components/ui/list-state";
import { DetailSection } from "@/components/detail-page";
import { SEVERITIES, severityTone } from "@/components/SecurityScanList";
import { BaselineBadge } from "@/components/security-baseline";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneColor, toneSoft, type StatusTone } from "@/lib/status";
import { formatAge } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import type {
  GetSecurityConfigPosturesResponse,
  SecurityConfigPosture,
  SecurityRunActivityPoint,
} from "@/rpc/platform/service_pb";

function statusTone(status: string): StatusTone {
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

function lastRunUnix(p: SecurityConfigPosture): bigint {
  const ts: Timestamp | undefined = p.lastCompletedAt ?? p.lastStartedAt;
  if (!ts) return 0n;
  return BigInt(Math.floor(timestampDate(ts).getTime() / 1000));
}

/** Compact horizontal stacked bar of actionable findings, colored by severity. */
function ActionableSeverityBar({ scanName, counts }: { scanName: string; counts: Record<string, number> }) {
  const parts = SEVERITIES
    .map((s) => ({ severity: s, count: counts[`actionable_${s}`] ?? counts[`open_${s}`] ?? 0 }))
    .filter((p) => p.count > 0);
  const total = parts.reduce((sum, p) => sum + p.count, 0);
  if (total === 0) return null;
  const label = parts.map((p) => `${p.count} ${p.severity}`).join(", ");
  return (
    <div
      role="img"
      aria-label={`Actionable findings for ${scanName} by severity: ${label}`}
      title={label}
      className="flex h-1.5 w-24 overflow-hidden rounded-full bg-muted/40"
    >
      {parts.map((p) => (
        <div
          key={p.severity}
          style={{
            width: `${(p.count / total) * 100}%`,
            backgroundColor: toneColor[severityTone(p.severity)],
          }}
        />
      ))}
    </div>
  );
}

/** Hand-rolled SVG sparkline of total findings across recent runs, oldest first. */
function TrendSparkline({ scanName, activity }: { scanName: string; activity: SecurityRunActivityPoint[] }) {
  if (activity.length < 2) {
    return <span className="text-muted-foreground/60">—</span>;
  }
  const width = 100;
  const height = 28;
  const pad = 3;
  const max = Math.max(1, ...activity.map((p) => p.total));
  const points = activity.map((p, i) => ({
    x: pad + (i * (width - 2 * pad)) / (activity.length - 1),
    y: height - pad - (p.total / max) * (height - 2 * pad),
  }));
  const last = points[points.length - 1];
  const tooltip = activity.map((p) => `${p.runName}: ${p.total} findings`).join("\n");
  return (
    <svg
      width={width}
      height={height}
      viewBox={`0 0 ${width} ${height}`}
      role="img"
      aria-label={`Finding trend for ${scanName}: ${activity
        .map((p) => `${p.runName}: ${p.total} findings`)
        .join("; ")}`}
      className="text-primary"
    >
      <title>{tooltip}</title>
      <polyline
        fill="none"
        stroke="currentColor"
        strokeWidth="1.5"
        strokeLinejoin="round"
        strokeLinecap="round"
        points={points.map((p) => `${p.x},${p.y}`).join(" ")}
      />
      <circle cx={last.x} cy={last.y} r="2" fill="currentColor" />
    </svg>
  );
}

type SortKey = "name" | "open" | "age";
type SortDir = "asc" | "desc";

/** Default ordering: worst posture first (actionable critical, then high, then total). */
function severityChain(p: SecurityConfigPosture): [number, number, number] {
  const c = p.findingCounts;
  return [
    c["actionable_critical"] ?? c["open_critical"] ?? 0,
    c["actionable_high"] ?? c["open_high"] ?? 0,
    c["actionable"] ?? c["open"] ?? 0,
  ];
}

function compare(a: SecurityConfigPosture, b: SecurityConfigPosture, key: SortKey): number {
  switch (key) {
    case "name":
      return a.scanName.localeCompare(b.scanName);
    case "open": {
      const ca = severityChain(a);
      const cb = severityChain(b);
      for (let i = 0; i < ca.length; i++) {
        if (ca[i] !== cb[i]) return ca[i] - cb[i];
      }
      return a.scanName.localeCompare(b.scanName);
    }
    case "age": {
      const ua = lastRunUnix(a);
      const ub = lastRunUnix(b);
      return ua === ub ? 0 : ua < ub ? -1 : 1;
    }
  }
}

function SortableHead({
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
        className="inline-flex items-center gap-1 hover:text-foreground"
      >
        {label}
        {active &&
          (sort.dir === "asc" ? (
            <ArrowUp className="size-3" aria-hidden />
          ) : (
            <ArrowDown className="size-3" aria-hidden />
          ))}
      </button>
    </TableHead>
  );
}

const BASELINE_CHANGE_STATES = ["new", "regressed", "resolved"] as const;

function PostureRow({ posture, now }: { posture: SecurityConfigPosture; now: number }) {
  const counts = posture.findingCounts;
  const changes = BASELINE_CHANGE_STATES
    .map((state) => ({ state, count: counts[`baseline_${state}`] ?? 0 }))
    .filter((c) => c.count > 0);
  return (
    <TableRow>
      <TableCell>
        <Link to="/security/configs" className="font-medium text-primary hover:underline">
          {posture.scanName}
        </Link>
      </TableCell>
      <TableCell className="font-mono text-sm text-muted-foreground">
        {posture.repository || "—"}
      </TableCell>
      <TableCell>
        <div className="flex items-center gap-2">
          <span className="w-6 text-right font-medium tabular-nums">{counts["actionable"] ?? counts["open"] ?? 0}</span>
          <ActionableSeverityBar scanName={posture.scanName} counts={counts} />
        </div>
      </TableCell>
      <TableCell>
        {changes.length === 0 ? (
          <span className="text-sm text-muted-foreground">—</span>
        ) : (
          <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1">
            {changes.map((c) => (
              <span key={c.state} className="inline-flex items-center gap-1">
                <BaselineBadge state={c.state} />
                <span className="text-[12px] font-medium tabular-nums">{c.count}</span>
              </span>
            ))}
          </span>
        )}
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <Badge
            variant="outline"
            className={cn("capitalize border-transparent", toneSoft[statusTone(posture.lastRunStatus)])}
          >
            {posture.lastRunStatus || "unknown"}
          </Badge>
          {posture.lastRunName && (
            <span className="text-[12px] text-muted-foreground">{posture.lastRunName}</span>
          )}
          <span className="text-[12px] tabular-nums text-muted-foreground">
            {formatAge(lastRunUnix(posture), now)}
          </span>
        </div>
      </TableCell>
      <TableCell>
        <TrendSparkline scanName={posture.scanName} activity={posture.activity} />
      </TableCell>
    </TableRow>
  );
}

/**
 * SecurityConfigPostures is a self-fetching Security overview section that
 * compares per-configuration security posture across recent runs.
 */
export function SecurityConfigPostures() {
  const [resp, setResp] = useState<GetSecurityConfigPosturesResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [sort, setSort] = useState<{ key: SortKey; dir: SortDir }>({ key: "open", dir: "desc" });
  const now = useNow();

  const fetchPostures = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      setResp(await client.getSecurityConfigPostures({ namespace: "", activityLimit: 0 }));
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to load configuration postures");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchPostures();
  }, [fetchPostures]);

  const onSort = useCallback((key: SortKey) => {
    setSort((prev) =>
      prev.key === key
        ? { key, dir: prev.dir === "desc" ? "asc" : "desc" }
        : { key, dir: key === "name" ? "asc" : "desc" },
    );
  }, []);

  const sorted = useMemo(() => {
    const postures = [...(resp?.postures ?? [])];
    postures.sort((a, b) => {
      const c = compare(a, b, sort.key);
      return sort.dir === "asc" ? c : -c;
    });
    return postures;
  }, [resp, sort]);

  if (resp && !resp.storeSupported) return null;
  const warnings = resp?.warnings ?? [];

  return (
    <DetailSection
      title="Configurations"
      description="Per-configuration posture across recent runs."
      aside={
        <Link to="/security/configs" className="text-primary hover:underline">
          Manage configurations
        </Link>
      }
    >
      {loading ? (
        <ListRowSkeleton rows={3} />
      ) : error ? (
        <div className="flex flex-wrap items-center gap-2 text-[12.5px]">
          <span role="alert" className="text-destructive">{error}</span>
          <Button variant="outline" size="sm" onClick={() => void fetchPostures()}>
            Retry
          </Button>
        </div>
      ) : (
        <div className="space-y-3">
          {warnings.length > 0 && (
            <div
              role="alert"
              className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-[12.5px]"
            >
              <div className="min-w-0 flex-1 basis-64">
                <span className="font-medium">Partial data — some posture sources failed to load.</span>
                <ul className="mt-1 list-disc pl-5 text-muted-foreground">
                  {warnings.map((w) => (
                    <li key={w}>{w}</li>
                  ))}
                </ul>
              </div>
              <Button variant="outline" size="sm" onClick={() => void fetchPostures()}>
                Retry
              </Button>
            </div>
          )}
          {sorted.length === 0 ? (
            warnings.length === 0 && (
              <p className="text-[12.5px] text-muted-foreground">No persisted scan data yet.</p>
            )
          ) : (
            <Table>
              <TableCaption className="sr-only">Per-configuration security posture</TableCaption>
              <TableHeader>
                <TableRow>
                  <SortableHead label="Configuration" sortKey="name" sort={sort} onSort={onSort} />
                  <TableHead>Repository</TableHead>
                  <SortableHead label="Actionable" sortKey="open" sort={sort} onSort={onSort} />
                  <TableHead>Changes</TableHead>
                  <SortableHead label="Last run" sortKey="age" sort={sort} onSort={onSort} />
                  <TableHead>Trend</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sorted.map((posture) => (
                  <PostureRow key={posture.scanName} posture={posture} now={now} />
                ))}
              </TableBody>
            </Table>
          )}
        </div>
      )}
    </DetailSection>
  );
}
