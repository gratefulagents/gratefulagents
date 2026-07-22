import { useEffect, useState } from "react";
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
import type {
  ActivityEntry,
  GitHubRepository,
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
      repositoryName={repo.name}
      disabledHint="Enable it in repository settings."
    />
  );
}

/** Phase presentation: tone encodes who the item is waiting on. */
function workItemPhasePresentation(item: MaintainerWorkItem): ReportPresentation {
  // NotActionable is terminal: the controller closes the issue but leaves the
  // lifecycle phase at Triaged, so present the disposition instead.
  if (item.disposition === "NotActionable") {
    return { label: "Not actionable", tone: "neutral" };
  }
  switch (item.phase) {
    case "AwaitingDecision":
      return { label: "Needs decision", tone: "warning" };
    case "ReadyToDispatch":
      return { label: "Ready to dispatch", tone: "info" };
    case "Dispatched":
      return { label: "Dispatched", tone: "running" };
    case "Implementing":
      return { label: "Implementing", tone: "running" };
    case "ReadyToMerge":
      return { label: "Ready to merge", tone: "purple" };
    case "Delivered":
      return { label: "Delivered", tone: "success" };
    case "Triaged":
      return { label: "Triaged", tone: "neutral" };
    case "PendingTriage":
    default:
      return { label: "Pending triage", tone: "neutral" };
  }
}

/** Terminal work items: delivered, or closed by triage as not actionable. */
function workItemIsTerminal(item: MaintainerWorkItem): boolean {
  return item.phase === "Delivered" || item.disposition === "NotActionable";
}

/** How often the open panel refetches the queue to pick up controller updates. */
const WORK_ITEM_REFRESH_MS = 30_000;

function useMaintainerWorkItems(namespace: string, repositoryName: string, enabled: boolean) {
  const [items, setItems] = useState<MaintainerWorkItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!enabled || !repositoryName) return;

    let cancelled = false;
    let initial = true;
    async function loadItems() {
      if (initial) setLoading(true);
      try {
        const response = await client.listMaintainerWorkItems({ namespace, repositoryName });
        if (cancelled) return;
        setItems(response.items);
        setError(null);
      } catch (fetchError) {
        if (cancelled) return;
        // Keep showing the last good queue on background refresh failures.
        if (initial) setItems([]);
        setError(fetchError instanceof Error ? fetchError.message : "Failed to load work items");
      } finally {
        if (!cancelled && initial) {
          initial = false;
          setLoading(false);
        }
      }
    }

    void loadItems();
    const refresh = window.setInterval(() => void loadItems(), WORK_ITEM_REFRESH_MS);
    return () => {
      cancelled = true;
      window.clearInterval(refresh);
    };
  }, [enabled, namespace, repositoryName]);

  return { items, loading, error };
}

function prCheckLabel(checkState: string): string {
  switch (checkState) {
    case "Passing":
      return "checks passing";
    case "Failing":
      return "checks failing";
    case "Pending":
      return "checks pending";
    default:
      return "";
  }
}

function prReviewLabel(reviewDecision: string): string {
  switch (reviewDecision) {
    case "APPROVED":
      return "approved";
    case "CHANGES_REQUESTED":
      return "changes requested";
    case "REVIEW_REQUIRED":
      return "review required";
    default:
      return "";
  }
}

