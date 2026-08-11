/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Loader2, Settings2, ShieldAlert, ShieldCheck } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toaster";
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import { DetailSection } from "@/components/detail-page";
import { SecurityNav } from "@/components/SecurityNav";
import { SeverityCountBadges } from "@/components/SecurityScanList";
import { BaselineBadge, formatDurationSeconds } from "@/components/security-baseline";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneColor, toneSoft, toneText, type StatusTone } from "@/lib/status";
import { formatAge } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import type {
  GetSecurityOverviewResponse,
  SecurityScan,
  SecuritySkillsStatus,
} from "@/rpc/platform/service_pb";

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

/**
 * PostureTile is one figure of the security posture strip: a small label
 * over a large tabular number. A tone paints an inset accent along the top
 * edge (and the number itself) so critical/high counts read at a glance;
 * zero counts stay quiet even when a tone is set.
 */
function PostureTile({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone?: StatusTone;
}) {
  const active = tone !== undefined && value > 0;
  return (
    <div
      className="rounded-lg border border-border/70 bg-muted/20 px-3 py-2.5"
      style={active ? { boxShadow: `inset 0 2px 0 0 ${toneColor[tone]}` } : undefined}
    >
      <p className="truncate text-[11.5px] text-muted-foreground">{label}</p>
      <p
        className={cn(
          "mt-0.5 font-mono text-xl font-semibold leading-tight tabular-nums",
          active ? toneText[tone] : "text-foreground",
        )}
      >
        {value}
      </p>
    </div>
  );
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
  const [skillsStatus, setSkillsStatus] = useState<SecuritySkillsStatus | null>(null);
  const [skillsLoading, setSkillsLoading] = useState(true);
  const [skillsError, setSkillsError] = useState(false);
  const [skillsInstalling, setSkillsInstalling] = useState(false);
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

  const fetchSkillsStatus = useCallback(async () => {
    setSkillsLoading(true);
    setSkillsError(false);
    try {
      setSkillsStatus(await client.getSecuritySkillsStatus({}));
    } catch {
      setSkillsError(true);
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
      setSkillsError(false);
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : "Failed to install security skills");
    } finally {
      setSkillsInstalling(false);
    }
  }

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
        <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto sm:justify-end">
          <Badge
            variant="outline"
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
          ) : skillsStatus &&
            skillsStatus.state !== "installed" &&
            skillsStatus.state !== "unavailable" &&
            skillsStatus.installedCount + skillsStatus.conflictCount < skillsStatus.availableCount ? (
            <Button size="sm" disabled={skillsInstalling} onClick={() => void installSecuritySkills()}>
              {skillsInstalling && <Loader2 className="animate-spin" />}
              {skillsInstalling ? "Installing…" : "Install security skills"}
            </Button>
          ) : null}
        </div>
      </div>
      <SecurityNav />

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
              <div
                className="grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-6"
                data-testid="security-posture"
              >
                <PostureTile label="Open critical" value={counts["open_critical"] ?? 0} tone="danger" />
                <PostureTile label="Open high" value={counts["open_high"] ?? 0} tone="warning" />
                <PostureTile label="Open findings" value={counts["open"] ?? 0} />
                <PostureTile label="Total findings" value={counts["total"] ?? 0} />
                <PostureTile label="Active scans" value={overview.activeScans.length} tone={overview.activeScans.length > 0 ? "running" : undefined} />
                <PostureTile label="Configurations" value={overview.configCount} />
              </div>
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
                      const target = overview.recentScans[0];
                      const chip = (
                        <span className="inline-flex items-center gap-1">
                          <BaselineBadge state={state} />
                          <span className="font-medium tabular-nums">{count}</span>
                        </span>
                      );
                      return target ? (
                        <Link
                          key={state}
                          to={`/security/${target.namespace}/${target.runName}?baseline=${state}`}
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
                {overview.trends && (overview.trends.triagedCount > 0 || overview.trends.resolvedCount > 0) && (
                  <p className="text-[12px] text-muted-foreground" aria-label="Triage trends">
                    Time to triage: median {formatDurationSeconds(overview.trends.medianTimeToTriageSeconds)}
                    {" "}(avg {formatDurationSeconds(overview.trends.avgTimeToTriageSeconds)}, {overview.trends.triagedCount} triaged)
                    {" · "}Time to resolution: median {formatDurationSeconds(overview.trends.medianTimeToResolutionSeconds)}
                    {" "}(avg {formatDurationSeconds(overview.trends.avgTimeToResolutionSeconds)}, {overview.trends.resolvedCount} resolved)
                  </p>
                )}
              </div>
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
