/* eslint-disable react-refresh/only-export-components, react-hooks/set-state-in-effect */
import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { clone, create } from "@bufbuild/protobuf";
import {
  ArrowDown,
  ArrowUp,
  ArrowUpDown,
  CircleStop,
  Copy,
  History,
  MoreHorizontal,
  Pause,
  Pencil,
  Play,
  Plus,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { FilterBar, FilterSelect, type FilterOption } from "@/components/ui/filter-bar";
import { ReadyBadge } from "@/components/ReadyBadge";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { ResourceListPage } from "@/components/list-page";
import { SecurityNav } from "@/components/SecurityNav";
import { ProgramTargetImportDialog } from "@/components/ProgramTargetImportDialog";
import { EmptyCell, SeverityCountBadges, STATUS_PILL } from "@/components/SecurityScanList";
import { toast } from "@/components/ui/toaster";
import {
  scanConfigUsesSavedCredentials,
  SecurityScanFormDialog,
} from "@/components/SecurityScanFormDialog";
import { client } from "@/lib/client";
import { formatScheduleTime } from "@/lib/format";
import { toneSoft } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { ProgramScanTarget } from "@/lib/programTargetCatalog";
import {
  hasSeverityAtLeast,
  optionsFrom,
  repoLabel,
  severityCountTotal,
  SEVERITY_FILTER_OPTIONS,
  SEVERITY_ORDER,
  TIME_RANGE_OPTIONS,
  topSeverity,
  withinTimeRange,
} from "@/lib/securityFilters";
import { useNow } from "@/hooks/useNow";
import { useUrlFilters } from "@/hooks/useUrlFilters";
import {
  SecurityScanConfigSchema,
  SecurityScanConfigSpecSchema,
  UpdateSecurityScanRequestSchema,
  type SecurityProgramResource,
  type SecurityScanConfig,
} from "@/rpc/platform/service_pb";

/** Every filter and the sort live in the URL, so a view is shareable. */
const FILTER_SPEC = {
  q: "",
  status: "all",
  findings: "all",
  scanned: "all",
  schedule: "all",
  program: "all",
  repo: "all",
  sort: "scanned",
} as const;

const STATUS_OPTIONS: FilterOption[] = [
  { value: "all", label: "Any status" },
  { value: "ready", label: "Ready" },
  { value: "suspended", label: "Suspended" },
  { value: "attention", label: "Needs attention" },
];

const FINDINGS_OPTIONS: FilterOption[] = [
  { value: "all", label: "Any findings" },
  { value: "any", label: "Has findings" },
  { value: "none", label: "No findings" },
  // Severity labels come from the shared vocabulary and mean "or worse", the
  // same way the runs list reads them.
  ...SEVERITY_FILTER_OPTIONS.filter((option) => option.value !== "all"),
];

const SCANNED_OPTIONS: FilterOption[] = [
  ...TIME_RANGE_OPTIONS.map((option) =>
    option.value === "all" ? { value: "all", label: "Scanned any time" } : option,
  ),
  { value: "never", label: "Never scanned" },
];

const SCHEDULE_OPTIONS: FilterOption[] = [
  { value: "all", label: "Any schedule" },
  { value: "recurring", label: "Recurring" },
  { value: "once", label: "One-time" },
  { value: "manual", label: "Manual only" },
];

const SORT_OPTIONS = [
  { value: "name", label: "Name", direction: "ascending" as const },
  { value: "scanned", label: "Last Scan", direction: "descending" as const },
  { value: "findings", label: "Findings", direction: "descending" as const },
];

export type ScanConfigFilters = {
  status: string;
  findings: string;
  scanned: string;
  schedule: string;
  program: string;
  repo: string;
};

function configRepoUrl(config: SecurityScanConfig): string {
  return config.spec?.repoUrl || config.spec?.targetUrl || "";
}

/**
 * How the configuration is triggered: "manual" (never runs on its own),
 * "recurring" (has a cron schedule), or "once" (a single unscheduled run).
 * The schedule filter and the row label read from this same function so they
 * can never disagree.
 */
export function scheduleKind(config: SecurityScanConfig): "manual" | "recurring" | "once" {
  if (config.spec?.manualOnly) return "manual";
  return config.spec?.schedule.trim() ? "recurring" : "once";
}

/** "@daily UTC", "One-time", or "Manual only" — the schedule in one phrase. */
function scheduleSummary(config: SecurityScanConfig): string {
  const spec = config.spec;
  if (spec?.manualOnly) return "Manual only";
  const schedule = spec?.schedule.trim() ?? "";
  if (!schedule) return "One-time";
  const zone = spec?.timeZone.trim();
  return zone ? `${schedule} ${zone}` : schedule;
}

function isSuspended(config: SecurityScanConfig): boolean {
  return config.spec?.suspend ?? false;
}

/**
 * A coordinator run reports through the trigger's own phase; a deterministic
 * execution reports through `lastExecution`, so a scan is running when either
 * says so.
 */
function isRunning(config: SecurityScanConfig): boolean {
  return config.phase === "Running" || config.lastExecution?.phase === "Running";
}

function configKey(config: SecurityScanConfig): string {
  return `${config.namespace}/${config.name}`;
}

type BulkAction = "run" | "stop" | "suspend" | "resume" | "delete";

const BULK_LABELS: Record<BulkAction, string> = {
  run: "Run now",
  stop: "Stop",
  suspend: "Suspend",
  resume: "Resume",
  delete: "Delete",
};

/**
 * The selected configurations a bulk action can actually apply to. "Run" stays
 * available for a row that is already running (the backend queues or rejects
 * it per its own rules); only suspension gates it.
 */
function eligibleFor(action: BulkAction, configs: SecurityScanConfig[]): SecurityScanConfig[] {
  if (action === "run" || action === "suspend") return configs.filter((config) => !isSuspended(config));
  if (action === "stop") return configs.filter(isRunning);
  if (action === "resume") return configs.filter(isSuspended);
  return configs;
}

function isReady(config: SecurityScanConfig): boolean {
  return !isSuspended(config) && config.conditionReady.toLowerCase() === "true";
}

function lastScanMs(config: SecurityScanConfig): number {
  return Number(config.lastScanTimeUnix) * 1000;
}

export function filterScanConfigs(
  configs: SecurityScanConfig[],
  filters: ScanConfigFilters,
  nowMs: number,
): SecurityScanConfig[] {
  return configs.filter((config) => {
    if (filters.status === "ready" && !isReady(config)) return false;
    if (filters.status === "suspended" && !isSuspended(config)) return false;
    if (filters.status === "attention" && (isReady(config) || isSuspended(config))) return false;

    const findingTotal = severityCountTotal(config.findingCounts);
    if (filters.findings === "any" && findingTotal === 0) return false;
    if (filters.findings === "none" && findingTotal > 0) return false;
    if (
      !["all", "any", "none"].includes(filters.findings)
      && !hasSeverityAtLeast(config.findingCounts, filters.findings)
    ) return false;

    if (filters.scanned === "never") {
      if (lastScanMs(config) !== 0) return false;
    } else if (!withinTimeRange(lastScanMs(config), filters.scanned, nowMs)) {
      return false;
    }

    // Three distinct kinds, matching what the row actually says: a cron
    // schedule, a single unscheduled run, and manual-only (program-imported
    // targets that never run on their own). Treating manual-only as "one-time"
    // just because its schedule is empty made the filter contradict the
    // "Manual only" label rendered next to it.
    if (filters.schedule !== "all" && scheduleKind(config) !== filters.schedule) return false;

    const programRef = config.spec?.securityProgramRef ?? "";
    if (filters.program === "none" && programRef) return false;
    if (filters.program !== "all" && filters.program !== "none" && programRef !== filters.program) {
      return false;
    }

    if (filters.repo !== "all" && repoLabel(configRepoUrl(config)) !== filters.repo) return false;
    return true;
  });
}

function severityRank(config: SecurityScanConfig): number {
  const worst = topSeverity(config.findingCounts);
  const index = SEVERITY_ORDER.findIndex((severity) => severity === worst);
  return index === -1 ? SEVERITY_ORDER.length : index;
}

function byName(a: SecurityScanConfig, b: SecurityScanConfig): number {
  return a.name.localeCompare(b.name) || a.namespace.localeCompare(b.namespace);
}

/**
 * Each sortable column has exactly one useful order — names A→Z, freshest
 * scans first, worst findings first — so the URL carries the column alone and
 * headers never toggle into an order nobody asks for.
 */
export function sortScanConfigs(
  configs: SecurityScanConfig[],
  sort: string,
): SecurityScanConfig[] {
  const sorted = [...configs];
  if (sort === "name") return sorted.sort(byName);
  if (sort === "findings") {
    return sorted.sort(
      (a, b) =>
        severityRank(a) - severityRank(b)
        || severityCountTotal(b.findingCounts) - severityCountTotal(a.findingCounts)
        || byName(a, b),
    );
  }
  return sorted.sort((a, b) => {
    if (a.lastScanTimeUnix === b.lastScanTimeUnix) return byName(a, b);
    return b.lastScanTimeUnix > a.lastScanTimeUnix ? 1 : -1;
  });
}

/**
 * Sortable headers must be indistinguishable from static ones apart from the
 * caret, so the button repeats `TableHead`'s typography instead of inheriting
 * a browser-default button font in title case.
 */
function SortableHead({
  sortKey,
  active,
  onSort,
  className,
}: {
  sortKey: string;
  active: string;
  onSort: (key: string) => void;
  className?: string;
}) {
  const option = SORT_OPTIONS.find((candidate) => candidate.value === sortKey);
  if (!option) return null;
  const on = active === sortKey;
  const Icon = on ? (option.direction === "ascending" ? ArrowUp : ArrowDown) : ArrowUpDown;
  return (
    <TableHead aria-sort={on ? option.direction : "none"} className={className}>
      <button
        type="button"
        onClick={() => onSort(sortKey)}
        className={cn(
          "inline-flex items-center gap-1 rounded-sm text-[11px] font-medium uppercase",
          "tracking-[0.04em] text-muted-foreground/70 transition-colors hover:text-foreground",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
        )}
      >
        {option.label}
        <Icon className={cn("size-3", on ? "opacity-100" : "opacity-40")} aria-hidden />
      </button>
    </TableHead>
  );
}

export function SecurityScanConfigList() {
  const [configs, setConfigs] = useState<SecurityScanConfig[]>([]);
  const [programs, setPrograms] = useState<SecurityProgramResource[]>([]);
  const [personalNamespace, setPersonalNamespace] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [pendingDelete, setPendingDelete] = useState<SecurityScanConfig | null>(null);
  const [pendingDuplicate, setPendingDuplicate] = useState<SecurityScanConfig | null>(null);
  const [runNowPending, setRunNowPending] = useState<string | null>(null);
  const [stopPending, setStopPending] = useState<string | null>(null);
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkFailures, setBulkFailures] = useState<{ key: string; name: string; error: string }[]>([]);
  const [pendingBulkDelete, setPendingBulkDelete] = useState(false);
  const [selectedProgramScanTarget, setSelectedProgramScanTarget] = useState<ProgramScanTarget | null>(null);
  const importTriggerRef = useRef<HTMLButtonElement>(null);
  const selectAllRef = useRef<HTMLInputElement>(null);
  const now = useNow();
  const { values, set, reset, activeCount } = useUrlFilters(FILTER_SPEC);

  function closeImportedScanForm() {
    setSelectedProgramScanTarget(null);
    requestAnimationFrame(() => importTriggerRef.current?.focus());
  }

  const fetchConfigs = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [resp, credentials] = await Promise.all([
        client.listSecurityScanConfigs({ namespace: "" }),
        client.listMyCredentials({}),
      ]);
      setConfigs(resp.configs);
      // A refresh can drop rows (deleted elsewhere, or by a bulk delete here);
      // a selection pointing at rows that no longer exist would keep the bulk
      // toolbar open over nothing.
      const live = new Set(resp.configs.map(configKey));
      setSelected((current) => new Set([...current].filter((key) => live.has(key))));
      setPersonalNamespace(credentials.namespace);
      try {
        const programList = await client.listSecurityPrograms({ namespace: "" });
        setPrograms(programList.programs);
      } catch {
        setPrograms([]);
      }
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to load security scan configurations");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchConfigs();
  }, [fetchConfigs]);

  async function updateSuspend(config: SecurityScanConfig, suspend: boolean) {
    const spec = config.spec
      ? clone(SecurityScanConfigSpecSchema, config.spec)
      : create(SecurityScanConfigSpecSchema, {});
    spec.suspend = suspend;
    await client.updateSecurityScan(
      create(UpdateSecurityScanRequestSchema, {
        namespace: config.namespace,
        name: config.name,
        spec,
        useSavedCredentials: scanConfigUsesSavedCredentials(config),
      }),
    );
  }

  async function toggleSuspend(config: SecurityScanConfig) {
    setActionError(null);
    try {
      await updateSuspend(config, !isSuspended(config));
      await fetchConfigs();
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to update security scan");
    }
  }

  async function handleRunNow(config: SecurityScanConfig) {
    setActionError(null);
    setRunNowPending(`${config.namespace}/${config.name}`);
    try {
      await client.runSecurityScanNow({ namespace: config.namespace, name: config.name });
      await fetchConfigs();
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to start the security scan");
    } finally {
      setRunNowPending(null);
    }
  }

  async function handleDelete(config: SecurityScanConfig) {
    setActionError(null);
    try {
      await client.deleteSecurityScan({ namespace: config.namespace, name: config.name });
      await fetchConfigs();
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to delete security scan");
    }
  }

  async function handleStop(config: SecurityScanConfig) {
    setActionError(null);
    setStopPending(configKey(config));
    try {
      await client.cancelSecurityScanRun({ namespace: config.namespace, name: config.name });
      await fetchConfigs();
    } catch (e: unknown) {
      setActionError(e instanceof Error ? e.message : "Failed to stop the security scan");
    } finally {
      setStopPending(null);
    }
  }

  function toggleSelected(key: string) {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function clearSelection() {
    setSelected(new Set());
    setBulkFailures([]);
  }

  function toggleSelectAllVisible() {
    setSelected((current) => {
      const keys = visible.map(configKey);
      const next = new Set(current);
      if (keys.every((key) => next.has(key))) {
        for (const key of keys) next.delete(key);
      } else {
        for (const key of keys) next.add(key);
      }
      return next;
    });
  }

  async function applyToConfig(action: BulkAction, config: SecurityScanConfig) {
    // Actions that cannot apply are reported per item instead of being sent as
    // an RPC the backend is guaranteed to reject.
    if (action === "run") {
      if (isSuspended(config)) throw new Error("suspended — resume it before requesting a run");
      await client.runSecurityScanNow({ namespace: config.namespace, name: config.name });
      return;
    }
    if (action === "stop") {
      if (!isRunning(config)) throw new Error("nothing is running");
      await client.cancelSecurityScanRun({ namespace: config.namespace, name: config.name });
      return;
    }
    if (action === "delete") {
      await client.deleteSecurityScan({ namespace: config.namespace, name: config.name });
      return;
    }
    await updateSuspend(config, action === "suspend");
  }

  async function applyBulk(action: BulkAction) {
    // The selection survives search and filter changes, so an action applies
    // to everything the user picked — not only the rows currently on screen —
    // but only the rows it can actually apply to are sent as RPCs.
    const targets = eligibleFor(action, selectedConfigs);
    if (!targets.length) return;
    setBulkBusy(true);
    setActionError(null);
    setBulkFailures([]);
    const failures: { key: string; name: string; error: string }[] = [];
    const succeeded = new Set<string>();
    let applied = 0;
    try {
      for (const config of targets) {
        // Suspend/Resume name a target state, so a configuration already in it
        // is left alone rather than flipped back.
        if ((action === "suspend" || action === "resume") && isSuspended(config) === (action === "suspend")) {
          continue;
        }
        try {
          await applyToConfig(action, config);
          applied += 1;
          succeeded.add(configKey(config));
        } catch (e: unknown) {
          failures.push({
            key: configKey(config),
            name: config.name,
            error: e instanceof Error ? e.message : `Failed to apply ${BULK_LABELS[action]}`,
          });
        }
      }
      setBulkFailures(failures);
      const summary = `${BULK_LABELS[action]} applied to ${applied} configuration${applied === 1 ? "" : "s"}`;
      if (failures.length) {
        toast.warning(`${summary} · ${failures.length} failed`, {
          description: "The configurations that were not changed stay selected for a retry.",
        });
      } else {
        toast.success(summary);
      }
      // Rows that failed — and rows the action never applied to — stay
      // selected so it can be retried on exactly those; everything that
      // succeeded drops out of the selection.
      setSelected((current) => new Set([...current].filter((key) => !succeeded.has(key))));
    } finally {
      // The toolbar stays disabled until the refreshed list is in hand, so a
      // second bulk action can never run against the stale rows.
      await fetchConfigs();
      setBulkBusy(false);
    }
  }

  const programUrls = new Map(programs.map((program) => [program.name, program.programUrl]));
  const programBase = optionsFrom(
    [
      ...programs.map((program) => program.name),
      ...configs.map((config) => config.spec?.securityProgramRef ?? ""),
    ],
    "All programs",
  );
  const programOptions: FilterOption[] = [
    programBase[0],
    { value: "none", label: "No program" },
    ...programBase.slice(1),
  ];
  const repoOptions = optionsFrom(
    configs.map((config) => repoLabel(configRepoUrl(config))),
    "All repositories",
  );

  const searched = filterByQuery(configs, values.q, (config) => [
    config.name,
    config.namespace,
    configRepoUrl(config),
    config.spec?.schedule ?? "",
    config.spec?.securityProgramRef ?? "",
    programUrls.get(config.spec?.securityProgramRef ?? "") ?? "",
    config.phase,
  ]);
  const visible = sortScanConfigs(
    filterScanConfigs(searched, values, now),
    values.sort,
  );
  const filterCount = activeCount(["q", "sort"]);
  const visibleSelectedCount = visible.filter((config) => selected.has(configKey(config))).length;
  const allVisibleSelected = visible.length > 0 && visibleSelectedCount === visible.length;
  // The toolbar counts — and acts on — the whole selection, including rows the
  // current search or filters hide.
  const selectedConfigs = configs.filter((config) => selected.has(configKey(config)));
  const selectedCount = selectedConfigs.length;
  const someVisibleSelected = visibleSelectedCount > 0 && visibleSelectedCount < visible.length;
  const hiddenSelectedCount = selectedCount - visibleSelectedCount;
  const bulkEligible: Record<BulkAction, number> = {
    run: eligibleFor("run", selectedConfigs).length,
    stop: eligibleFor("stop", selectedConfigs).length,
    suspend: eligibleFor("suspend", selectedConfigs).length,
    resume: eligibleFor("resume", selectedConfigs).length,
    delete: selectedCount,
  };
  const mixedEligibility = (["run", "stop", "suspend", "resume"] as const).some(
    (action) => bulkEligible[action] > 0 && bulkEligible[action] < selectedCount,
  );
  function bulkButtonLabel(action: BulkAction): string {
    const count = bulkEligible[action];
    return count === selectedCount ? BULK_LABELS[action] : `${BULK_LABELS[action]} (${count})`;
  }
  // React has no `indeterminate` prop, so the checkbox's own DOM property
  // carries the mixed state for a partial selection.
  useEffect(() => {
    if (selectAllRef.current) selectAllRef.current.indeterminate = someVisibleSelected;
  }, [someVisibleSelected]);
  const narrowedView = Boolean(values.q.trim()) || filterCount > 0;
  // Nothing configured and nothing asked for: a search box and a filter strip
  // over an empty page imply the list is narrowed when it is simply empty.
  const nothingToSearch = !configs.length && !narrowedView;

  return (
    <ResourceListPage
      title="Scan Configurations"
      description="Configured security scans that analyze repositories, once or on a schedule."
      query={values.q}
      onQuery={(value) => set("q", value)}
      searchPlaceholder="Search configurations…"
      hideSearch={nothingToSearch}
      loading={loading}
      error={error}
      onRetry={fetchConfigs}
      empty={!visible.length}
      skeleton={<TableRowSkeleton rows={5} />}
      emptyIcon={<ShieldCheck className="size-6" />}
      emptyTitle={
        narrowedView && configs.length
          ? "No configurations match these filters"
          : "No scan configurations yet"
      }
      emptyDescription={
        narrowedView && configs.length
          ? "No configuration matches the current search and filters. Clear them to see all configurations."
          : "Create a security scan to analyze a repository for vulnerabilities, once or on a schedule."
      }
      emptyAction={
        narrowedView && configs.length ? (
          <Button variant="outline" size="sm" onClick={() => reset()}>
            Clear filters
          </Button>
        ) : (
          <SecurityScanFormDialog
            trigger={
              <Button size="sm">
                <Plus />
                Create your first scan
              </Button>
            }
            onSaved={() => void fetchConfigs()}
          />
        )
      }
      actions={
        <div className="flex items-center gap-2">
          <ProgramTargetImportDialog
            programs={programs}
            existingNames={new Set(
              configs
                .filter((config) => config.namespace === personalNamespace)
                .map((config) => config.name),
            )}
            trigger={<Button ref={importTriggerRef} variant="outline" size="sm">Import scan targets</Button>}
            onTargetSelected={setSelectedProgramScanTarget}
            onImported={() => void fetchConfigs()}
          />
          {selectedProgramScanTarget && (
            <SecurityScanFormDialog
              key={selectedProgramScanTarget.name}
              initialConfig={create(SecurityScanConfigSchema, {
                name: selectedProgramScanTarget.name,
                spec: {
                  repoUrl: selectedProgramScanTarget.repoUrl,
                  targetUrl: selectedProgramScanTarget.targetUrl,
                  baseBranch: selectedProgramScanTarget.baseBranch,
                  workflowRef: selectedProgramScanTarget.workflowRef,
                  policyPackRef: selectedProgramScanTarget.policyPackRef,
                  securityProgramRef: selectedProgramScanTarget.securityProgramRef,
                  parameterValues: selectedProgramScanTarget.parameterValues,
                  manualOnly: true,
                  minSeverity: "high",
                  parallelism: 4,
                  dedupe: { enabled: true },
                },
              })}
              defaultOpen
              onOpenChange={(open) => {
                if (!open) closeImportedScanForm();
              }}
              onSaved={() => void fetchConfigs()}
            />
          )}
          <SecurityScanFormDialog
            trigger={
              <Button size="sm">
                <Plus />
                New scan
              </Button>
            }
            onSaved={() => void fetchConfigs()}
          />
        </div>
      }
      nav={<SecurityNav />}
      toolbar={
        !nothingToSearch && (
          <FilterBar
            label="Configuration filters"
            activeCount={filterCount}
            onClear={() => reset()}
            resultLabel={`Showing ${visible.length} of ${configs.length} configurations`}
          >
            <FilterSelect
              label="Status"
              value={values.status}
              onChange={(value) => set("status", value)}
              options={STATUS_OPTIONS}
            />
            <FilterSelect
              label="Findings"
              value={values.findings}
              onChange={(value) => set("findings", value)}
              options={FINDINGS_OPTIONS}
            />
            <FilterSelect
              label="Last scan"
              value={values.scanned}
              onChange={(value) => set("scanned", value)}
              options={SCANNED_OPTIONS}
            />
            <FilterSelect
              label="Schedule"
              value={values.schedule}
              onChange={(value) => set("schedule", value)}
              options={SCHEDULE_OPTIONS}
            />
            <FilterSelect
              label="Program"
              value={values.program}
              onChange={(value) => set("program", value)}
              options={programOptions}
            />
            <FilterSelect
              label="Repository"
              value={values.repo}
              onChange={(value) => set("repo", value)}
              options={repoOptions}
            />
          </FilterBar>
        )
      }
    >
      {actionError && (
        <p role="alert" className="mb-3 text-sm text-destructive">
          {actionError}
        </p>
      )}
      {selectedCount > 0 && (
        <div
          role="toolbar"
          aria-label="Bulk actions"
          className="sticky top-0 z-10 mb-3 flex flex-wrap items-center gap-2 rounded-lg border border-border/70 bg-background/95 px-3 py-2 shadow-sm backdrop-blur"
        >
          <span className="text-[12.5px] font-medium">{selectedCount} selected</span>
          {hiddenSelectedCount > 0 && (
            <span className="text-[11px] text-muted-foreground">
              · {hiddenSelectedCount} hidden by filters
            </span>
          )}
          <Button
            variant="outline"
            size="sm"
            disabled={bulkBusy || bulkEligible.run === 0}
            onClick={() => void applyBulk("run")}
          >
            <Play />
            {bulkButtonLabel("run")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={bulkBusy || bulkEligible.stop === 0}
            onClick={() => void applyBulk("stop")}
          >
            <CircleStop />
            {bulkButtonLabel("stop")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={bulkBusy || bulkEligible.suspend === 0}
            onClick={() => void applyBulk("suspend")}
          >
            <Pause />
            {bulkButtonLabel("suspend")}
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={bulkBusy || bulkEligible.resume === 0}
            onClick={() => void applyBulk("resume")}
          >
            <Play />
            {bulkButtonLabel("resume")}
          </Button>
          <Button
            variant="destructive"
            size="sm"
            disabled={bulkBusy}
            onClick={() => setPendingBulkDelete(true)}
          >
            <Trash2 />
            Delete
          </Button>
          {mixedEligibility && (
            <span className="text-[11px] text-muted-foreground">
              Mixed actions apply only to eligible configurations.
            </span>
          )}
          <Button variant="ghost" size="sm" disabled={bulkBusy} onClick={clearSelection}>
            <X />
            Clear selection
          </Button>
        </div>
      )}
      {bulkFailures.length > 0 && (
        <div
          role="alert"
          className="mb-3 rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]"
        >
          <span className="font-medium">
            {bulkFailures.length === 1
              ? "1 configuration was not changed."
              : `${bulkFailures.length} configurations were not changed.`}
          </span>
          <ul className="mt-1 list-disc pl-5 text-muted-foreground">
            {bulkFailures.map((failure) => (
              <li key={failure.key} className="text-[11.5px]">
                <span className="font-mono">{failure.name}</span>: {failure.error}
              </li>
            ))}
          </ul>
        </div>
      )}
      <Table>
        <TableCaption className="sr-only">Security scan configurations</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead className="w-8">
              <input
                ref={selectAllRef}
                type="checkbox"
                aria-label="Select all configurations"
                aria-checked={someVisibleSelected ? "mixed" : allVisibleSelected}
                checked={allVisibleSelected}
                onChange={toggleSelectAllVisible}
                className="size-3.5 accent-primary"
              />
            </TableHead>
            {/* The name cell holds the repository, schedule and (on narrow
                viewports) the last scan, so it keeps the widest share; the
                icon-only action column only needs its buttons. */}
            <SortableHead
              sortKey="name"
              active={values.sort}
              onSort={(key) => set("sort", key)}
              className="sm:w-[36%]"
            />
            <TableHead className="hidden lg:table-cell lg:w-[20%]">Program</TableHead>
            <TableHead className="hidden sm:table-cell sm:w-[7.5rem]">Status</TableHead>
            <SortableHead
              sortKey="findings"
              active={values.sort}
              onSort={(key) => set("sort", key)}
              className="hidden sm:table-cell sm:w-[22%]"
            />
            <SortableHead
              sortKey="scanned"
              active={values.sort}
              onSort={(key) => set("sort", key)}
              className="hidden sm:table-cell sm:w-[8rem]"
            />
            <TableHead className="w-px text-right whitespace-nowrap">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {visible.map((config) => {
            const key = configKey(config);
            const suspended = isSuspended(config);
            const running = isRunning(config);
            const programRef = config.spec?.securityProgramRef ?? "";
            const programUrl = programUrls.get(programRef);
            const repository = configRepoUrl(config);
            const statusPill = suspended ? (
              <span className={cn(STATUS_PILL, toneSoft.warning)}>Suspended</span>
            ) : (
              <ReadyBadge status={config.conditionReady} />
            );
            return (
              <TableRow key={key}>
                <TableCell className="align-top">
                  <input
                    type="checkbox"
                    aria-label={`Select ${config.name}`}
                    checked={selected.has(key)}
                    onChange={() => toggleSelected(key)}
                    className="size-3.5 accent-primary"
                  />
                </TableCell>
                <TableCell className="max-w-[26rem] align-top whitespace-normal">
                  <Link
                    to={`/security/configs/${config.namespace}/${config.name}`}
                    className="block truncate font-medium text-primary hover:underline"
                  >
                    {config.name}
                  </Link>
                  <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-[11.5px] text-muted-foreground">
                    <span className="truncate font-mono" title={repository || undefined}>
                      {repoLabel(repository) || "No repository"}
                    </span>
                    <span aria-hidden>·</span>
                    <span className="truncate">{scheduleSummary(config)}</span>
                    {programRef && (
                      <span className="truncate font-mono lg:hidden">· {programRef}</span>
                    )}
                  </div>
                  {/* Below `sm` the status, findings and last-scan columns are
                      hidden, so a phone reads the whole row here instead of
                      scrolling the table sideways past a badge clipped at the
                      card edge. */}
                  <div className="mt-1 flex flex-wrap items-center gap-1 sm:hidden" data-testid="config-summary">
                    {statusPill}
                    <SeverityCountBadges counts={config.findingCounts} />
                    <span className="text-[11.5px] text-muted-foreground">
                      {config.lastScanTimeUnix === 0n
                        ? "Never scanned"
                        : `Scanned ${formatScheduleTime(config.lastScanTimeUnix, now)}`}
                    </span>
                  </div>
                </TableCell>
                <TableCell className="hidden max-w-56 align-top text-sm text-muted-foreground lg:table-cell">
                  {programRef ? (
                    <div className="space-y-0.5">
                      <div className="truncate font-mono text-[12px]">{programRef}</div>
                      {programUrl && (
                        <a
                          href={programUrl}
                          target="_blank"
                          rel="noreferrer"
                          title={programUrl}
                          className="block truncate font-mono text-[11px] underline underline-offset-2"
                        >
                          {programUrl}
                        </a>
                      )}
                    </div>
                  ) : <EmptyCell meaning="No security program linked" />}
                </TableCell>
                <TableCell className="hidden align-top sm:table-cell">{statusPill}</TableCell>
                <TableCell className="hidden align-top sm:table-cell">
                  <SeverityCountBadges counts={config.findingCounts} />
                </TableCell>
                <TableCell className="hidden align-top text-muted-foreground sm:table-cell">
                  {config.lastScanTimeUnix === 0n ? (
                    <EmptyCell meaning="Never scanned" />
                  ) : (
                    formatScheduleTime(config.lastScanTimeUnix, now)
                  )}
                </TableCell>
                <TableCell className="align-top text-right">
                  <div className="inline-flex items-center justify-end gap-1">
                    {suspended ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={bulkBusy}
                        onClick={() => void toggleSuspend(config)}
                      >
                        <Play />
                        Resume
                      </Button>
                    ) : running ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={bulkBusy || stopPending === key}
                        onClick={() => void handleStop(config)}
                      >
                        <CircleStop />
                        {stopPending === key ? "Stopping…" : "Stop"}
                      </Button>
                    ) : (
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={bulkBusy || runNowPending !== null}
                        onClick={() => void handleRunNow(config)}
                      >
                        <Play />
                        {runNowPending === key ? "Starting…" : "Run now"}
                      </Button>
                    )}
                    <SecurityScanFormDialog
                      config={config}
                      trigger={
                        <Button variant="ghost" size="icon-sm" aria-label={`Edit ${config.name}`}>
                          <Pencil />
                        </Button>
                      }
                      onSaved={() => void fetchConfigs()}
                    />
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        render={
                          <Button
                            variant="ghost"
                            size="icon-sm"
                            aria-label={`More actions for ${config.name}`}
                          />
                        }
                      >
                        <MoreHorizontal />
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end" className="min-w-[180px]">
                        <DropdownMenuItem
                          disabled={bulkBusy}
                          onClick={() => setPendingDuplicate(config)}
                        >
                          <Copy />
                          Duplicate
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          render={
                            <Link
                              to={`/security/runs?q=${encodeURIComponent(config.name)}`}
                              aria-label={`View runs for ${config.name}`}
                            />
                          }
                        >
                          <History />
                          View runs
                        </DropdownMenuItem>
                        {!suspended && (
                          <DropdownMenuItem
                            disabled={bulkBusy}
                            onClick={() => void toggleSuspend(config)}
                          >
                            <Pause />
                            Suspend
                          </DropdownMenuItem>
                        )}
                        <DropdownMenuSeparator />
                        <DropdownMenuItem
                          variant="destructive"
                          disabled={bulkBusy}
                          onClick={() => setPendingDelete(config)}
                        >
                          <Trash2 />
                          Delete
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </div>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
      {pendingDuplicate && (
        <SecurityScanFormDialog
          key={`duplicate-${pendingDuplicate.namespace}/${pendingDuplicate.name}`}
          duplicateFrom={pendingDuplicate}
          defaultOpen
          onOpenChange={(open) => {
            if (!open) setPendingDuplicate(null);
          }}
          onSaved={() => void fetchConfigs()}
        />
      )}
      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null);
        }}
        title={`Delete ${pendingDelete?.name ?? "scan"}?`}
        description="Deleting the configuration also removes its recorded scan runs and findings."
        confirmLabel="Delete"
        destructive
        onConfirm={async () => {
          if (pendingDelete) await handleDelete(pendingDelete);
          setPendingDelete(null);
        }}
      />
      <ConfirmDialog
        open={pendingBulkDelete}
        onOpenChange={(open) => {
          if (!open) setPendingBulkDelete(false);
        }}
        title={`Delete ${selectedCount} configuration${selectedCount === 1 ? "" : "s"}?`}
        description="Deleting these configurations also removes their recorded scan runs and findings."
        confirmLabel="Delete"
        destructive
        onConfirm={async () => {
          setPendingBulkDelete(false);
          await applyBulk("delete");
        }}
      />
    </ResourceListPage>
  );
}
