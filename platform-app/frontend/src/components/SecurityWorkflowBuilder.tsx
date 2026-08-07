import { useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Link2, Plus, Trash2, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { FlowField } from "@/components/create-flow/create-flow";
import {
  SecurityScanTaskConfigSchema,
  SecurityScanTaskToolsSchema,
  SecurityWorkflowParameterSchema,
  type SecurityScanTaskConfig,
  type SecurityWorkflowParameter,
} from "@/rpc/platform/service_pb";

/**
 * Security specialist roles shipped as RoleInstruction bootstrap assets
 * (configs/roleinstructions). The picker offers these plus whatever role an
 * existing workflow already uses, so imported workflows stay editable.
 */
export const SECURITY_SPECIALIST_ROLES = [
  "security-reviewer",
  "vulnerability-hunter",
  "dependency-auditor",
  "finding-triager",
  "exploit-validator",
  "secrets-auditor",
  "threat-modeler",
] as const;

/**
 * One task being edited. Number-ish fields stay strings so an untouched
 * draft round-trips through the inputs byte-for-byte (the maxFindings
 * pattern); tool lists are comma-separated for the same reason.
 */
export interface WorkflowTaskDraft {
  name: string;
  objective: string;
  category: string;
  role: string;
  model: string;
  dependsOn: string[];
  maxFindings: string;
  /** Retry budget in deterministic execution (0-10); "" inherits the scan default. */
  maxRetries: string;
  /** Go duration string, e.g. "30m"; "" = no task limit. */
  timeout: string;
  maxTurns: string;
  /** Decimal USD ceiling, e.g. "2.50"; "" = none. */
  maxCostUsd: string;
  /** Comma-separated tool names; non-empty restricts the task run to these. */
  toolsAllowed: string;
  /** Comma-separated tool names denied to the task run (deny wins). */
  toolsDenied: string;
  /** JSON Schema (object form) contract for the task's structured output. */
  outputSchema: string;
  /** Name of a dependency whose JSON-array output this task fans out over. */
  forEach: string;
  maxInstances: string;
  repeats: string;
}

export interface WorkflowFieldError {
  field: string;
  message: string;
}

export function emptyWorkflowTask(): WorkflowTaskDraft {
  return {
    name: "",
    objective: "",
    category: "",
    role: "",
    model: "",
    dependsOn: [],
    maxFindings: "",
    maxRetries: "",
    timeout: "",
    maxTurns: "",
    maxCostUsd: "",
    toolsAllowed: "",
    toolsDenied: "",
    outputSchema: "",
    forEach: "",
    maxInstances: "",
    repeats: "",
  };
}

function splitCsv(value: string): string[] {
  return value
    .split(",")
    .map((v) => v.trim())
    .filter(Boolean);
}

/** workflowTasksFromProto converts proto tasks into editable drafts. */
export function workflowTasksFromProto(tasks: SecurityScanTaskConfig[]): WorkflowTaskDraft[] {
  return tasks.map((t) => ({
    name: t.name,
    objective: t.objective,
    category: t.category,
    role: t.role,
    model: t.model,
    dependsOn: [...t.dependsOn],
    maxFindings: t.maxFindings ? String(t.maxFindings) : "",
    maxRetries: t.maxRetries !== undefined ? String(t.maxRetries) : "",
    timeout: t.timeout,
    maxTurns: t.maxTurns ? String(t.maxTurns) : "",
    maxCostUsd: t.maxCostUsd,
    toolsAllowed: (t.tools?.allowed ?? []).join(", "),
    toolsDenied: (t.tools?.denied ?? []).join(", "),
    outputSchema: t.outputSchema,
    forEach: t.forEach,
    maxInstances: t.maxInstances ? String(t.maxInstances) : "",
    repeats: t.repeats ? String(t.repeats) : "",
  }));
}

/**
 * workflowTasksToProto serializes drafts back into proto tasks. Loading a
 * workflow with workflowTasksFromProto and saving untouched drafts produces
 * an identical message (round-trip safe).
 */
export function workflowTasksToProto(tasks: WorkflowTaskDraft[]): SecurityScanTaskConfig[] {
  return tasks.map((t) => {
    const allowed = splitCsv(t.toolsAllowed);
    const denied = splitCsv(t.toolsDenied);
    return create(SecurityScanTaskConfigSchema, {
      name: t.name.trim(),
      objective: t.objective,
      category: t.category.trim(),
      role: t.role.trim(),
      model: t.model.trim(),
      dependsOn: t.dependsOn.filter((d) => d.trim() !== ""),
      maxFindings: t.maxFindings.trim() ? Number(t.maxFindings) : 0,
      maxRetries: t.maxRetries.trim() !== "" ? Number(t.maxRetries) : undefined,
      timeout: t.timeout.trim(),
      maxTurns: t.maxTurns.trim() ? Number(t.maxTurns) : 0,
      maxCostUsd: t.maxCostUsd.trim(),
      tools:
        allowed.length > 0 || denied.length > 0
          ? create(SecurityScanTaskToolsSchema, { allowed, denied })
          : undefined,
      outputSchema: t.outputSchema,
      forEach: t.forEach.trim(),
      maxInstances: t.maxInstances.trim() ? Number(t.maxInstances) : 0,
      repeats: t.repeats.trim() ? Number(t.repeats) : 0,
    });
  });
}

/* ── Workflow parameters ─────────────────────────────────────── */

/** One scan-time input referenced as {{params.<name>}} in objectives. */
export interface WorkflowParameterDraft {
  name: string;
  description: string;
  default: string;
  required: boolean;
}

export function emptyWorkflowParameter(): WorkflowParameterDraft {
  return { name: "", description: "", default: "", required: false };
}

export function workflowParametersFromProto(
  parameters: SecurityWorkflowParameter[],
): WorkflowParameterDraft[] {
  return parameters.map((p) => ({
    name: p.name,
    description: p.description,
    default: p.default,
    required: p.required,
  }));
}

export function workflowParametersToProto(
  parameters: WorkflowParameterDraft[],
): SecurityWorkflowParameter[] {
  return parameters.map((p) =>
    create(SecurityWorkflowParameterSchema, {
      name: p.name.trim(),
      description: p.description,
      default: p.default,
      required: p.required,
    }),
  );
}

const PARAMETER_NAME_PATTERN = /^[A-Za-z_][A-Za-z0-9_]*$/;

export function validateWorkflowParameters(
  parameters: WorkflowParameterDraft[],
): WorkflowFieldError[] {
  const errors: WorkflowFieldError[] = [];
  const names = new Set<string>();
  parameters.forEach((param, index) => {
    const field = `parameters[${index}].name`;
    const name = param.name.trim();
    if (!PARAMETER_NAME_PATTERN.test(name)) {
      errors.push({
        field,
        message: `Invalid parameter name "${param.name}" (letters, digits and underscores, referenced as {{params.${name || "name"}}}).`,
      });
    } else if (names.has(name)) {
      errors.push({ field, message: `Duplicate parameter name "${name}".` });
    }
    names.add(name);
  });
  return errors;
}

const TASK_NAME_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const ROLE_PATTERN = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/;
/** Go time.ParseDuration syntax, e.g. "30m", "1h30m", "45s". */
const GO_DURATION_PATTERN = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;
const DECIMAL_PATTERN = /^\d+(\.\d+)?$/;

function isNonNegativeInt(value: string): boolean {
  return /^\d+$/.test(value);
}

/**
 * validateWorkflowTasks mirrors the server-side validation
 * (ValidateSecurityWorkflowTasks in api/triggers/v1alpha1): non-empty
 * workflow, unique DNS-label names, objectives, maxFindings bounds, valid
 * roles/models, resolvable dependsOn entries, execution budgets, forEach
 * fan-out, and an acyclic graph. Editors must refuse to save while this
 * returns errors.
 */
export function validateWorkflowTasks(tasks: WorkflowTaskDraft[]): WorkflowFieldError[] {
  const errors: WorkflowFieldError[] = [];
  if (tasks.length === 0) {
    return [{ field: "tasks", message: "A workflow needs at least one task." }];
  }
  const names = new Set<string>();
  tasks.forEach((task, index) => {
    const field = `tasks[${index}]`;
    const name = task.name.trim();
    if (!TASK_NAME_PATTERN.test(name) || name.length > 63) {
      errors.push({ field: `${field}.name`, message: `Invalid task name "${task.name}" (lowercase letters, digits and dashes).` });
    } else if (names.has(name)) {
      errors.push({ field: `${field}.name`, message: `Duplicate task name "${name}".` });
    }
    names.add(name);
    if (task.objective.trim() === "") {
      errors.push({ field: `${field}.objective`, message: `Task "${name || index + 1}" needs an objective.` });
    }
    const maxFindings = task.maxFindings.trim();
    if (maxFindings !== "" && !isNonNegativeInt(maxFindings)) {
      errors.push({ field: `${field}.maxFindings`, message: `Task "${name}" max findings must be a non-negative number.` });
    }
    const role = task.role.trim();
    if (role !== "" && !ROLE_PATTERN.test(role)) {
      errors.push({ field: `${field}.role`, message: `Invalid role "${task.role}".` });
    }
    if (task.model !== task.model.trim() || /\s/.test(task.model.trim())) {
      errors.push({ field: `${field}.model`, message: `Invalid model "${task.model}" (no whitespace).` });
    }
    const maxRetries = task.maxRetries.trim();
    if (maxRetries !== "" && (!isNonNegativeInt(maxRetries) || Number(maxRetries) > 10)) {
      errors.push({ field: `${field}.maxRetries`, message: `Task "${name}" max retries must be between 0 and 10.` });
    }
    const timeout = task.timeout.trim();
    if (timeout !== "" && !GO_DURATION_PATTERN.test(timeout)) {
      errors.push({ field: `${field}.timeout`, message: `Task "${name}" timeout must be a Go duration like "30m".` });
    }
    const maxTurns = task.maxTurns.trim();
    if (maxTurns !== "" && !isNonNegativeInt(maxTurns)) {
      errors.push({ field: `${field}.maxTurns`, message: `Task "${name}" max turns must be a non-negative number.` });
    }
    const maxCostUsd = task.maxCostUsd.trim();
    if (maxCostUsd !== "" && !DECIMAL_PATTERN.test(maxCostUsd)) {
      errors.push({ field: `${field}.maxCostUsd`, message: `Task "${name}" max cost must be a decimal USD amount like "2.50".` });
    }
    const outputSchema = task.outputSchema.trim();
    if (outputSchema !== "") {
      let parsed: unknown;
      try {
        parsed = JSON.parse(outputSchema);
      } catch {
        parsed = undefined;
      }
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
        errors.push({ field: `${field}.outputSchema`, message: `Task "${name}" output schema must be a JSON object.` });
      }
    }
    const forEach = task.forEach.trim();
    if (forEach !== "" && !task.dependsOn.includes(forEach)) {
      errors.push({ field: `${field}.forEach`, message: `Task "${name}" for-each must name one of its dependencies.` });
    }
    const maxInstances = task.maxInstances.trim();
    if (maxInstances !== "" && (!isNonNegativeInt(maxInstances) || Number(maxInstances) > 50)) {
      errors.push({ field: `${field}.maxInstances`, message: `Task "${name}" max instances must be between 0 and 50.` });
    }
    const repeats = task.repeats.trim();
    if (repeats !== "" && (!isNonNegativeInt(repeats) || Number(repeats) > 5)) {
      errors.push({ field: `${field}.repeats`, message: `Task "${name}" repeats must be between 0 and 5.` });
    }
  });
  tasks.forEach((task, index) => {
    const field = `tasks[${index}].dependsOn`;
    const name = task.name.trim();
    for (const dep of task.dependsOn) {
      if (dep === name) {
        errors.push({ field, message: `Task "${name}" cannot depend on itself.` });
      } else if (!names.has(dep)) {
        errors.push({ field, message: `Task "${name}" depends on unknown task "${dep}".` });
      }
    }
  });
  // Mirrors the server-side multi-instance rules: a fan-out source must be a
  // single-instance task, and a multi-instance task's aggregated output is a
  // JSON array so single-field access can never resolve.
  const byName = new Map<string, WorkflowTaskDraft>();
  for (const task of tasks) byName.set(task.name.trim(), task);
  const isMultiInstance = (task: WorkflowTaskDraft): boolean =>
    task.forEach.trim() !== "" || (isNonNegativeInt(task.repeats.trim()) && Number(task.repeats.trim()) > 1);
  tasks.forEach((task, index) => {
    const name = task.name.trim();
    const forEach = task.forEach.trim();
    if (forEach !== "") {
      const source = byName.get(forEach);
      if (source && isMultiInstance(source)) {
        errors.push({
          field: `tasks[${index}].forEach`,
          message: `Task "${name}" for-each source "${forEach}" is itself multi-instance (for-each or repeats); fan-out sources must be single-instance tasks.`,
        });
      }
    }
    const seenFieldRefs = new Set<string>();
    for (const match of task.objective.matchAll(/\{\{\s*tasks\.([a-zA-Z0-9-]+)\.output\./g)) {
      const ref = match[1];
      if (seenFieldRefs.has(ref)) continue;
      seenFieldRefs.add(ref);
      const source = byName.get(ref);
      if (source && isMultiInstance(source)) {
        errors.push({
          field: `tasks[${index}].objective`,
          message: `Task "${name}" references {{tasks.${ref}.output.<field>}} but task "${ref}" is multi-instance and its output is a JSON array of instance outputs; use {{tasks.${ref}.output}}.`,
        });
      }
    }
  });
  if (errors.length > 0) return errors;

  const cycle = workflowCycle(tasks);
  if (cycle.length > 0) {
    errors.push({ field: "tasks", message: `Dependency cycle: ${cycle.join(" → ")}.` });
  }
  return errors;
}

/** workflowCycle returns one dependency cycle, or [] when acyclic. */
export function workflowCycle(tasks: WorkflowTaskDraft[]): string[] {
  const deps = new Map<string, string[]>();
  for (const task of tasks) deps.set(task.name.trim(), task.dependsOn);
  const state = new Map<string, "visiting" | "done">();
  const stack: string[] = [];
  const visit = (name: string): string[] => {
    state.set(name, "visiting");
    stack.push(name);
    for (const dep of deps.get(name) ?? []) {
      if (state.get(dep) === "visiting") {
        return [...stack.slice(stack.indexOf(dep)), dep];
      }
      if (!state.has(dep) && deps.has(dep)) {
        const cycle = visit(dep);
        if (cycle.length > 0) return cycle;
      }
    }
    stack.pop();
    state.set(name, "done");
    return [];
  };
  for (const task of tasks) {
    const name = task.name.trim();
    if (!state.has(name)) {
      const cycle = visit(name);
      if (cycle.length > 0) return cycle;
    }
  }
  return [];
}

/**
 * workflowLayers groups tasks into topological layers: layer 0 has no
 * dependencies, each next layer depends only on earlier ones. Tasks trapped
 * in a cycle are appended as a final layer so the graph still renders while
 * validation reports the cycle.
 */
export function workflowLayers(tasks: WorkflowTaskDraft[]): WorkflowTaskDraft[][] {
  const known = new Set(tasks.map((t) => t.name.trim()));
  const placed = new Map<string, number>();
  const remaining = [...tasks];
  const layers: WorkflowTaskDraft[][] = [];
  for (let depth = 0; remaining.length > 0 && depth < tasks.length; depth++) {
    const ready = remaining.filter((t) =>
      t.dependsOn.every((dep) => !known.has(dep) || placed.has(dep)),
    );
    if (ready.length === 0) break;
    layers.push(ready);
    for (const t of ready) {
      placed.set(t.name.trim(), depth);
      remaining.splice(remaining.indexOf(t), 1);
    }
  }
  if (remaining.length > 0) layers.push(remaining);
  return layers;
}

const NODE_WIDTH = 148;
const NODE_HEIGHT = 34;
const LAYER_GAP = 64;
const NODE_GAP = 10;
const CANVAS_PAD = 12;

/** nextTaskName picks the first free "task-N" so a canvas-added node is drawable immediately. */
function nextTaskName(tasks: WorkflowTaskDraft[]): string {
  const used = new Set(tasks.map((t) => t.name.trim()));
  for (let n = 1; ; n++) {
    const candidate = `task-${n}`;
    if (!used.has(candidate)) return candidate;
  }
}

/**
 * WorkflowDagEditor is the interactive node/edge editor over the same layered
 * layout the old read-only view used (layout stays algorithmic — no drag
 * repositioning). It edits the parent-owned draft array via onChange:
 * - click a node to select it and open the inspector (rename, quick edits,
 *   delete — deleting detaches dependents' dependsOn);
 * - the per-node link handle starts connect mode: the next node clicked
 *   becomes a dependent of the source (target.dependsOn += source). Self
 *   edges, duplicates, and cycle-creating edges are rejected with a message;
 * - the × on an edge removes that dependency;
 * - New task adds an auto-named node directly on the canvas.
 */
export function WorkflowDagEditor({
  tasks,
  onChange,
  idPrefix = "wf",
}: {
  tasks: WorkflowTaskDraft[];
  onChange: (tasks: WorkflowTaskDraft[]) => void;
  idPrefix?: string;
}) {
  const [selectedIndex, setSelectedIndex] = useState<number | null>(null);
  const [connectFrom, setConnectFrom] = useState<number | null>(null);
  const [message, setMessage] = useState<string | null>(null);

  const drawable = tasks.filter((t) => t.name.trim() !== "");
  const layers = workflowLayers(drawable);
  const positions = new Map<string, { x: number; y: number }>();
  layers.forEach((layer, layerIndex) => {
    layer.forEach((task, taskIndex) => {
      positions.set(task.name.trim(), {
        x: CANVAS_PAD + layerIndex * (NODE_WIDTH + LAYER_GAP),
        y: CANVAS_PAD + taskIndex * (NODE_HEIGHT + NODE_GAP),
      });
    });
  });
  const width =
    layers.length > 0 ? layers.length * (NODE_WIDTH + LAYER_GAP) - LAYER_GAP + CANVAS_PAD * 2 : 0;
  const height =
    layers.length > 0
      ? Math.max(...layers.map((layer) => layer.length)) * (NODE_HEIGHT + NODE_GAP) -
        NODE_GAP +
        CANVAS_PAD * 2
      : 0;

  const selected = selectedIndex !== null ? tasks[selectedIndex] : undefined;
  const connectSource = connectFrom !== null ? tasks[connectFrom] : undefined;

  const addTask = () => {
    const name = nextTaskName(tasks);
    onChange([...tasks, { ...emptyWorkflowTask(), name }]);
    setSelectedIndex(tasks.length);
    setMessage(null);
  };

  const deleteTask = (index: number) => {
    const removedName = tasks[index].name.trim();
    onChange(
      tasks
        .filter((_, i) => i !== index)
        .map((t) => ({
          ...t,
          dependsOn: t.dependsOn.filter((d) => d !== removedName),
          forEach: t.forEach === removedName ? "" : t.forEach,
        })),
    );
    setSelectedIndex(null);
    setConnectFrom(null);
  };

  const renameTask = (index: number, nextName: string) => {
    const previous = tasks[index].name.trim();
    const next = nextName.trim();
    onChange(
      tasks.map((t, i) => {
        if (i === index) return { ...t, name: nextName };
        return {
          ...t,
          // Renames follow into other tasks' dependencies and fan-out refs.
          dependsOn: t.dependsOn.map((d) => (d === previous ? next : d)),
          forEach: t.forEach === previous ? next : t.forEach,
        };
      }),
    );
  };

  const completeConnect = (targetIndex: number) => {
    if (connectFrom === null) return;
    const sourceIndex = connectFrom;
    setConnectFrom(null);
    const source = tasks[sourceIndex].name.trim();
    const target = tasks[targetIndex];
    const targetName = target.name.trim();
    if (sourceIndex === targetIndex) {
      setMessage(`Rejected: task "${source}" cannot depend on itself.`);
      return;
    }
    if (target.dependsOn.includes(source)) {
      setMessage(`Rejected: "${targetName}" already depends on "${source}".`);
      return;
    }
    const next = tasks.map((t, i) =>
      i === targetIndex ? { ...t, dependsOn: [...t.dependsOn, source] } : t,
    );
    const cycle = workflowCycle(next);
    if (cycle.length > 0) {
      setMessage(
        `Rejected: "${targetName}" after "${source}" would create a dependency cycle (${cycle.join(" → ")}).`,
      );
      return;
    }
    setMessage(null);
    onChange(next);
  };

  const removeEdge = (dep: string, targetName: string) => {
    onChange(
      tasks.map((t) =>
        t.name.trim() === targetName
          ? {
              ...t,
              dependsOn: t.dependsOn.filter((d) => d !== dep),
              forEach: t.forEach === dep ? "" : t.forEach,
            }
          : t,
      ),
    );
    setMessage(null);
  };

  const nodeClick = (index: number) => {
    if (connectFrom !== null) {
      completeConnect(index);
      return;
    }
    setSelectedIndex((current) => (current === index ? null : index));
    setMessage(null);
  };

  return (
    <div
      className="space-y-2"
      onKeyDown={(event) => {
        if (event.key === "Escape" && connectFrom !== null) {
          setConnectFrom(null);
          setMessage(null);
        }
      }}
    >
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs font-medium text-muted-foreground">Dependency graph</p>
        <Button type="button" variant="outline" size="sm" onClick={addTask}>
          <Plus className="size-3.5" /> New task
        </Button>
      </div>
      {connectSource && (
        <p className="text-xs text-muted-foreground" data-testid="dag-connect-hint">
          Linking from <span className="font-mono">{connectSource.name.trim()}</span> — click the
          task that should run after it (Esc cancels).
        </p>
      )}
      {message && (
        <p role="status" className="text-xs text-destructive" data-testid="dag-message">
          {message}
        </p>
      )}
      {drawable.length === 0 ? (
        <p className="text-xs text-muted-foreground" data-testid="workflow-dag-empty">
          Name at least one task to see the dependency graph.
        </p>
      ) : (
        <div className="overflow-x-auto rounded-md border bg-muted/30" data-testid="workflow-dag">
          <div className="relative" style={{ width, height }}>
            <svg
              role="img"
              aria-label="Workflow dependency graph"
              width={width}
              height={height}
              viewBox={`0 0 ${width} ${height}`}
              className="absolute inset-0"
            >
              {drawable.flatMap((task) =>
                task.dependsOn
                  .filter((dep) => positions.has(dep) && positions.has(task.name.trim()))
                  .map((dep) => {
                    const from = positions.get(dep)!;
                    const to = positions.get(task.name.trim())!;
                    const x1 = from.x + NODE_WIDTH;
                    const y1 = from.y + NODE_HEIGHT / 2;
                    const x2 = to.x;
                    const y2 = to.y + NODE_HEIGHT / 2;
                    const mid = (x1 + x2) / 2;
                    return (
                      <path
                        key={`${dep}->${task.name}`}
                        d={`M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2} ${y2}`}
                        fill="none"
                        className="stroke-muted-foreground/50"
                        strokeWidth={1.25}
                      />
                    );
                  }),
              )}
            </svg>
            {drawable.flatMap((task) =>
              task.dependsOn
                .filter((dep) => positions.has(dep) && positions.has(task.name.trim()))
                .map((dep) => {
                  const from = positions.get(dep)!;
                  const to = positions.get(task.name.trim())!;
                  const mx = (from.x + NODE_WIDTH + to.x) / 2;
                  const my = (from.y + to.y + NODE_HEIGHT) / 2;
                  const targetName = task.name.trim();
                  return (
                    <button
                      key={`x-${dep}->${targetName}`}
                      type="button"
                      aria-label={`Remove dependency ${dep} → ${targetName}`}
                      title={`Remove dependency ${dep} → ${targetName}`}
                      onClick={() => removeEdge(dep, targetName)}
                      className="absolute z-10 flex size-4 -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full border bg-background text-muted-foreground hover:text-destructive"
                      style={{ left: mx, top: my }}
                    >
                      <X className="size-2.5" />
                    </button>
                  );
                }),
            )}
            {drawable.map((task) => {
              const name = task.name.trim();
              const pos = positions.get(name)!;
              const index = tasks.indexOf(task);
              const isSelected = selectedIndex === index;
              const isConnectSource = connectFrom === index;
              return (
                <div
                  key={name}
                  className="absolute"
                  style={{ left: pos.x, top: pos.y, width: NODE_WIDTH, height: NODE_HEIGHT }}
                >
                  <button
                    type="button"
                    data-testid={`dag-node-${name}`}
                    aria-pressed={isSelected}
                    title={
                      connectFrom !== null && !isConnectSource
                        ? `Run ${name} after ${connectSource?.name.trim()}`
                        : name
                    }
                    onClick={() => nodeClick(index)}
                    onKeyDown={(event) => {
                      if (event.key === "Delete" || event.key === "Backspace") {
                        event.preventDefault();
                        deleteTask(index);
                      }
                    }}
                    className={`flex h-full w-[calc(100%-1.25rem)] items-center truncate rounded-md border bg-background px-2 text-[11px] font-medium ${
                      isSelected
                        ? "border-primary ring-1 ring-primary"
                        : isConnectSource
                          ? "border-primary/60 border-dashed"
                          : "border-border hover:border-primary/50"
                    }`}
                  >
                    <span className="truncate">{name}</span>
                  </button>
                  <button
                    type="button"
                    aria-label={`Start dependency from ${name}`}
                    title={`Link: the next task clicked will run after ${name}`}
                    data-testid={`dag-connect-${name}`}
                    onClick={() => {
                      setSelectedIndex(null);
                      setMessage(null);
                      setConnectFrom((current) => (current === index ? null : index));
                    }}
                    className={`absolute right-0 top-1/2 flex size-4 -translate-y-1/2 items-center justify-center rounded-full border bg-background ${
                      isConnectSource ? "border-primary text-primary" : "text-muted-foreground hover:text-primary"
                    }`}
                  >
                    <Link2 className="size-2.5" />
                  </button>
                </div>
              );
            })}
          </div>
        </div>
      )}
      {selected && selectedIndex !== null && (
        <div className="space-y-3 rounded-md border bg-muted/20 p-3" data-testid="dag-inspector">
          <div className="flex items-center justify-between">
            <p className="text-xs font-medium">
              Task <span className="font-mono">{selected.name.trim() || "(unnamed)"}</span>
            </p>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label="Close inspector"
              onClick={() => setSelectedIndex(null)}
            >
              <X className="size-3.5" />
            </Button>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <FlowField id={`${idPrefix}-inspector-name`} label="Task name" required>
              <Input
                id={`${idPrefix}-inspector-name`}
                value={selected.name}
                onChange={(event) => renameTask(selectedIndex, event.target.value)}
                className="font-mono"
              />
            </FlowField>
            <FlowField id={`${idPrefix}-inspector-category`} label="Category">
              <Input
                id={`${idPrefix}-inspector-category`}
                value={selected.category}
                onChange={(event) =>
                  onChange(
                    tasks.map((t, i) =>
                      i === selectedIndex ? { ...t, category: event.target.value } : t,
                    ),
                  )
                }
              />
            </FlowField>
          </div>
          <FlowField id={`${idPrefix}-inspector-objective`} label="Objective" required>
            <Textarea
              id={`${idPrefix}-inspector-objective`}
              value={selected.objective}
              onChange={(event) =>
                onChange(
                  tasks.map((t, i) =>
                    i === selectedIndex ? { ...t, objective: event.target.value } : t,
                  ),
                )
              }
              className="min-h-14"
            />
          </FlowField>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="text-destructive"
            aria-label={`Delete task ${selected.name.trim()}`}
            onClick={() => deleteTask(selectedIndex)}
          >
            <Trash2 className="size-3.5" /> Delete task
          </Button>
        </div>
      )}
    </div>
  );
}

const selectClass = "h-8 rounded-md border border-input bg-background px-2 text-sm w-full";

/**
 * WorkflowParametersEditor edits the workflow's declared scan-time inputs
 * (name/description/default/required rows). Scans supply values via
 * spec.parameterValues; objectives reference them as {{params.<name>}}.
 */
export function WorkflowParametersEditor({
  parameters,
  onChange,
  idPrefix = "wf",
}: {
  parameters: WorkflowParameterDraft[];
  onChange: (parameters: WorkflowParameterDraft[]) => void;
  idPrefix?: string;
}) {
  const update = (index: number, patch: Partial<WorkflowParameterDraft>) => {
    onChange(parameters.map((p, i) => (i === index ? { ...p, ...patch } : p)));
  };
  return (
    <div className="space-y-2" data-testid="workflow-parameters">
      {parameters.map((param, index) => (
        <div
          key={index}
          className="grid items-end gap-2 rounded-md border p-2 sm:grid-cols-[1fr_1.4fr_1fr_auto_auto]"
          data-testid={`workflow-parameter-${index}`}
        >
          <FlowField id={`${idPrefix}-param-name-${index}`} label="Name" required>
            <Input
              id={`${idPrefix}-param-name-${index}`}
              value={param.name}
              onChange={(event) => update(index, { name: event.target.value })}
              placeholder="target_service"
              className="font-mono"
            />
          </FlowField>
          <FlowField id={`${idPrefix}-param-description-${index}`} label="Description">
            <Input
              id={`${idPrefix}-param-description-${index}`}
              value={param.description}
              onChange={(event) => update(index, { description: event.target.value })}
            />
          </FlowField>
          <FlowField
            id={`${idPrefix}-param-default-${index}`}
            label="Default"
            hint={param.required ? "Ignored while required." : undefined}
          >
            <Input
              id={`${idPrefix}-param-default-${index}`}
              value={param.default}
              onChange={(event) => update(index, { default: event.target.value })}
            />
          </FlowField>
          <label className="flex h-8 items-center gap-1.5 text-xs">
            <input
              type="checkbox"
              checked={param.required}
              onChange={(event) => update(index, { required: event.target.checked })}
              aria-label={`Parameter ${param.name.trim() || index + 1} required`}
            />
            Required
          </label>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={`Remove parameter ${param.name.trim() || index + 1}`}
            onClick={() => onChange(parameters.filter((_, i) => i !== index))}
          >
            <Trash2 className="size-3.5" />
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => onChange([...parameters, emptyWorkflowParameter()])}
      >
        Add parameter
      </Button>
    </div>
  );
}

/**
 * SecurityWorkflowBuilder is the structured task editor used by the security
 * workflow library and duplicated inline flows: add/remove/rename tasks, set
 * objective/category/role/model plus execution budgets and fan-out, pick
 * dependencies from the other task names, and edit the same graph directly
 * in the interactive DAG editor. It renders inline validation from
 * validateWorkflowTasks; callers must block save while errors exist.
 */
export function SecurityWorkflowBuilder({
  tasks,
  onChange,
  idPrefix = "wf",
}: {
  tasks: WorkflowTaskDraft[];
  onChange: (tasks: WorkflowTaskDraft[]) => void;
  idPrefix?: string;
}) {
  const errors = validateWorkflowTasks(tasks);
  const updateTask = (index: number, patch: Partial<WorkflowTaskDraft>) => {
    onChange(tasks.map((t, i) => (i === index ? { ...t, ...patch } : t)));
  };
  const removeTask = (index: number) => {
    const removedName = tasks[index].name.trim();
    onChange(
      tasks
        .filter((_, i) => i !== index)
        .map((t) => ({
          ...t,
          dependsOn: t.dependsOn.filter((d) => d !== removedName),
          forEach: t.forEach === removedName ? "" : t.forEach,
        })),
    );
  };
  const roleOptions = (current: string) => {
    const options = [...SECURITY_SPECIALIST_ROLES] as string[];
    const trimmed = current.trim();
    if (trimmed !== "" && !options.includes(trimmed)) options.push(trimmed);
    return options;
  };

  return (
    <div className="space-y-3">
      {tasks.map((task, index) => (
        <div key={index} className="space-y-3 rounded-md border p-3" data-testid={`workflow-task-${index}`}>
          <div className="grid gap-3 sm:grid-cols-2">
            <FlowField id={`${idPrefix}-task-name-${index}`} label="Task name" required>
              <Input
                id={`${idPrefix}-task-name-${index}`}
                value={task.name}
                onChange={(event) => {
                  const previous = task.name.trim();
                  const next = event.target.value;
                  onChange(
                    tasks.map((t, i) => {
                      if (i === index) return { ...t, name: next };
                      // Renames follow into other tasks' dependencies.
                      return {
                        ...t,
                        dependsOn: t.dependsOn.map((d) => (d === previous ? next.trim() : d)),
                        forEach: t.forEach === previous ? next.trim() : t.forEach,
                      };
                    }),
                  );
                }}
                placeholder="injection-hunt"
                className="font-mono"
              />
            </FlowField>
            <FlowField id={`${idPrefix}-task-category-${index}`} label="Category" hint="Vulnerability class tag.">
              <Input
                id={`${idPrefix}-task-category-${index}`}
                value={task.category}
                onChange={(event) => updateTask(index, { category: event.target.value })}
                placeholder="injection"
              />
            </FlowField>
          </div>
          <FlowField id={`${idPrefix}-task-objective-${index}`} label="Objective" required>
            <Textarea
              id={`${idPrefix}-task-objective-${index}`}
              value={task.objective}
              onChange={(event) => updateTask(index, { objective: event.target.value })}
              className="min-h-16"
              placeholder="Hunt for SQL injection in the API layer."
            />
          </FlowField>
          <div className="grid gap-3 sm:grid-cols-3">
            <FlowField id={`${idPrefix}-task-role-${index}`} label="Specialist role" hint="Empty = security-reviewer.">
              <select
                id={`${idPrefix}-task-role-${index}`}
                className={selectClass}
                value={task.role}
                onChange={(event) => updateTask(index, { role: event.target.value })}
              >
                <option value="">security-reviewer (default)</option>
                {roleOptions(task.role).map((role) => (
                  <option key={role} value={role}>
                    {role}
                  </option>
                ))}
              </select>
            </FlowField>
            <FlowField id={`${idPrefix}-task-model-${index}`} label="Model override" hint="Empty = scan default.">
              <Input
                id={`${idPrefix}-task-model-${index}`}
                value={task.model}
                onChange={(event) => updateTask(index, { model: event.target.value })}
                className="font-mono"
              />
            </FlowField>
            <FlowField
              id={`${idPrefix}-task-max-findings-${index}`}
              label="Max findings"
              hint="Empty or 0 = unlimited."
            >
              <Input
                id={`${idPrefix}-task-max-findings-${index}`}
                type="number"
                min={0}
                value={task.maxFindings}
                onChange={(event) => updateTask(index, { maxFindings: event.target.value })}
              />
            </FlowField>
          </div>
          <FlowField
            id={`${idPrefix}-task-depends-${index}`}
            label="Depends on"
            hint="Runs only after the selected tasks finish."
          >
            <div className="flex flex-wrap gap-2" id={`${idPrefix}-task-depends-${index}`}>
              {tasks
                .filter((other, i) => i !== index && other.name.trim() !== "")
                .map((other) => {
                  const dep = other.name.trim();
                  const checked = task.dependsOn.includes(dep);
                  return (
                    <label
                      key={dep}
                      className="inline-flex cursor-pointer items-center gap-1.5 rounded-md border px-2 py-1 text-xs"
                    >
                      <input
                        type="checkbox"
                        checked={checked}
                        onChange={() =>
                          updateTask(index, {
                            dependsOn: checked
                              ? task.dependsOn.filter((d) => d !== dep)
                              : [...task.dependsOn, dep],
                            ...(checked && task.forEach === dep ? { forEach: "" } : {}),
                          })
                        }
                      />
                      <span className="font-mono">{dep}</span>
                    </label>
                  );
                })}
              {tasks.filter((_, i) => i !== index).every((t) => t.name.trim() === "") && (
                <span className="text-xs text-muted-foreground">No other named tasks yet.</span>
              )}
            </div>
          </FlowField>
          <details className="rounded-md border border-border/70 px-3 py-2">
            <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
              Execution, budgets &amp; fan-out
            </summary>
            <div className="space-y-3 pt-3">
              <div className="grid gap-3 sm:grid-cols-4">
                <FlowField
                  id={`${idPrefix}-task-max-retries-${index}`}
                  label="Max retries"
                  hint="0-10; empty inherits the scan default."
                >
                  <Input
                    id={`${idPrefix}-task-max-retries-${index}`}
                    type="number"
                    min={0}
                    max={10}
                    value={task.maxRetries}
                    onChange={(event) => updateTask(index, { maxRetries: event.target.value })}
                  />
                </FlowField>
                <FlowField
                  id={`${idPrefix}-task-timeout-${index}`}
                  label="Timeout"
                  hint='Go duration, e.g. "30m"; empty = none.'
                >
                  <Input
                    id={`${idPrefix}-task-timeout-${index}`}
                    value={task.timeout}
                    onChange={(event) => updateTask(index, { timeout: event.target.value })}
                    placeholder="30m"
                    className="font-mono"
                  />
                </FlowField>
                <FlowField
                  id={`${idPrefix}-task-max-turns-${index}`}
                  label="Max turns"
                  hint="Empty or 0 = no limit."
                >
                  <Input
                    id={`${idPrefix}-task-max-turns-${index}`}
                    type="number"
                    min={0}
                    value={task.maxTurns}
                    onChange={(event) => updateTask(index, { maxTurns: event.target.value })}
                  />
                </FlowField>
                <FlowField
                  id={`${idPrefix}-task-max-cost-${index}`}
                  label="Max cost (USD)"
                  hint='e.g. "2.50"; empty = none.'
                >
                  <Input
                    id={`${idPrefix}-task-max-cost-${index}`}
                    value={task.maxCostUsd}
                    onChange={(event) => updateTask(index, { maxCostUsd: event.target.value })}
                    placeholder="5"
                    className="font-mono"
                  />
                </FlowField>
              </div>
              <div className="grid gap-3 sm:grid-cols-2">
                <FlowField
                  id={`${idPrefix}-task-tools-allowed-${index}`}
                  label="Allowed tools"
                  hint="Comma-separated; non-empty restricts the run to these."
                >
                  <Input
                    id={`${idPrefix}-task-tools-allowed-${index}`}
                    value={task.toolsAllowed}
                    onChange={(event) => updateTask(index, { toolsAllowed: event.target.value })}
                    placeholder="read_file, grep"
                    className="font-mono"
                  />
                </FlowField>
                <FlowField
                  id={`${idPrefix}-task-tools-denied-${index}`}
                  label="Denied tools"
                  hint="Comma-separated; deny wins over allow."
                >
                  <Input
                    id={`${idPrefix}-task-tools-denied-${index}`}
                    value={task.toolsDenied}
                    onChange={(event) => updateTask(index, { toolsDenied: event.target.value })}
                    placeholder="Bash"
                    className="font-mono"
                  />
                </FlowField>
              </div>
              <FlowField
                id={`${idPrefix}-task-output-schema-${index}`}
                label="Output schema"
                hint="JSON Schema (object form) for the task's structured output; dependents read it via {{tasks.name.output}}."
              >
                <Textarea
                  id={`${idPrefix}-task-output-schema-${index}`}
                  value={task.outputSchema}
                  onChange={(event) => updateTask(index, { outputSchema: event.target.value })}
                  className="min-h-16 font-mono"
                  placeholder='{"type":"object","properties":{...}}'
                />
              </FlowField>
              <div className="grid gap-3 sm:grid-cols-3">
                <FlowField
                  id={`${idPrefix}-task-for-each-${index}`}
                  label="Fan out per record of"
                  hint="A dependency whose JSON-array output spawns one instance per record ({{item.field}})."
                >
                  <select
                    id={`${idPrefix}-task-for-each-${index}`}
                    className={selectClass}
                    value={task.forEach}
                    onChange={(event) => updateTask(index, { forEach: event.target.value })}
                  >
                    <option value="">Off</option>
                    {task.dependsOn
                      .filter((d) => d.trim() !== "")
                      .map((dep) => (
                        <option key={dep} value={dep}>
                          {dep}
                        </option>
                      ))}
                    {task.forEach !== "" && !task.dependsOn.includes(task.forEach) && (
                      <option value={task.forEach}>{task.forEach}</option>
                    )}
                  </select>
                </FlowField>
                <FlowField
                  id={`${idPrefix}-task-max-instances-${index}`}
                  label="Max instances"
                  hint="Fan-out cap; empty or 0 = 10, max 50."
                >
                  <Input
                    id={`${idPrefix}-task-max-instances-${index}`}
                    type="number"
                    min={0}
                    max={50}
                    value={task.maxInstances}
                    onChange={(event) => updateTask(index, { maxInstances: event.target.value })}
                  />
                </FlowField>
                <FlowField
                  id={`${idPrefix}-task-repeats-${index}`}
                  label="Repeats"
                  hint="Ensemble instances; empty, 0 or 1 = single, max 5."
                >
                  <Input
                    id={`${idPrefix}-task-repeats-${index}`}
                    type="number"
                    min={0}
                    max={5}
                    value={task.repeats}
                    onChange={(event) => updateTask(index, { repeats: event.target.value })}
                  />
                </FlowField>
              </div>
            </div>
          </details>
          <Button type="button" variant="ghost" size="sm" onClick={() => removeTask(index)}>
            Remove task
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => onChange([...tasks, emptyWorkflowTask()])}
      >
        Add task
      </Button>

      <WorkflowDagEditor tasks={tasks} onChange={onChange} idPrefix={idPrefix} />

      {errors.length > 0 && (
        <ul className="space-y-1 rounded-md border border-destructive/40 bg-destructive/5 p-2.5" data-testid="workflow-errors">
          {errors.map((error, index) => (
            <li key={`${error.field}-${index}`} className="text-xs text-destructive">
              {error.message}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
