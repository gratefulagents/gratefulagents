import { Link } from "react-router-dom";
import { GitPullRequest } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { toneSoft, toneText } from "@/lib/status";
import { itemPhaseTone } from "@/components/maintainer/phases";
import {
  AnswerDialog,
  DispatchDialog,
  FinalizeDialog,
  MergeDialog,
  TriageDialog,
} from "@/components/maintainer/commandDialogs";
import type { MaintainerWorkItem } from "@/rpc/platform/service_pb";

function prLabel(pr: { checkState: string; reviewDecision: string; draft: boolean }): string {
  const parts: string[] = [];
  if (pr.draft) parts.push("draft");
  if (pr.checkState === "Passing") parts.push("checks passing");
  else if (pr.checkState === "Failing") parts.push("checks failing");
  else if (pr.checkState === "Pending") parts.push("checks pending");
  if (pr.reviewDecision === "APPROVED") parts.push("approved");
  else if (pr.reviewDecision === "CHANGES_REQUESTED") parts.push("changes requested");
  else if (pr.reviewDecision === "REVIEW_REQUIRED") parts.push("review required");
  return parts.join(" · ");
}

function CommandReceipt({ item }: { item: MaintainerWorkItem }) {
  if (
    item.latestCommandPhase !== "Rejected" &&
    item.latestCommandPhase !== "Failed"
  )
    return null;
  return (
    <p className={cn("text-[11px] leading-snug", toneText.danger)}>
      {item.latestCommandType} command {item.latestCommandPhase.toLowerCase()}
      {item.latestCommandMessage ? `: ${item.latestCommandMessage}` : ""}
    </p>
  );
}

type WorkItemCardProps = {
  item: MaintainerWorkItem;
  namespace: string;
  onAction: () => void;
  onOpen: (itemName: string) => void;
};

