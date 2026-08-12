/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { clone, create } from "@bufbuild/protobuf";
import { Copy, Filter, Pencil, Play, Plus, ShieldCheck, Trash2 } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { ReadyBadge } from "@/components/ReadyBadge";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { ResourceListPage } from "@/components/list-page";
import { SecurityNav } from "@/components/SecurityNav";
import { ImmunefiTargetImportDialog } from "@/components/ImmunefiTargetImportDialog";
import { SeverityCountBadges } from "@/components/SecurityScanList";
import {
  scanConfigUsesSavedCredentials,
  SecurityScanFormDialog,
} from "@/components/SecurityScanFormDialog";
import { client } from "@/lib/client";
import { formatScheduleTime } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import {
  SecurityScanConfigSpecSchema,
  UpdateSecurityScanRequestSchema,
  type SecurityProgramResource,
  type SecurityScanConfig,
} from "@/rpc/platform/service_pb";

type StatusFilter = "all" | "ready" | "suspended" | "attention";
type FindingsFilter = "all" | "any" | "critical" | "high" | "none";
type ScanAgeFilter = "all" | "24h" | "7d" | "30d" | "never";
type ScheduleFilter = "all" | "once" | "recurring";

function hasFindings(config: SecurityScanConfig, severity?: string): boolean {
  if (severity) return (config.findingCounts[severity] ?? 0) > 0;
  return Object.entries(config.findingCounts).some(
    ([key, count]) => key !== "total" && key !== "open" && count > 0,
  );
}

function matchesScanAge(config: SecurityScanConfig, age: ScanAgeFilter, nowMs: number): boolean {
  if (age === "all") return true;
  if (age === "never") return config.lastScanTimeUnix === 0n;
  if (config.lastScanTimeUnix === 0n) return false;
  const windows: Record<Exclude<ScanAgeFilter, "all" | "never">, number> = {
    "24h": 24 * 60 * 60 * 1000,
    "7d": 7 * 24 * 60 * 60 * 1000,
    "30d": 30 * 24 * 60 * 60 * 1000,
  };
  return Number(config.lastScanTimeUnix) * 1000 >= nowMs - windows[age];
}

function filterScanConfigs(
  configs: SecurityScanConfig[],
  filters: {
    status: StatusFilter;
    findings: FindingsFilter;
    scanAge: ScanAgeFilter;
    schedule: ScheduleFilter;
    program: string;
  },
  nowMs: number,
): SecurityScanConfig[] {
  return configs.filter((config) => {
    const suspended = config.spec?.suspend ?? false;
    const ready = !suspended && config.conditionReady.toLowerCase() === "true";
    if (filters.status === "ready" && !ready) return false;
    if (filters.status === "suspended" && !suspended) return false;
    if (filters.status === "attention" && (ready || suspended)) return false;

    if (filters.findings === "any" && !hasFindings(config)) return false;
    if (filters.findings === "none" && hasFindings(config)) return false;
    if (
      (filters.findings === "critical" || filters.findings === "high")
      && !hasFindings(config, filters.findings)
    ) return false;

    if (!matchesScanAge(config, filters.scanAge, nowMs)) return false;

    const recurring = Boolean(config.spec?.schedule.trim());
    if (filters.schedule === "once" && recurring) return false;
    if (filters.schedule === "recurring" && !recurring) return false;

    const programRef = config.spec?.securityProgramRef ?? "";
    if (filters.program === "none" && programRef) return false;
    if (filters.program !== "all" && filters.program !== "none" && programRef !== filters.program) return false;
    return true;
  });
}

function FilterSelect({
  label,
  value,
  onChange,
  children,
}: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  children: ReactNode;
}) {
  return (
    <label className="flex min-w-[130px] flex-1 items-center gap-1.5 text-xs text-muted-foreground sm:min-w-0 sm:flex-none">
      <span className="sr-only">{label}</span>
      <select
        aria-label={label}
        value={value}
        onChange={(event) => onChange(event.currentTarget.value)}
        className={`h-7 w-full rounded-lg border px-2 text-[12px] text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 sm:w-auto ${
          value === "all"
            ? "border-input bg-background dark:bg-input/30"
            : "border-primary/40 bg-primary/5"
        }`}
      >
        {children}
      </select>
    </label>
  );
}