function WorkItemRow({ item, namespace }: { item: MaintainerWorkItem; namespace: string }) {
  const presentation = workItemPhasePresentation(item);
  const title = item.issueTitle || `Issue #${item.issueNumber}`;
  const commandFailed =
    item.latestCommandPhase === "Rejected" || item.latestCommandPhase === "Failed";
  const blocked =
    !item.readyToDispatch && !item.readyToMerge && item.unmetRequirements.length > 0;

  return (
    <li className="space-y-2 px-4 py-3.5 first:pt-3 last:pb-3">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <span className="text-[11.5px] font-medium tabular-nums text-muted-foreground">
          #{item.issueNumber}
        </span>
        {item.issueUrl ? (
          <a
            href={item.issueUrl}
            target="_blank"
            rel="noreferrer"
            className="min-w-0 flex-1 truncate text-[13px] font-medium text-foreground underline-offset-2 hover:text-primary hover:underline"
          >
            {title}
          </a>
        ) : (
          <span className="min-w-0 flex-1 truncate text-[13px] font-medium">{title}</span>
        )}
        <Badge variant="secondary" className={cn("gap-1.5 text-[11px]", toneSoft[presentation.tone])}>
          <span className="size-1.5 rounded-full bg-current" aria-hidden />
          {presentation.label}
        </Badge>
      </div>

      {item.pendingDecision ? (
        <div className={cn("space-y-1 rounded-[6px] px-2.5 py-2", toneSoft.warning)}>
          <p className="text-[12px] font-medium leading-relaxed">{item.pendingDecision.question}</p>
          {item.pendingDecision.options.length > 0 ? (
            <p className="text-[11px] opacity-80">Options: {item.pendingDecision.options.join(" · ")}</p>
          ) : null}
        </div>
      ) : null}

      {commandFailed ? (
        <p className={cn("rounded-[6px] px-2.5 py-1.5 text-[11.5px] leading-relaxed", toneSoft.danger)}>
          {item.latestCommandType} command {item.latestCommandPhase.toLowerCase()}
          {item.latestCommandMessage ? `: ${item.latestCommandMessage}` : ""}
        </p>
      ) : null}

      {blocked ? (
        <p className="text-[11.5px] leading-relaxed text-muted-foreground">
          Blocked on: {item.unmetRequirements.join("; ")}
        </p>
      ) : null}

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11.5px] text-muted-foreground">
        {item.pullRequests.map((pr) => {
          const facts = [
            prCheckLabel(pr.checkState),
            prReviewLabel(pr.reviewDecision),
            pr.state === "merged" ? "merged" : "",
            pr.draft ? "draft" : "",
          ]
            .filter(Boolean)
            .join(" · ");
          const label = `${pr.repository}#${pr.number}`;
          return (
            <span key={label} className="inline-flex items-center gap-1">
              {pr.url ? (
                <a href={pr.url} target="_blank" rel="noreferrer" className="text-primary underline-offset-2 hover:underline">
                  {label}
                </a>
              ) : (
                <span>{label}</span>
              )}
              {facts ? <span>· {facts}</span> : null}
            </span>
          );
        })}
        {item.agentRuns.map((run) => (
          <Link
            key={run.name}
            to={`/runs/${namespace}/${run.name}`}
            className="text-primary underline-offset-2 hover:underline"
          >
            {run.name}
            {run.phase ? ` (${run.phase})` : ""}
          </Link>
        ))}
        {item.childrenTotal > 0 ? (
          <span className="tabular-nums">
            {item.childrenDelivered}/{item.childrenTotal} children delivered
          </span>
        ) : null}
        {item.dependenciesTotal > 0 && item.dependenciesDelivered < item.dependenciesTotal ? (
          <span className="tabular-nums">
            waiting on {item.dependenciesTotal - item.dependenciesDelivered} dependenc
            {item.dependenciesTotal - item.dependenciesDelivered === 1 ? "y" : "ies"}
          </span>
        ) : null}
        {item.phase === "Delivered" && item.deliverySummary ? (
          <span className="min-w-0 truncate">{item.deliverySummary}</span>
        ) : null}
      </div>
    </li>
  );
}

