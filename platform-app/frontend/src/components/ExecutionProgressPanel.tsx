import { Fragment, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { ChevronDown, ChevronRight, ListTree, RotateCcw, X } from "lucide-react";

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
  SecurityScanPostScriptJobState,
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

/** prettyJson re-indents a JSON payload for display; non-JSON stays raw. */
function prettyJson(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

/** Terminal instance states that count toward the "done" progress figure. */
const DONE_STATES = new Set(["Succeeded", "Skipped"]);
const POST_SCRIPT_DONE_STATES = new Set(["Succeeded", "Failed", "Skipped"]);

/**
 * PostScriptJobsTable lists the durable per-finding post-script pipelines the
 * deterministic engine materializes after research tasks finish. Every
 * pipeline must reach a terminal state before the sink may submit the report.
 */
function PostScriptJobsTable({
  namespace,
  jobs,
  findingLinkBase,
}: {
  namespace: string;
  jobs: SecurityScanPostScriptJobState[];
  findingLinkBase?: string;
}) {
  return (
    <div className="space-y-1.5" data-testid="execution-post-scripts">
      <p className="text-xs font-medium text-muted-foreground">
        Post-script pipelines
        <span className="ml-1.5 font-normal">
          (matching scripts run in order; oversized pipelines split safely and the report waits for all chunks)
        </span>
      </p>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Scripts</TableHead>
            <TableHead>Finding</TableHead>
            <TableHead>State</TableHead>
            <TableHead>Attempts</TableHead>
            <TableHead>Run</TableHead>
            <TableHead>Result</TableHead>
            <TableHead>Duration</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {jobs.map((job) => {
            const key = job.findingId
              ? `${job.findingId}#${job.order}`
              : `${job.script}#${job.fingerprint}#${job.order}`;
            const finding = job.fingerprint || job.findingId || "—";
            const scripts = job.scripts.length > 0 ? job.scripts : job.script ? [job.script] : [];
            return (
              <TableRow key={key} data-testid={`execution-post-script-pipeline-${key}`}>
                <TableCell className="font-mono text-[13px]">{scripts.join(" → ") || "—"}</TableCell>
                <TableCell className="max-w-48 font-mono text-[12px]">
                  {findingLinkBase && job.findingId ? (
                    <Link
                      className="block truncate underline underline-offset-2"
                      title={finding}
                      to={`${findingLinkBase}/${job.findingId}`}
                    >
                      {finding}
                    </Link>
                  ) : (
                    <span className="block truncate text-muted-foreground" title={finding}>
                      {finding}
                    </span>
                  )}
                </TableCell>
                <TableCell>
                  <StatePill state={job.state} />
                </TableCell>
                <TableCell>{job.attempts}</TableCell>
                <TableCell className="font-mono text-[13px]">
                  {job.runName ? (
                    <Link
                      className="underline underline-offset-2"
                      to={`/runs/${namespace}/${job.runName}`}
                    >
                      {job.runName}
                    </Link>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="max-w-56 text-xs">
                  {job.lastError && job.state === "Failed" ? (
                    <span className="block truncate text-destructive" title={job.lastError}>
                      {job.lastError}
                    </span>
                  ) : job.result ? (
                    <span className="block truncate text-muted-foreground" title={job.result}>
                      {job.result}
                    </span>
                  ) : (
                    <span className="text-muted-foreground">—</span>
                  )}
                </TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {formatDuration(job.startedAtUnix, job.finishedAtUnix)}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}

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
  name,
  group,
  forEach,
  selected,
  onClick,
}: {
  name: string;
  group: TaskGroup | undefined;
  /** Fan-out source recorded in the execution plan (or workflow fallback). */
  forEach: string;
  selected: boolean;
  onClick: () => void;
}) {
  const state = group?.state ?? "Blocked";
  const tone = group?.tone ?? "neutral";
  const fanout = (group && group.instances.length > 1) || forEach !== "";
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
            : forEach
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
 * refreshes the scan config it reads from — except for the optional Resume
 * action, which the parent wires to the ResumeSecurityScan RPC and which
 * appears only for a Failed execution.
 */
export function ExecutionProgressPanel({
  namespace,
  execution,
  workflowTasks,
  onResume,
  findingLinkBase,
}: {
  namespace: string;
  execution: SecurityScanExecutionState;
  workflowTasks?: SecurityScanTaskConfig[];
  /** Called when the user asks to resume a Failed execution. */
  onResume?: () => Promise<void> | void;
  /** Route prefix for finding links in post-script job rows. */
  findingLinkBase?: string;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [focusTask, setFocusTask] = useState<string | null>(null);
  const [resuming, setResuming] = useState(false);

  const groups = useMemo(() => groupInstances(execution), [execution]);
  const groupByName = useMemo(() => new Map(groups.map((g) => [g.name, g])), [groups]);

  // The planned workflow supplies the dependency edges; execution state alone
  // has no graph shape. The dependency edges come from the execution's own
  // plan snapshot when the controller recorded one (authoritative: the source
  // workflow may have been edited since planning), falling back to the passed
  // workflow tasks for executions that predate plan recording. Nodes are the
  // union with the observed instance groups so instances of tasks missing
  // from the graph source still render (as edge-less nodes).
  const dagNodes = useMemo(() => {
    const plan = execution.plan ?? [];
    const nodes =
      plan.length > 0
        ? plan.map((p) => ({
            name: p.name.trim(),
            dependsOn: p.dependsOn,
            forEach: p.forEach,
            planned: true,
          }))
        : (workflowTasks ?? []).map((t) => ({
            name: t.name.trim(),
            dependsOn: t.dependsOn,
            forEach: t.forEach,
            planned: true,
          }));
    if (nodes.length === 0) return [];
    const known = new Set(nodes.map((n) => n.name));
    for (const group of groups) {
      if (!known.has(group.name)) {
        nodes.push({ name: group.name, dependsOn: [], forEach: "", planned: false });
      }
    }
    return nodes;
  }, [execution.plan, workflowTasks, groups]);

  const layout = useMemo(() => dagLayout(dagLayers(dagNodes)), [dagNodes]);
  const edges = useMemo(() => dagEdges(dagNodes), [dagNodes]);

  const totalInstances = execution.tasks.length;
  const doneInstances = execution.tasks.filter((t) => DONE_STATES.has(t.state)).length;
  // Task-level progress counts a task done only when EVERY instance of it is
  // terminal; the raw instance figure is appended when fan-out/ensembles make
  // the two differ, so "4/5" can never mean one five-instance task.
  const doneGroups = groups.filter((g) => g.done === g.instances.length).length;
  // Nullish fallbacks: tests (and older servers) may omit the arrays.
  const postScriptJobs = execution.postScriptJobs ?? [];
  const coverageGaps = execution.coverageGaps ?? [];
  const donePostScripts = postScriptJobs.filter((j) => POST_SCRIPT_DONE_STATES.has(j.state)).length;

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
              {doneGroups}/{groups.length} tasks done
              {totalInstances !== groups.length &&
                ` · ${doneInstances}/${totalInstances} instances`}
            </span>
          )}
          {postScriptJobs.length > 0 && (
            <span
              className="rounded-md bg-muted/60 px-2 py-0.5 text-muted-foreground ring-1 ring-inset ring-border/70"
              data-testid="execution-post-script-progress"
            >
              {donePostScripts}/{postScriptJobs.length} post-script pipelines done
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
          {onResume && execution.phase === "Failed" && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="ml-auto"
              disabled={resuming}
              data-testid="execution-resume"
              onClick={() => {
                setResuming(true);
                void Promise.resolve(onResume()).finally(() => setResuming(false));
              }}
            >
              <RotateCcw className="size-3.5" />
              {resuming ? "Resuming…" : "Resume execution"}
            </Button>
          )}
        </div>

        {coverageGaps.length > 0 && (
          <div
            role="alert"
            data-testid="execution-coverage-gaps"
            className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-[12.5px]"
          >
            <span className="font-medium">
              Partial coverage: read this execution's report as incomplete, not as an all-clear.
            </span>
            <ul className="mt-1 list-disc pl-5 text-muted-foreground">
              {coverageGaps.map((gap) => (
                <li key={gap}>{gap}</li>
              ))}
            </ul>
          </div>
        )}

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
                    name={node.name}
                    group={groupByName.get(node.name)}
                    forEach={node.forEach}
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
              const expandable = task.retries.length > 0 || task.outputJson !== "";
              return (
                <Fragment key={key}>
                  <TableRow data-testid={`execution-task-${key}`}>
                    <TableCell>
                      {expandable && (
                        <Button
                          type="button"
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`${isOpen ? "Hide" : "Show"} details for ${instanceLabel(task)}`}
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
                        {task.retries.length > 0 && (
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
                        )}
                        {task.outputJson !== "" && (
                          <div className="space-y-1 py-1" data-testid={`execution-output-${key}`}>
                            <p className="text-[11px] font-medium text-muted-foreground">
                              Structured output
                            </p>
                            <pre className="max-h-64 overflow-auto rounded-md border border-border/60 bg-muted/30 p-2 font-mono text-[11.5px] leading-relaxed">
                              {prettyJson(task.outputJson)}
                            </pre>
                          </div>
                        )}
                      </TableCell>
                    </TableRow>
                  )}
                </Fragment>
              );
            })}
          </TableBody>
        </Table>

        {postScriptJobs.length > 0 && (
          <PostScriptJobsTable
            namespace={namespace}
            jobs={postScriptJobs}
            findingLinkBase={findingLinkBase}
          />
        )}
      </div>
    </DetailSection>
  );
}
