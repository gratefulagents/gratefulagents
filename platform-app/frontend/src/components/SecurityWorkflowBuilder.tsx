import { create } from "@bufbuild/protobuf";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { FlowField } from "@/components/create-flow/create-flow";
import {
  SecurityScanTaskConfigSchema,
  type SecurityScanTaskConfig,
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

/** One task being edited; maxFindings stays a string for input round-trips. */
export interface WorkflowTaskDraft {
  name: string;
  objective: string;
  category: string;
  role: string;
  model: string;
  dependsOn: string[];
  maxFindings: string;
}

export interface WorkflowFieldError {
  field: string;
  message: string;
}

export function emptyWorkflowTask(): WorkflowTaskDraft {
  return { name: "", objective: "", category: "", role: "", model: "", dependsOn: [], maxFindings: "" };
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
  }));
}

/**
 * workflowTasksToProto serializes drafts back into proto tasks. Loading a
 * workflow with workflowTasksFromProto and saving untouched drafts produces
 * an identical message (round-trip safe).
 */
export function workflowTasksToProto(tasks: WorkflowTaskDraft[]): SecurityScanTaskConfig[] {
  return tasks.map((t) =>
    create(SecurityScanTaskConfigSchema, {
      name: t.name.trim(),
      objective: t.objective,
      category: t.category.trim(),
      role: t.role.trim(),
      model: t.model.trim(),
      dependsOn: t.dependsOn.filter((d) => d.trim() !== ""),
      maxFindings: t.maxFindings.trim() ? Number(t.maxFindings) : 0,
    }),
  );
}

const TASK_NAME_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
const ROLE_PATTERN = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/;

/**
 * validateWorkflowTasks mirrors the server-side validation
 * (ValidateSecurityWorkflowTasks in api/triggers/v1alpha1): non-empty
 * workflow, unique DNS-label names, objectives, maxFindings bounds, valid
 * roles/models, resolvable dependsOn entries, and an acyclic graph. Editors
 * must refuse to save while this returns errors.
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
    if (maxFindings !== "" && (!/^\d+$/.test(maxFindings) || Number(maxFindings) < 0)) {
      errors.push({ field: `${field}.maxFindings`, message: `Task "${name}" max findings must be a non-negative number.` });
    }
    const role = task.role.trim();
    if (role !== "" && !ROLE_PATTERN.test(role)) {
      errors.push({ field: `${field}.role`, message: `Invalid role "${task.role}".` });
    }
    if (task.model !== task.model.trim() || /\s/.test(task.model.trim())) {
      errors.push({ field: `${field}.model`, message: `Invalid model "${task.model}" (no whitespace).` });
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

/**
 * WorkflowDagView is a read-only layered rendering of the current task graph:
 * columns are topological layers, curves are dependsOn edges.
 */
export function WorkflowDagView({ tasks }: { tasks: WorkflowTaskDraft[] }) {
  const drawable = tasks.filter((t) => t.name.trim() !== "");
  if (drawable.length === 0) {
    return (
      <p className="text-xs text-muted-foreground" data-testid="workflow-dag-empty">
        Name at least one task to see the dependency graph.
      </p>
    );
  }
  const layers = workflowLayers(drawable);
  const positions = new Map<string, { x: number; y: number }>();
  layers.forEach((layer, layerIndex) => {
    layer.forEach((task, taskIndex) => {
      positions.set(task.name.trim(), {
        x: layerIndex * (NODE_WIDTH + LAYER_GAP),
        y: taskIndex * (NODE_HEIGHT + NODE_GAP),
      });
    });
  });
  const width = layers.length * (NODE_WIDTH + LAYER_GAP) - LAYER_GAP;
  const height = Math.max(...layers.map((layer) => layer.length)) * (NODE_HEIGHT + NODE_GAP) - NODE_GAP;

  return (
    <div className="overflow-x-auto rounded-md border bg-muted/30 p-3" data-testid="workflow-dag">
      <svg
        role="img"
        aria-label="Workflow dependency graph"
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        className="min-w-full"
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
        {drawable.map((task) => {
          const pos = positions.get(task.name.trim())!;
          return (
            <g key={task.name} data-testid={`dag-node-${task.name.trim()}`}>
              <rect
                x={pos.x}
                y={pos.y}
                width={NODE_WIDTH}
                height={NODE_HEIGHT}
                rx={6}
                className="fill-background stroke-border"
                strokeWidth={1}
              />
              <text
                x={pos.x + NODE_WIDTH / 2}
                y={pos.y + NODE_HEIGHT / 2 + 4}
                textAnchor="middle"
                className="fill-foreground text-[11px] font-medium"
              >
                {task.name.trim().length > 20 ? `${task.name.trim().slice(0, 19)}…` : task.name.trim()}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}

const selectClass = "h-8 rounded-md border border-input bg-background px-2 text-sm w-full";

/**
 * SecurityWorkflowBuilder is the structured task editor used by the security
 * workflow library and duplicated inline flows: add/remove/rename tasks, set
 * objective/category/role/model/max findings, pick dependencies from the
 * other task names, and watch the live DAG. It renders inline validation from
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
        .map((t) => ({ ...t, dependsOn: t.dependsOn.filter((d) => d !== removedName) })),
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
                      if (previous === "" || !t.dependsOn.includes(previous)) return t;
                      // Renames follow into other tasks' dependencies.
                      return { ...t, dependsOn: t.dependsOn.map((d) => (d === previous ? next.trim() : d)) };
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

      <div className="space-y-1.5">
        <p className="text-xs font-medium text-muted-foreground">Dependency graph (read-only)</p>
        <WorkflowDagView tasks={tasks} />
      </div>

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
