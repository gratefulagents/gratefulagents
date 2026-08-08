import { Fragment, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { ChevronDown, ChevronRight, ListTree, X } from "lucide-react";

import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { DetailSection } from "@/components/detail-page";
import {
  DAG_NODE_HEIGHT,
  DAG_NODE_WIDTH,
  DagCanvas,
  DagEdgeLayer,
  dagEdges,
  dagLayers,
  dagLayout,
} from "@/components/security-dag";
import { cn } from "@/lib/utils";
import { phaseTone, toneColor, toneSoft, type StatusTone } from "@/lib/status";
import type {
  SecurityScanExecutionState,
  SecurityScanTaskConfig,
  SecurityScanTaskExecutionState,
} from "@/rpc/platform/service_pb";

function formatUnix(unix: bigint): string {
  if (unix <= 0n) return "—";
  return new Date(Number(unix) * 1000).toLocaleString();
}

/** formatDuration renders the span between two unix seconds compactly. */
function formatDuration(startUnix: bigint, endUnix: bigint): string {
  if (startUnix <= 0n || endUnix <= 0n || endUnix < startUnix) return "—";
  let seconds = Number(endUnix - startUnix);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  seconds = seconds % 60;
  if (minutes < 60) return seconds > 0 ? `${minutes}m ${seconds}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const rem = minutes % 60;
  return rem > 0 ? `${hours}h ${rem}m` : `${hours}h`;
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

/** Terminal instance states that count toward the "done" progress figure. */
const DONE_STATES = new Set(["Succeeded", "Skipped"]);

/**
 * aggregateState summarizes a group of fan-out instances into one node state
 * by severity precedence: a single failure marks the task failed, any live
 * instance marks it running, and only a fully terminal group reads done.
 */
export function aggregateState(instances: SecurityScanTaskExecutionState[]): string {
  const states = new Set(instances.map((i) => i.state));
  if (states.has("Failed")) return "Failed";
  if (states.has("Running")) return "Running";
  if (states.has("Pending")) return "Pending";
  if (states.has("Blocked")) return "Blocked";
  if (states.has("Succeeded")) return "Succeeded";
  if (states.has("Skipped")) return "Skipped";
  return instances[0]?.state ?? "unknown";
}

interface TaskGroup {
  name: string;
  instances: SecurityScanTaskExecutionState[];
  state: string;
  tone: StatusTone;
  attempts: number;
  done: number;
}

function groupInstances(execution: SecurityScanExecutionState): TaskGroup[] {
  const byName = new Map<string, SecurityScanTaskExecutionState[]>();
  for (const task of execution.tasks) {
    const list = byName.get(task.name) ?? [];
    list.push(task);
    byName.set(task.name, list);
  }
  return [...byName.entries()].map(([name, instances]) => {
    const state = aggregateState(instances);
    return {
      name,
      instances,
      state,
      tone: phaseTone(state),
      attempts: instances.reduce((acc, i) => acc + i.attempts, 0),
      done: instances.filter((i) => DONE_STATES.has(i.state)).length,
    };
  });
}

/**
 * ExecutionDagNode renders one workflow task on the live canvas: the task
 * name, its aggregate state (colored left accent + label), fan-out progress
 * for multi-instance tasks, and a retry marker when attempts exceed one.
 */
function ExecutionDagNode({
  group,
  planned,
  selected,
  onClick,
}: {
  group: TaskGroup | undefined;
  planned: SecurityScanTaskConfig | undefined;
  selected: boolean;
  onClick: () => void;
}) {
  const name = group?.name ?? planned?.name ?? "";
  const state = group?.state ?? "Blocked";
  const tone = group?.tone ?? "neutral";
  const fanout = (group && group.instances.length > 1) || !!planned?.forEach;
  const retried = (group?.attempts ?? 0) > (group?.instances.length ?? 0);
  return (
    <button
      type="button"
      data-testid={`execution-node-${name}`}
      aria-pressed={selected}
      onClick={onClick}
      title={`${name}: ${state}`}
      className={cn(
        "flex h-full w-full flex-col justify-center gap-0.5 overflow-hidden rounded-lg border bg-background px-2.5 text-left",
        selected ? "border-primary ring-1 ring-primary" : "border-border hover:border-primary/50",
      )}
      style={{ boxShadow: `inset 2.5px 0 0 0 ${toneColor[tone]}` }}
    >
      <span className="flex min-w-0 items-center gap-1.5">
        {state === "Running" && (
          <span
            aria-hidden
            className="size-1.5 shrink-0 animate-pulse rounded-full"
            style={{ backgroundColor: toneColor[tone] }}
          />
        )}
        <span className="truncate font-mono text-[11.5px] font-medium leading-tight">{name}</span>
      </span>
      <span className="flex min-w-0 items-center gap-1 text-[10.5px] leading-tight text-muted-foreground">
        <span className="truncate">
          {group
            ? fanout
              ? `${state} · ${group.done}/${group.instances.length} instances`
              : state
            : planned?.forEach
              ? "Waiting · fans out"
              : "Waiting"}
        </span>
        {retried && (
          <span className={cn("shrink-0 rounded px-1 py-px text-[10px]", toneSoft["warning"])}>
            retried
          </span>
        )}
      </span>
    </button>
  );
}

/**
 * ExecutionProgressPanel renders the observed state of the most recent
 * deterministic workflow execution. When the planned workflow tasks are
 * available it draws the dependency DAG with live per-task state (click a
 * node to focus its instances); the instance table below always lists every
 * task instance with attempts, expandable retry history, timings, and a link
 * to the AgentRun serving the task. It is pure presentation — the parent
 * refreshes the scan config it reads from.
 */
export function ExecutionProgressPanel({
  namespace,
  execution,
  workflowTasks,
}: {
  namespace: string;
  execution: SecurityScanExecutionState;
  workflowTasks?: SecurityScanTaskConfig[];
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [focusTask, setFocusTask] = useState<string | null>(null);

  const groups = useMemo(() => groupInstances(execution), [execution]);
  const groupByName = useMemo(() => new Map(groups.map((g) => [g.name, g])), [groups]);

  // The planned workflow supplies the dependency edges; execution state alone
  // has no graph shape. Nodes come from the union so instances of tasks that
  // no longer exist in the config still render (as edge-less nodes).
  const dagNodes = useMemo(() => {
    if (!workflowTasks || workflowTasks.length === 0) return [];
    const nodes = workflowTasks.map((t) => ({
      name: t.name.trim(),
      dependsOn: t.dependsOn,
      forEach: t.forEach,
      config: t as SecurityScanTaskConfig | undefined,
    }));
    const known = new Set(nodes.map((n) => n.name));
    for (const group of groups) {
      if (!known.has(group.name)) {
        nodes.push({ name: group.name, dependsOn: [], forEach: "", config: undefined });
      }
    }
    return nodes;
  }, [workflowTasks, groups]);

  const layout = useMemo(() => dagLayout(dagLayers(dagNodes)), [dagNodes]);
  const edges = useMemo(() => dagEdges(dagNodes), [dagNodes]);

  const totalInstances = execution.tasks.length;
  const doneInstances = execution.tasks.filter((t) => DONE_STATES.has(t.state)).length;

  const toggle = (key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const visibleTasks = focusTask
    ? execution.tasks.filter((t) => t.name === focusTask)
    : execution.tasks;

  return (
    <DetailSection title="Execution progress">
      <div className="space-y-3" data-testid="execution-progress">
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <StatePill state={execution.phase} />
          {totalInstances > 0 && (
            <span
              className="rounded-md bg-muted/60 px-2 py-0.5 text-muted-foreground ring-1 ring-inset ring-border/70"
              data-testid="execution-instance-progress"
            >
              {doneInstances}/{totalInstances} tasks done
            </span>
          )}
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
          {execution.startedAtUnix > 0n && execution.completedAtUnix > 0n && (
            <span className="text-muted-foreground">
              ({formatDuration(execution.startedAtUnix, execution.completedAtUnix)})
            </span>
          )}
          {execution.id && (
            <span className="font-mono text-[11px] text-muted-foreground/70" title="Execution ID">
              {execution.id}
            </span>
          )}
        </div>

        {dagNodes.length > 0 && (
          <DagCanvas layout={layout} testId="execution-dag">
            <DagEdgeLayer edges={edges} layout={layout} label="Workflow execution graph" />
            {dagNodes.map((node) => {
              const pos = layout.positions.get(node.name);
              if (!pos) return null;
              return (
                <div
                  key={node.name}
                  className="absolute"
                  style={{
                    left: pos.x,
                    top: pos.y,
                    width: DAG_NODE_WIDTH,
                    height: DAG_NODE_HEIGHT,
                  }}
                >
                  <ExecutionDagNode
                    group={groupByName.get(node.name)}
                    planned={node.config}
                    selected={focusTask === node.name}
                    onClick={() =>
                      setFocusTask((current) => (current === node.name ? null : node.name))
                    }
                  />
                </div>
              );
            })}
          </DagCanvas>
        )}

        {focusTask && (
          <div
            className="flex items-center gap-2 text-xs text-muted-foreground"
            data-testid="execution-focus"
          >
            <ListTree className="size-3.5" aria-hidden />
            <span>
              Showing instances of <span className="font-mono text-foreground">{focusTask}</span>
            </span>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-6 px-1.5 text-xs"
              onClick={() => setFocusTask(null)}
            >
              <X className="size-3" /> Show all
            </Button>
          </div>
        )}

        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-8" />
              <TableHead>Task</TableHead>
              <TableHead>State</TableHead>
              <TableHead>Attempts</TableHead>
              <TableHead>Run</TableHead>
              <TableHead>Duration</TableHead>
              <TableHead>Next retry</TableHead>
              <TableHead>Last error</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {visibleTasks.map((task) => {
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
                      {formatDuration(task.startedAtUnix, task.finishedAtUnix)}
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
                      <TableCell colSpan={7}>
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
