import { Fragment, useState } from "react";
import { Link } from "react-router-dom";
import { ChevronDown, ChevronRight } from "lucide-react";

import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { DetailSection } from "@/components/detail-page";
import { cn } from "@/lib/utils";
import { phaseTone, toneSoft } from "@/lib/status";
import type {
  SecurityScanExecutionState,
  SecurityScanTaskExecutionState,
} from "@/rpc/platform/service_pb";

function formatUnix(unix: bigint): string {
  if (unix <= 0n) return "—";
  return new Date(Number(unix) * 1000).toLocaleString();
}

function StatePill({ state }: { state: string }) {
  return (
    <span
      className={cn(
        "rounded-md px-2 py-0.5 text-xs font-medium",
        toneSoft[phaseTone(state)],
      )}
    >
      {state || "unknown"}
    </span>
  );
}

function instanceLabel(task: SecurityScanTaskExecutionState): string {
  return task.instance > 0 ? `${task.name} #${task.instance}` : task.name;
}

/**
 * ExecutionProgressPanel renders the observed state of the most recent
 * deterministic workflow execution: one row per task instance with its
 * state, attempts, expandable retry history, and a link to the AgentRun
 * serving the task. It is pure presentation — the parent refreshes the
 * scan config it reads from.
 */
export function ExecutionProgressPanel({
  namespace,
  execution,
}: {
  namespace: string;
  execution: SecurityScanExecutionState;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  const toggle = (key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  return (
    <DetailSection title="Execution progress">
      <div className="space-y-3" data-testid="execution-progress">
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <StatePill state={execution.phase} />
          <span
            className="rounded-md bg-muted/60 px-2 py-0.5 text-muted-foreground ring-1 ring-inset ring-border/70"
            title={execution.effectiveParallelismNote || undefined}
            data-testid="execution-parallelism"
          >
            parallelism {execution.effectiveParallelism}
            {execution.effectiveParallelismNote && ` — ${execution.effectiveParallelismNote}`}
          </span>
          <span className="text-muted-foreground">started {formatUnix(execution.startedAtUnix)}</span>
          <span className="text-muted-foreground">
            completed {formatUnix(execution.completedAtUnix)}
          </span>
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8" />
              <TableHead>Task</TableHead>
              <TableHead>State</TableHead>
              <TableHead>Attempts</TableHead>
              <TableHead>Run</TableHead>
              <TableHead>Next retry</TableHead>
              <TableHead>Last error</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {execution.tasks.map((task) => {
              const key = `${task.name}#${task.instance}`;
              const isOpen = expanded.has(key);
              return (
                <Fragment key={key}>
                  <TableRow data-testid={`execution-task-${key}`}>
                    <TableCell>
                      {task.retries.length > 0 && (
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`${isOpen ? "Hide" : "Show"} retries for ${instanceLabel(task)}`}
                          aria-expanded={isOpen}
                          onClick={() => toggle(key)}
                        >
                          {isOpen ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                        </Button>
                      )}
                    </TableCell>
                    <TableCell className="font-mono text-[13px]">{instanceLabel(task)}</TableCell>
                    <TableCell>
                      <StatePill state={task.state} />
                    </TableCell>
                    <TableCell>{task.attempts}</TableCell>
                    <TableCell className="font-mono text-[13px]">
                      {task.runName ? (
                        <Link
                          className="underline underline-offset-2"
                          to={`/runs/${namespace}/${task.runName}`}
                        >
                          {task.runName}
                        </Link>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {formatUnix(task.nextRetryTimeUnix)}
                    </TableCell>
                    <TableCell className="max-w-56">
                      {task.lastError ? (
                        <span className="block truncate text-xs text-destructive" title={task.lastError}>
                          {task.lastError}
                        </span>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                  </TableRow>
                  {isOpen && (
                    <TableRow data-testid={`execution-retries-${key}`}>
                      <TableCell />
                      <TableCell colSpan={6}>
                        <ul className="space-y-1 py-1 text-xs text-muted-foreground">
                          {task.retries.map((retry, index) => (
                            <li key={`${retry.runName}-${index}`}>
                              <span className="font-mono">{retry.runName || "(no run)"}</span>{" "}
                              <span
                                className={cn(
                                  "rounded-md px-1.5 py-0.5",
                                  toneSoft[retry.class === "non-retryable" ? "danger" : "warning"],
                                )}
                              >
                                {retry.class || "retryable"}
                              </span>{" "}
                              — {retry.reason || "no reason recorded"} ·{" "}
                              {formatUnix(retry.startedAtUnix)} → {formatUnix(retry.finishedAtUnix)}
                            </li>
                          ))}
                        </ul>
                      </TableCell>
                    </TableRow>
                  )}
                </Fragment>
              );
            })}
          </TableBody>
        </Table>
      </div>
    </DetailSection>
  );
}
