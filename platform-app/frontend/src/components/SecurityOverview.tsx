/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { History, Settings2, ShieldAlert, ShieldCheck } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import { DetailSection, StatBar, Stat } from "@/components/detail-page";
import { SeverityCountBadges } from "@/components/SecurityScanList";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";
import { formatAge } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import type { GetSecurityOverviewResponse, SecurityScan } from "@/rpc/platform/service_pb";

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

function scanUnix(ts: Timestamp | undefined): bigint {
  if (!ts) return 0n;
  return BigInt(Math.floor(timestampDate(ts).getTime() / 1000));
}

function ScanRow({ scan, now }: { scan: SecurityScan; now: number }) {
  return (
    <TableRow>
      <TableCell>
        <Link
          to={`/security/${scan.namespace}/${scan.runName}`}
          className="font-medium text-primary hover:underline"
        >
          {scan.runName}
        </Link>
      </TableCell>
      <TableCell className="font-mono text-sm text-muted-foreground">
        {scan.repository || "—"}
      </TableCell>
      <TableCell>
        <Badge variant="outline" className={cn("capitalize border-transparent", toneSoft[scanTone(scan.status)])}>
          {scan.status || "unknown"}
        </Badge>
      </TableCell>
      <TableCell>
        <SeverityCountBadges counts={scan.counts} />
      </TableCell>
      <TableCell className="text-right text-muted-foreground">
        {formatAge(scanUnix(scan.completedAt ?? scan.startedAt), now)}
      </TableCell>
    </TableRow>
  );
}

export function SecurityOverview() {
  const [overview, setOverview] = useState<GetSecurityOverviewResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const now = useNow();

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

  const counts = overview?.findingCounts ?? {};
  const empty =
    !!overview &&
    overview.activeScans.length === 0 &&
    overview.recentScans.length === 0 &&
    overview.configCount === 0;

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-0.5">
          <h1 className="text-[22px] font-semibold leading-tight tracking-[-0.015em]">Security</h1>
          <p className="text-[13px] text-muted-foreground">
            Repository security posture: scan activity, open findings, and scan configurations that need attention.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/security/runs" />}>
            <History />
            Run history
          </Button>
          <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/security/configs" />}>
            <Settings2 />
            Scan configurations
          </Button>
        </div>
      </div>

      <ListState
        loading={loading}
        error={error}
        empty={empty || (!overview && !!error)}
        onRetry={fetchOverview}
        skeleton={<ListRowSkeleton rows={5} />}
        emptyIcon={<ShieldCheck className="size-6" />}
        emptyTitle="No security scans yet"
        emptyDescription="Create a scan configuration to analyze a repository for vulnerabilities, once or on a schedule."
        emptyAction={
          <Button variant="outline" size="sm" nativeButton={false} render={<Link to="/security/configs" />}>
            <Settings2 />
            Configure a scan
          </Button>
        }
      >
        {overview && (
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
              <StatBar>
                <Stat
                  label="Open critical"
                  value={
                    <span className={cn(toneSoft["danger"], "rounded-md px-2 py-0.5")}>
                      {counts["open_critical"] ?? 0}
                    </span>
                  }
                  mono={false}
                />
                <Stat
                  label="Open high"
                  value={
                    <span className={cn(toneSoft["warning"], "rounded-md px-2 py-0.5")}>
                      {counts["open_high"] ?? 0}
                    </span>
                  }
                  mono={false}
                />
                <Stat label="Open findings" value={counts["open"] ?? 0} />
                <Stat label="Total findings" value={counts["total"] ?? 0} />
                <Stat label="Active scans" value={overview.activeScans.length} />
                <Stat label="Configurations" value={overview.configCount} />
              </StatBar>
            )}

            {overview.storeSupported && (
              <p className="text-[12px] text-muted-foreground/70">
                {overview.baselineAvailable ? (
                  <>
                    Since the last baseline: {overview.newFindings} new, {overview.recurringFindings} recurring,{" "}
                    {overview.resolvedFindings} resolved findings.
                  </>
                ) : (
                  <>New, recurring, and resolved finding counts appear here once baseline comparisons are available.</>
                )}
              </p>
            )}

            {overview.activeScans.length > 0 && (
              <DetailSection
                title="Active scans"
                description="Scan runs that are still in progress."
              >
                <Table>
                  <TableCaption className="sr-only">Active security scans</TableCaption>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Scan run</TableHead>
                      <TableHead>Repository</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead>Findings</TableHead>
                      <TableHead className="text-right">Started</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {overview.activeScans.map((scan) => (
                      <ScanRow key={`${scan.namespace}/${scan.runName}`} scan={scan} now={now} />
                    ))}
                  </TableBody>
                </Table>
              </DetailSection>
            )}

            {(overview.configIssues.length > 0 || overview.configCount > 0) && (
              <DetailSection
                title="Needs attention"
                description="Scan configurations that are failing, blocked, or suspended."
                aside={
                  <Link to="/security/configs" className="text-primary hover:underline">
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
                      <li
                        key={`${issue.namespace}/${issue.name}`}
                        className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg border border-border/60 px-3 py-2 text-[12.5px]"
                      >
                        <ShieldAlert className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
                        <Link to="/security/configs" className="font-medium text-primary hover:underline">
                          {issue.name}
                        </Link>
                        <Badge
                          variant="outline"
                          className={cn(
                            "border-transparent capitalize",
                            toneSoft[issue.suspended ? "neutral" : "danger"],
                          )}
                        >
                          {issue.readyReason || issue.phase || "NotReady"}
                        </Badge>
                        {issue.message && (
                          <span className="min-w-0 flex-1 basis-64 truncate text-muted-foreground" title={issue.message}>
                            {issue.message}
                          </span>
                        )}
                      </li>
                    ))}
                  </ul>
                )}
              </DetailSection>
            )}

            {overview.storeSupported && (
              <DetailSection
                title="Recent scans"
                description="The newest completed scan runs."
                aside={
                  <Link to="/security/runs" className="text-primary hover:underline">
                    View all runs
                  </Link>
                }
              >
                {overview.recentScans.length === 0 ? (
                  <p className="text-[12.5px] text-muted-foreground">No completed scan runs yet.</p>
                ) : (
                  <Table>
                    <TableCaption className="sr-only">Recent security scans</TableCaption>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Scan run</TableHead>
                        <TableHead>Repository</TableHead>
                        <TableHead>Status</TableHead>
                        <TableHead>Findings</TableHead>
                        <TableHead className="text-right">Completed</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {overview.recentScans.map((scan) => (
                        <ScanRow key={`${scan.namespace}/${scan.runName}`} scan={scan} now={now} />
                      ))}
                    </TableBody>
                  </Table>
                )}
              </DetailSection>
            )}
          </div>
        )}
      </ListState>
    </div>
  );
}