function WorkItemsSection({
  namespace,
  repositoryName,
  enabled,
}: {
  namespace: string;
  repositoryName: string;
  enabled: boolean;
}) {
  const { items, loading, error } = useMaintainerWorkItems(namespace, repositoryName, enabled);
  const needsDecision = items.filter((item) => item.phase === "AwaitingDecision").length;
  const active = items.filter((item) => !workItemIsTerminal(item)).length;
  const summary = loading
    ? "Loading…"
    : items.length === 0
      ? "None yet"
      : needsDecision > 0
        ? `${active} active · ${needsDecision} need${needsDecision === 1 ? "s" : ""} your decision`
        : `${active} active · ${items.length - active} closed`;
  const [open, setOpen] = useState(false);

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
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
            open && "rotate-90",
          )}
        />
        <span className="text-[12.5px] font-medium">Work items</span>
        {!loading && needsDecision > 0 ? (
          <Badge variant="secondary" className={cn("text-[10.5px]", toneSoft.warning)}>
            {needsDecision}
          </Badge>
        ) : null}
        <span className="ml-auto text-[11px] text-muted-foreground">{summary}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="border-t border-border/60">
          {loading ? (
            <div className="flex items-center gap-2 px-4 py-5 text-sm text-muted-foreground">
              <Loader2 className="size-4 animate-spin" />
              Loading work items…
            </div>
          ) : error ? (
            <p className="px-4 py-5 text-sm text-destructive">{error}</p>
          ) : items.length === 0 ? (
            <p className="px-4 py-5 text-sm leading-relaxed text-muted-foreground">
              No work items yet — the maintainer files each triaged issue here.
            </p>
          ) : (
            <ul className="divide-y divide-border/60">
              {items.map((item) => (
                <WorkItemRow key={item.name} item={item} namespace={namespace} />
              ))}
            </ul>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

/**
 * Structural view of GitHubRepositoryMaintainerStatus so the card renders the
 * same maintainer from a repository or a project trigger read model.
 */
export type MaintainerStatusLike = {
  runName?: string;
  lastWakeUnix?: bigint;
  dispatchesToday?: number;
  lastReportTimeUnix?: bigint;
  lastReportState?: string;
  lastReportSummary?: string;
};

export type MaintainerCardProps = {
  namespace: string;
  enabled: boolean;
  maintainer?: MaintainerStatusLike;
  maxDispatchesPerDay?: number;
  allowPrMerge?: boolean;
  /**
   * GitHubRepository resource name backing this maintainer. When set, the
   * card lists the durable work-item queue for that repository.
   */
  repositoryName?: string;
  /** Where to enable the maintainer when it is off. */
  disabledHint?: string;
};

export function MaintainerCard({
  namespace,
  enabled,
  maintainer,
  maxDispatchesPerDay,
  allowPrMerge,
  repositoryName,
  disabledHint,
}: MaintainerCardProps) {
  const { reports, loading, error } = useMaintainerReports(namespace, maintainer?.runName ?? "", enabled);
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

  return (
    <div className="surface-card overflow-hidden">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-border/60 px-4 py-3">
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

      <div className="space-y-4 px-4 py-3.5">
        <div className="space-y-1">
          <p className="text-sm leading-relaxed text-foreground">
            {maintainer?.lastReportSummary || "No maintainer report yet."}
          </p>
          <p className="text-[11.5px] text-muted-foreground">
            {hasReport ? `Last report ${formatAge(maintainer!.lastReportTimeUnix!)} ago` : "Awaiting first report"}
          </p>
        </div>

        <dl className="flex flex-wrap items-stretch gap-y-3">
          <div className="min-w-[130px] border-r border-border/50 pr-5 sm:pr-7">
            <dt className="text-[10.5px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
              Dispatches today
            </dt>
            <dd className="mt-1 text-[13px] font-medium tabular-nums">
              {maintainer?.dispatchesToday ?? 0} / {dailyCap}
            </dd>
          </div>
          <div className="min-w-[110px] px-5 sm:px-7">
            <dt className="text-[10.5px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
              Last wake
            </dt>
            <dd className="mt-1 text-[13px]">
              {maintainer?.lastWakeUnix ? `${formatAge(maintainer.lastWakeUnix)} ago` : "—"}
            </dd>
          </div>
          {allowPrMerge ? (
            <div className="flex items-end px-5 sm:px-7">
              <Badge variant="secondary" className={cn("text-[10.5px]", toneSoft.danger)}>
                PR merging enabled
              </Badge>
            </div>
          ) : null}
        </dl>
      </div>

      {repositoryName ? (
        <WorkItemsSection namespace={namespace} repositoryName={repositoryName} enabled={enabled} />
      ) : null}

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
            {loading ? "Loading…" : `${reports.length} report${reports.length === 1 ? "" : "s"}`}
          </span>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="border-t border-border/60">
            {loading ? (
              <div className="flex items-center gap-2 px-4 py-5 text-sm text-muted-foreground">
                <Loader2 className="size-4 animate-spin" />
                Loading reports…
              </div>
            ) : error ? (
              <p className="px-4 py-5 text-sm text-destructive">{error}</p>
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
