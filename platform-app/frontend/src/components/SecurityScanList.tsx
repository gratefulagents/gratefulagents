/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { ShieldAlert, Settings2 } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { ResourceListPage } from "@/components/list-page";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";
import { formatAge } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
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
  const present = SEVERITIES.filter((s) => (counts[s] ?? 0) > 0);
  if (!present.length) {
    return <span className="text-sm text-muted-foreground">—</span>;
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {present.map((s) => (
        <SeverityBadge key={s} severity={s} count={counts[s]} />
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

function lastScanUnix(scan: SecurityScan): bigint {
  const ts: Timestamp | undefined = scan.completedAt ?? scan.startedAt;
  if (!ts) return 0n;
  return BigInt(Math.floor(timestampDate(ts).getTime() / 1000));
}

export function SecurityScanList() {
  const [scans, setScans] = useState<SecurityScan[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const now = useNow();

  const fetchScans = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const resp = await client.listSecurityScans({ namespace: "" });
      setScans(resp.scans);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to load security scans");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchScans();
  }, [fetchScans]);

  const filtered = filterByQuery(scans, query, (scan) => [
    scan.scanName,
    scan.runName,
    scan.namespace,
    scan.repository,
    scan.status,
  ]);

  return (
    <ResourceListPage
      title="Security Scans"
      description="Security scan runs and the findings they reported."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search security scans…"
      loading={loading}
      error={error}
      onRetry={fetchScans}
      empty={!filtered.length}
      skeleton={<TableRowSkeleton rows={5} />}
      emptyIcon={<ShieldAlert className="size-6" />}
      emptyTitle={query ? `No matches for "${query}"` : "No security scans found"}
      emptyDescription={
        query
          ? "Clear the search to see all security scans."
          : "Create a scan configuration to scan a repository for vulnerabilities."
      }
      actions={
        <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/security/configs" />}>
          <Settings2 />
          Configure scans
        </Button>
      }
    >
      <Table>
        <TableCaption className="sr-only">Security scans</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Scan</TableHead>
            <TableHead>Repository</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Findings</TableHead>
            <TableHead className="text-right">Last Scan</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filtered.map((scan) => (
            <TableRow key={`${scan.namespace}/${scan.runName}`}>
              <TableCell>
                <Link
                  to={`/security/${scan.namespace}/${scan.runName}`}
                  className="font-medium text-primary hover:underline"
                >
                  {scan.runName}
                </Link>
                {scan.scanName && scan.scanName !== scan.runName && (
                  <span className="ml-2 text-xs text-muted-foreground">{scan.scanName}</span>
                )}
              </TableCell>
              <TableCell className="font-mono text-sm text-muted-foreground">
                {scan.repository || "—"}
              </TableCell>
              <TableCell>
                <Badge variant="outline" className={cn("capitalize border-transparent", toneSoft[scanStatusTone(scan.status)])}>
                  {scan.status || "unknown"}
                </Badge>
              </TableCell>
              <TableCell>
                <SeverityCountBadges counts={scan.counts} />
              </TableCell>
              <TableCell className="text-right text-muted-foreground">
                {formatAge(lastScanUnix(scan), now)}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </ResourceListPage>
  );
}