export function SecurityScanConfigList() {
  const [configs, setConfigs] = useState<SecurityScanConfig[]>([]);
  const [programs, setPrograms] = useState<SecurityProgramResource[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [findingsFilter, setFindingsFilter] = useState<FindingsFilter>("all");
  const [scanAgeFilter, setScanAgeFilter] = useState<ScanAgeFilter>("all");
  const [scheduleFilter, setScheduleFilter] = useState<ScheduleFilter>("all");
  const [programFilter, setProgramFilter] = useState("all");
  const [pendingDelete, setPendingDelete] = useState<SecurityScanConfig | null>(null);
  const [runNowPending, setRunNowPending] = useState<string | null>(null);
  const now = useNow();

  const fetchConfigs = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const resp = await client.listSecurityScanConfigs({ namespace: "" });
      setConfigs(resp.configs);
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

  async function toggleSuspend(config: SecurityScanConfig) {
    setActionError(null);
    const spec = config.spec
      ? clone(SecurityScanConfigSpecSchema, config.spec)
      : create(SecurityScanConfigSpecSchema, {});
    spec.suspend = !spec.suspend;
    try {
      await client.updateSecurityScan(
        create(UpdateSecurityScanRequestSchema, {
          namespace: config.namespace,
          name: config.name,
          spec,
          useSavedCredentials: scanConfigUsesSavedCredentials(config),
        }),
      );
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

  const programUrls = new Map(programs.map((program) => [program.name, program.programUrl]));
  const programOptions = Array.from(new Set([
    ...programs.map((program) => program.name),
    ...configs.map((config) => config.spec?.securityProgramRef ?? "").filter(Boolean),
  ])).sort((a, b) => a.localeCompare(b));
  const searched = filterByQuery(configs, query, (config) => [
    config.name,
    config.namespace,
    config.spec?.repoUrl ?? "",
    config.spec?.schedule ?? "",
    config.spec?.securityProgramRef ?? "",
    programUrls.get(config.spec?.securityProgramRef ?? "") ?? "",
    config.phase,
  ]);
  const filtered = filterScanConfigs(searched, {
    status: statusFilter,
    findings: findingsFilter,
    scanAge: scanAgeFilter,
    schedule: scheduleFilter,
    program: programFilter,
  }, now);
  const activeFilterCount = [
    statusFilter !== "all",
    findingsFilter !== "all",
    scanAgeFilter !== "all",
    scheduleFilter !== "all",
    programFilter !== "all",
  ].filter(Boolean).length;
  const hasActiveView = Boolean(query.trim()) || activeFilterCount > 0;

  function clearFilters() {
    setQuery("");
    setStatusFilter("all");
    setFindingsFilter("all");
    setScanAgeFilter("all");
    setScheduleFilter("all");
    setProgramFilter("all");
  }

  return (
    <ResourceListPage
      title="Scan Configurations"
      description="Configured security scans that analyze repositories, once or on a schedule."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search scan configurations…"
      loading={loading}
      error={error}
      onRetry={fetchConfigs}
      empty={!filtered.length}
      skeleton={<TableRowSkeleton rows={5} />}
      emptyIcon={<ShieldCheck className="size-6" />}
      emptyTitle={hasActiveView ? "No configurations match these filters" : "No scan configurations found"}
      emptyDescription={
        hasActiveView
          ? "Clear filters or broaden the date range to see more configurations."
          : "Create a security scan to analyze a repository for vulnerabilities."
      }
      actions={
        <div className="flex items-center gap-2">
          <ImmunefiTargetImportDialog
            existingNames={new Set(configs.map((config) => config.name))}
            trigger={<Button variant="outline" size="sm">Import Immunefi targets</Button>}
            onImported={() => void fetchConfigs()}
          />
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
        <div
          className="flex flex-wrap items-center gap-2 rounded-lg border border-border/70 bg-muted/20 px-2.5 py-2"
          aria-label="Configuration filters"
          role="group"
        >
          <span className="inline-flex basis-full items-center gap-1.5 text-xs font-medium text-muted-foreground sm:basis-auto">
            <Filter className="size-3.5" aria-hidden />
            Filters
            {activeFilterCount > 0 && (
              <span
                className="rounded-full bg-primary/10 px-1.5 text-[11px] text-primary"
                aria-label={`${activeFilterCount} active filters`}
              >
                {activeFilterCount}
              </span>
            )}
          </span>
          <FilterSelect label="Status" value={statusFilter} onChange={(value) => setStatusFilter(value as StatusFilter)}>
            <option value="all">All statuses</option>
            <option value="ready">Ready</option>
            <option value="suspended">Suspended</option>
            <option value="attention">Needs attention</option>
          </FilterSelect>
          <FilterSelect label="Findings" value={findingsFilter} onChange={(value) => setFindingsFilter(value as FindingsFilter)}>
            <option value="all">All findings</option>
            <option value="any">Has findings</option>
            <option value="critical">Has critical</option>
            <option value="high">Has high</option>
            <option value="none">No findings</option>
          </FilterSelect>
          <FilterSelect label="Last scan" value={scanAgeFilter} onChange={(value) => setScanAgeFilter(value as ScanAgeFilter)}>
            <option value="all">Scanned any time</option>
            <option value="24h">Last 24 hours</option>
            <option value="7d">Last 7 days</option>
            <option value="30d">Last 30 days</option>
            <option value="never">Never scanned</option>
          </FilterSelect>
          <FilterSelect label="Schedule" value={scheduleFilter} onChange={(value) => setScheduleFilter(value as ScheduleFilter)}>
            <option value="all">All schedules</option>
            <option value="once">One-time</option>
            <option value="recurring">Recurring</option>
          </FilterSelect>
          <FilterSelect label="Program" value={programFilter} onChange={setProgramFilter}>
            <option value="all">All programs</option>
            <option value="none">No program</option>
            {programOptions.map((program) => (
              <option key={program} value={program}>{program}</option>
            ))}
          </FilterSelect>
          <span className="basis-full text-[11px] text-muted-foreground sm:ml-auto sm:basis-auto" aria-live="polite">
            Showing {filtered.length} of {configs.length} configurations
          </span>
          {hasActiveView && (
            <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={clearFilters}>
              Clear filters
            </Button>
          )}
        </div>
      }
    >
      {actionError && (
        <p role="alert" className="mb-3 text-sm text-destructive">
          {actionError}
        </p>
      )}
      <Table>
        <TableCaption className="sr-only">Security scan configurations</TableCaption>
        <TableHeader>
          <TableRow>
            <TableHead>Name</TableHead>
            <TableHead>Repository</TableHead>
            <TableHead>Program</TableHead>
            <TableHead>Schedule</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Findings</TableHead>
            <TableHead>Last Scan</TableHead>
            <TableHead className="text-right">Actions</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {filtered.map((config) => (
            <TableRow key={`${config.namespace}/${config.name}`}>
              <TableCell>
                <Link
                  to={`/security/configs/${config.namespace}/${config.name}`}
                  className="font-medium text-primary hover:underline"
                >
                  {config.name}
                </Link>
              </TableCell>
              <TableCell className="font-mono text-sm text-muted-foreground">
                {config.spec?.repoUrl || "-"}
              </TableCell>
              <TableCell className="max-w-64 text-sm text-muted-foreground">
                {config.spec?.securityProgramRef ? (
                  <div className="space-y-0.5">
                    <div className="font-mono text-[12px]">{config.spec.securityProgramRef}</div>
                    {programUrls.has(config.spec.securityProgramRef) && (
                      <a
                        href={programUrls.get(config.spec.securityProgramRef)}
                        target="_blank"
                        rel="noreferrer"
                        title={programUrls.get(config.spec.securityProgramRef)}
                        className="block truncate font-mono text-[11px] underline underline-offset-2"
                      >
                        {programUrls.get(config.spec.securityProgramRef)}
                      </a>
                    )}
                  </div>
                ) : "-"}
              </TableCell>
              <TableCell className="font-mono text-sm text-muted-foreground">
                {config.spec?.schedule || "once"}
              </TableCell>
              <TableCell>
                {config.spec?.suspend ? (
                  <Badge variant="secondary">Suspended</Badge>
                ) : (
                  <ReadyBadge status={config.conditionReady} />
                )}
              </TableCell>
              <TableCell>
                <SeverityCountBadges counts={config.findingCounts} />
              </TableCell>
              <TableCell className="text-muted-foreground">
                {formatScheduleTime(config.lastScanTimeUnix, now)}
              </TableCell>
              <TableCell className="text-right">
                <div className="inline-flex items-center gap-1">
                  {!config.spec?.suspend && (
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={runNowPending !== null}
                      onClick={() => void handleRunNow(config)}
                    >
                      <Play />
                      {runNowPending === `${config.namespace}/${config.name}`
                        ? "Starting…"
                        : "Run now"}
                    </Button>
                  )}
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => void toggleSuspend(config)}
                  >
                    {config.spec?.suspend ? "Resume" : "Suspend"}
                  </Button>
                  <SecurityScanFormDialog
                    config={config}
                    trigger={
                      <Button variant="ghost" size="sm" aria-label={`Edit ${config.name}`}>
                        <Pencil />
                      </Button>
                    }
                    onSaved={() => void fetchConfigs()}
                  />
                  <SecurityScanFormDialog
                    duplicateFrom={config}
                    trigger={
                      <Button variant="ghost" size="sm" aria-label={`Duplicate ${config.name}`}>
                        <Copy />
                      </Button>
                    }
                    onSaved={() => void fetchConfigs()}
                  />
                  <Button
                    variant="ghost"
                    size="sm"
                    aria-label={`Delete ${config.name}`}
                    onClick={() => setPendingDelete(config)}
                  >
                    <Trash2 />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null);
        }}
        title={`Delete ${pendingDelete?.name ?? "scan"}?`}
        description="The scan configuration is removed; recorded findings stay available."
        confirmLabel="Delete"
        destructive
        onConfirm={async () => {
          if (pendingDelete) await handleDelete(pendingDelete);
          setPendingDelete(null);
        }}
      />
    </ResourceListPage>
  );
}
