/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Copy, Download, FileText, SquareArrowOutUpRight, X } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import { ListSearchInput } from "@/components/ui/list-search";
import {
  DetailHeader, DetailSection, StatBar, Stat, FactList, Fact, FactLink,
} from "@/components/detail-page";
import {
  SEVERITIES, SeverityBadge, severityTone,
} from "@/components/SecurityScanList";
import { SecurityScanRunPanel } from "@/components/SecurityScanRunPanel";
import { SecurityScanFormDialog } from "@/components/SecurityScanFormDialog";
import { ExecutionProgressPanel } from "@/components/ExecutionProgressPanel";
import {
  BASELINE_STATES, BaselineBadge, ExpiryBadge, SuppressedBadge, suppressionSummary,
} from "@/components/security-baseline";
import { packBudgetSummary } from "@/components/SecurityPolicyPackDialog";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { downloadBlob } from "@/lib/download";
import { toneSoft } from "@/lib/status";
import type {
  BulkUpdateSecurityFindingOutcome,
  SecurityFinding,
  SecurityFindingEvent,
  SecurityScan,
  SecurityScanConfig,
  SecurityScanTaskConfig,
  SecuritySavedFilter,
} from "@/rpc/platform/service_pb";

export const FINDING_STATUSES = [
  "open",
  "triaged",
  "confirmed",
  "false_positive",
  "fixed",
  "accepted_risk",
] as const;

export function statusLabel(status: string): string {
  return status.replace(/_/g, " ");
}

export function formatSeen(ts: Timestamp | undefined): string {
  if (!ts) return "—";
  return timestampDate(ts).toLocaleString();
}

function cweUrl(cwe: string): string {
  const id = cwe.replace(/^CWE-?/i, "");
  return `https://cwe.mitre.org/data/definitions/${id}.html`;
}

const filterSelectClass =
  "h-8 rounded-md border border-border/70 bg-background px-2 text-[12.5px] text-foreground capitalize focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60";

