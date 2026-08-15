import { useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Link2, Plus, Trash2, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { FlowField } from "@/components/create-flow/create-flow";
import { SkillPicker } from "@/components/SkillPicker";
import {
  DAG_NODE_HEIGHT,
  DAG_NODE_WIDTH,
  DagCanvas,
  DagEdgeLayer,
  dagEdges,
  dagLayers,
  dagLayout,
  edgeMidpoint,
} from "@/components/security-dag";
import {
  SecurityScanTaskConditionSchema,
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
 * draft round-trips through the inputs byte-for-byte; tool lists are
 * comma-separated for the same reason.
 */
export interface WorkflowTaskDraft {
  name: string;
  objective: string;
  category: string;
  role: string;
  model: string;
  dependsOn: string[];
  /** Retry budget in deterministic execution (0-10); "" inherits the scan default. */
  maxRetries: string;
  /** Go duration string, e.g. "30m"; "" = no task limit. */
  timeout: string;
  maxTurns: string;
  /** Decimal USD ceiling, e.g. "2.50"; "" = none. */
  maxCostUsd: string;
  skillRefs: string[];
  /** Comma-separated tool names; non-empty restricts the task run to these. */
  toolsAllowed: string;
  /** Comma-separated tool names denied to the task run (deny wins). */
  toolsDenied: string;
  /** JSON Schema (object form) contract for the task's structured output. */
  outputSchema: string;
  /** Name of a dependency whose JSON-array output this task fans out over. */
  forEach: string;
  maxInstances: string;
  /** Desired chunk count for complete fan-out (1-50); exclusive with maxInstances. */
  targetRuns: string;
  repeats: string;
  /** Dependency-output launch condition; all fields empty disables it. */
  whenTask: string;
  whenPath: string;
  whenEquals: string;
  whenOtherwiseOutput: string;
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
    maxRetries: "",
    timeout: "",
    maxTurns: "",
    maxCostUsd: "",
    skillRefs: [],
    toolsAllowed: "",
    toolsDenied: "",
    outputSchema: "",
    forEach: "",
    maxInstances: "",
    targetRuns: "",
    repeats: "",
    whenTask: "",
    whenPath: "",
    whenEquals: "",
    whenOtherwiseOutput: "",
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
    maxRetries: t.maxRetries !== undefined ? String(t.maxRetries) : "",
    timeout: t.timeout,
    maxTurns: t.maxTurns ? String(t.maxTurns) : "",
    maxCostUsd: t.maxCostUsd,
    skillRefs: [...t.skillRefs],
    toolsAllowed: (t.tools?.allowed ?? []).join(", "),
    toolsDenied: (t.tools?.denied ?? []).join(", "),
    outputSchema: t.outputSchema,
    forEach: t.forEach,
    maxInstances: t.maxInstances ? String(t.maxInstances) : "",
    targetRuns: t.targetRuns ? String(t.targetRuns) : "",
    repeats: t.repeats ? String(t.repeats) : "",
    whenTask: t.when?.task ?? "",
    whenPath: t.when?.path ?? "",
    whenEquals: t.when?.equals ?? "",
    whenOtherwiseOutput: t.when?.otherwiseOutput ?? "",
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
    const task = create(SecurityScanTaskConfigSchema, {
      name: t.name.trim(),
      objective: t.objective,
      category: t.category.trim(),
      role: t.role.trim(),
      model: t.model.trim(),
      dependsOn: t.dependsOn.filter((d) => d.trim() !== ""),
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
      targetRuns: t.targetRuns.trim() ? Number(t.targetRuns) : 0,
      repeats: t.repeats.trim() ? Number(t.repeats) : 0,
      skillRefs: t.skillRefs.map((name) => name.trim()).filter(Boolean),
      when:
        t.whenTask.trim() || t.whenPath.trim() || t.whenEquals.trim() || t.whenOtherwiseOutput.trim()
          ? create(SecurityScanTaskConditionSchema, {
              task: t.whenTask.trim(),
              path: t.whenPath.trim(),
              equals: t.whenEquals.trim(),
              otherwiseOutput: t.whenOtherwiseOutput.trim(),
            })
          : undefined,
    });
    return task;
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
 * workflow, unique DNS-label names, objectives, valid roles/models,
 * resolvable dependsOn entries, execution budgets, forEach
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
    const conditionEnabled = Boolean(
      task.whenTask.trim() || task.whenPath.trim() || task.whenEquals.trim() || task.whenOtherwiseOutput.trim(),
    );
    if (conditionEnabled) {
      const conditionTask = task.whenTask.trim();
      if (conditionTask === "" || !task.dependsOn.includes(conditionTask)) {
        errors.push({ field: `${field}.when.task`, message: `Task "${name}" condition must name one of its dependencies.` });
      }
      if (task.whenPath.trim() === "") {
        errors.push({ field: `${field}.when.path`, message: `Task "${name}" condition needs an output path.` });
      }
      let expected: unknown;
      try {
        expected = JSON.parse(task.whenEquals.trim());
      } catch {
        expected = undefined;
      }
      if (typeof expected !== "boolean" && typeof expected !== "string") {
        errors.push({ field: `${field}.when.equals`, message: `Task "${name}" condition equals must be a JSON boolean or string such as true or "detected".` });
      }
      const otherwiseOutput = task.whenOtherwiseOutput.trim();
      if (outputSchema !== "" && otherwiseOutput === "") {
        errors.push({ field: `${field}.when.otherwiseOutput`, message: `Task "${name}" needs otherwise output because it declares an output schema.` });
      } else if (otherwiseOutput !== "") {
        try {
          JSON.parse(otherwiseOutput);
        } catch {
          errors.push({ field: `${field}.when.otherwiseOutput`, message: `Task "${name}" otherwise output must be valid JSON.` });
        }
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
    const targetRuns = task.targetRuns.trim();
    if (targetRuns !== "" && (!isNonNegativeInt(targetRuns) || Number(targetRuns) < 1 || Number(targetRuns) > 50)) {
      errors.push({ field: `${field}.targetRuns`, message: `Task "${name}" target runs must be between 1 and 50.` });
    }
    if (maxInstances !== "" && Number(maxInstances) > 0 && targetRuns !== "") {
      errors.push({ field: `${field}.targetRuns`, message: `Task "${name}" cannot combine target runs with max instances.` });
    }
    if (targetRuns !== "" && Number(targetRuns) > 0 && task.forEach.trim() === "") {
      errors.push({ field: `${field}.targetRuns`, message: `Task "${name}" may set target runs only with fan-out.` });
    }
    if (targetRuns !== "" && Number(targetRuns) > 0 && outputSchema !== "") {
      try {
        const parsed = JSON.parse(outputSchema) as { type?: unknown };
        if (parsed.type !== "array") {
          errors.push({ field: `${field}.outputSchema`, message: `Task "${name}" output schema must declare type array when target runs is set.` });
        }
      } catch {
        // The generic output-schema validation above reports malformed JSON.
      }
    }
    const repeats = task.repeats.trim();
    if (targetRuns !== "" && Number(targetRuns) > 0 && Number(repeats) > 1) {
      errors.push({ field: `${field}.targetRuns`, message: `Task "${name}" cannot combine target runs with repeats.` });
    }
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
  // single-instance task, a multi-instance task's aggregated output is a
  // JSON array so single-field access can never resolve, and only a task
  // with an output schema ever publishes structured output to interpolate.
  const byName = new Map<string, WorkflowTaskDraft>();
  for (const task of tasks) byName.set(task.name.trim(), task);
  const isMultiInstance = (task: WorkflowTaskDraft): boolean =>
    task.forEach.trim() !== "" || (isNonNegativeInt(task.repeats.trim()) && Number(task.repeats.trim()) > 1);
  tasks.forEach((task, index) => {
    const name = task.name.trim();
    const conditionTask = task.whenTask.trim();
    if (conditionTask !== "") {
      const source = byName.get(conditionTask);
      if (source?.outputSchema.trim()) {
        try {
          let schema = JSON.parse(source.outputSchema) as { type?: unknown; properties?: Record<string, unknown> };
          if (schema.type !== undefined && schema.type !== "object") {
            errors.push({ field: `tasks[${index}].when.task`, message: `Task "${name}" condition source "${conditionTask}" must publish an object.` });
          } else {
            const segments = task.whenPath.trim().split(".");
            for (const [segmentIndex, segment] of segments.entries()) {
              if (!schema.properties) break;
              const child = schema.properties[segment] as { type?: unknown; properties?: Record<string, unknown> } | undefined;
              const last = segmentIndex === segments.length - 1;
              const incompatible = !child || (!last && child.type !== undefined && child.type !== "object") ||
                (last && (child.type === "object" || child.type === "array"));
              if (incompatible) {
                errors.push({ field: `tasks[${index}].when.path`, message: `Task "${name}" condition path is incompatible with "${conditionTask}" output schema.` });
                break;
              }
              schema = child;
            }
          }
        } catch {
          // Generic output-schema validation reports malformed JSON.
        }
      }
    }
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
    const seenOutputRefs = new Set<string>();
    for (const match of task.objective.matchAll(/\{\{\s*tasks\.([a-zA-Z0-9-]+)\.output/g)) {
      const ref = match[1];
      if (seenOutputRefs.has(ref)) continue;
      seenOutputRefs.add(ref);
      const source = byName.get(ref);
      if (source && ref !== name && source.outputSchema.trim() === "") {
        errors.push({
          field: `tasks[${index}].objective`,
          message: `Task "${name}" references {{tasks.${ref}.output}} but task "${ref}" declares no output schema and therefore never publishes structured output; add an output schema to "${ref}" or stop interpolating its output.`,
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
 * validation reports the cycle. (Delegates to the shared security DAG layout
 * so the builder and the execution view draw identical graphs.)
 */
export function workflowLayers(tasks: WorkflowTaskDraft[]): WorkflowTaskDraft[][] {
  return dagLayers(tasks);
}

/** nextTaskName picks the first free "task-N" so a canvas-added node is drawable immediately. */
function nextTaskName(tasks: WorkflowTaskDraft[]): string {
  const used = new Set(tasks.map((t) => t.name.trim()));
  for (let n = 1; ; n++) {
    const candidate = `task-${n}`;
    if (!used.has(candidate)) return candidate;
  }
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
 * SecurityWorkflowBuilder is the graph-first task editor used by the security
 * workflow library and AI-draft review flows. The dependency graph is the
 * single editing surface (layout stays algorithmic — no drag repositioning):
 * - Add task drops an auto-named node on the canvas and selects it;
 * - clicking a node opens the full inspector beside the canvas (objective,
 *   role, model, dependencies, skills, execution budgets & fan-out, delete —
 *   deleting detaches dependents' dependsOn and renames follow into them);
 * - the per-node link handle starts connect mode: the next node clicked
 *   becomes a dependent of the source (target.dependsOn += source). Self
 *   edges, duplicates, and cycle-creating edges are rejected with a message;
 * - hovering the canvas reveals an × affordance on each edge to remove it.
 * It renders inline validation from validateWorkflowTasks; callers must
 * block save while errors exist.
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
  const [selectedIndex, setSelectedIndex] = useState<number | null>(tasks.length > 0 ? 0 : null);
  const [connectFrom, setConnectFrom] = useState<number | null>(null);
  const [message, setMessage] = useState<string | null>(null);

  const errors = validateWorkflowTasks(tasks);
  const drawable = tasks.filter((t) => t.name.trim() !== "");
  const layout = dagLayout(workflowLayers(drawable));
  const edges = dagEdges(
    drawable.map((t) => ({ name: t.name.trim(), dependsOn: t.dependsOn, forEach: t.forEach })),
  );

  const selected =
    selectedIndex !== null && selectedIndex < tasks.length ? tasks[selectedIndex] : undefined;
  const connectSource =
    connectFrom !== null && connectFrom < tasks.length ? tasks[connectFrom] : undefined;

  const updateTask = (index: number, patch: Partial<WorkflowTaskDraft>) => {
    onChange(tasks.map((t, i) => (i === index ? { ...t, ...patch } : t)));
  };

  const addTask = () => {
    const name = nextTaskName(tasks);
    onChange([...tasks, { ...emptyWorkflowTask(), name }]);
    setSelectedIndex(tasks.length);
    setConnectFrom(null);
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
    setSelectedIndex(index);
    setMessage(null);
  };

  const roleOptions = (current: string) => {
    const options = [...SECURITY_SPECIALIST_ROLES] as string[];
    const trimmed = current.trim();
    if (trimmed !== "" && !options.includes(trimmed)) options.push(trimmed);
    return options;
  };

  const unnamedIndexes = tasks
    .map((t, index) => ({ task: t, index }))
    .filter(({ task }) => task.name.trim() === "")
    .map(({ index }) => index);

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
        <div>
          <p className="text-[12.5px] font-medium">Tasks &amp; dependencies</p>
          <p className="text-[11px] text-muted-foreground">
            Click a task to edit it in the inspector; use a task's{" "}
            <Link2 className="inline size-3 align-[-2px]" aria-hidden /> handle to draw
            &ldquo;runs after&rdquo; edges.
          </p>
        </div>
        <Button type="button" variant="outline" size="sm" onClick={addTask}>
          <Plus className="size-3.5" /> Add task
        </Button>
      </div>
      {connectSource && (
        <p
          className="rounded-md border border-primary/30 bg-primary/5 px-2.5 py-1.5 text-xs"
          data-testid="dag-connect-hint"
        >
          Linking from <span className="font-mono font-medium">{connectSource.name.trim()}</span> —
          click the task that should run after it (Esc cancels).
        </p>
      )}
      {message && (
        <p
          role="status"
          className="rounded-md border border-destructive/40 bg-destructive/5 px-2.5 py-1.5 text-xs text-destructive"
          data-testid="dag-message"
        >
          {message}
        </p>
      )}
      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(300px,360px)] lg:items-stretch">
        <div className="min-w-0 space-y-2">
          {drawable.length === 0 ? (
            <div
              className="flex h-44 flex-col items-center justify-center gap-1 rounded-xl border border-dashed bg-muted/20 px-4 text-center"
              data-testid="workflow-dag-empty"
            >
              <p className="text-sm font-medium">
                {tasks.length === 0 ? "No tasks yet" : "Name at least one task"}
              </p>
              <p className="max-w-sm text-xs text-muted-foreground">
                {tasks.length === 0
                  ? "Add a task to start building the workflow graph."
                  : "Tasks appear on the graph once they have a name."}
              </p>
            </div>
          ) : (
            <DagCanvas layout={layout} testId="workflow-dag">
              <DagEdgeLayer edges={edges} layout={layout} label="Workflow dependency graph" />
              {edges.map((edge) => {
                const mid = edgeMidpoint(layout, edge.from, edge.to);
                if (!mid) return null;
                return (
                  <button
                    key={`x-${edge.from}->${edge.to}`}
                    type="button"
                    aria-label={`Remove dependency ${edge.from} → ${edge.to}`}
                    title={`Remove dependency ${edge.from} → ${edge.to}`}
                    onClick={() => removeEdge(edge.from, edge.to)}
                    className="absolute z-10 flex size-5 -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full border bg-background text-muted-foreground opacity-0 shadow-sm transition-opacity hover:border-destructive/50 hover:text-destructive focus-visible:opacity-100 group-hover/dag:opacity-100"
                    style={{ left: mid.x, top: mid.y }}
                  >
                    <X className="size-3" />
                  </button>
                );
              })}
              {drawable.map((task) => {
                const name = task.name.trim();
                const pos = layout.positions.get(name)!;
                const index = tasks.indexOf(task);
                const isSelected = selectedIndex === index;
                const isConnectSource = connectFrom === index;
                const isConnectTarget = connectFrom !== null && !isConnectSource;
                return (
                  <div
                    key={name}
                    className="absolute"
                    style={{
                      left: pos.x,
                      top: pos.y,
                      width: DAG_NODE_WIDTH,
                      height: DAG_NODE_HEIGHT,
                    }}
                  >
                    <button
                      type="button"
                      data-testid={`dag-node-${name}`}
                      aria-pressed={isSelected}
                      title={
                        isConnectTarget ? `Run ${name} after ${connectSource?.name.trim()}` : name
                      }
                      onClick={() => nodeClick(index)}
                      onKeyDown={(event) => {
                        if (event.key === "Delete" || event.key === "Backspace") {
                          event.preventDefault();
                          deleteTask(index);
                        }
                      }}
                      className={`flex h-full w-full flex-col justify-center gap-1 overflow-hidden rounded-lg border bg-card px-3 text-left shadow-sm transition-[border-color,box-shadow] ${
                        isSelected
                          ? "border-primary ring-1 ring-primary"
                          : isConnectSource
                            ? "border-dashed border-primary/70"
                            : isConnectTarget
                              ? "cursor-crosshair border-primary/40 ring-1 ring-primary/20 hover:border-primary hover:ring-primary/50"
                              : "border-border hover:border-primary/50 hover:shadow-md"
                      }`}
                    >
                      <span className="flex min-w-0 items-center gap-1.5">
                        <span className="truncate font-mono text-xs font-medium leading-tight">
                          {name}
                        </span>
                        {task.outputSchema.trim() && (
                          <span
                            aria-label="Produces structured output"
                            title="Produces structured output (JSON Schema contract)"
                            className="size-1.5 shrink-0 rounded-full bg-primary/70"
                          />
                        )}
                      </span>
                      <span className="flex min-w-0 items-center gap-1 text-[10.5px] leading-tight text-muted-foreground">
                        <span className="truncate">{task.role.trim() || "security-reviewer"}</span>
                        {task.forEach.trim() && (
                          <span
                            className="shrink-0 rounded bg-primary/10 px-1 py-px text-[9.5px] font-medium text-primary"
                            title={`Fans out per record of ${task.forEach.trim()}'s output`}
                          >
                            fan-out
                          </span>
                        )}
                        {Number(task.repeats.trim() || "0") > 1 && (
                          <span
                            className="shrink-0 rounded bg-muted px-1 py-px text-[9.5px] font-medium"
                            title={`Ensemble: ${task.repeats.trim()} repeated instances`}
                          >
                            ×{task.repeats.trim()}
                          </span>
                        )}
                      </span>
                    </button>
                    {task.dependsOn.length > 0 && (
                      <span
                        aria-hidden
                        className="absolute -left-1 top-1/2 size-2 -translate-y-1/2 rounded-full border border-muted-foreground/50 bg-background"
                      />
                    )}
                    <button
                      type="button"
                      aria-label={`Start dependency from ${name}`}
                      title={`Link: the next task clicked will run after ${name}`}
                      data-testid={`dag-connect-${name}`}
                      onClick={() => {
                        setMessage(null);
                        setConnectFrom((current) => (current === index ? null : index));
                      }}
                      className={`absolute -right-2.5 top-1/2 flex size-5 -translate-y-1/2 items-center justify-center rounded-full border bg-background shadow-sm transition-colors ${
                        isConnectSource
                          ? "border-primary text-primary"
                          : "border-border text-muted-foreground hover:border-primary hover:text-primary"
                      }`}
                    >
                      <Link2 className="size-3" />
                    </button>
                  </div>
                );
              })}
            </DagCanvas>
          )}
          {unnamedIndexes.length > 0 && (
            <div
              className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground"
              data-testid="workflow-dag-unnamed"
            >
              <span>
                Not on the graph until named — select to edit{unnamedIndexes.length > 1 ? ":" : " it:"}
              </span>
              {unnamedIndexes.map((index, n) => (
                <button
                  key={index}
                  type="button"
                  data-testid={`dag-unnamed-${index}`}
                  aria-pressed={selectedIndex === index}
                  onClick={() => {
                    setSelectedIndex(index);
                    setConnectFrom(null);
                    setMessage(null);
                  }}
                  className={`rounded-md border px-2 py-0.5 font-mono transition-colors ${
                    selectedIndex === index
                      ? "border-primary text-primary"
                      : "hover:border-primary/50 hover:text-foreground"
                  }`}
                >
                  Unnamed task #{n + 1}
                </button>
              ))}
            </div>
          )}
        </div>

        {selected && selectedIndex !== null ? (
          <div className="space-y-3 rounded-xl border bg-card/50 p-3.5" data-testid="dag-inspector">
            <div className="flex items-center justify-between gap-2">
              <p className="min-w-0 truncate text-xs font-medium">
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
                  placeholder="injection-hunt"
                  className="font-mono"
                />
              </FlowField>
              <FlowField
                id={`${idPrefix}-inspector-category`}
                label="Category"
                hint="Vulnerability class tag."
              >
                <Input
                  id={`${idPrefix}-inspector-category`}
                  value={selected.category}
                  onChange={(event) => updateTask(selectedIndex, { category: event.target.value })}
                  placeholder="injection"
                />
              </FlowField>
            </div>
            <FlowField id={`${idPrefix}-inspector-objective`} label="Objective" required>
              <Textarea
                id={`${idPrefix}-inspector-objective`}
                value={selected.objective}
                onChange={(event) => updateTask(selectedIndex, { objective: event.target.value })}
                className="min-h-20"
                placeholder="Hunt for SQL injection in the API layer."
              />
            </FlowField>
            <div className="grid gap-3 sm:grid-cols-2">
              <FlowField
                id={`${idPrefix}-inspector-role`}
                label="Specialist role"
                hint="Empty = security-reviewer."
              >
                <select
                  id={`${idPrefix}-inspector-role`}
                  className={selectClass}
                  value={selected.role}
                  onChange={(event) => updateTask(selectedIndex, { role: event.target.value })}
                >
                  <option value="">security-reviewer (default)</option>
                  {roleOptions(selected.role).map((role) => (
                    <option key={role} value={role}>
                      {role}
                    </option>
                  ))}
                </select>
              </FlowField>
              <FlowField
                id={`${idPrefix}-inspector-model`}
                label="Model override"
                hint="Empty = scan default."
              >
                <Input
                  id={`${idPrefix}-inspector-model`}
                  value={selected.model}
                  onChange={(event) => updateTask(selectedIndex, { model: event.target.value })}
                  className="font-mono"
                />
              </FlowField>
            </div>
            <FlowField
              id={`${idPrefix}-inspector-depends`}
              label="Runs after"
              hint="Runs only after the selected tasks finish; the link handles on the graph edit the same edges."
            >
              <div className="flex flex-wrap gap-2" id={`${idPrefix}-inspector-depends`}>
                {tasks
                  .filter((other, i) => i !== selectedIndex && other.name.trim() !== "")
                  .map((other) => {
                    const dep = other.name.trim();
                    const checked = selected.dependsOn.includes(dep);
                    return (
                      <label
                        key={dep}
                        className={`inline-flex cursor-pointer items-center gap-1.5 rounded-md border px-2 py-1 text-xs transition-colors ${
                          checked ? "border-primary/50 bg-primary/5" : "hover:border-primary/40"
                        }`}
                      >
                        <input
                          type="checkbox"
                          checked={checked}
                          onChange={() =>
                            updateTask(selectedIndex, {
                              dependsOn: checked
                                ? selected.dependsOn.filter((d) => d !== dep)
                                : [...selected.dependsOn, dep],
                              ...(checked && selected.forEach === dep ? { forEach: "" } : {}),
                            })
                          }
                        />
                        <span className="font-mono">{dep}</span>
                      </label>
                    );
                  })}
                {tasks.filter((_, i) => i !== selectedIndex).every((t) => t.name.trim() === "") && (
                  <span className="text-xs text-muted-foreground">No other named tasks yet.</span>
                )}
              </div>
            </FlowField>
            <FlowField
              label="Skills"
              hint="Reusable instructions loaded only for this task. A skill may also bring required MCP servers."
            >
              <SkillPicker
                selected={selected.skillRefs}
                onChange={(skillRefs) => updateTask(selectedIndex, { skillRefs })}
              />
            </FlowField>
            <details className="rounded-md border border-border/70 px-3 py-2">
              <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
                Execution, budgets &amp; fan-out
              </summary>
              <div className="space-y-3 pt-3">
                <div className="grid gap-3 sm:grid-cols-2">
                  <FlowField
                    id={`${idPrefix}-inspector-max-retries`}
                    label="Max retries"
                    hint="0-10; empty inherits the scan default."
                  >
                    <Input
                      id={`${idPrefix}-inspector-max-retries`}
                      type="number"
                      min={0}
                      max={10}
                      value={selected.maxRetries}
                      onChange={(event) =>
                        updateTask(selectedIndex, { maxRetries: event.target.value })
                      }
                    />
                  </FlowField>
                  <FlowField
                    id={`${idPrefix}-inspector-timeout`}
                    label="Timeout"
                    hint='Go duration like "30m"; empty = none.'
                  >
                    <Input
                      id={`${idPrefix}-inspector-timeout`}
                      value={selected.timeout}
                      onChange={(event) =>
                        updateTask(selectedIndex, { timeout: event.target.value })
                      }
                      placeholder="30m"
                      className="font-mono"
                    />
                  </FlowField>
                  <FlowField
                    id={`${idPrefix}-inspector-max-turns`}
                    label="Max turns"
                    hint="Empty or 0 = no limit."
                  >
                    <Input
                      id={`${idPrefix}-inspector-max-turns`}
                      type="number"
                      min={0}
                      value={selected.maxTurns}
                      onChange={(event) =>
                        updateTask(selectedIndex, { maxTurns: event.target.value })
                      }
                    />
                  </FlowField>
                  <FlowField
                    id={`${idPrefix}-inspector-max-cost`}
                    label="Max cost (USD)"
                    hint='Decimal like "2.50"; empty = none.'
                  >
                    <Input
                      id={`${idPrefix}-inspector-max-cost`}
                      value={selected.maxCostUsd}
                      onChange={(event) =>
                        updateTask(selectedIndex, { maxCostUsd: event.target.value })
                      }
                      placeholder="2.50"
                      className="font-mono"
                    />
                  </FlowField>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <FlowField
                    id={`${idPrefix}-inspector-tools-allowed`}
                    label="Allowed tools"
                    hint="Comma-separated; non-empty restricts the run to these."
                  >
                    <Input
                      id={`${idPrefix}-inspector-tools-allowed`}
                      value={selected.toolsAllowed}
                      onChange={(event) =>
                        updateTask(selectedIndex, { toolsAllowed: event.target.value })
                      }
                      placeholder="read_file, grep"
                      className="font-mono"
                    />
                  </FlowField>
                  <FlowField
                    id={`${idPrefix}-inspector-tools-denied`}
                    label="Denied tools"
                    hint="Comma-separated; deny wins over allow."
                  >
                    <Input
                      id={`${idPrefix}-inspector-tools-denied`}
                      value={selected.toolsDenied}
                      onChange={(event) =>
                        updateTask(selectedIndex, { toolsDenied: event.target.value })
                      }
                      placeholder="Bash"
                      className="font-mono"
                    />
                  </FlowField>
                </div>
                <FlowField
                  id={`${idPrefix}-inspector-output-schema`}
                  label="Output schema"
                  hint="JSON Schema (object form) for the task's structured output; dependents read it via {{tasks.name.output}}."
                >
                  <Textarea
                    id={`${idPrefix}-inspector-output-schema`}
                    value={selected.outputSchema}
                    onChange={(event) =>
                      updateTask(selectedIndex, { outputSchema: event.target.value })
                    }
                    className="min-h-16 font-mono"
                    placeholder='{"type":"object","properties":{...}}'
                  />
                </FlowField>
                <div className="grid gap-3 sm:grid-cols-2">
                  <FlowField
                    id={`${idPrefix}-inspector-when-task`}
                    label="Run only when"
                    hint="Dependency whose structured output controls whether an AgentRun is created."
                  >
                    <select
                      id={`${idPrefix}-inspector-when-task`}
                      className={selectClass}
                      value={selected.whenTask}
                      onChange={(event) => updateTask(selectedIndex, { whenTask: event.target.value })}
                    >
                      <option value="">Off</option>
                      {selected.dependsOn.filter(Boolean).map((dep) => (
                        <option key={dep} value={dep}>{dep}</option>
                      ))}
                    </select>
                  </FlowField>
                  <FlowField
                    id={`${idPrefix}-inspector-when-path`}
                    label="Condition path"
                    hint="Dot-separated object path in that task's structured output."
                  >
                    <Input
                      id={`${idPrefix}-inspector-when-path`}
                      value={selected.whenPath}
                      onChange={(event) => updateTask(selectedIndex, { whenPath: event.target.value })}
                      placeholder="specialists.evm-protocol-specialist"
                      className="font-mono"
                    />
                  </FlowField>
                  <FlowField
                    id={`${idPrefix}-inspector-when-equals`}
                    label="Condition equals"
                    hint={'JSON boolean or string that enables the run, such as true or "detected".'}
                  >
                    <Input
                      id={`${idPrefix}-inspector-when-equals`}
                      value={selected.whenEquals}
                      onChange={(event) => updateTask(selectedIndex, { whenEquals: event.target.value })}
                      placeholder="true"
                      className="font-mono"
                    />
                  </FlowField>
                  <FlowField
                    id={`${idPrefix}-inspector-when-otherwise`}
                    label="Otherwise output"
                    hint="Structured JSON published without launching an agent when the condition is false."
                  >
                    <Textarea
                      id={`${idPrefix}-inspector-when-otherwise`}
                      value={selected.whenOtherwiseOutput}
                      onChange={(event) => updateTask(selectedIndex, { whenOtherwiseOutput: event.target.value })}
                      placeholder='{"status":"skipped"}'
                      className="min-h-16 font-mono"
                    />
                  </FlowField>
                </div>
                <div className="grid gap-3 sm:grid-cols-2">
                  <FlowField
                    id={`${idPrefix}-inspector-for-each`}
                    label="Fan out per record of"
                    hint="A dependency whose JSON-array output spawns one instance per record ({{item.field}})."
                  >
                    <select
                      id={`${idPrefix}-inspector-for-each`}
                      className={selectClass}
                      value={selected.forEach}
                      onChange={(event) =>
                        updateTask(selectedIndex, { forEach: event.target.value })
                      }
                    >
                      <option value="">Off</option>
                      {selected.dependsOn
                        .filter((d) => d.trim() !== "")
                        .map((dep) => (
                          <option key={dep} value={dep}>
                            {dep}
                          </option>
                        ))}
                      {selected.forEach !== "" && !selected.dependsOn.includes(selected.forEach) && (
                        <option value={selected.forEach}>{selected.forEach}</option>
                      )}
                    </select>
                  </FlowField>
                  <FlowField
                    id={`${idPrefix}-inspector-max-instances`}
                    label="Max instances"
                    hint="Legacy fan-out cap; empty or 0 = 10, max 50."
                  >
                    <Input
                      id={`${idPrefix}-inspector-max-instances`}
                      type="number"
                      min={0}
                      max={50}
                      value={selected.maxInstances}
                      onChange={(event) =>
                        updateTask(selectedIndex, { maxInstances: event.target.value })
                      }
                    />
                  </FlowField>
                  <FlowField
                    id={`${idPrefix}-inspector-target-runs`}
                    label="Target runs"
                    hint="Split every record across 1-50 runs; exclusive with max instances."
                  >
                    <Input
                      id={`${idPrefix}-inspector-target-runs`}
                      type="number"
                      min={1}
                      max={50}
                      value={selected.targetRuns}
                      onChange={(event) =>
                        updateTask(selectedIndex, { targetRuns: event.target.value })
                      }
                    />
                  </FlowField>
                  <FlowField
                    id={`${idPrefix}-inspector-repeats`}
                    label="Repeats"
                    hint="Ensemble instances; empty, 0 or 1 = single, max 5."
                  >
                    <Input
                      id={`${idPrefix}-inspector-repeats`}
                      type="number"
                      min={0}
                      max={5}
                      value={selected.repeats}
                      onChange={(event) =>
                        updateTask(selectedIndex, { repeats: event.target.value })
                      }
                    />
                  </FlowField>
                </div>
              </div>
            </details>
            <div className="flex justify-end border-t border-border/60 pt-2.5">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="text-destructive"
                aria-label={`Delete task ${selected.name.trim() || "(unnamed)"}`}
                onClick={() => deleteTask(selectedIndex)}
              >
                <Trash2 className="size-3.5" /> Delete task
              </Button>
            </div>
          </div>
        ) : (
          tasks.length > 0 && (
            <div
              className="hidden min-h-32 items-center justify-center rounded-xl border border-dashed p-4 text-center lg:flex"
              data-testid="dag-inspector-empty"
            >
              <p className="max-w-48 text-xs text-muted-foreground">
                Select a task on the graph to edit its objective, role, and budgets.
              </p>
            </div>
          )
        )}
      </div>

      {errors.length > 0 && (
        <ul
          className="space-y-1 rounded-md border border-destructive/40 bg-destructive/5 p-2.5"
          data-testid="workflow-errors"
        >
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
