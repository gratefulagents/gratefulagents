import { useCallback, useEffect, useRef, useState } from "react";
import { ChevronRight, Loader2 } from "lucide-react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { formatAge } from "@/lib/format";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";
import { MaintainerBoard } from "@/components/maintainer/MaintainerBoard";
import type {
  ActivityEntry,
  GitHubRepository,
  MaintainerBoardCapacity,
  MaintainerWorkItem,
} from "@/rpc/platform/service_pb";

type MaintainerReport = {
  id: string;
  state: string;
  summary: string;
  decisions: string;
  time: bigint;
};

type ReportPresentation = {
  label: string;
  tone: StatusTone;
};

function reportPresentation(state: string): ReportPresentation {
  switch (state) {
    case "healthy":
      return { label: "Healthy", tone: "success" };
    case "needs_attention":
      return { label: "Needs attention", tone: "warning" };
    case "blocked":
      return { label: "Blocked", tone: "danger" };
    default:
      return { label: "No report yet", tone: "neutral" };
  }
}

function ReportStateBadge({ state }: { state: string }) {
  const presentation = reportPresentation(state);
  return (
    <Badge variant="secondary" className={cn("gap-1.5 text-[11px]", toneSoft[presentation.tone])}>
      <span className="size-1.5 rounded-full bg-current" aria-hidden />
      {presentation.label}
    </Badge>
  );
}

function reportTime(value: unknown, fallback: bigint): bigint {
  if (typeof value === "bigint") return value;
  if (typeof value === "number" && Number.isFinite(value)) {
    return BigInt(Math.floor(value > 100_000_000_000 ? value / 1000 : value));
  }
  if (typeof value === "string") {
    const numeric = Number(value);
    if (Number.isFinite(numeric) && value.trim() !== "") {
      return BigInt(Math.floor(numeric > 100_000_000_000 ? numeric / 1000 : numeric));
    }
    const parsed = Date.parse(value);
    if (!Number.isNaN(parsed)) return BigInt(Math.floor(parsed / 1000));
  }
  return fallback;
}

function decisionsText(value: unknown): string {
  if (typeof value === "string") return value.trim();
  if (Array.isArray(value)) {
    return value
      .map((decision) => (typeof decision === "string" ? decision : JSON.stringify(decision)))
      .filter(Boolean)
      .join("\n");
  }
  if (value && typeof value === "object") return JSON.stringify(value);
  return "";
}

function parseMaintainerReport(entry: ActivityEntry): MaintainerReport | null {
  if (entry.type !== "maintainer_report") return null;
  const payloadEntry = entry as ActivityEntry & {
    detail?: unknown;
    payload?: unknown;
    preview?: unknown;
    payloadPreview?: unknown;
  };
  const candidates = [
    payloadEntry.detail,
    payloadEntry.payload,
    payloadEntry.preview,
    payloadEntry.payloadPreview,
    entry.message,
    entry.output,
    entry.inputRaw,
    entry.input,
  ];

  for (const candidate of candidates) {
    let payload: unknown = candidate;
    if (typeof candidate === "string") {
      try {
        payload = JSON.parse(candidate);
      } catch {
        continue;
      }
    }
    if (!payload || typeof payload !== "object" || Array.isArray(payload)) continue;
    const report = payload as Record<string, unknown>;
    if (typeof report.state !== "string" || typeof report.summary !== "string") continue;
    return {
      id: entry.eventId ? String(entry.eventId) : `${entry.timestampUnix}-${report.summary}`,
      state: report.state,
      summary: report.summary,
      decisions: decisionsText(report.decisions),
      time: reportTime(report.time, entry.timestampUnix),
    };
  }
  return null;
}

function useMaintainerReports(namespace: string, runName: string, enabled: boolean) {
  const [reports, setReports] = useState<MaintainerReport[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!enabled || !runName) return;

    let cancelled = false;
    async function loadReports() {
      setLoading(true);
      setError(null);
      try {
        const reportsByID = new Map<string, MaintainerReport>();
        let beforeEventId: bigint | undefined;
        for (let page = 0; page < 5 && reportsByID.size < 10; page++) {
          const response = await client.getActivityLog({
            namespace,
            name: runName,
            limit: 200,
            payloadPreviewBytes: 16384,
            ...(beforeEventId === undefined ? {} : { beforeEventId }),
          });
          for (const entry of response.entries) {
            const report = parseMaintainerReport(entry);
            if (report !== null && !reportsByID.has(report.id)) reportsByID.set(report.id, report);
          }
          if (!response.hasMoreBefore) break;
          beforeEventId = response.firstEventId;
        }
        if (cancelled) return;
        const nextReports = [...reportsByID.values()]
          .sort((a, b) => (a.time > b.time ? -1 : a.time < b.time ? 1 : 0))
          .slice(0, 10);
        setReports(nextReports);
      } catch (fetchError) {
        if (cancelled) return;
        setReports([]);
        setError(fetchError instanceof Error ? fetchError.message : "Failed to load report history");
      } finally {
        if (!cancelled) setLoading(false);
      }
    }

    void loadReports();
    return () => {
      cancelled = true;
    };
  }, [enabled, namespace, runName]);

  return { reports, loading, error };
}