export function SecurityScanDetail() {
  const { namespace, runName } = useParams<{ namespace: string; runName: string }>();
  const [searchParams, setSearchParams] = useSearchParams();

  const [scan, setScan] = useState<SecurityScan | null>(null);
  const [scanConfig, setScanConfig] = useState<SecurityScanConfig | null>(null);
  const [workflowTasks, setWorkflowTasks] = useState<SecurityScanTaskConfig[]>([]);
  const [summary, setSummary] = useState<Record<string, number>>({});
  const [findings, setFindings] = useState<SecurityFinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [findingsLoading, setFindingsLoading] = useState(true);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);

  // Filters live in the URL so a shared link reproduces the same view and
  // the finding detail page can hand the same context back on return.
  const severity = searchParams.get("severity") ?? "";
  const status = searchParams.get("status") ?? "actionable";
  const category = searchParams.get("category") ?? "";
  const search = searchParams.get("q") ?? "";
  const baseline = searchParams.get("baseline") ?? "";
  const assigneeFilter = searchParams.get("assignee") ?? "";
  const suppressed = searchParams.get("suppressed") ?? "";

  const setFilter = useCallback(
    (key: "severity" | "status" | "category" | "q" | "baseline" | "assignee" | "suppressed", value: string) => {
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

  const findingLinkSearch = searchParams.toString();

  const findingHref = useCallback(
    (id: string) =>
      `/security/${namespace}/${runName}/findings/${id}${findingLinkSearch ? `?${findingLinkSearch}` : ""}`,
    [namespace, runName, findingLinkSearch],
  );

  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [statusSaving, setStatusSaving] = useState(false);

  const [reportBusy, setReportBusy] = useState<"markdown" | "sarif" | null>(null);
  const [reportNotice, setReportNotice] = useState<string | null>(null);
  const [bundleBusy, setBundleBusy] = useState(false);
  const [bundleNotice, setBundleNotice] = useState<string | null>(null);

  // Multi-select bulk triage.
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkStatus, setBulkStatus] = useState("");
  const [bulkNote, setBulkNote] = useState("");
  const [bulkAssignee, setBulkAssignee] = useState("");
  const [bulkFailures, setBulkFailures] = useState<BulkUpdateSecurityFindingOutcome[]>([]);

  // Saved filter views, private to the current user.
  const [savedFilters, setSavedFilters] = useState<SecuritySavedFilter[]>([]);
  const [savedFilterName, setSavedFilterName] = useState("");
  const [savedFilterBusy, setSavedFilterBusy] = useState(false);
  const [appliedSavedFilter, setAppliedSavedFilter] = useState("");

  const [exportBusy, setExportBusy] = useState(false);

  const [events, setEvents] = useState<SecurityFindingEvent[]>([]);
  const [eventsLoading, setEventsLoading] = useState(false);
  const [eventsError, setEventsError] = useState("");

  const fetchScan = useCallback(async (background = false) => {
    if (!namespace || !runName) return;
    if (!background) {
      setLoading(true);
      setError("");
    }
    try {
      const [scanResp, summaryResp] = await Promise.all([
        client.getSecurityScan({ namespace, runName }),
        client.getSecurityFindingSummary({ namespace, runName }),
      ]);
      setScan(scanResp);
      setSummary(summaryResp.counts);
      // Repository integration state (check publishing / notifications)
      // lives on the SecurityScan trigger config; best-effort.
      if (scanResp.scanName) {
        try {
          const config = await client.getSecurityScanConfig({ namespace, name: scanResp.scanName });
          setScanConfig(config);
          // The execution DAG prefers the plan snapshot recorded on the
          // execution itself (authoritative even after the source workflow
          // is edited). Only executions predating plan recording need the
          // planned task graph resolved here: inline workflow tasks when
          // present, otherwise the referenced SecurityWorkflow's tasks;
          // best-effort (the graph degrades to a plain table).
          if ((config.lastExecution?.plan ?? []).length > 0) {
            setWorkflowTasks([]);
          } else if ((config.spec?.workflow ?? []).length > 0) {
            setWorkflowTasks(config.spec!.workflow);
          } else if (config.spec?.workflowRef) {
            try {
              const wf = await client.getSecurityWorkflow({
                namespace,
                name: config.spec.workflowRef,
              });
              setWorkflowTasks(wf.tasks ?? []);
            } catch {
              setWorkflowTasks([]);
            }
          } else {
            setWorkflowTasks([]);
          }
        } catch {
          setScanConfig(null);
          setWorkflowTasks([]);
        }
      }
    } catch (e: unknown) {
      if (!background) setError(e instanceof Error ? e.message : "Failed to load security scan");
    } finally {
      if (!background) setLoading(false);
    }
  }, [namespace, runName]);

  const fetchFindings = useCallback(async (background = false) => {
    if (!namespace || !runName) return;
    if (!background) setFindingsLoading(true);
    try {
      const resp = await client.listSecurityFindings({
        namespace,
        runName,
        severity,
        status: status === "all" ? "" : status,
        category,
        search,
        baselineState: baseline,
        assignee: assigneeFilter,
        suppressed,
      });
      setFindings(resp.findings);
    } catch (e: unknown) {
      if (!background) setError(e instanceof Error ? e.message : "Failed to load security findings");
    } finally {
      if (!background) setFindingsLoading(false);
    }
  }, [namespace, runName, severity, status, category, search, baseline, assigneeFilter, suppressed]);

  const fetchEvents = useCallback(async () => {
    if (!selectedId) {
      setEvents([]);
      setEventsError("");
      return;
    }
    setEventsLoading(true);
    setEventsError("");
    try {
      const resp = await client.getSecurityFinding({ id: selectedId, namespace: namespace ?? "" });
      setEvents(resp.events);
    } catch (e: unknown) {
      setEventsError(e instanceof Error ? e.message : "Failed to load finding history");
    } finally {
      setEventsLoading(false);
    }
  }, [selectedId, namespace]);

  useEffect(() => {
    void fetchScan();
  }, [fetchScan]);

  useEffect(() => {
    void fetchFindings();
  }, [fetchFindings]);

  useEffect(() => {
    void fetchEvents();
  }, [fetchEvents]);

  // While the scan is still executing, its record, summary, and findings keep
  // changing server-side; poll quietly (no loading flicker) until it settles,
  // skipping refreshes while the tab is hidden.
  const scanLoaded = scan !== null;
  const scanSettled = Boolean(scan?.completedAt) && scan?.status.toLowerCase() !== "running";
  useEffect(() => {
    if (!scanLoaded || scanSettled) return;
    const id = window.setInterval(() => {
      if (document.hidden) return;
      void fetchScan(true);
      void fetchFindings(true);
    }, 5_000);
    return () => window.clearInterval(id);
  }, [scanLoaded, scanSettled, fetchScan, fetchFindings]);

  const categories = useMemo(() => {
    const set = new Set<string>();
    for (const finding of findings) {
      if (finding.category) set.add(finding.category);
    }
    if (category) set.add(category);
    return [...set].sort();
  }, [findings, category]);

  const selected = findings.find((f) => f.id === selectedId) ?? null;

  // When the linked AgentRun transitions into a terminal phase, re-fetch the
  // persisted scan row, summary, and findings so no stale state lingers on
  // screen. Background mode is essential: a foreground fetch flips the
  // page-level loading state, which swaps the whole page for a skeleton and
  // unmounts the run panel — and a remounting run panel re-observing a
  // terminal run would restart its watch streams (flicker loop).
  const handleRunSettled = useCallback(() => {
    void fetchScan(true);
    void fetchFindings(true);
  }, [fetchScan, fetchFindings]);

  async function downloadReport(format: "markdown" | "sarif") {
    if (!namespace || !runName) return;
    setReportNotice(null);
    setReportBusy(format);
    try {
      const resp = await client.getSecurityScanReport({ namespace, runName, format });
      downloadBlob(
        resp.filename || `${runName}.${format === "sarif" ? "sarif" : "md"}`,
        new TextEncoder().encode(resp.content),
        format === "sarif" ? "application/json" : "text/markdown",
      );
    } catch (e: unknown) {
      setReportNotice(e instanceof Error ? e.message : "Failed to fetch the scan report");
    } finally {
      setReportBusy(null);
    }
  }

  async function downloadSubmissionBundle(finding: SecurityFinding) {
    if (!namespace) return;
    setBundleNotice(null);
    setBundleBusy(true);
    try {
      const resp = await client.getSecurityFindingSubmissionBundle({
        namespace,
        findingId: finding.id,
      });
      if (resp.status !== "ready" || resp.content.length === 0) {
        setBundleNotice(resp.error || `Bundle is ${resp.status || "unavailable"}.`);
        return;
      }
      downloadBlob(
        resp.filename || `${finding.fingerprint}-bounty-submission.zip`,
        resp.content,
        "application/zip",
      );
      setBundleNotice(resp.sha256 ? `Downloaded. SHA-256: ${resp.sha256}` : "Downloaded.");
    } catch (e: unknown) {
      setBundleNotice(e instanceof Error ? e.message : "Failed to fetch the bounty bundle");
    } finally {
      setBundleBusy(false);
    }
  }

  async function changeStatus(finding: SecurityFinding, nextStatus: string) {
    setActionError(null);
    setStatusSaving(true);
    const previous = findings;
    // Optimistic update; the authoritative refresh follows below.
    setFindings((current) =>
      current.map((f) => (f.id === finding.id ? { ...f, status: nextStatus } : f)),
    );
    try {
      await client.updateSecurityFindingStatus({
        id: finding.id,
        status: nextStatus,
        note: "",
        namespace: namespace ?? "",
      });
      await Promise.all([fetchFindings(true), fetchScan(true), fetchEvents()]);
    } catch (e: unknown) {
      setFindings(previous);
      setActionError(e instanceof Error ? e.message : "Failed to update finding status");
    } finally {
      setStatusSaving(false);
    }
  }

  const fetchSavedFilters = useCallback(async () => {
    if (!namespace) return;
    try {
      const resp = await client.listSecuritySavedFilters({ namespace });
      setSavedFilters(resp.filters);
    } catch {
      // Saved views are a convenience; the page works without them.
    }
  }, [namespace]);

  useEffect(() => {
    void fetchSavedFilters();
  }, [fetchSavedFilters]);

  const FILTER_KEYS = ["severity", "status", "category", "q", "baseline", "assignee", "suppressed"] as const;

  function currentFilterQuery(): string {
    const query: Record<string, string> = {};
    for (const key of FILTER_KEYS) {
      const value = searchParams.get(key);
      if (value) query[key] = value;
    }
    return JSON.stringify(query);
  }

  async function saveCurrentFilter() {
    if (!namespace || !savedFilterName.trim()) return;
    setSavedFilterBusy(true);
    setActionError(null);
    try {
      await client.saveSecuritySavedFilter({
        namespace,
        name: savedFilterName.trim(),
        query: currentFilterQuery(),
      });
      setSavedFilterName("");
      await fetchSavedFilters();
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to save the view");
    } finally {
      setSavedFilterBusy(false);
    }
  }

  function applySavedFilter(name: string) {
    setAppliedSavedFilter(name);
    const filter = savedFilters.find((f) => f.name === name);
    const next = new URLSearchParams();
    if (filter) {
      try {
        const query = JSON.parse(filter.query || "{}") as Record<string, unknown>;
        for (const key of FILTER_KEYS) {
          const value = query[key];
          if (typeof value === "string" && value) next.set(key, value);
        }
      } catch {
        // A corrupt saved query simply clears the filters.
      }
    }
    setSearchParams(next, { replace: true });
  }

  async function deleteSavedFilter() {
    if (!namespace || !appliedSavedFilter) return;
    setSavedFilterBusy(true);
    setActionError(null);
    try {
      await client.deleteSecuritySavedFilter({ namespace, name: appliedSavedFilter });
      setAppliedSavedFilter("");
      await fetchSavedFilters();
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to delete the view");
    } finally {
      setSavedFilterBusy(false);
    }
  }

  function toggleSelected(id: string) {
    setSelectedIds((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function toggleSelectAll() {
    setSelectedIds((current) =>
      current.size === findings.length ? new Set() : new Set(findings.map((f) => f.id)),
    );
  }

  async function applyBulk(update: { status?: string; setAssignee?: boolean; assignee?: string }) {
    if (!namespace || !scan || selectedIds.size === 0) return;
    setBulkBusy(true);
    setActionError(null);
    setBulkFailures([]);
    try {
      const resp = await client.bulkUpdateSecurityFindingStatus({
        namespace,
        scanName: scan.scanName,
        ids: [...selectedIds],
        status: update.status ?? "",
        setAssignee: update.setAssignee ?? false,
        assignee: update.assignee ?? "",
        note: bulkNote.trim(),
      });
      const failures = resp.results.filter((r) => !r.ok);
      setBulkFailures(failures);
      if (failures.length === 0) {
        setSelectedIds(new Set());
        setBulkStatus("");
        setBulkNote("");
        setBulkAssignee("");
      }
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Bulk update failed");
    } finally {
      setBulkBusy(false);
      await Promise.all([fetchFindings(), fetchScan()]);
    }
  }

  async function exportAuditLog() {
    if (!namespace || !scan) return;
    setExportBusy(true);
    setActionError(null);
    try {
      const resp = await client.exportSecurityFindingAuditLog({
        namespace,
        scanName: scan.scanName,
        format: "csv",
      });
      downloadBlob(
        resp.filename || `security-audit-${scan.scanName}.csv`,
        resp.content,
        resp.contentType || "text/csv",
      );
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to export the audit log");
    } finally {
      setExportBusy(false);
    }
  }

  return (
    <ListState
      loading={loading && !scan}
      error={error}
      empty={!scan}
      skeleton={<ListRowSkeleton rows={4} />}
      emptyTitle="Security scan not found"
      emptyDescription="This scan may have been removed or you may not have access."
    >
      {scan && (
        <div className="space-y-7">
          <DetailHeader
            parentLabel="Security"
            parentTo="/security"
            title={scan.runName}
            meta={
              <Badge variant="outline" className="capitalize">
                {scan.status || "unknown"}
              </Badge>
            }
            subtitle={
              <span className="font-mono text-[12.5px] text-muted-foreground">
                {scan.repository}
                {scan.revision && ` @ ${scan.revision.slice(0, 12)}`}
              </span>
            }
            actions={
              <>
                {scanConfig &&
                  scanConfig.namespace === scan.namespace &&
                  scanConfig.name === scan.scanName && (
                  <SecurityScanFormDialog
                    key={`${scanConfig.namespace}/${scanConfig.name}`}
                    duplicateFrom={scanConfig}
                    trigger={
                      <Button variant="outline" size="sm">
                        <Copy />
                        Duplicate scan
                      </Button>
                    }
                  />
                )}
                <Button
                  variant="outline"
                  size="sm"
                  nativeButton={false}
                  render={<Link to={`/runs/${scan.namespace}/${scan.runName}`} />}
                >
                  <SquareArrowOutUpRight />
                  Agent run
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={reportBusy !== null}
                  onClick={() => void downloadReport("markdown")}
                >
                  <FileText />
                  {reportBusy === "markdown" ? "Fetching…" : "Report"}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={reportBusy !== null}
                  onClick={() => void downloadReport("sarif")}
                >
                  <Download />
                  {reportBusy === "sarif" ? "Fetching…" : "SARIF"}
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={exportBusy}
                  onClick={() => void exportAuditLog()}
                >
                  <Download />
                  {exportBusy ? "Exporting…" : "Audit CSV"}
                </Button>
              </>
            }
          />

          {scan.status.toLowerCase() === "running" && (
            <p className="text-[12.5px] text-muted-foreground">
              This scan run has not finished. The Markdown report and SARIF artifact become
              available once the run submits its results.
            </p>
          )}

          {scanConfig && (scanConfig.lastCheck || scanConfig.lastNotifications) && (
            <section
              aria-label="Repository integration"
              className="space-y-2 rounded-lg border border-border/70 bg-muted/20 px-3 py-2.5 text-[12.5px]"
            >
              <p className="font-medium text-foreground">Repository integration</p>
              {scanConfig.lastCheck && (
                <p className="text-muted-foreground">
                  GitHub check on{" "}
                  <span className="font-mono">{scanConfig.lastCheck.revision.slice(0, 12)}</span>:{" "}
                  {scanConfig.lastCheck.error ? (
                    <span className="text-destructive">
                      publish failed — {scanConfig.lastCheck.error} (retried automatically)
                    </span>
                  ) : (
                    <>
                      <Badge variant="outline" className="capitalize">{scanConfig.lastCheck.conclusion}</Badge>
                      {scanConfig.lastCheck.url && (
                        <>
                          {" "}
                          <a
                            className="underline underline-offset-2"
                            href={scanConfig.lastCheck.url}
                            target="_blank"
                            rel="noreferrer"
                          >
                            view check
                          </a>
                        </>
                      )}
                    </>
                  )}
                  {scanConfig.lastCheck.sarifError && (
                    <span className="text-destructive"> · SARIF upload failed — {scanConfig.lastCheck.sarifError}</span>
                  )}
                  {scanConfig.lastCheck.sarifUploaded && " · SARIF uploaded to code scanning"}
                </p>
              )}
              {scanConfig.lastNotifications && (
                <p className="text-muted-foreground">
                  Notifications: {scanConfig.lastNotifications.sent} sent,{" "}
                  {scanConfig.lastNotifications.suppressed} suppressed as duplicates
                  {scanConfig.lastNotifications.lastError ? (
                    <span className="text-destructive">
                      {" "}
                      — last error: {scanConfig.lastNotifications.lastError} (retried automatically)
                    </span>
                  ) : null}
                </p>
              )}
            </section>
          )}
          {reportNotice && (
            <p role="status" className="rounded-lg border border-border/70 bg-muted/30 px-3 py-2 text-[12.5px] text-muted-foreground">
              {reportNotice}
            </p>
          )}

          {scanConfig?.budgetExceeded && (
            <div
              role="alert"
              data-testid="budget-warning"
              className="rounded-lg border border-amber-500/50 bg-amber-500/10 px-3 py-2.5 text-[12.5px]"
            >
              <p className="font-medium">Budget exceeded — new runs of this scan will not start.</p>
              <p className="text-muted-foreground">{scanConfig.budgetMessage}</p>
            </div>
          )}

          {scanConfig && (scanConfig.effectiveBudgets || scanConfig.retention) && (
            <section
              aria-label="Budgets and retention"
              className="space-y-2 rounded-lg border border-border/70 bg-muted/20 px-3 py-2.5 text-[12.5px]"
            >
              <p className="font-medium text-foreground">Budgets &amp; retention</p>
              {scanConfig.effectiveBudgets && (
                <p className="text-muted-foreground" data-testid="effective-budgets">
                  Effective budgets (scan merged with its policy pack):{" "}
                  {packBudgetSummary(scanConfig.effectiveBudgets)}. Platform-observed usage is
                  checked against these limits before and during each run
                  {scanConfig.budgetExceeded ? "" : "; no limit is currently exceeded"}.
                </p>
              )}
              {scanConfig.retention && (
                <p className="text-muted-foreground" data-testid="retention-sweep">
                  Retention sweep
                  {scanConfig.retention.lastSweepTimeUnix > 0n &&
                    ` (last ran ${new Date(Number(scanConfig.retention.lastSweepTimeUnix) * 1000).toLocaleString()})`}
                  : {String(scanConfig.retention.scansPurged)} scan runs,{" "}
                  {String(scanConfig.retention.findingsPurged)} findings, and{" "}
                  {String(scanConfig.retention.reportsPurged)} reports purged;{" "}
                  {String(scanConfig.retention.evidenceRedacted)} evidence and{" "}
                  {String(scanConfig.retention.pocRedacted)} PoC entries redacted;{" "}
                  {String(scanConfig.retention.auditEventsPurged)} audit events purged
                  {scanConfig.retention.moreWork ? " · sweep still in progress" : ""}
                  {scanConfig.retention.lastError ? (
                    <span className="text-destructive">
                      {" "}
                      · last sweep error: {scanConfig.retention.lastError} (retried automatically)
                    </span>
                  ) : null}
                </p>
              )}
            </section>
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

          {namespace &&
            scanConfig?.lastExecution &&
            scanConfig.lastExecution.mode === "deterministic" && (
              <ExecutionProgressPanel
                namespace={namespace}
                execution={scanConfig.lastExecution}
                workflowTasks={workflowTasks}
                findingLinkBase={runName ? `/security/${namespace}/${runName}/findings` : undefined}
                onResume={async () => {
                  setActionError(null);
                  try {
                    await client.resumeSecurityScan({ namespace, name: scanConfig.name });
                    await fetchScan();
                  } catch (e: unknown) {
                    setActionError(
                      e instanceof Error ? e.message : "Failed to resume the execution",
                    );
                  }
                }}
              />
            )}

          {namespace && runName && (
            <SecurityScanRunPanel
              namespace={namespace}
              runName={runName}
              onRunSettled={handleRunSettled}
              hideWhenMissing={Boolean(scanConfig?.lastExecution)}
            />
          )}

          {scan.summary && (
            <DetailSection title="Scan Summary">
              <p className="max-w-[90ch] whitespace-pre-wrap text-[13px] leading-relaxed text-muted-foreground">
                {scan.summary}
              </p>
            </DetailSection>
          )}

          {actionError && (
            <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]">
              {actionError}
            </div>
          )}

          <DetailSection title="Findings">
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
              <select
                aria-label="Filter by baseline state"
                className={filterSelectClass}
                value={baseline}
                onChange={(e) => setFilter("baseline", e.target.value)}
              >
                <option value="">All baselines</option>
                {BASELINE_STATES.map((b) => (
                  <option key={b} value={b}>{b}</option>
                ))}
              </select>
              <select
                aria-label="Filter suppressed findings"
                className={filterSelectClass}
                value={suppressed}
                onChange={(e) => setFilter("suppressed", e.target.value)}
              >
                <option value="">Hide suppressed</option>
                <option value="include">Include suppressed</option>
                <option value="only">Only suppressed</option>
              </select>
              <input
                aria-label="Filter by assignee"
                type="text"
                value={assigneeFilter}
                onChange={(e) => setFilter("assignee", e.target.value)}
                placeholder="Assignee…"
                className="h-8 w-28 rounded-md border border-border/70 bg-background px-2 text-[12.5px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
              />
              <div className="ml-auto flex flex-wrap items-center gap-2">
                <select
                  aria-label="Saved views"
                  className={filterSelectClass}
                  value={appliedSavedFilter}
                  onChange={(e) => applySavedFilter(e.target.value)}
                >
                  <option value="">Saved views…</option>
                  {savedFilters.map((f) => (
                    <option key={f.name} value={f.name}>{f.name}</option>
                  ))}
                </select>
                {appliedSavedFilter && (
                  <Button
                    variant="outline"
                    size="sm"
                    aria-label={`Delete saved view ${appliedSavedFilter}`}
                    disabled={savedFilterBusy}
                    onClick={() => void deleteSavedFilter()}
                  >
                    <X />
                    Delete view
                  </Button>
                )}
                <input
                  aria-label="New saved view name"
                  type="text"
                  value={savedFilterName}
                  onChange={(e) => setSavedFilterName(e.target.value)}
                  placeholder="Save view as…"
                  className="h-8 w-28 rounded-md border border-border/70 bg-background px-2 text-[12.5px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                />
                <Button
                  variant="outline"
                  size="sm"
                  disabled={savedFilterBusy || !savedFilterName.trim()}
                  onClick={() => void saveCurrentFilter()}
                >
                  Save view
                </Button>
              </div>
            </div>
            <div className={cn("mt-4 grid gap-4", selected && "lg:grid-cols-[minmax(0,1fr)_minmax(320px,420px)]")}>
              <ListState
                loading={findingsLoading}
                empty={!findings.length}
                skeleton={<ListRowSkeleton rows={4} />}
                emptyTitle="No findings"
                emptyDescription={
                  severity || status || category || search
                    ? "No findings match the current filters."
                    : "This scan reported no findings."
                }
              >
                <>
                {selectedIds.size > 0 && (
                  <div
                    role="toolbar"
                    aria-label="Bulk actions"
                    className="flex flex-wrap items-center gap-2 rounded-lg border border-border/70 bg-muted/20 px-3 py-2"
                  >
                    <span className="text-[12.5px] font-medium">
                      {selectedIds.size} selected
                    </span>
                    <select
                      aria-label="Bulk status"
                      className={filterSelectClass}
                      value={bulkStatus}
                      disabled={bulkBusy}
                      onChange={(e) => setBulkStatus(e.target.value)}
                    >
                      <option value="">Set status…</option>
                      {FINDING_STATUSES.map((s) => (
                        <option key={s} value={s}>{statusLabel(s)}</option>
                      ))}
                    </select>
                    <input
                      aria-label="Bulk note"
                      type="text"
                      value={bulkNote}
                      disabled={bulkBusy}
                      onChange={(e) => setBulkNote(e.target.value)}
                      placeholder="Audit note (optional)"
                      className="h-8 w-40 rounded-md border border-border/70 bg-background px-2 text-[12.5px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                    />
                    <Button
                      size="sm"
                      disabled={bulkBusy || !bulkStatus}
                      onClick={() => void applyBulk({ status: bulkStatus })}
                    >
                      {bulkBusy ? "Applying…" : "Apply status"}
                    </Button>
                    <input
                      aria-label="Bulk assignee"
                      type="text"
                      value={bulkAssignee}
                      disabled={bulkBusy}
                      onChange={(e) => setBulkAssignee(e.target.value)}
                      placeholder="Assignee"
                      className="h-8 w-32 rounded-md border border-border/70 bg-background px-2 text-[12.5px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                    />
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={bulkBusy || !bulkAssignee.trim()}
                      onClick={() => void applyBulk({ setAssignee: true, assignee: bulkAssignee.trim() })}
                    >
                      Assign
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={bulkBusy}
                      onClick={() => void applyBulk({ setAssignee: true, assignee: "" })}
                    >
                      Clear assignee
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={bulkBusy}
                      onClick={() => {
                        setSelectedIds(new Set());
                        setBulkFailures([]);
                      }}
                    >
                      <X />
                      Clear selection
                    </Button>
                  </div>
                )}
                {bulkFailures.length > 0 && (
                  <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]">
                    <span className="font-medium">
                      Bulk update failed — no findings were changed.
                    </span>
                    <ul className="mt-1 list-disc pl-5 text-muted-foreground">
                      {bulkFailures.map((f) => (
                        <li key={f.id} className="font-mono text-[11.5px]">
                          {f.id}: {f.error}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
                <Table>
                  <TableCaption className="sr-only">Security findings</TableCaption>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-8">
                        <input
                          type="checkbox"
                          aria-label="Select all findings"
                          checked={findings.length > 0 && selectedIds.size === findings.length}
                          onChange={toggleSelectAll}
                        />
                      </TableHead>
                      <TableHead>Title</TableHead>
                      <TableHead>Severity</TableHead>
                      <TableHead>Category</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead>Baseline</TableHead>
                      <TableHead>Assignee</TableHead>
                      <TableHead className="text-right">Score</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {findings.map((finding) => (
                      <TableRow
                        key={finding.id}
                        data-state={finding.id === selectedId ? "selected" : undefined}
                        className="cursor-pointer"
                        onClick={() => setSelectedId(finding.id)}
                      >
                        <TableCell onClick={(e) => e.stopPropagation()}>
                          <input
                            type="checkbox"
                            aria-label={`Select ${finding.title}`}
                            checked={selectedIds.has(finding.id)}
                            onChange={() => toggleSelected(finding.id)}
                          />
                        </TableCell>
                        <TableCell>
                          <button
                            type="button"
                            className="text-left font-medium text-primary hover:underline"
                            onClick={() => setSelectedId(finding.id)}
                          >
                            {finding.title}
                          </button>
                          <div className="mt-0.5 flex items-center gap-2">
                            <span className="truncate font-mono text-[11.5px] text-muted-foreground">
                              {finding.filePath}
                              {finding.startLine > 0 && `:${finding.startLine}`}
                            </span>
                            <Link
                              to={findingHref(finding.id)}
                              onClick={(e) => e.stopPropagation()}
                              className="shrink-0 text-[11.5px] text-muted-foreground underline-offset-2 hover:text-primary hover:underline"
                            >
                              Open full page
                            </Link>
                          </div>
                        </TableCell>
                        <TableCell>
                          <SeverityBadge severity={finding.severity} />
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {finding.category || "—"}
                        </TableCell>
                        <TableCell className="text-sm capitalize text-muted-foreground">
                          <span className="flex items-center gap-1.5">
                            {statusLabel(finding.status)}
                            {finding.status === "accepted_risk" && (
                              <ExpiryBadge ts={finding.acceptedRiskExpiresAt} />
                            )}
                            <SuppressedBadge finding={finding} />
                          </span>
                        </TableCell>
                        <TableCell>
                          <BaselineBadge state={finding.baselineState} />
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {finding.assignee || "—"}
                        </TableCell>
                        <TableCell className="text-right font-mono tabular-nums">
                          {finding.score.toFixed(1)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
                </>
              </ListState>

              {selected && (
                <aside
                  aria-label="Finding details"
                  className="surface-card h-fit space-y-4 rounded-xl border border-border/60 bg-muted/10 p-4"
                >
                  <div className="flex items-start justify-between gap-2">
                    <div className="min-w-0 space-y-1">
                      <h3 className="text-[14px] font-semibold leading-snug">{selected.title}</h3>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <SeverityBadge severity={selected.severity} />
                        {selected.category && (
                          <Badge variant="outline" className="text-[11px]">{selected.category}</Badge>
                        )}
                        <SuppressedBadge finding={selected} />
                      </div>
                    </div>
                    <div className="flex shrink-0 items-center gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label="Open finding full page"
                        nativeButton={false}
                        render={<Link to={findingHref(selected.id)} />}
                      >
                        <SquareArrowOutUpRight className="size-4" />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label="Close finding details"
                        onClick={() => setSelectedId(null)}
                      >
                        <X className="size-4" />
                      </Button>
                    </div>
                  </div>

                  <div className="flex items-center gap-2">
                    <label
                      htmlFor="finding-status"
                      className="text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70"
                    >
                      Status
                    </label>
                    <select
                      id="finding-status"
                      className={filterSelectClass}
                      value={selected.status}
                      disabled={statusSaving}
                      onChange={(e) => void changeStatus(selected, e.target.value)}
                    >
                      {FINDING_STATUSES.map((s) => (
                        <option key={s} value={s}>{statusLabel(s)}</option>
                      ))}
                    </select>
                  </div>

                  <div className="space-y-1.5">
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={bundleBusy}
                      onClick={() => void downloadSubmissionBundle(selected)}
                    >
                      <Download />
                      {bundleBusy ? "Fetching bundle…" : "Download bounty bundle"}
                    </Button>
                    {bundleNotice && (
                      <p role="status" className="break-all text-[11px] text-muted-foreground">
                        {bundleNotice}
                      </p>
                    )}
                  </div>

                  {selected.suppressedBy && (
                    <p
                      role="note"
                      data-testid="finding-suppression-note"
                      className="rounded-md border border-violet-500/40 bg-violet-500/10 px-2.5 py-2 text-[12px]"
                    >
                      <span className="font-medium">Suppressed by policy</span> —{" "}
                      {suppressionSummary(selected)}
                      {selected.suppressedReason ? `. Reason: ${selected.suppressedReason}` : ""}
                    </p>
                  )}

                  {selected.description && (
                    <FindingText label="Description" text={selected.description} />
                  )}
                  {selected.impact && <FindingText label="Impact" text={selected.impact} />}
                  {selected.attackVector && (
                    <FindingText label="Attack Vector" text={selected.attackVector} />
                  )}
                  {selected.remediation && (
                    <FindingText label="Remediation" text={selected.remediation} />
                  )}

                  <FactList>
                    <Fact
                      label="Location"
                      mono
                      value={
                        selected.filePath
                          ? `${selected.filePath}${selected.startLine > 0 ? `:${selected.startLine}${selected.endLine > selected.startLine ? `-${selected.endLine}` : ""}` : ""}`
                          : "—"
                      }
                    />
                    {selected.symbol && <Fact label="Symbol" mono value={selected.symbol} />}
                    <Fact label="Score" mono value={selected.score.toFixed(1)} />
                    <Fact label="Confidence" value={selected.confidence || "—"} />
                    <Fact label="Source Agent" mono value={selected.sourceAgent || "—"} />
                    <Fact label="Occurrences" mono value={String(selected.occurrences)} />
                    <Fact label="First Seen" value={formatSeen(selected.firstSeenAt)} />
                    <Fact label="Last Seen" value={formatSeen(selected.lastSeenAt)} />
                    {selected.cwe.length > 0 && (
                      <Fact
                        label="CWE"
                        value={
                          <span className="flex flex-wrap gap-x-3 gap-y-1">
                            {selected.cwe.map((cwe) => (
                              <FactLink key={cwe} href={cweUrl(cwe)}>{cwe}</FactLink>
                            ))}
                          </span>
                        }
                      />
                    )}
                    {selected.references.length > 0 && (
                      <Fact
                        label="References"
                        value={
                          <span className="flex flex-col gap-1">
                            {selected.references.map((ref) => (
                              <FactLink key={ref} href={ref}>{ref}</FactLink>
                            ))}
                          </span>
                        }
                      />
                    )}
                  </FactList>

                  {selected.raw && (
                    <details className="text-[12px]">
                      <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
                        Evidence
                      </summary>
                      <pre className="mt-2 max-h-64 overflow-auto rounded-md border border-border/60 bg-muted/30 p-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-all">
                        {selected.raw}
                      </pre>
                    </details>
                  )}

                  <section aria-label="Finding history" className="space-y-1.5">
                    <h4 className="text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
                      History
                    </h4>
                    {eventsLoading ? (
                      <p className="text-[12px] text-muted-foreground">Loading history…</p>
                    ) : eventsError ? (
                      <p role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-2 py-1.5 text-[12px]">
                        {eventsError}
                      </p>
                    ) : events.length === 0 ? (
                      <p className="text-[12px] text-muted-foreground">No history recorded.</p>
                    ) : (
                      <ul className="space-y-1.5">
                        {events.map((event) => (
                          <li key={String(event.id)} className="text-[12px] leading-relaxed">
                            <span className="font-medium capitalize">{statusLabel(event.eventType)}</span>
                            {event.actor && (
                              <span className="text-muted-foreground"> · {event.actor}</span>
                            )}
                            {event.createdAt && (
                              <span className="text-muted-foreground"> · {formatSeen(event.createdAt)}</span>
                            )}
                            {event.note && (
                              <div className="text-muted-foreground">{event.note}</div>
                            )}
                          </li>
                        ))}
                      </ul>
                    )}
                  </section>
                </aside>
              )}
            </div>
          </DetailSection>
        </div>
      )}
    </ListState>
  );
}

function FindingText({ label, text }: { label: string; text: string }) {
  return (
    <div className="space-y-1">
      <h4 className="text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
        {label}
      </h4>
      <p className="whitespace-pre-wrap text-[12.5px] leading-relaxed text-foreground/90">{text}</p>
    </div>
  );
}
