import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { formatAge } from "@/lib/format";
import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";
import type { MaintainerWorkItem } from "@/rpc/platform/service_pb";

export type WorkItemPresentation = {
  label: string;
  tone: StatusTone;
};

/** Phase presentation: tone encodes who the item is waiting on. */
export function workItemPhasePresentation(item: MaintainerWorkItem): WorkItemPresentation {
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
export function workItemIsTerminal(item: MaintainerWorkItem): boolean {
  return item.phase === "Delivered" || item.disposition === "NotActionable";
}

export function prCheckLabel(checkState: string): string {
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

export function prReviewLabel(reviewDecision: string): string {
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

export function workItemCommandFailed(item: MaintainerWorkItem): boolean {
  return item.latestCommandPhase === "Rejected" || item.latestCommandPhase === "Failed";
}

/**
 * Board columns group lifecycle phases by who the work is waiting on:
 * triage (the maintainer), a decision (a human), execution (dispatched
 * agents), merge (the controller's guarded merge), and done.
 */
type BoardColumn = {
  key: string;
  title: string;
  /** Tone accent painted as the column's top rule. */
  toneVar: string;
  emptyHint: string;
  matches: (item: MaintainerWorkItem) => boolean;
};

const BOARD_COLUMNS: BoardColumn[] = [
  {
    key: "triage",
    title: "Triage",
    toneVar: "var(--muted-foreground)",
    emptyHint: "No issues waiting on triage.",
    matches: (item) =>
      !workItemIsTerminal(item) && (item.phase === "PendingTriage" || item.phase === "Triaged" || item.phase === ""),
  },
  {
    key: "decision",
    title: "Needs decision",
    toneVar: "var(--tone-warning)",
    emptyHint: "No questions for you right now.",
    matches: (item) => item.phase === "AwaitingDecision",
  },
  {
    key: "progress",
    title: "In progress",
    toneVar: "var(--tone-running)",
    emptyHint: "Nothing dispatched yet.",
    matches: (item) =>
      !workItemIsTerminal(item) &&
      (item.phase === "ReadyToDispatch" || item.phase === "Dispatched" || item.phase === "Implementing"),
  },
  {
    key: "merge",
    title: "Ready to merge",
    toneVar: "var(--tone-purple)",
    emptyHint: "No pull requests awaiting merge.",
    matches: (item) => !workItemIsTerminal(item) && item.phase === "ReadyToMerge",
  },
  {
    key: "done",
    title: "Done",
    toneVar: "var(--tone-success)",
    emptyHint: "Delivered and closed items land here.",
    matches: workItemIsTerminal,
  },
];

function BoardCard({ item, namespace }: { item: MaintainerWorkItem; namespace: string }) {
  const presentation = workItemPhasePresentation(item);
  const title = item.issueTitle || `Issue #${item.issueNumber}`;
  return (
    <li className="space-y-1.5 rounded-[6px] border border-border/60 bg-background p-2.5 shadow-xs">
      <div className="flex items-baseline justify-between gap-2 text-[10.5px] text-muted-foreground">
        <span className="font-medium tabular-nums">#{item.issueNumber}</span>
        {item.createdAtUnix > 0n ? <span>{formatAge(item.createdAtUnix)} ago</span> : null}
      </div>
      {item.issueUrl ? (
        <a
          href={item.issueUrl}
          target="_blank"
          rel="noreferrer"
          className="line-clamp-2 text-[12.5px] font-medium leading-snug text-foreground underline-offset-2 hover:text-primary hover:underline"
        >
          {title}
        </a>
      ) : (
        <p className="line-clamp-2 text-[12.5px] font-medium leading-snug">{title}</p>
      )}

      <Badge variant="secondary" className={cn("gap-1 text-[10.5px]", toneSoft[presentation.tone])}>
        <span className="size-1 rounded-full bg-current" aria-hidden />
        {presentation.label}
      </Badge>

      {item.pendingDecision ? (
        <p className={cn("line-clamp-2 rounded-[5px] px-2 py-1.5 text-[11px] leading-snug", toneSoft.warning)}>
          {item.pendingDecision.question}
        </p>
      ) : null}

      {workItemCommandFailed(item) ? (
        <p className={cn("line-clamp-2 rounded-[5px] px-2 py-1.5 text-[10.5px] leading-snug", toneSoft.danger)}>
          {item.latestCommandType} command {item.latestCommandPhase.toLowerCase()}
          {item.latestCommandMessage ? `: ${item.latestCommandMessage}` : ""}
        </p>
      ) : null}

      {item.phase === "Delivered" && item.deliverySummary ? (
        <p className="line-clamp-2 text-[11px] leading-snug text-muted-foreground">{item.deliverySummary}</p>
      ) : null}

      <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[10.5px] text-muted-foreground">
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
            {item.childrenDelivered}/{item.childrenTotal} children
          </span>
        ) : null}
      </div>
    </li>
  );
}

/**
 * Read-only kanban view of the maintainer's durable work-item queue. Columns
 * mirror the controller-managed lifecycle; items move as commands are
 * verified, not by dragging.
 */
export function MaintainerWorkItemBoard({
  items,
  namespace,
}: {
  items: MaintainerWorkItem[];
  namespace: string;
}) {
  return (
    <div
      className="grid grid-flow-col gap-2.5 overflow-x-auto p-3"
      style={{ gridAutoColumns: "minmax(185px, 1fr)" }}
      role="list"
      aria-label="Work item board"
    >
      {BOARD_COLUMNS.map((column) => {
        const columnItems = items.filter(column.matches);
        return (
          <section
            key={column.key}
            role="listitem"
            aria-label={`${column.title} (${columnItems.length})`}
            className="flex min-w-0 flex-col overflow-hidden rounded-[8px] border border-border/60 bg-muted/20"
          >
            <div className="h-[3px] shrink-0" style={{ background: column.toneVar, opacity: 0.65 }} aria-hidden />
            <header className="flex items-center gap-2 border-b border-border/60 px-2.5 py-2">
              <h4 className="text-[11.5px] font-medium">{column.title}</h4>
              <span className="ml-auto rounded-full bg-muted px-1.5 text-[10.5px] font-medium tabular-nums text-muted-foreground">
                {columnItems.length}
              </span>
            </header>
            {columnItems.length === 0 ? (
              <p className="m-2.5 rounded-[6px] border border-dashed border-border/70 px-2.5 py-4 text-center text-[11px] leading-relaxed text-muted-foreground">
                {column.emptyHint}
              </p>
            ) : (
              <ul className="flex flex-col gap-2 p-2.5">
                {columnItems.map((item) => (
                  <BoardCard key={item.name} item={item} namespace={namespace} />
                ))}
              </ul>
            )}
          </section>
        );
      })}
    </div>
  );
}