function ReportHistoryItem({ report }: { report: MaintainerReport }) {
  const [decisionsOpen, setDecisionsOpen] = useState(false);
  return (
    <li className="space-y-2 px-4 py-3.5 first:pt-3 last:pb-3">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <ReportStateBadge state={report.state} />
        <span className="text-[11px] text-muted-foreground">{formatAge(report.time)} ago</span>
      </div>
      <p className="text-[13px] leading-relaxed text-foreground">{report.summary}</p>
      {report.decisions ? (
        <div className="space-y-1.5">
          <p
            className={cn(
              "whitespace-pre-wrap text-[12px] leading-relaxed text-muted-foreground",
              !decisionsOpen && "line-clamp-2",
            )}
          >
            {report.decisions}
          </p>
          <button
            type="button"
            className="text-[11px] font-medium text-muted-foreground underline-offset-2 transition-colors hover:text-foreground hover:underline"
            onClick={() => setDecisionsOpen((open) => !open)}
          >
            {decisionsOpen ? "Hide decisions" : "Show decisions"}
          </button>
        </div>
      ) : null}
    </li>
  );
}

export function MaintainerPanel({ repo }: { repo: GitHubRepository }) {
  const settings = repo.triggerSettings;
  return (
    <MaintainerCard
      namespace={repo.namespace}
      enabled={Boolean(settings?.maintainerEnabled)}
      maintainer={repo.maintainerStatus}
      maxDispatchesPerDay={settings?.maintainerMaxDispatchesPerDay}
      allowPrMerge={settings?.maintainerAllowPrMerge}
      fullControl={settings?.maintainerFullControl}
      repositoryName={repo.name}
      disabledHint="Enable it in repository settings."
    />
  );
}

/** How often the board refetches the queue while mounted. */
const WORK_ITEM_REFRESH_MS = 10_000;

function useMaintainerWorkItems(namespace: string, repositoryName: string, enabled: boolean) {
  const [items, setItems] = useState<MaintainerWorkItem[]>([]);
  const [capacity, setCapacity] = useState<MaintainerBoardCapacity | undefined>();
  const [loading, setLoading] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestGeneration = useRef(0);
  const refetchRef = useRef<() => void>(() => undefined);

  useEffect(() => {
    const generation = ++requestGeneration.current;
    let cancelled = false;
    let latestRequest = 0;
    let initialPending = true;

    const isCurrent = (request?: number) =>
      !cancelled &&
      requestGeneration.current === generation &&
      (request === undefined || request === latestRequest);

    if (!enabled || !repositoryName) {
      refetchRef.current = () => undefined;
      return () => {
        cancelled = true;
      };
    }

    async function loadItems() {
      const request = ++latestRequest;
      const isInitial = initialPending;
      if (isInitial && isCurrent(request)) {
        setLoading(true);
        setLoaded(false);
      }

      try {
        const response = await client.listMaintainerWorkItems({ namespace, repositoryName });
        if (!isCurrent(request)) return;
        setItems(response.items);
        setCapacity(response.capacity);
        setLoaded(true);
        setError(null);
      } catch (fetchError) {
        if (!isCurrent(request)) return;
        if (isInitial) {
          setItems([]);
          setCapacity(undefined);
          setLoaded(false);
        }
        setError(fetchError instanceof Error ? fetchError.message : "Failed to load work items");
      } finally {
        if (isInitial && isCurrent(request)) {
          initialPending = false;
          setLoading(false);
        }
      }
    }

    const refetchCurrent = () => {
      if (isCurrent()) void loadItems();
    };
    refetchRef.current = refetchCurrent;
    void loadItems();
    const refresh = window.setInterval(refetchCurrent, WORK_ITEM_REFRESH_MS);

    return () => {
      cancelled = true;
      window.clearInterval(refresh);
      if (refetchRef.current === refetchCurrent) refetchRef.current = () => undefined;
    };
  }, [enabled, namespace, repositoryName]);

  const refetch = useCallback(() => refetchRef.current(), []);

  return { items, capacity, loading, loaded, error, refetch };
}

export type MaintainerCardProps = {
  namespace: string;
  enabled: boolean;
  maintainer?: {
    runName?: string;
    lastReportState?: string;
    lastReportSummary?: string;
    lastReportTimeUnix?: bigint;
    lastWakeUnix?: bigint;
    dispatchesToday?: number;
  };
  maxDispatchesPerDay?: number;
  allowPrMerge?: boolean;
  fullControl?: boolean;
  repositoryName?: string;
  disabledHint?: string;
};

