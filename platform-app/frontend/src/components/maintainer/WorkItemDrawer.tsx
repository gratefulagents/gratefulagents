import { Link } from "react-router-dom";
import { X } from "lucide-react";

import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetClose,
} from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { toneSoft, toneText } from "@/lib/status";
import { itemPhaseTone } from "@/components/maintainer/phases";
import {
  AnswerDialog,
  ConfigureGraphDialog,
  DispatchDialog,
  FinalizeDialog,
  MergeDialog,
  RequestDecisionDialog,
  TriageDialog,
} from "@/components/maintainer/commandDialogs";
import type { MaintainerWorkItem } from "@/rpc/platform/service_pb";

type WorkItemDrawerProps = {
  item: MaintainerWorkItem | null;
  allItems: MaintainerWorkItem[];
  namespace: string;
  onClose: () => void;
  onAction: () => void;
};

export function WorkItemDrawer({
  item,
  allItems,
  namespace,
  onClose,
  onAction,
}: WorkItemDrawerProps) {
  if (!item) return null;

  const { label, tone } = itemPhaseTone(item);
  const decision = item.pendingDecision;

  return (
    <Sheet open={Boolean(item)} onOpenChange={(open) => !open && onClose()}>
      <SheetContent side="right" className="w-full sm:max-w-lg overflow-y-auto" showCloseButton={false}>
        <SheetHeader className="border-b pb-3 flex-row items-start justify-between">
          <div className="space-y-1 min-w-0 flex-1">
            <div className="flex items-center gap-2 flex-wrap">
              <span className="font-mono text-[12px] text-muted-foreground">
                #{item.issueNumber}
              </span>
              <Badge variant="secondary" className={cn("text-[10px]", toneSoft[tone])}>
                {label}
              </Badge>
              {item.disposition && item.disposition !== "NotActionable" && (
                <Badge variant="outline" className="text-[10px]">
                  {item.disposition}
                </Badge>
              )}
            </div>
            <SheetTitle className="text-[15px] leading-snug">
              {item.issueUrl ? (
                <a
                  href={item.issueUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="hover:underline underline-offset-2"
                >
                  {item.issueTitle}
                </a>
              ) : (
                item.issueTitle
              )}
            </SheetTitle>
          </div>
          <SheetClose
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                className="shrink-0 mt-0.5"
                onClick={onClose}
                aria-label="Close"
              />
            }
          >
            <X className="size-4" />
          </SheetClose>
        </SheetHeader>

        <div className="space-y-5 p-4">
          {/* Pending decision */}
          {decision && (
            <section className="space-y-3">
              <h3 className="text-[12px] font-semibold uppercase tracking-wide text-muted-foreground">
                Pending decision
              </h3>
              <div className={cn("rounded-md px-3 py-2.5 text-sm space-y-2", toneSoft.warning)}>
                <p className="font-medium">{decision.question}</p>
                {decision.options.length > 0 && (
                  <p className="text-[12px]">Options: {decision.options.join(" · ")}</p>
                )}
              </div>
              <AnswerDialog
                item={item}
                trigger={
                  <Button size="sm" variant="default">
                    Answer
                  </Button>
                }
                onSuccess={() => {
                  onAction();
                  onClose();
                }}
              />
            </section>
          )}

          {/* Evidence summary */}
          {item.evidenceSummary && (
            <section className="space-y-1.5">
              <h3 className="text-[12px] font-semibold uppercase tracking-wide text-muted-foreground">
                Evidence summary
              </h3>
              <p className="text-[13px] leading-relaxed text-foreground">{item.evidenceSummary}</p>
            </section>
          )}

          {/* Accepted scope */}
          {(item.acceptedScopeStatement || item.acceptedScopeCriteria.length > 0) && (
            <section className="space-y-1.5">
              <h3 className="text-[12px] font-semibold uppercase tracking-wide text-muted-foreground">
                Accepted scope
              </h3>
              {item.acceptedScopeStatement && (
                <p className="text-[13px] leading-relaxed">{item.acceptedScopeStatement}</p>
              )}
              {item.acceptedScopeCriteria.length > 0 && (
                <ul className="list-disc list-inside space-y-0.5 text-[12.5px] text-muted-foreground">
                  {item.acceptedScopeCriteria.map((c, i) => (
                    <li key={i}>{c}</li>
                  ))}
                </ul>
              )}
            </section>
          )}

          {/* Children */}
          {item.children.length > 0 && (
            <section className="space-y-1.5">
              <h3 className="text-[12px] font-semibold uppercase tracking-wide text-muted-foreground">
                Children ({item.childrenDelivered}/{item.childrenTotal})
              </h3>
              <ul className="space-y-1">
                {item.children.map((c) => (
                  <li
                    key={c.workItemName}
                    className={cn(
                      "flex items-center gap-2 text-[12.5px]",
                      c.delivered && "text-muted-foreground line-through",
                    )}
                  >
                    <span className="font-mono text-[11px]">#{c.issueNumber}</span>
                    {c.title}
                  </li>
                ))}
              </ul>
            </section>
          )}

          {/* Dependencies */}
          {item.dependencies.length > 0 && (
            <section className="space-y-1.5">
              <h3 className="text-[12px] font-semibold uppercase tracking-wide text-muted-foreground">
                Dependencies ({item.dependenciesDelivered}/{item.dependenciesTotal})
              </h3>
              <ul className="space-y-1">
                {item.dependencies.map((d) => (
                  <li
                    key={d.workItemName}
                    className={cn(
                      "flex items-center gap-2 text-[12.5px]",
                      d.delivered && "text-muted-foreground line-through",
                    )}
                  >
                    <span className="font-mono text-[11px]">#{d.issueNumber}</span>
                    {d.title}
                  </li>
                ))}
              </ul>
            </section>
          )}

          {/* Unmet requirements */}
          {item.unmetRequirements.length > 0 && (
            <section className="space-y-1.5">
              <h3 className={cn("text-[12px] font-semibold uppercase tracking-wide", toneText.warning)}>
                Blocked on
              </h3>
              <ul className="list-disc list-inside space-y-0.5 text-[12.5px] text-muted-foreground">
                {item.unmetRequirements.map((req) => (
                  <li key={req}>{req}</li>
                ))}
              </ul>
            </section>
          )}

          {/* Pull requests */}
          {item.pullRequests.length > 0 && (
            <section className="space-y-1.5">
              <h3 className="text-[12px] font-semibold uppercase tracking-wide text-muted-foreground">
                Pull requests
              </h3>
              <ul className="space-y-1.5">
                {item.pullRequests.map((pr) => (
                  <li key={pr.number} className="text-[12.5px]">
                    <a
                      href={pr.url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="font-medium hover:underline underline-offset-2"
                    >
                      {pr.repository}#{pr.number}
                    </a>
                    <span className="ml-2 text-muted-foreground text-[11.5px]">
                      {[
                        pr.draft && "draft",
                        pr.checkState === "Passing" && "checks passing",
                        pr.checkState === "Failing" && "checks failing",
                        pr.checkState === "Pending" && "checks pending",
                        pr.reviewDecision === "APPROVED" && "approved",
                        pr.reviewDecision === "CHANGES_REQUESTED" && "changes requested",
                        pr.reviewDecision === "REVIEW_REQUIRED" && "review required",
                        pr.headSha && `HEAD ${pr.headSha.slice(0, 7)}`,
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </span>
                  </li>
                ))}
              </ul>
            </section>
          )}

          {/* Agent runs */}
          {item.agentRuns.length > 0 && (
            <section className="space-y-1.5">
              <h3 className="text-[12px] font-semibold uppercase tracking-wide text-muted-foreground">
                Agent runs
              </h3>
              <ul className="space-y-1">
                {item.agentRuns.map((run) => (
                  <li key={run.name} className="flex items-center gap-2 text-[12.5px]">
                    <Link
                      to={`/runs/${namespace}/${run.name}`}
                      className="hover:underline underline-offset-2 text-primary"
                    >
                      {run.name}
                    </Link>
                    <span className="text-muted-foreground">({run.role} · {run.phase})</span>
                  </li>
                ))}
              </ul>
            </section>
          )}

          {/* Delivery summary */}
          {item.deliverySummary && (
            <section className="space-y-1.5">
              <h3 className="text-[12px] font-semibold uppercase tracking-wide text-muted-foreground">
                Delivery
              </h3>
              <p className="text-[13px] leading-relaxed">{item.deliverySummary}</p>
            </section>
          )}

          {/* Latest command receipt */}
          {(item.latestCommandPhase === "Rejected" || item.latestCommandPhase === "Failed") && (
            <section className="space-y-1.5">
              <h3 className={cn("text-[12px] font-semibold uppercase tracking-wide", toneText.danger)}>
                Last command
              </h3>
              <p className={cn("text-[12.5px]", toneText.danger)}>
                {item.latestCommandType} {item.latestCommandPhase.toLowerCase()}
                {item.latestCommandMessage ? `: ${item.latestCommandMessage}` : ""}
              </p>
            </section>
          )}

          {/* Actions */}
          <section className="space-y-2 pt-2 border-t border-border/60">
            <h3 className="text-[12px] font-semibold uppercase tracking-wide text-muted-foreground">
              Actions
            </h3>
            <div className="flex flex-wrap gap-2">
              {item.phase !== "Delivered" && item.disposition !== "NotActionable" && (
                <TriageDialog
                  item={item}
                  trigger={
                    <Button size="sm" variant="outline">
                      {item.phase === "PendingTriage" ? "Triage" : "Re-triage"}
                    </Button>
                  }
                  onSuccess={onAction}
                />
              )}

              {item.phase !== "Delivered" && item.disposition !== "NotActionable" && (
                <RequestDecisionDialog
                  item={item}
                  trigger={
                    <Button size="sm" variant="outline">
                      Ask a question
                    </Button>
                  }
                  onSuccess={onAction}
                />
              )}

              {item.phase === "Triaged" && (
                <ConfigureGraphDialog
                  item={item}
                  allItems={allItems}
                  trigger={
                    <Button size="sm" variant="outline">
                      {item.graphConfigured ? "Edit graph" : "Configure graph"}
                    </Button>
                  }
                  onSuccess={onAction}
                />
              )}

              {item.phase === "ReadyToDispatch" && (
                <DispatchDialog
                  item={item}
                  trigger={
                    <Button
                      size="sm"
                      variant={item.readyToDispatch ? "default" : "outline"}
                      className={!item.readyToDispatch ? "opacity-60" : ""}
                    >
                      Dispatch now
                    </Button>
                  }
                  onSuccess={onAction}
                />
              )}

              {item.phase === "ReadyToMerge" && item.pullRequests.length > 0 && (
                <MergeDialog
                  item={item}
                  trigger={<Button size="sm" variant="default">Merge</Button>}
                  onSuccess={onAction}
                />
              )}

              {(item.phase === "ReadyToMerge" || item.phase === "Implementing" || item.phase === "Dispatched") && (
                <FinalizeDialog
                  item={item}
                  trigger={<Button size="sm" variant="outline">Finalize</Button>}
                  onSuccess={onAction}
                />
              )}
            </div>
          </section>
        </div>
      </SheetContent>
    </Sheet>
  );
}
