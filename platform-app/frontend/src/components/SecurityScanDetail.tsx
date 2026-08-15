/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Copy, Download, FileText, FilterX, Info, SquareArrowOutUpRight, X } from "lucide-react";

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
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useUrlFilters } from "@/hooks/useUrlFilters";
import { SEVERITY_ORDER, optionsFrom } from "@/lib/securityFilters";
import {
  DetailHeader, DetailSection, FactList, Fact, FactLink,
} from "@/components/detail-page";
import {
  SEVERITIES, STATUS_PILL, SeverityBadge, severityTone,
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
import { toneColor, toneSoft, type StatusTone } from "@/lib/status";
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

const filterInputClass =
  "h-7 w-32 rounded-lg border border-input bg-background px-2 text-[12px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 dark:bg-input/30";

/**
 * Canonical finding-filter contract, shared with the configuration and finding
 * detail pages. `selected` is the finding shown in the split panel: it lives in
 * the URL so a specific finding view is shareable and survives a reload.
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

/** Keys "Clear" resets and saved views persist; `selected` is not a filter. */
const FILTER_KEYS = [
  "q", "severity", "status", "category", "tool", "file", "baseline", "assignee",
  "suppressed", "dupes",
] as const;

/**
 * Filters that narrow the table to a subset of the run. While any of them is
 * set, the table count is expected to be smaller than the run-wide actionable
 * total, so reconciling the two numbers would be noise.
 */
const NARROWING_FILTER_KEYS = [
  "q", "severity", "category", "tool", "file", "baseline", "assignee",
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
  { value: "include", label: "Shown" },
  { value: "only", label: "Only" },
];

const DUPES_FILTER_OPTIONS: FilterOption[] = [
  { value: "include", label: "Duplicates" },
];

/**
 * Selected chips have to win against the toolbar's own tinted surface, so the
 * pressed state gets a full-strength border and label while the unpressed one
 * keeps readable (not disabled-looking) foreground text.
 */
const CHIP_STATES = cn(
  "[&_button]:font-medium",
  "[&_button[aria-pressed=false]]:border-border [&_button[aria-pressed=false]]:text-foreground/75",
  "[&_button[aria-pressed=false]]:hover:border-primary/40 [&_button[aria-pressed=false]]:hover:bg-muted/60",
  "[&_button[aria-pressed=true]]:border-primary [&_button[aria-pressed=true]]:bg-primary/20",
  "[&_button[aria-pressed=true]]:text-foreground [&_button[aria-pressed=true]]:font-semibold",
);

/** "Actionable" drives the default status filter, so it has to be explainable. */
const ACTIONABLE_HELP =
  "Findings that still need work: open, triaged, or confirmed. Fixed, false positive, "
  + "accepted-risk, and suppressed findings are excluded.";

/** Statuses where the run has not yet submitted its report artifacts. */
const IN_FLIGHT_SCAN_STATUSES = new Set(["pending", "queued", "running"]);

const ARTIFACTS_PENDING_HINT =
  "Available once the scan run finishes and submits its results.";

function scanStatusTone(status: string): StatusTone {
  switch (status.toLowerCase()) {
    case "completed":
    case "succeeded":
      return "success";
    case "running":
    case "pending":
    case "queued":
      return "running";
    case "failed":
    case "error":
      return "danger";
    default:
      return "neutral";
  }
}

const FINDING_STATUS_TONES: Record<string, StatusTone> = {
  open: "warning",
  triaged: "info",
  confirmed: "danger",
  false_positive: "neutral",
  fixed: "success",
  accepted_risk: "purple",
};

const RECOVERY_LINKS = [
  { to: "/security/runs", label: "Back to scan runs" },
  { to: "/security", label: "Security overview" },
];

const bundlePollIntervalMs = 2_000;
const bundlePollTimeoutMs = 5 * 60_000;

function wait(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(signal.reason);
      return;
    }
    const onAbort = () => {
      window.clearTimeout(timer);
      reject(signal.reason);
    };
    const timer = window.setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

export function SecurityScanDetail() {
  const { namespace, runName } = useParams<{ namespace: string; runName: string }>();

  const [scan, setScan] = useState<SecurityScan | null>(null);
  const [scanConfig, setScanConfig] = useState<SecurityScanConfig | null>(null);
  const [workflowTasks, setWorkflowTasks] = useState<SecurityScanTaskConfig[]>([]);
  const [summary, setSummary] = useState<Record<string, number>>({});
  const [findings, setFindings] = useState<SecurityFinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [findingsLoading, setFindingsLoading] = useState(true);
  const [error, setError] = useState("");
  const [findingsError, setFindingsError] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);

  // Filters live in the URL so a shared link reproduces the same view and
  // the finding detail page can hand the same context back on return.
  const { values, set, setMany, isActive, activeCount, queryString } = useUrlFilters(FILTER_SPEC);
  const { severity, status, category, baseline, assignee: assigneeFilter, suppressed } = values;
  const search = values.q;

  const findingHref = useCallback(
    (id: string) => `/security/${namespace}/${runName}/findings/${id}${queryString}`,
    [namespace, runName, queryString],
  );

  const selectedId = values.selected || null;
  const selectFinding = useCallback((id: string) => set("selected", id), [set]);

  // `tool` and `file` have no server-side equivalent on listSecurityFindings,
  // so they narrow the page it returned.
  const visibleFindings = useMemo(() => {
    const file = values.file.trim().toLowerCase();
    return findings.filter((finding) => {
      if (values.tool !== "all" && finding.sourceAgent !== values.tool) return false;
      if (file && !finding.filePath.toLowerCase().includes(file)) return false;
      return true;
    });
  }, [findings, values.tool, values.file]);

  // The severity tiles count every actionable finding the run recorded, while
  // the table only lists the ones the default filters admit — suppressed and
  // duplicate findings are hidden. Left unsaid, "5 actionable" next to "4 of 4
  // findings" reads as a contradiction, so name the gap and offer the switch.
  const hiddenActionable = useMemo(() => {
    if (findingsLoading || status !== "actionable" || suppressed === "only") return null;
    const hidesSuppressed = suppressed === "exclude";
    const hidesDuplicates = values.dupes !== "include";
    if (!hidesSuppressed && !hidesDuplicates) return null;
    if (NARROWING_FILTER_KEYS.some((key) => isActive(key))) return null;
    const total = summary["actionable"] ?? 0;
    const hidden = total - visibleFindings.length;
    if (hidden <= 0) return null;
    const kind = hidesSuppressed && hidesDuplicates
      ? "suppressed and duplicate"
      : hidesSuppressed
        ? "suppressed"
        : "duplicate";
    return {
      shown: visibleFindings.length,
      total,
      reason: `${hidden} hidden — ${kind} findings are excluded by default`,
      action: hidesSuppressed && hidesDuplicates
        ? "Show hidden"
        : hidesSuppressed
          ? "Show suppressed"
          : "Show duplicates",
    };
  }, [
    findingsLoading, status, suppressed, values.dupes, isActive, summary,
    visibleFindings.length,
  ]);

  const selected = visibleFindings.find((finding) => finding.id === selectedId) ?? null;
  const selectedPresent = selected !== null;
  const [statusSaving, setStatusSaving] = useState(false);

  const [reportBusy, setReportBusy] = useState<"markdown" | "sarif" | null>(null);
  const [reportNotice, setReportNotice] = useState<string | null>(null);
  const [bundleBusy, setBundleBusy] = useState(false);
  const [bundleNotice, setBundleNotice] = useState<string | null>(null);
  const bundleAbortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    setBundleNotice(null);
    return () => bundleAbortRef.current?.abort();
  }, [selectedId, selectedPresent]);

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
        severity: severity === "all" ? "" : severity,
        status: status === "all" ? "" : status,
        category: category === "all" ? "" : category,
        search,
        baselineState: baseline === "all" ? "" : baseline,
        assignee: assigneeFilter,
        suppressed,
        includeDuplicates: values.dupes === "include",
      });
      setFindings(resp.findings);
      setFindingsError("");
    } catch (e: unknown) {
      if (!background) {
        setFindingsError(e instanceof Error ? e.message : "Failed to load security findings");
      }
    } finally {
      if (!background) setFindingsLoading(false);
    }
  }, [
    namespace, runName, severity, status, category, search, baseline, assigneeFilter,
    suppressed, values.dupes,
  ]);

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
    bundleAbortRef.current?.abort();
    const controller = new AbortController();
    bundleAbortRef.current = controller;
    const deadline = Date.now() + bundlePollTimeoutMs;
    setBundleNotice(null);
    setBundleBusy(true);
    try {
      let resp;
      while (!controller.signal.aborted) {
        resp = await client.getSecurityFindingSubmissionBundle({
          namespace,
          findingId: finding.id,
        }, { signal: controller.signal });
        if (resp.status !== "generating") break;
        setBundleNotice("Bundle is generating. The download will start automatically when it is ready.");
        const remaining = deadline - Date.now();
        if (remaining <= 0) break;
        await wait(Math.min(bundlePollIntervalMs, remaining), controller.signal);
      }
      if (!resp || controller.signal.aborted) return;
      if (resp.status !== "ready" || resp.content.length === 0) {
        const message = resp.status === "generating"
          ? "Bundle is still generating. Try again shortly."
          : resp.error || `Bundle is ${resp.status || "unavailable"}.`;
        setBundleNotice(message);
        return;
      }
      downloadBlob(
        resp.filename || `${finding.fingerprint}-bounty-submission.zip`,
        resp.content,
        "application/zip",
      );
      setBundleNotice(resp.sha256 ? `Downloaded. SHA-256: ${resp.sha256}` : "Downloaded.");
    } catch (e: unknown) {
      if (!controller.signal.aborted) {
        setBundleNotice(e instanceof Error ? e.message : "Failed to fetch the bounty bundle");
      }
    } finally {
      if (bundleAbortRef.current === controller) {
        bundleAbortRef.current = null;
        setBundleBusy(false);
      }
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

  const clearFilters = useCallback(() => {
    setMany(Object.fromEntries(FILTER_KEYS.map((key) => [key, FILTER_SPEC[key]])));
  }, [setMany]);

  function currentFilterQuery(): string {
    const query: Record<string, string> = {};
    for (const key of FILTER_KEYS) {
      if (isActive(key)) query[key] = values[key];
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
    const patch: Record<string, string> = Object.fromEntries(
      FILTER_KEYS.map((key) => [key, FILTER_SPEC[key]]),
    );
    if (filter) {
      try {
        const query = JSON.parse(filter.query || "{}") as Record<string, unknown>;
        for (const key of FILTER_KEYS) {
          const value = query[key];
          if (typeof value === "string" && value) patch[key] = value;
        }
      } catch {
        // A corrupt saved query simply clears the filters.
      }
    }
    setMany(patch);
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
      current.size === visibleFindings.length
        ? new Set()
        : new Set(visibleFindings.map((f) => f.id)),
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

  if (!namespace || !runName) {
    return (
      <DetailErrorState
        kind="not-found"
        title="Scan run not found"
        description="This link is missing the namespace or the scan run name."
        links={RECOVERY_LINKS}
      />
    );
  }

  if (loading && !scan) {
    return (
      <div role="status" aria-live="polite">
        <ListRowSkeleton rows={4} />
      </div>
    );
  }

  if (!scan) {
    const kind = error ? classifyDetailError(error) : "not-found";
    return (
      <DetailErrorState
        kind={kind}
        title={kind === "not-found" ? "Scan run not found" : undefined}
        description={
          kind === "not-found"
            ? `No scan run named "${runName}" exists in ${namespace}. It may have been deleted by retention, or the link may point at another namespace.`
            : undefined
        }
        detail={error || undefined}
        onRetry={() => {
          void fetchScan();
          void fetchFindings();
        }}
        links={RECOVERY_LINKS}
      />
    );
  }

  const filtersActive = activeCount(["selected"]);
  const filteredEmpty = visibleFindings.length === 0 && filtersActive > 0;
  const artifactsReady = !IN_FLIGHT_SCAN_STATUSES.has(scan.status.toLowerCase());
  const exportHint = artifactsReady ? undefined : ARTIFACTS_PENDING_HINT;

  return (
    <div className="space-y-7">
      <DetailHeader
        parentLabel="Security"
        parentTo="/security"
        title={scan.runName}
        meta={<ScanStatusPill status={scan.status} />}
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
                  <Button variant="ghost" size="sm">
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
            {/* One secondary download cluster instead of three peer buttons
                competing with the title. The hint lives on the group as well as
                each control because a disabled button never gets a hover. */}
            <span
              title={exportHint}
              className="inline-flex items-center gap-0.5 rounded-lg border border-border/70 bg-muted/20 p-0.5"
            >
              <span className="pl-1.5 pr-0.5 text-[10.5px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
                Download
              </span>
              <Button
                variant="ghost"
                size="sm"
                className="h-7"
                title={exportHint}
                disabled={!artifactsReady || reportBusy !== null}
                onClick={() => void downloadReport("markdown")}
              >
                <FileText />
                {reportBusy === "markdown" ? "Fetching…" : "Report"}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="h-7"
                title={exportHint}
                disabled={!artifactsReady || reportBusy !== null}
                onClick={() => void downloadReport("sarif")}
              >
                <Download />
                {reportBusy === "sarif" ? "Fetching…" : "SARIF"}
              </Button>
              <Button
                variant="ghost"
                size="sm"
                className="h-7"
                title={exportHint}
                disabled={!artifactsReady || exportBusy}
                onClick={() => void exportAuditLog()}
              >
                <Download />
                {exportBusy ? "Exporting…" : "Audit CSV"}
              </Button>
            </span>
          </>
        }
      />

      {!artifactsReady && (
        <p className="text-[12.5px] text-muted-foreground">
          This scan run has not finished. The Markdown report and SARIF artifact become
          available once the run submits its results.
        </p>
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

      {actionError && (
        <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]">
          {actionError}
        </div>
      )}

      {/* The severity summary is the answer to "what did this scan find?", so it
          sits directly under the header and the findings workspace follows it;
          execution, run, and configuration detail live below the table. */}
      <section aria-label="Finding summary" className="space-y-3">
        <div className="flex flex-wrap items-stretch gap-2">
          <SummaryTile label="Total" value={summary["total"] ?? 0} />
          <SummaryTile
            label="Actionable"
            value={summary["actionable"] ?? summary["open"] ?? 0}
            help={ACTIONABLE_HELP}
          />
          <span aria-hidden className="mx-1 hidden w-px self-stretch bg-border/60 sm:block" />
          {SEVERITIES.map((s) => (
            <SummaryTile
              key={s}
              label={s}
              value={summary[`actionable_${s}`] ?? summary[s] ?? 0}
              tone={severityTone(s)}
            />
          ))}
        </div>
        {scan.summary && (
          <p className="max-w-[90ch] whitespace-pre-wrap text-[13px] leading-relaxed text-muted-foreground">
            {scan.summary}
          </p>
        )}
      </section>

      <DetailSection title="Findings">
        {/* Sticky so the filters stay reachable while a long findings list
            scrolls under them. The background is opaque: rows pass behind it. */}
        <div className="sticky top-0 z-20 -mx-1 space-y-2 bg-background px-1 py-2">
          <div className="flex flex-wrap items-center gap-2">
            <ListSearchInput
              value={search}
              onChange={(value) => set("q", value)}
              placeholder="Search findings…"
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
          <FilterBar
            label="Finding filters"
            activeCount={filtersActive}
            onClear={clearFilters}
            resultLabel={`${visibleFindings.length} of ${findings.length} ${findings.length === 1 ? "finding" : "findings"}`}
          >
            <ChipGroup label="Severity">
              <FilterChips
                label="Severity"
                className={CHIP_STATES}
                options={SEVERITY_CHIP_OPTIONS}
                selected={severity === "all" ? [] : [severity]}
                onToggle={(value) => set("severity", severity === value ? "all" : value)}
              />
            </ChipGroup>
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
            {/* Suppressed and duplicates are visibility switches, not lookups:
                as dropdowns they read as "Suppressed · Hidden", which says
                nothing about what picking the other value would do. */}
            <ChipGroup label="Suppressed">
              <FilterChips
                label="Suppressed findings"
                className={CHIP_STATES}
                options={SUPPRESSED_FILTER_OPTIONS}
                selected={[suppressed]}
                onToggle={(value) => set("suppressed", value)}
              />
            </ChipGroup>
            <FilterChips
              label="Duplicates"
              className={CHIP_STATES}
              options={DUPES_FILTER_OPTIONS}
              selected={values.dupes === "include" ? ["include"] : []}
              onToggle={() => set("dupes", values.dupes === "include" ? "hide" : "include")}
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
              value={assigneeFilter}
              onChange={(e) => set("assignee", e.target.value)}
              placeholder="Assignee…"
              className={filterInputClass}
            />
          </FilterBar>
        </div>
        {hiddenActionable && (
          <div
            role="status"
            data-testid="hidden-findings-notice"
            className="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg border border-border/70 bg-muted/20 px-3 py-2 text-[12.5px] text-muted-foreground"
          >
            <span>
              Showing{" "}
              <span className="font-medium text-foreground">{hiddenActionable.shown}</span> of{" "}
              <span className="font-medium text-foreground">{hiddenActionable.total}</span>{" "}
              actionable findings
            </span>
            <span aria-hidden>·</span>
            <span>{hiddenActionable.reason}</span>
            <Button
              variant="outline"
              size="sm"
              className="h-6"
              onClick={() => setMany({ suppressed: "include", dupes: "include" })}
            >
              {hiddenActionable.action}
            </Button>
          </div>
        )}
        <div className={cn("mt-4 grid gap-4", selected && "lg:grid-cols-[minmax(0,1fr)_minmax(320px,420px)]")}>
          <ListState
            loading={findingsLoading}
            error={findingsError}
            empty={!visibleFindings.length}
            skeleton={<ListRowSkeleton rows={4} />}
            onRetry={() => void fetchFindings()}
            emptyIcon={filteredEmpty ? <FilterX className="size-6" /> : undefined}
            emptyTitle={filteredEmpty ? "No findings match these filters" : "No findings"}
            emptyDescription={
              filteredEmpty
                ? "Clear the filters to see everything this scan reported."
                : "This scan reported no findings."
            }
            emptyAction={
              filteredEmpty ? (
                <Button variant="outline" size="sm" onClick={clearFilters}>
                  <FilterX />
                  Clear filters
                </Button>
              ) : undefined
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
              <TableCaption className="sr-only">
                Security findings{filtersActive > 0 ? " matching the current filters" : ""}
              </TableCaption>
              {/* Not sticky: the shared table wrapper is a scroll container
                  (overflow-x), so a sticky offset here resolves against that
                  never-scrolled box and only pushes the header down over the
                  first rows. The filter bar above carries the sticky duty. */}
              <TableHeader>
                <TableRow>
                  <TableHead className="w-8">
                    <input
                      type="checkbox"
                      aria-label="Select all findings"
                      checked={visibleFindings.length > 0 && selectedIds.size === visibleFindings.length}
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
                {visibleFindings.map((finding) => (
                  // The row itself is the control: focusable, activated with
                  // Enter/Space, and marked selected for assistive tech.
                  <TableRow
                    key={finding.id}
                    tabIndex={0}
                    aria-selected={finding.id === selectedId}
                    data-state={finding.id === selectedId ? "selected" : undefined}
                    className="cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/60"
                    onClick={() => selectFinding(finding.id)}
                    onKeyDown={(e) => {
                      if (e.key !== "Enter" && e.key !== " ") return;
                      e.preventDefault();
                      selectFinding(finding.id);
                    }}
                  >
                    <TableCell onClick={(e) => e.stopPropagation()}>
                      <input
                        type="checkbox"
                        aria-label={`Select ${finding.title}`}
                        checked={selectedIds.has(finding.id)}
                        onChange={() => toggleSelected(finding.id)}
                      />
                    </TableCell>
                    <TableCell className="max-w-[42ch]">
                      <span
                        className="block truncate font-medium text-foreground"
                        title={finding.title}
                      >
                        {finding.title}
                      </span>
                      <div className="mt-0.5 flex items-center gap-2">
                        <span
                          className="min-w-0 truncate font-mono text-[11.5px] text-muted-foreground"
                          title={finding.filePath}
                        >
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
                    <TableCell className="max-w-[16ch] text-sm text-muted-foreground">
                      <span className="block truncate" title={finding.category}>
                        {finding.category || "—"}
                      </span>
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">
                      <span className="flex items-center gap-1.5">
                        <StatusPill status={finding.status} />
                        {finding.status === "accepted_risk" && (
                          <ExpiryBadge ts={finding.acceptedRiskExpiresAt} />
                        )}
                        <SuppressedBadge finding={finding} />
                      </span>
                    </TableCell>
                    <TableCell>
                      <BaselineBadge state={finding.baselineState} />
                    </TableCell>
                    <TableCell className="max-w-[16ch] text-sm text-muted-foreground">
                      <span className="block truncate" title={finding.assignee || undefined}>
                        {finding.assignee || "—"}
                      </span>
                    </TableCell>
                    <TableCell className="text-right font-mono tabular-nums whitespace-nowrap">
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
                    onClick={() => set("selected", "")}
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
                  {bundleBusy ? "Waiting for bundle…" : "Download bounty bundle"}
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

    </div>
  );
}

/**
 * Status pill matching the severity pill's geometry so a row reads as one band
 * of chips; the tone lives in the dot, keeping severity the only tinted pill.
 */
function StatusPill({ status }: { status: string }) {
  const tone = FINDING_STATUS_TONES[status] ?? "neutral";
  return (
    <span
      className={cn(
        STATUS_PILL,
        "capitalize bg-muted/60 text-foreground/90 ring-1 ring-inset ring-border/70",
      )}
    >
      <span
        aria-hidden
        className="size-1.5 rounded-full"
        style={{ backgroundColor: toneColor[tone] }}
      />
      {statusLabel(status)}
    </span>
  );
}

/** Run status, promoted to the loudest element in the header after the title. */
function ScanStatusPill({ status }: { status: string }) {
  const tone = scanStatusTone(status);
  return (
    <span
      className={cn(
        "inline-flex h-6 items-center gap-1.5 rounded-full px-2.5 text-[12px] font-semibold capitalize",
        toneSoft[tone],
      )}
    >
      <span
        aria-hidden
        className={cn("size-1.5 rounded-full", tone === "running" && "animate-pulse")}
        style={{ backgroundColor: toneColor[tone] }}
      />
      {status || "unknown"}
    </span>
  );
}

/**
 * One treatment for every summary number. Zero counts drop their surface so a
 * clean severity reads as absence instead of as a result worth looking at.
 */
function SummaryTile({
  label,
  value,
  tone,
  help,
}: {
  label: string;
  value: number;
  tone?: StatusTone;
  help?: string;
}) {
  const zero = value === 0;
  return (
    <div
      className={cn(
        "flex min-w-[88px] flex-col gap-1.5 rounded-lg border px-3 py-2",
        zero ? "border-border/40" : "border-border/70 bg-muted/25",
      )}
    >
      <span className="flex items-center gap-1 text-[10.5px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
        {tone && (
          <span
            aria-hidden
            className={cn("size-1.5 rounded-full", zero && "opacity-40")}
            style={{ backgroundColor: toneColor[tone] }}
          />
        )}
        {label}
        {help && <HelpHint label={`What "${label}" means`} text={help} />}
      </span>
      <span
        className={cn(
          "font-mono text-[20px] font-semibold leading-none tabular-nums",
          zero ? "text-muted-foreground/40" : "text-foreground",
        )}
      >
        {value}
      </span>
    </div>
  );
}

function HelpHint({ label, text }: { label: string; text: string }) {
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <button
            type="button"
            aria-label={label}
            title={text}
            className="inline-flex rounded-full text-muted-foreground/60 transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
          >
            <Info className="size-3" aria-hidden />
          </button>
        }
      />
      <TooltipContent>{text}</TooltipContent>
    </Tooltip>
  );
}

/** Visible caption for a chip group, so it matches the labelled dropdowns. */
function ChipGroup({ label, children }: { label: string; children: ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className="text-[11px] text-muted-foreground/80">{label}</span>
      {children}
    </span>
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