export function MaintainerCard({
  namespace,
  enabled,
  maintainer,
  maxDispatchesPerDay,
  allowPrMerge,
  fullControl,
  repositoryName,
  disabledHint,
}: MaintainerCardProps) {
  const { reports, loading: reportsLoading, error: reportsError } = useMaintainerReports(
    namespace,
    maintainer?.runName ?? "",
    enabled,
  );
  const { items, capacity, loading: itemsLoading, loaded: itemsLoaded, error: itemsError, refetch } =
    useMaintainerWorkItems(namespace, repositoryName ?? "", enabled && Boolean(repositoryName));
  const [historyOpen, setHistoryOpen] = useState(false);

  if (!enabled) {
    return (
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-[8px] border border-border/60 px-3.5 py-3 text-[12.5px]">
        <span className="font-medium text-muted-foreground">Maintainer is disabled.</span>
        {disabledHint ? <span className="text-muted-foreground">{disabledHint}</span> : null}
      </div>
    );
  }

  const state = maintainer?.lastReportState ?? "";
  const hasReport = Boolean(maintainer?.lastReportTimeUnix);
  const dailyCap = maxDispatchesPerDay || 10;
  const dispatchesToday = maintainer?.dispatchesToday ?? 0;

  return (
    <div className="surface-card overflow-hidden">
      {/* Header rail */}
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border/60 px-4 py-3">
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 min-w-0">
          {maintainer?.runName ? (
            <Link
              to={`/runs/${namespace}/${maintainer.runName}`}
              className="text-[13px] font-medium text-primary underline-offset-2 hover:underline"
            >
              {maintainer.runName}
            </Link>
          ) : (
            <span className="text-[13px] font-medium">Standing maintainer</span>
          )}
          <ReportStateBadge state={state} />
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[12px]">
          <span className="tabular-nums text-muted-foreground">
            <span className="font-medium text-foreground">{dispatchesToday} / {dailyCap}</span>
            {" dispatches today"}
          </span>
          {capacity && (
            <span className="tabular-nums text-muted-foreground">
              <span className="font-medium text-foreground">
                {capacity.activeDispatches}/{capacity.concurrencyCap}
              </span>
              {" in flight"}
            </span>
          )}
          {maintainer?.lastWakeUnix ? (
            <span className="text-muted-foreground">
              last wake {formatAge(maintainer.lastWakeUnix)} ago
            </span>
          ) : null}
          {allowPrMerge || fullControl ? (
            <Badge variant="secondary" className={cn("text-[10.5px]", toneSoft.danger)}>
              {fullControl ? "Full control" : "PR merging enabled"}
            </Badge>
          ) : null}
        </div>
      </div>

      {/* Report summary line */}
      <div className="px-4 py-3 border-b border-border/60">
        <p className="text-sm leading-relaxed text-foreground">
          {maintainer?.lastReportSummary || "No maintainer report yet."}
        </p>
        <p className="text-[11.5px] text-muted-foreground mt-0.5">
          {hasReport ? `Last report ${formatAge(maintainer!.lastReportTimeUnix!)} ago` : "Awaiting first report"}
        </p>
      </div>

      {/* Work board */}
      {repositoryName ? (
        <div className="border-b border-border/60">
          {itemsLoading ? (
            <div className="flex items-center gap-2 px-4 py-5 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              Loading work items…
            </div>
          ) : itemsError && !itemsLoaded ? (
            <p className="px-4 py-5 text-sm text-destructive">{itemsError}</p>
          ) : items.length === 0 ? (
            <p className="px-4 py-5 text-sm leading-relaxed text-muted-foreground">
              No work items yet — the maintainer files each triaged issue here.
            </p>
          ) : (
            <MaintainerBoard
              items={items}
              capacity={capacity}
              namespace={namespace}
              onRefetch={refetch}
            />
          )}
        </div>
      ) : null}

      {/* Report history collapsible */}
      <Collapsible open={historyOpen} onOpenChange={setHistoryOpen}>
        <CollapsibleTrigger
          render={
            <button
              type="button"
              className="group flex w-full items-center gap-2 border-t border-border/60 px-4 py-3 text-left transition-colors hover:bg-muted/35"
            />
          }
        >
          <ChevronRight
            className={cn(
              "size-3.5 shrink-0 text-muted-foreground transition-transform duration-[var(--dur-fast)]",
              historyOpen && "rotate-90",
            )}
          />
          <span className="text-[12.5px] font-medium">Report history</span>
          <span className="ml-auto text-[11px] text-muted-foreground">
            {reportsLoading
              ? "Loading…"
              : `${reports.length} report${reports.length === 1 ? "" : "s"}`}
          </span>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="border-t border-border/60">
            {reportsLoading ? (
              <div className="flex items-center gap-2 px-4 py-5 text-sm text-muted-foreground">
                <Loader2 className="size-4 animate-spin" />
                Loading reports…
              </div>
            ) : reportsError ? (
              <p className="px-4 py-5 text-sm text-destructive">{reportsError}</p>
            ) : reports.length === 0 ? (
              <p className="px-4 py-5 text-sm leading-relaxed text-muted-foreground">
                No reports yet — the maintainer records its decisions here.
              </p>
            ) : (
              <ul className="divide-y divide-border/60">
                {reports.map((report) => (
                  <ReportHistoryItem key={report.id} report={report} />
                ))}
              </ul>
            )}
          </div>
        </CollapsibleContent>
      </Collapsible>
    </div>
  );
}