export function WorkItemCard({
  item,
  namespace,
  onAction,
  onOpen,
}: WorkItemCardProps) {
  const { label, tone } = itemPhaseTone(item);
  const isAwaitingDecision = item.phase === "AwaitingDecision";
  const decision = item.pendingDecision;

  const cardClasses = cn(
    // `relative` anchors the stretched open-button below the content: the whole
    // card is clickable without nesting the PR/run links inside a <button>,
    // which would be invalid HTML and unreachable for keyboard and AT users.
    "relative rounded-[8px] border p-3 space-y-2 text-left w-full transition-colors",
    "hover:bg-muted/20 focus-within:ring-2 focus-within:ring-ring/50",
    isAwaitingDecision
      ? "border-[color-mix(in_oklch,var(--tone-warning)_35%,transparent)] bg-[color-mix(in_oklch,var(--tone-warning)_6%,transparent)]"
      : "border-border/60 bg-card",
  );

  const trigger = (
    <div className={cardClasses}>
      {/* Stretched hit target: opens the detail drawer from anywhere on the
          card that is not itself a link. */}
      <button
        type="button"
        className="absolute inset-0 z-0 cursor-pointer rounded-[8px] focus:outline-none"
        onClick={() => onOpen(item.name)}
        aria-label={`Work item #${item.issueNumber}: ${item.issueTitle}`}
      />

      {/* Header row */}
      <div className="flex items-start justify-between gap-2">
        <span className="text-[11px] font-mono text-muted-foreground">#{item.issueNumber}</span>
        <Badge variant="secondary" className={cn("text-[10px] shrink-0", toneSoft[tone])}>
          {label}
        </Badge>
      </div>

      {/* Title */}
      {item.issueUrl ? (
        <a
          href={item.issueUrl}
          target="_blank"
          rel="noopener noreferrer"
          className="relative z-10 block w-fit text-[12.5px] font-medium leading-snug hover:underline underline-offset-2"
        >
          {item.issueTitle}
        </a>
      ) : (
        <p className="text-[12.5px] font-medium leading-snug">{item.issueTitle}</p>
      )}

      {/* Pending decision inline */}
      {decision && (
        <div className={cn("rounded-md px-2.5 py-2 text-[12px] space-y-1", toneSoft.warning)}>
          <p className="font-medium leading-snug">{decision.question}</p>
          {decision.options.length > 0 && (
            <p className="text-[11px]">Options: {decision.options.join(" · ")}</p>
          )}
        </div>
      )}

      {/* Blocked on */}
      {item.unmetRequirements.length > 0 && (
        <p className="text-[11.5px] text-muted-foreground leading-snug">
          blocked on: {item.unmetRequirements.join(", ")}
        </p>
      )}

      {/* Children/deps progress */}
      {(item.childrenTotal > 0 || item.dependenciesTotal > 0) && (
        <p className="text-[11px] text-muted-foreground">
          {item.childrenTotal > 0 && (
            <span>{item.childrenDelivered}/{item.childrenTotal} children</span>
          )}
          {item.childrenTotal > 0 && item.dependenciesTotal > 0 && " · "}
          {item.dependenciesTotal > 0 && (
            <span>{item.dependenciesDelivered}/{item.dependenciesTotal} deps</span>
          )}
        </p>
      )}

      {/* PR chips */}
      {item.pullRequests.length > 0 && (
        <div className="relative z-10 flex flex-wrap gap-1">
          {item.pullRequests.map((pr) => (
            <a
              key={pr.number}
              href={pr.url}
              target="_blank"
              rel="noopener noreferrer"
              className={cn(
                "inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10.5px] font-medium",
                "ring-1 ring-inset ring-border/60 bg-muted/40 hover:bg-muted/70 transition-colors",
              )}
              aria-label={`${pr.repository}#${pr.number}`}
            >
              <GitPullRequest className="size-2.5 shrink-0" />
              {pr.repository}#{pr.number}
              {prLabel(pr) ? ` · ${prLabel(pr)}` : ""}
            </a>
          ))}
        </div>
      )}

      {/* Agent run links */}
      {item.agentRuns.length > 0 && (
        <div className="relative z-10 flex flex-wrap gap-1">
          {item.agentRuns.map((run) => (
            <Link
              key={run.name}
              to={`/runs/${namespace}/${run.name}`}
              className="text-[10.5px] text-muted-foreground underline-offset-2 hover:underline hover:text-foreground"
            >
              {run.name} ({run.phase})
            </Link>
          ))}
        </div>
      )}

      {/* Delivery summary for shipped items */}
      {item.deliverySummary && (
        <p className="text-[11.5px] text-muted-foreground leading-snug">{item.deliverySummary}</p>
      )}

      {/* Command receipt: rejected / failed */}
      <CommandReceipt item={item} />
    </div>
  );

  return (
    <div className="space-y-1.5">
      {trigger}

      {/* Inline quick actions */}
      <div className="flex flex-wrap gap-1 px-0.5">
        {/* Answer — headline action on AwaitingDecision */}
        {isAwaitingDecision && decision && (
          <AnswerDialog
            item={item}
            trigger={
              <Button
                type="button"
                size="sm"
                variant="default"
                className="h-7 text-[11.5px]"
                aria-label="Answer decision"
              >
                Answer
              </Button>
            }
            onSuccess={onAction}
          />
        )}

        {/* Dispatch */}
        {item.phase === "ReadyToDispatch" && (
          <DispatchDialog
            item={item}
            trigger={
              <Button
                type="button"
                size="sm"
                variant="outline"
                className="h-7 text-[11.5px]"
                aria-label="Dispatch now"
                disabled={!item.readyToDispatch}
                title={
                  !item.readyToDispatch && item.unmetRequirements.length > 0
                    ? `Not ready: ${item.unmetRequirements.join(", ")}`
                    : undefined
                }
              >
                Dispatch
              </Button>
            }
            onSuccess={onAction}
          />
        )}

        {/* Merge */}
        {item.phase === "ReadyToMerge" && item.pullRequests.length > 0 && (
          <MergeDialog
            item={item}
            trigger={
              <Button
                type="button"
                size="sm"
                variant="outline"
                className="h-7 text-[11.5px]"
              >
                Merge
              </Button>
            }
            onSuccess={onAction}
          />
        )}

        {/* Finalize */}
        {(item.phase === "ReadyToMerge" || item.phase === "Implementing") && (
          <FinalizeDialog
            item={item}
            trigger={
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-7 text-[11.5px]"
              >
                Finalize
              </Button>
            }
            onSuccess={onAction}
          />
        )}

        {/* Triage (always available on active items) */}
        {item.phase !== "Delivered" && item.disposition !== "NotActionable" && (
          <TriageDialog
            item={item}
            trigger={
              <Button
                type="button"
                size="sm"
                variant="ghost"
                className="h-7 text-[11.5px] text-muted-foreground"
              >
                {item.phase === "PendingTriage" ? "Triage" : "Re-triage"}
              </Button>
            }
            onSuccess={onAction}
          />
        )}
      </div>
    </div>
  );
}
