/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useRef, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import {
  BadgeCheck, CircleDashed, Copy, ListOrdered, MoreHorizontal, Pencil, Plus, ScrollText,
  SearchX, ShieldCheck, Trash2, Workflow,
} from "lucide-react";

import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { ListState, TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { FilterBar, FilterSelect, type FilterOption } from "@/components/ui/filter-bar";
import { ResourceListPage } from "@/components/list-page";
import { useUrlFilters, type UrlFilters } from "@/hooks/useUrlFilters";
import { optionsFrom, SEVERITY_FILTER_OPTIONS } from "@/lib/securityFilters";
import { SecurityNav } from "@/components/SecurityNav";
import { FlowField } from "@/components/create-flow/create-flow";
import {
  SecurityWorkflowBuilder,
  WorkflowParametersEditor,
  validateWorkflowParameters,
  validateWorkflowTasks,
  workflowParametersFromProto,
  workflowParametersToProto,
  workflowTasksFromProto,
  workflowTasksToProto,
  type WorkflowParameterDraft,
  type WorkflowTaskDraft,
} from "@/components/SecurityWorkflowBuilder";
import {
  GenerateSecurityDraftDialog,
  type SecurityDraft,
} from "@/components/SecurityDraftDialog";
import {
  ExportSecurityPackDialog,
  ImportSecurityPackDialog,
} from "@/components/SecurityPackDialogs";
import {
  PolicyPackEditorDialog,
  packBudgetSummary,
  packRetentionSummary,
} from "@/components/SecurityPolicyPackDialog";
import { SecurityProgramDialog } from "@/components/SecurityProgramDialog";
import { SecurityCatalogDialog } from "@/components/SecurityCatalogDialog";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import {
  CreateSecurityPostScriptRequestSchema,
  CreateSecurityRankerRequestSchema,
  CreateSecurityWorkflowRequestSchema,
  SecurityPostScriptResourceSchema,
  SecurityRankerResourceSchema,
  SecurityWorkflowResourceSchema,
  UpdateSecurityPostScriptRequestSchema,
  UpdateSecurityRankerRequestSchema,
  UpdateSecurityWorkflowRequestSchema,
  type SecurityPolicyPackResource,
  type SecurityPostScriptResource,
  type SecurityProgramResource,
  type SecurityRankerResource,
  type SecurityWorkflowResource,
} from "@/rpc/platform/service_pb";

const selectClass = "h-8 rounded-md border border-input bg-background px-2 text-sm w-full";

const SNAPSHOT_COPY =
  "Scans resolve and snapshot referenced library content when each run starts, so " +
  "editing a library resource never changes runs that already happened.";

/** Fields every library resource shares; enough to filter and sort on. */
type LibraryItem = {
  name: string;
  usageCount: number;
  referencingScans: string[];
  createdAtUnix: bigint;
};

function inUse(item: LibraryItem): boolean {
  return item.usageCount > 0 || item.referencingScans.length > 0;
}

/**
 * Per-row usage indicator. Finding library content no scan references is the
 * main reason to sweep this page, so "unused" reads as an explicit state
 * instead of an empty cell.
 */
function UsageCell({ item }: { item: LibraryItem }) {
  if (!inUse(item)) {
    return (
      <span className="inline-flex items-center gap-1 rounded-md border border-dashed border-border/80 px-1.5 py-0.5 text-[11px] text-muted-foreground">
        <CircleDashed className="size-3" aria-hidden /> unused
      </span>
    );
  }
  const count = item.usageCount || item.referencingScans.length;
  return (
    <Badge variant="secondary" title={`Referenced by: ${item.referencingScans.join(", ")}`}>
      {count} scan{count === 1 ? "" : "s"}
    </Badge>
  );
}

function programTargetLabel(program: SecurityProgramResource): string {
  const count = program.scanTargets?.length || (program.scanTarget ? 1 : 0);
  return `${count} ${count === 1 ? "repo" : "repos"}`;
}

/* ── Workflow editor ─────────────────────────────────────────── */

function WorkflowEditorDialog({
  source,
  mode,
  trigger,
  onSaved,
  defaultOpen = false,
  notice,
  onClosed,
}: {
  source?: SecurityWorkflowResource;
  mode: "create" | "edit" | "duplicate";
  trigger?: React.ReactElement;
  onSaved: () => void;
  /** Open immediately (used when an AI draft is loaded for review). */
  defaultOpen?: boolean;
  /** Banner shown above the form, e.g. the AI-draft review warning. */
  notice?: React.ReactNode;
  onClosed?: () => void;
}) {
  const isEdit = mode === "edit";
  const [open, setOpen] = useState(defaultOpen);
  const [name, setName] = useState(() => (mode === "duplicate" ? "" : (source?.name ?? "")));
  const [description, setDescription] = useState(() => source?.description ?? "");
  const [parallelism, setParallelism] = useState(() =>
    source?.parallelism ? String(source.parallelism) : "",
  );
  const [tasks, setTasks] = useState<WorkflowTaskDraft[]>(() =>
    workflowTasksFromProto(source?.tasks ?? []),
  );
  const [parameters, setParameters] = useState<WorkflowParameterDraft[]>(() =>
    workflowParametersFromProto(source?.parameters ?? []),
  );
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const validationErrors = validateWorkflowTasks(tasks);
  const parameterErrors = validateWorkflowParameters(parameters);
  const parallelismInvalid =
    parallelism.trim() !== "" && (Number(parallelism) < 1 || Number(parallelism) > 16);
  const blocked =
    validationErrors.length > 0 ||
    parameterErrors.length > 0 ||
    parallelismInvalid ||
    name.trim() === "";

  function reset() {
    setName(mode === "duplicate" ? "" : (source?.name ?? ""));
    setDescription(source?.description ?? "");
    setParallelism(source?.parallelism ? String(source.parallelism) : "");
    setTasks(workflowTasksFromProto(source?.tasks ?? []));
    setParameters(workflowParametersFromProto(source?.parameters ?? []));
    setError(null);
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (blocked) return;
    setSubmitting(true);
    setError(null);
    try {
      const resource = create(SecurityWorkflowResourceSchema, {
        name: name.trim(),
        description: description.trim(),
        parallelism: parallelism.trim() ? Number(parallelism) : 0,
        tasks: workflowTasksToProto(tasks),
        parameters: workflowParametersToProto(parameters),
      });
      // Server-side validation runs the same rules plus anything newer.
      const check = await client.validateSecurityWorkflow({
        tasks: resource.tasks,
        parallelism: resource.parallelism,
        parameters: resource.parameters,
      });
      if (!check.valid) {
        setError(check.errors.map((e) => e.message).join(" "));
        return;
      }
      if (isEdit) {
        await client.updateSecurityWorkflow(
          create(UpdateSecurityWorkflowRequestSchema, { workflow: resource }),
        );
      } else {
        await client.createSecurityWorkflow(
          create(CreateSecurityWorkflowRequestSchema, { workflow: resource }),
        );
      }
      setOpen(false);
      reset();
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save workflow");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (!nextOpen) {
          reset();
          onClosed?.();
        }
      }}
    >
      {trigger && <DialogTrigger render={trigger} />}
      <DialogContent className="flex w-full max-w-3xl flex-col gap-0 overflow-hidden p-0 sm:max-w-6xl max-h-[92vh]" showCloseButton>
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <DialogTitle className="text-base">
              {isEdit
                ? `Edit workflow ${source?.name}`
                : mode === "duplicate"
                  ? `Duplicate workflow ${source?.name}`
                  : "New security workflow"}
            </DialogTitle>
            <DialogDescription>{SNAPSHOT_COPY}</DialogDescription>
          </DialogHeader>
          <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-6 py-5">
            {notice}
            <div className="grid gap-3 sm:grid-cols-2">
              <FlowField id="wf-name" label="Name" required>
                <Input
                  id="wf-name"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  disabled={isEdit}
                  placeholder="payments-deep-dive"
                  className="font-mono"
                />
              </FlowField>
              <FlowField id="wf-parallelism" label="Parallelism" hint="1-16; empty keeps the scan's own setting.">
                <Input
                  id="wf-parallelism"
                  type="number"
                  min={1}
                  max={16}
                  value={parallelism}
                  onChange={(event) => setParallelism(event.target.value)}
                />
              </FlowField>
            </div>
            <FlowField id="wf-description" label="Description">
              <Textarea
                id="wf-description"
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                className="min-h-14"
                placeholder="What this workflow hunts for."
              />
            </FlowField>
            <SecurityWorkflowBuilder tasks={tasks} onChange={setTasks} />
            <FlowField
              id="wf-parameters"
              label="Parameters"
              hint="Scan-time inputs referenced as {{params.name}} in task objectives; scans supply values when they run."
            >
              <div id="wf-parameters" className="pt-1">
                <WorkflowParametersEditor parameters={parameters} onChange={setParameters} />
              </div>
            </FlowField>
            {parameterErrors.length > 0 && (
              <ul className="space-y-1 rounded-md border border-destructive/40 bg-destructive/5 p-2.5" data-testid="parameter-errors">
                {parameterErrors.map((err, index) => (
                  <li key={`${err.field}-${index}`} className="text-xs text-destructive">
                    {err.message}
                  </li>
                ))}
              </ul>
            )}
            {parallelismInvalid && (
              <p className="text-xs text-destructive">Parallelism must be between 1 and 16.</p>
            )}
            {error && <p className="text-sm text-destructive">{error}</p>}
          </div>
          <div className="flex justify-end gap-2 border-t px-6 py-4">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting || blocked}>
              {submitting ? "Saving…" : isEdit ? "Save workflow" : "Create workflow"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/* ── Ranker editor ───────────────────────────────────────────── */

function RankerEditorDialog({
  source,
  mode,
  trigger,
  onSaved,
}: {
  source?: SecurityRankerResource;
  mode: "create" | "edit" | "duplicate";
  trigger: React.ReactElement;
  onSaved: () => void;
}) {
  const isEdit = mode === "edit";
  const [open, setOpen] = useState(false);
  const [name, setName] = useState(() => (mode === "duplicate" ? "" : (source?.name ?? "")));
  const [description, setDescription] = useState(() => source?.description ?? "");
  const [rules, setRules] = useState(() => (source?.rules ?? []).join("\n"));
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const blocked = name.trim() === "" || rules.trim() === "";

  function reset() {
    setName(mode === "duplicate" ? "" : (source?.name ?? ""));
    setDescription(source?.description ?? "");
    setRules((source?.rules ?? []).join("\n"));
    setError(null);
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (blocked) return;
    setSubmitting(true);
    setError(null);
    try {
      const resource = create(SecurityRankerResourceSchema, {
        name: name.trim(),
        description: description.trim(),
        rules: rules.split("\n").filter((line) => line.trim() !== ""),
      });
      if (isEdit) {
        await client.updateSecurityRanker(create(UpdateSecurityRankerRequestSchema, { ranker: resource }));
      } else {
        await client.createSecurityRanker(create(CreateSecurityRankerRequestSchema, { ranker: resource }));
      }
      setOpen(false);
      reset();
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save ranker");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={(nextOpen) => { setOpen(nextOpen); if (!nextOpen) reset(); }}>
      <DialogTrigger render={trigger} />
      <DialogContent className="flex w-full max-w-xl flex-col gap-0 overflow-hidden p-0 sm:max-w-xl max-h-[92vh]" showCloseButton>
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <DialogTitle className="text-base">
              {isEdit
                ? `Edit ranker ${source?.name}`
                : mode === "duplicate"
                  ? `Duplicate ranker ${source?.name}`
                  : "New severity ranker"}
            </DialogTitle>
            <DialogDescription>{SNAPSHOT_COPY}</DialogDescription>
          </DialogHeader>
          <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-6 py-5">
            <FlowField id="ranker-name" label="Name" required>
              <Input
                id="ranker-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                disabled={isEdit}
                className="font-mono"
                placeholder="payments-priorities"
              />
            </FlowField>
            <FlowField id="ranker-description" label="Description">
              <Input
                id="ranker-description"
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                placeholder="What this ranker prioritizes."
              />
            </FlowField>
            <FlowField
              id="ranker-rules"
              label="Rules"
              required
              hint={
                'One rule per line: directives like "severity-floor: injection=high" plus free-form prose. ' +
                "Same language as a scan's inline severity rankers."
              }
            >
              <Textarea
                id="ranker-rules"
                value={rules}
                onChange={(event) => setRules(event.target.value)}
                className="min-h-32 font-mono"
                placeholder={"severity-floor: injection=high\nauth bypass is always critical"}
              />
            </FlowField>
            {error && <p className="text-sm text-destructive">{error}</p>}
          </div>
          <div className="flex justify-end gap-2 border-t px-6 py-4">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting || blocked}>
              {submitting ? "Saving…" : isEdit ? "Save ranker" : "Create ranker"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/* ── Post-script editor ──────────────────────────────────────── */

function PostScriptEditorDialog({
  source,
  mode,
  trigger,
  onSaved,
  defaultOpen = false,
  notice,
  onClosed,
}: {
  source?: SecurityPostScriptResource;
  mode: "create" | "edit" | "duplicate";
  trigger?: React.ReactElement;
  onSaved: () => void;
  /** Open immediately (used when an AI draft is loaded for review). */
  defaultOpen?: boolean;
  /** Banner shown above the form, e.g. the AI-draft review warning. */
  notice?: React.ReactNode;
  onClosed?: () => void;
}) {
  const isEdit = mode === "edit";
  const [open, setOpen] = useState(defaultOpen);
  const [name, setName] = useState(() => (mode === "duplicate" ? "" : (source?.name ?? "")));
  const [description, setDescription] = useState(() => source?.description ?? "");
  const [prompt, setPrompt] = useState(() => source?.prompt ?? "");
  const [runOn, setRunOn] = useState(() => source?.runOn || "all");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const blocked = name.trim() === "" || prompt.trim() === "";

  function reset() {
    setName(mode === "duplicate" ? "" : (source?.name ?? ""));
    setDescription(source?.description ?? "");
    setPrompt(source?.prompt ?? "");
    setRunOn(source?.runOn || "all");
    setError(null);
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (blocked) return;
    setSubmitting(true);
    setError(null);
    try {
      const resource = create(SecurityPostScriptResourceSchema, {
        name: name.trim(),
        description: description.trim(),
        prompt,
        runOn: runOn === "all" ? "" : runOn,
      });
      if (isEdit) {
        await client.updateSecurityPostScript(
          create(UpdateSecurityPostScriptRequestSchema, { postScript: resource }),
        );
      } else {
        await client.createSecurityPostScript(
          create(CreateSecurityPostScriptRequestSchema, { postScript: resource }),
        );
      }
      setOpen(false);
      reset();
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save post-script");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (!nextOpen) {
          reset();
          onClosed?.();
        }
      }}
    >
      {trigger && <DialogTrigger render={trigger} />}
      <DialogContent className="flex w-full max-w-xl flex-col gap-0 overflow-hidden p-0 sm:max-w-xl max-h-[92vh]" showCloseButton>
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <DialogTitle className="text-base">
              {isEdit
                ? `Edit post-script ${source?.name}`
                : mode === "duplicate"
                  ? `Duplicate post-script ${source?.name}`
                  : "New post-script"}
            </DialogTitle>
            <DialogDescription>{SNAPSHOT_COPY}</DialogDescription>
          </DialogHeader>
          <div className="min-h-0 flex-1 space-y-4 overflow-y-auto px-6 py-5">
            {notice}
            <FlowField id="ps-name" label="Name" required>
              <Input
                id="ps-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                disabled={isEdit}
                className="font-mono"
                placeholder="write-poc"
              />
            </FlowField>
            <FlowField id="ps-description" label="Description">
              <Input
                id="ps-description"
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                placeholder="What this post-script does."
              />
            </FlowField>
            <FlowField id="ps-prompt" label="Prompt" required hint="Executed once per matching finding.">
              <Textarea
                id="ps-prompt"
                value={prompt}
                onChange={(event) => setPrompt(event.target.value)}
                className="min-h-24"
                placeholder="Write a proof of concept demonstrating the finding."
              />
            </FlowField>
            <FlowField
              id="ps-run-on"
              label="Runs on"
              hint={
                runOn === "high-and-above-actionable" ||
                runOn === "medium-and-above-actionable" ||
                runOn === "low-and-above-actionable"
                  ? "Findings at or above the chosen severity; skips the first attempt when a successful earlier stage already marked the finding false positive, accepted risk, or fixed. Use all findings for finalizers."
                  : "Which findings this post-script runs against."
              }
            >
              <select
                id="ps-run-on"
                className={selectClass}
                value={runOn}
                onChange={(event) => setRunOn(event.target.value)}
              >
                <option value="all">all findings</option>
                <option value="confirmed">confirmed findings</option>
                <option value="high-and-above">high-and-above findings</option>
                <option value="high-and-above-actionable">high-and-above while actionable</option>
                <option value="medium-and-above-actionable">medium-and-above while actionable</option>
                <option value="low-and-above-actionable">low-and-above while actionable</option>
              </select>
            </FlowField>
            {error && <p className="text-sm text-destructive">{error}</p>}
          </div>
          <div className="flex justify-end gap-2 border-t px-6 py-4">
            <Button type="button" variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={submitting || blocked}>
              {submitting ? "Saving…" : isEdit ? "Save post-script" : "Create post-script"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/* ── Library page ────────────────────────────────────────────── */

const TABS = [
  {
    id: "workflows",
    label: "Workflows",
    help: "Reusable task graphs referenced by security scans.",
  },
  {
    id: "rankers",
    label: "Rankers",
    help: "Severity rankers referenced by security scans.",
  },
  {
    id: "post-scripts",
    label: "Post-scripts",
    help: "Prompts run against matching findings after a scan's workflow.",
  },
  {
    id: "policy-packs",
    label: "Policy packs",
    help: "Scan defaults, enforced floors, suppressions, retention, and budgets.",
  },
  {
    id: "programs",
    label: "Programs",
    help: "Verified program scope snapshots referenced by security scans.",
  },
] as const;

type TabId = (typeof TABS)[number]["id"];

/**
 * URL-backed view state: the tab, the search box, the per-kind filters and
 * the sort order. A link to /security/library?tab=policy-packs&q=owasp opens
 * exactly the view the sender was looking at.
 */
const FILTER_SPEC = {
  tab: TABS[0].id,
  q: "",
  usage: "all",
  sort: "name",
  rules: "all",
  trigger: "any",
  enforcement: "all",
  severity: "all",
  provider: "all",
  verified: "all",
};

type FilterKey = keyof typeof FILTER_SPEC;
type LibraryFilters = UrlFilters<typeof FILTER_SPEC>;

/** Filters that narrow each tab. Drives the active count and "Clear". */
const TAB_FILTER_KEYS: Record<TabId, FilterKey[]> = {
  workflows: ["q", "usage"],
  rankers: ["q", "usage", "rules"],
  "post-scripts": ["q", "usage", "trigger"],
  "policy-packs": ["q", "usage", "enforcement", "severity"],
  programs: ["q", "usage", "provider", "verified"],
};

const SORT_OPTIONS: FilterOption[] = [
  { value: "name", label: "Name" },
  { value: "usage", label: "Most used" },
  { value: "recent", label: "Recently created" },
];

const RULES_OPTIONS: FilterOption[] = [
  { value: "all", label: "Any rules" },
  { value: "custom", label: "Has custom rules" },
  { value: "none", label: "No rules" },
];

const ENFORCEMENT_OPTIONS: FilterOption[] = [
  { value: "all", label: "Any enforcement" },
  { value: "enforced", label: "Enforces fields" },
  { value: "advisory", label: "Advisory only" },
];

const VERIFIED_OPTIONS: FilterOption[] = [
  { value: "all", label: "Any verification" },
  { value: "verified", label: "Verified" },
  { value: "unverified", label: "Not verified" },
];

function matchesUsage(item: LibraryItem, usage: string): boolean {
  if (usage === "in-use") return inUse(item);
  if (usage === "unused") return !inUse(item);
  return true;
}

function usageOptions(items: LibraryItem[]): FilterOption[] {
  const used = items.filter(inUse).length;
  return [
    { value: "all", label: "Any usage", count: items.length },
    { value: "in-use", label: "In use", count: used },
    { value: "unused", label: "Unused", count: items.length - used },
  ];
}

function sortLibrary<T extends LibraryItem>(items: T[], sort: string): T[] {
  const byName = (a: T, b: T) => a.name.localeCompare(b.name);
  const sorted = [...items];
  if (sort === "usage") {
    sorted.sort((a, b) => b.usageCount - a.usageCount || byName(a, b));
  } else if (sort === "recent") {
    sorted.sort((a, b) => Number(b.createdAtUnix - a.createdAtUnix) || byName(a, b));
  } else {
    sorted.sort(byName);
  }
  return sorted;
}

/** Post-script triggers present in the library, plus the "any" sentinel. */
function triggerOptions(scripts: SecurityPostScriptResource[]): FilterOption[] {
  const counts = new Map<string, number>();
  for (const script of scripts) {
    const key = script.runOn || "all";
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return [
    { value: "any", label: "Any trigger" },
    ...Array.from(counts.keys())
      .sort((a, b) => a.localeCompare(b))
      .map((value) => ({ value, label: value, count: counts.get(value) })),
  ];
}

/** Shared filter strip: usage and sort always, per-kind filters in between. */
function LibraryFilterBar({
  label,
  noun,
  items,
  visible,
  filters,
  activeCount,
  onClear,
  children,
}: {
  label: string;
  noun: string;
  items: LibraryItem[];
  visible: number;
  filters: LibraryFilters;
  activeCount: number;
  onClear: () => void;
  children?: React.ReactNode;
}) {
  // Nothing to narrow yet: a first-run tab shows its empty state alone.
  if (items.length === 0 && activeCount === 0) return null;
  return (
    <FilterBar
      label={label}
      activeCount={activeCount}
      onClear={onClear}
      resultLabel={`${visible} of ${items.length} ${noun}`}
    >
      <FilterSelect
        label="Usage"
        value={filters.values.usage}
        onChange={(value) => filters.set("usage", value)}
        options={usageOptions(items)}
      />
      {children}
      <FilterSelect
        label="Sort"
        value={filters.values.sort}
        defaultValue="name"
        onChange={(value) => filters.set("sort", value)}
        options={SORT_OPTIONS}
      />
    </FilterBar>
  );
}

/**
 * Per-tab empty surface. A library with nothing in it explains what the kind
 * is and offers the create action; a library hidden behind filters says so
 * and offers a way back out.
 */
function LibraryTabState({
  empty,
  filtered,
  icon,
  title,
  description,
  action,
  onClear,
  children,
}: {
  empty: boolean;
  /** Empty because the filters excluded everything, not because it is new. */
  filtered: boolean;
  icon: React.ReactNode;
  title: string;
  description: string;
  action: React.ReactNode;
  onClear: () => void;
  children: React.ReactNode;
}) {
  return (
    <ListState
      empty={empty}
      emptyIcon={filtered ? <SearchX /> : icon}
      emptyTitle={filtered ? "Nothing matches these filters" : title}
      emptyDescription={
        filtered
          ? "No item in this tab matches the current search and filters."
          : description
      }
      emptyAction={
        filtered ? (
          <Button variant="outline" size="sm" onClick={onClear}>
            Clear filters
          </Button>
        ) : (
          action
        )
      }
    >
      {children}
    </ListState>
  );
}

/** Resource name plus the columns that collapse away on narrow screens. */
function NameCell({ name, meta }: { name: string; meta?: React.ReactNode }) {
  return (
    <TableCell className="w-[1%] max-w-[18rem] whitespace-normal align-top">
      <div className="truncate text-[13.5px] font-semibold tracking-[-0.005em] text-foreground">
        {name}
      </div>
      {meta ? (
        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px] text-foreground/70 md:hidden">
          {meta}
        </div>
      ) : null}
    </TableCell>
  );
}

/**
 * Description column. The most informative field on the page, so it takes the
 * width the fixed-size columns give back and shows two lines instead of one
 * clipped line; the full text stays reachable on hover and on keyboard focus.
 */
function DescriptionCell({
  text,
  className,
}: {
  text: string;
  /** Responsive visibility, e.g. `hidden md:table-cell`. */
  className?: string;
}) {
  return (
    <TableCell className={cn("w-auto align-top text-foreground/75", className)}>
      {text ? (
        <span
          title={text}
          tabIndex={0}
          className="line-clamp-2 rounded-sm outline-none focus-visible:line-clamp-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          {text}
        </span>
      ) : (
        <span className="text-muted-foreground">—</span>
      )}
    </TableCell>
  );
}

/**
 * Row actions: the routine edit (and duplicate) stay one click away, while the
 * destructive delete moves behind an overflow menu so it cannot be hit by
 * accident next to them.
 */
function RowActions({
  name,
  edit,
  duplicate,
  onDelete,
}: {
  name: string;
  edit: React.ReactNode;
  duplicate?: React.ReactNode;
  onDelete: () => void;
}) {
  return (
    <TableCell className="w-[1%] whitespace-nowrap text-right align-top">
      <div className="inline-flex items-center gap-0.5">
        {edit}
        {duplicate}
        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                variant="ghost"
                size="icon-sm"
                title="More actions"
                aria-label={`More actions for ${name}`}
              />
            }
          >
            <MoreHorizontal className="size-3.5" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-[160px]">
            <DropdownMenuItem variant="destructive" onClick={onDelete}>
              <Trash2 />
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </TableCell>
  );
}

/**
 * SecurityLibraryPage lists the reusable security resources (workflows,
 * severity rankers, post-scripts, policy packs, verified programs) with usage
 * counts and editors. Tab, search, filters and sort live in the URL. Deleting
 * a resource that scans still reference fails with an error naming the scans.
 */
export function SecurityLibraryPage() {
  const [workflows, setWorkflows] = useState<SecurityWorkflowResource[]>([]);
  const [rankers, setRankers] = useState<SecurityRankerResource[]>([]);
  const [postScripts, setPostScripts] = useState<SecurityPostScriptResource[]>([]);
  const [policyPacks, setPolicyPacks] = useState<SecurityPolicyPackResource[]>([]);
  const [programs, setPrograms] = useState<SecurityProgramResource[]>([]);
  const [programsError, setProgramsError] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [pendingDelete, setPendingDelete] = useState<{ kind: string; name: string } | null>(null);
  const [scanConfigNames, setScanConfigNames] = useState<string[]>([]);
  // AI drafts live only in the client until the operator saves them through
  // the normal create flow; the counter forces the editor to remount per draft.
  const [draft, setDraft] = useState<{ id: number; value: SecurityDraft } | null>(null);
  const draftSeq = useRef(0);

  const filters = useUrlFilters(FILTER_SPEC);
  const { values, set, setMany } = filters;
  const tab: TabId = TABS.find((entry) => entry.id === values.tab)?.id ?? TABS[0].id;
  const query = values.q;

  const setQuery = useCallback((next: string) => set("q", next), [set]);

  const clearFilters = useCallback(() => {
    const cleared: Partial<Record<FilterKey, string>> = {};
    for (const key of TAB_FILTER_KEYS[tab]) cleared[key] = FILTER_SPEC[key];
    setMany(cleared);
  }, [setMany, tab]);

  const activeFilterCount = filters.activeCount(
    (Object.keys(FILTER_SPEC) as FilterKey[]).filter(
      (key) => !TAB_FILTER_KEYS[tab].includes(key),
    ),
  );

  const fetchAll = useCallback(async () => {
    setLoading(true);
    setError("");
    setProgramsError("");
    try {
      const [wf, rk, ps, pp] = await Promise.all([
        client.listSecurityWorkflows({ namespace: "" }),
        client.listSecurityRankers({ namespace: "" }),
        client.listSecurityPostScripts({ namespace: "" }),
        client.listSecurityPolicyPacks({ namespace: "" }),
      ]);
      setWorkflows(wf.workflows);
      setRankers(rk.rankers);
      setPostScripts(ps.postScripts);
      setPolicyPacks(pp.policyPacks);
      // Programs were added after the original library API. Keep the existing
      // library usable during rolling upgrades or a program-specific outage.
      try {
        const programList = await client.listSecurityPrograms({ namespace: "" });
        setPrograms(programList.programs);
      } catch (programError: unknown) {
        setPrograms([]);
        setProgramsError(programError instanceof Error ? programError.message : "Failed to load security programs");
      }
      // Scan configurations are only needed for pack export; a failure there
      // must not blank the library itself.
      try {
        const scans = await client.listSecurityScanConfigs({ namespace: "" });
        setScanConfigNames(scans.configs.map((config) => config.name));
      } catch {
        setScanConfigNames([]);
      }
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to load the security library");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void fetchAll();
  }, [fetchAll]);

  async function handleDelete() {
    if (!pendingDelete) return;
    setActionError(null);
    try {
      if (pendingDelete.kind === "workflow") {
        await client.deleteSecurityWorkflow({ namespace: "", name: pendingDelete.name });
      } else if (pendingDelete.kind === "ranker") {
        await client.deleteSecurityRanker({ namespace: "", name: pendingDelete.name });
      } else if (pendingDelete.kind === "policy-pack") {
        await client.deleteSecurityPolicyPack({ namespace: "", name: pendingDelete.name });
      } else if (pendingDelete.kind === "program") {
        await client.deleteSecurityProgram({ namespace: "", name: pendingDelete.name });
      } else {
        await client.deleteSecurityPostScript({ namespace: "", name: pendingDelete.name });
      }
      await fetchAll();
    } catch (e: unknown) {
      // FailedPrecondition lists the referencing scans; surface it verbatim.
      setActionError(e instanceof Error ? e.message : "Failed to delete");
    } finally {
      setPendingDelete(null);
    }
  }

  const onSaved = () => {
    setActionError(null);
    setDraft(null);
    void fetchAll();
  };

  const acceptDraft = useCallback((value: SecurityDraft) => {
    draftSeq.current += 1;
    setDraft({ id: draftSeq.current, value });
  }, []);

  const draftNotice = (errors: SecurityDraft["validationErrors"]) => (
    <div className="rounded-md border border-amber-500/40 bg-amber-500/5 p-2.5 text-sm">
      <p className="font-medium">AI draft — review before saving.</p>
      <p className="text-muted-foreground">
        Generated content is untrusted until you review it. Nothing is saved until you submit this form.
      </p>
      {errors.length > 0 && (
        <ul className="mt-1.5 list-disc pl-4 text-xs text-destructive" data-testid="draft-validation-errors">
          {errors.map((e) => (
            <li key={`${e.field}-${e.message}`}>
              {e.field}: {e.message}
            </li>
          ))}
        </ul>
      )}
    </div>
  );

  // Search reaches the content that identifies each kind, not just its name:
  // an operator hunting "sql injection" means the task objectives, ranker
  // rules and prompts that mention it, wherever they live.
  const visibleWorkflows = sortLibrary(
    filterByQuery(workflows, query, (w) => [
      w.name,
      w.description,
      ...w.tasks.flatMap((task) => [task.name, task.objective, task.role]),
    ]).filter((w) => matchesUsage(w, values.usage)),
    values.sort,
  );

  const visibleRankers = sortLibrary(
    filterByQuery(rankers, query, (r) => [r.name, r.description, ...r.rules])
      .filter((r) => matchesUsage(r, values.usage))
      .filter((r) =>
        values.rules === "all"
          ? true
          : values.rules === "custom"
            ? r.rules.length > 0
            : r.rules.length === 0,
      ),
    values.sort,
  );

  const visiblePostScripts = sortLibrary(
    filterByQuery(postScripts, query, (p) => [p.name, p.description, p.prompt, p.runOn])
      .filter((p) => matchesUsage(p, values.usage))
      .filter((p) => values.trigger === "any" || (p.runOn || "all") === values.trigger),
    values.sort,
  );

  const visiblePolicyPacks = sortLibrary(
    filterByQuery(policyPacks, query, (p) => [
      p.name,
      p.description,
      p.minSeverity,
      p.failOnSeverity,
      ...p.enforced,
      ...p.requiredCategories,
      ...p.suppressions.flatMap((rule) => [
        rule.name,
        rule.reason,
        rule.owner,
        rule.matcher?.category,
        rule.matcher?.cwe,
        rule.matcher?.pathGlob,
      ]),
    ])
      .filter((p) => matchesUsage(p, values.usage))
      .filter((p) =>
        values.enforcement === "all"
          ? true
          : values.enforcement === "enforced"
            ? p.enforced.length > 0
            : p.enforced.length === 0,
      )
      .filter((p) => values.severity === "all" || p.minSeverity === values.severity),
    values.sort,
  );

  const visiblePrograms = sortLibrary(
    filterByQuery(programs, query, (program) => [
      program.name,
      program.provider,
      program.displayName,
      program.programUrl,
      program.scopePolicy,
      ...program.scanTargets.map((target) => target.repositoryUrl),
    ])
      .filter((p) => matchesUsage(p, values.usage))
      .filter((p) => values.provider === "all" || p.provider === values.provider)
      .filter((p) =>
        values.verified === "all"
          ? true
          : values.verified === "verified"
            ? Boolean(p.verifiedAt)
            : !p.verifiedAt,
      ),
    values.sort,
  );

  const tabCounts: Record<TabId, number> = {
    workflows: visibleWorkflows.length,
    rankers: visibleRankers.length,
    "post-scripts": visiblePostScripts.length,
    "policy-packs": visiblePolicyPacks.length,
    programs: visiblePrograms.length,
  };
  const filtersActive = activeFilterCount > 0;

  // One primary action per view: the create action for the kind in front of
  // you, docked to the tab bar it applies to. Drafting and pack portability
  // stay secondary so the eye lands on the primary first.
  const tabActions: Record<TabId, React.ReactNode> = {
    workflows: (
      <>
        <WorkflowEditorDialog
          mode="create"
          onSaved={onSaved}
          trigger={
            <Button size="sm">
              <Plus className="size-3.5" /> New workflow
            </Button>
          }
        />
        <GenerateSecurityDraftDialog kind="workflow" onDraft={acceptDraft} />
      </>
    ),
    rankers: (
      <RankerEditorDialog
        mode="create"
        onSaved={onSaved}
        trigger={
          <Button size="sm">
            <Plus className="size-3.5" /> New ranker
          </Button>
        }
      />
    ),
    "post-scripts": (
      <>
        <PostScriptEditorDialog
          mode="create"
          onSaved={onSaved}
          trigger={
            <Button size="sm">
              <Plus className="size-3.5" /> New post-script
            </Button>
          }
        />
        <GenerateSecurityDraftDialog kind="post-script" onDraft={acceptDraft} />
      </>
    ),
    "policy-packs": (
      <PolicyPackEditorDialog
        mode="create"
        onSaved={onSaved}
        trigger={
          <Button size="sm">
            <Plus className="size-3.5" /> New policy pack
          </Button>
        }
      />
    ),
    programs: (
      <SecurityProgramDialog
        onSaved={onSaved}
        trigger={
          <Button size="sm">
            <Plus className="size-3.5" /> New security program
          </Button>
        }
      />
    ),
  };

  return (
    <ResourceListPage
      title="Security library"
      description="Reusable workflows, severity rankers, post-scripts, policy packs, and verified program scope snapshots that security scans reference. Referenced content is snapshotted per run, so edits never rewrite past runs."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search the library…"
      loading={loading}
      error={error || undefined}
      onRetry={fetchAll}
      empty={Boolean(error)}
      skeleton={<TableRowSkeleton cols={5} />}
      nav={<SecurityNav />}
      actions={
        <>
          <SecurityCatalogDialog onInstalled={fetchAll} />
          <ExportSecurityPackDialog
            workflows={workflows.map((w) => w.name)}
            rankers={rankers.map((r) => r.name)}
            postScripts={postScripts.map((p) => p.name)}
            scanConfigs={scanConfigNames}
            policyPacks={policyPacks.map((p) => p.name)}
          />
          <ImportSecurityPackDialog onImported={onSaved} />
        </>
      }
    >
      {actionError && (
        <p className="rounded-md border border-destructive/40 bg-destructive/5 p-2.5 text-sm text-destructive" data-testid="library-action-error">
          {actionError}
        </p>
      )}
      {draft?.value.workflow && (
        <WorkflowEditorDialog
          key={`draft-workflow-${draft.id}`}
          mode="create"
          source={draft.value.workflow}
          defaultOpen
          notice={draftNotice(draft.value.validationErrors)}
          onClosed={() => setDraft(null)}
          onSaved={onSaved}
        />
      )}
      {draft?.value.postScript && (
        <PostScriptEditorDialog
          key={`draft-post-script-${draft.id}`}
          mode="create"
          source={draft.value.postScript}
          defaultOpen
          notice={draftNotice(draft.value.validationErrors)}
          onClosed={() => setDraft(null)}
          onSaved={onSaved}
        />
      )}
      <Tabs
        value={tab}
        onValueChange={(next) => {
          // Tabs are a place, not a filter: the back button should walk them.
          if (typeof next === "string") set("tab", next, { history: "push" });
        }}
      >
        <div className="flex flex-wrap items-end justify-between gap-x-4 gap-y-1 border-b border-border/70">
          {/* Underlined tabs, not a second pill bar: the section nav above is
              already segmented, so the kind tabs need a different shape to
              read as the level below it. */}
          <TabsList
            variant="line"
            className="max-w-full flex-1 justify-start gap-4 overflow-x-auto bg-transparent p-0 group-data-horizontal/tabs:h-9 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
          >
            {TABS.map((entry) => (
              <TabsTrigger
                key={entry.id}
                value={entry.id}
                className="group/tab h-9 flex-none rounded-none px-0.5 text-[13.5px] text-muted-foreground data-active:text-foreground dark:data-active:text-foreground group-data-horizontal/tabs:after:bottom-0 group-data-horizontal/tabs:after:h-[2px] data-active:after:bg-primary"
              >
                {entry.label}
                <span className="rounded-full bg-muted px-1.5 py-px text-[10.5px] font-medium tabular-nums text-muted-foreground transition-colors group-data-active/tab:bg-primary/15 group-data-active/tab:text-primary">
                  {tabCounts[entry.id]}
                </span>
              </TabsTrigger>
            ))}
          </TabsList>
          <div className="flex flex-wrap items-center gap-2 pb-1.5">{tabActions[tab]}</div>
        </div>
        {/* The kind's one-line explanation belongs with the tab that selects
            it, not stranded under the table as a caption. */}
        <p className="text-[12.5px] text-muted-foreground">
          {TABS.find((entry) => entry.id === tab)?.help}
          {tab === "programs" &&
            " A program URL records provenance only and does not authorize network testing."}
        </p>

        <TabsContent value="workflows" className="space-y-3 pt-1">
          <LibraryFilterBar
            label="Workflow filters"
            noun="workflows"
            items={workflows}
            visible={visibleWorkflows.length}
            filters={filters}
            activeCount={activeFilterCount}
            onClear={clearFilters}
          />
          <LibraryTabState
            empty={visibleWorkflows.length === 0}
            filtered={filtersActive}
            onClear={clearFilters}
            icon={<Workflow />}
            title="No reusable workflows yet"
            description="A workflow is a named task graph — recon, exploit, triage — that any scan can reference instead of restating it."
            action={
              <WorkflowEditorDialog
                mode="create"
                onSaved={onSaved}
                trigger={
                  <Button size="sm">
                    <Plus className="size-3.5" /> Create your first workflow
                  </Button>
                }
              />
            }
          >
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-[1%] whitespace-nowrap">Name</TableHead>
                  <TableHead className="hidden w-auto md:table-cell">Description</TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap text-right sm:table-cell">
                    Tasks
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap sm:table-cell">
                    Used by
                  </TableHead>
                  <TableHead className="w-[1%] whitespace-nowrap text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visibleWorkflows.map((workflow) => (
                  <TableRow key={workflow.name} data-testid={`workflow-row-${workflow.name}`}>
                    <NameCell
                      name={workflow.name}
                      meta={
                        <>
                          <span className="basis-full truncate">{workflow.description}</span>
                          <span>{workflow.tasks.length} tasks</span>
                          <UsageCell item={workflow} />
                        </>
                      }
                    />
                    <DescriptionCell
                      text={workflow.description}
                      className="hidden md:table-cell"
                    />
                    <TableCell className="hidden w-[1%] whitespace-nowrap text-right tabular-nums align-top sm:table-cell">
                      {workflow.tasks.length}
                    </TableCell>
                    <TableCell className="hidden w-[1%] whitespace-nowrap align-top sm:table-cell">
                      <UsageCell item={workflow} />
                    </TableCell>
                    <RowActions
                      name={workflow.name}
                      onDelete={() => setPendingDelete({ kind: "workflow", name: workflow.name })}
                      edit={
                        <WorkflowEditorDialog
                          mode="edit"
                          source={workflow}
                          onSaved={onSaved}
                          trigger={
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              title="Edit"
                              aria-label={`Edit ${workflow.name}`}
                            >
                              <Pencil className="size-3.5" />
                            </Button>
                          }
                        />
                      }
                      duplicate={
                        <WorkflowEditorDialog
                          mode="duplicate"
                          source={workflow}
                          onSaved={onSaved}
                          trigger={
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              title="Duplicate"
                              aria-label={`Duplicate ${workflow.name}`}
                            >
                              <Copy className="size-3.5" />
                            </Button>
                          }
                        />
                      }
                    />
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </LibraryTabState>
        </TabsContent>

        <TabsContent value="rankers" className="space-y-3 pt-1">
          <LibraryFilterBar
            label="Ranker filters"
            noun="rankers"
            items={rankers}
            visible={visibleRankers.length}
            filters={filters}
            activeCount={activeFilterCount}
            onClear={clearFilters}
          >
            <FilterSelect
              label="Rules"
              value={values.rules}
              onChange={(value) => set("rules", value)}
              options={RULES_OPTIONS}
            />
          </LibraryFilterBar>
          <LibraryTabState
            empty={visibleRankers.length === 0}
            filtered={filtersActive}
            onClear={clearFilters}
            icon={<ListOrdered />}
            title="No severity rankers yet"
            description="Rankers re-rank findings with severity floors and prose rules so every scan that references them agrees on what matters."
            action={
              <RankerEditorDialog
                mode="create"
                onSaved={onSaved}
                trigger={
                  <Button size="sm">
                    <Plus className="size-3.5" /> Create your first ranker
                  </Button>
                }
              />
            }
          >
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-[1%] whitespace-nowrap">Name</TableHead>
                  <TableHead className="hidden w-auto md:table-cell">Description</TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap text-right sm:table-cell">
                    Rules
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap sm:table-cell">
                    Used by
                  </TableHead>
                  <TableHead className="w-[1%] whitespace-nowrap text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visibleRankers.map((ranker) => (
                  <TableRow key={ranker.name} data-testid={`ranker-row-${ranker.name}`}>
                    <NameCell
                      name={ranker.name}
                      meta={
                        <>
                          <span className="basis-full truncate">{ranker.description}</span>
                          <span>{ranker.rules.length} rules</span>
                          <UsageCell item={ranker} />
                        </>
                      }
                    />
                    <DescriptionCell text={ranker.description} className="hidden md:table-cell" />
                    <TableCell className="hidden w-[1%] whitespace-nowrap text-right tabular-nums align-top sm:table-cell">
                      {ranker.rules.length}
                    </TableCell>
                    <TableCell className="hidden w-[1%] whitespace-nowrap align-top sm:table-cell">
                      <UsageCell item={ranker} />
                    </TableCell>
                    <RowActions
                      name={ranker.name}
                      onDelete={() => setPendingDelete({ kind: "ranker", name: ranker.name })}
                      edit={
                        <RankerEditorDialog
                          mode="edit"
                          source={ranker}
                          onSaved={onSaved}
                          trigger={
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              title="Edit"
                              aria-label={`Edit ${ranker.name}`}
                            >
                              <Pencil className="size-3.5" />
                            </Button>
                          }
                        />
                      }
                      duplicate={
                        <RankerEditorDialog
                          mode="duplicate"
                          source={ranker}
                          onSaved={onSaved}
                          trigger={
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              title="Duplicate"
                              aria-label={`Duplicate ${ranker.name}`}
                            >
                              <Copy className="size-3.5" />
                            </Button>
                          }
                        />
                      }
                    />
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </LibraryTabState>
        </TabsContent>

        <TabsContent value="post-scripts" className="space-y-3 pt-1">
          <LibraryFilterBar
            label="Post-script filters"
            noun="post-scripts"
            items={postScripts}
            visible={visiblePostScripts.length}
            filters={filters}
            activeCount={activeFilterCount}
            onClear={clearFilters}
          >
            <FilterSelect
              label="Trigger"
              value={values.trigger}
              defaultValue="any"
              onChange={(value) => set("trigger", value)}
              options={triggerOptions(postScripts)}
            />
          </LibraryFilterBar>
          <LibraryTabState
            empty={visiblePostScripts.length === 0}
            filtered={filtersActive}
            onClear={clearFilters}
            icon={<ScrollText />}
            title="No post-scripts yet"
            description="Post-scripts run one prompt per matching finding after the workflow finishes: proofs of concept, fix suggestions, triage notes."
            action={
              <PostScriptEditorDialog
                mode="create"
                onSaved={onSaved}
                trigger={
                  <Button size="sm">
                    <Plus className="size-3.5" /> Create your first post-script
                  </Button>
                }
              />
            }
          >
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-[1%] whitespace-nowrap">Name</TableHead>
                  <TableHead className="hidden w-auto md:table-cell">Description</TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap sm:table-cell">
                    Runs on
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap sm:table-cell">
                    Used by
                  </TableHead>
                  <TableHead className="w-[1%] whitespace-nowrap text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visiblePostScripts.map((script) => (
                  <TableRow key={script.name} data-testid={`post-script-row-${script.name}`}>
                    <NameCell
                      name={script.name}
                      meta={
                        <>
                          <span className="basis-full truncate">{script.description}</span>
                          <span>{script.runOn || "all"}</span>
                          <UsageCell item={script} />
                        </>
                      }
                    />
                    <DescriptionCell text={script.description} className="hidden md:table-cell" />
                    <TableCell className="hidden w-[1%] whitespace-nowrap align-top text-foreground/75 sm:table-cell">
                      {script.runOn || "all"}
                    </TableCell>
                    <TableCell className="hidden w-[1%] whitespace-nowrap align-top sm:table-cell">
                      <UsageCell item={script} />
                    </TableCell>
                    <RowActions
                      name={script.name}
                      onDelete={() => setPendingDelete({ kind: "post-script", name: script.name })}
                      edit={
                        <PostScriptEditorDialog
                          mode="edit"
                          source={script}
                          onSaved={onSaved}
                          trigger={
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              title="Edit"
                              aria-label={`Edit ${script.name}`}
                            >
                              <Pencil className="size-3.5" />
                            </Button>
                          }
                        />
                      }
                      duplicate={
                        <PostScriptEditorDialog
                          mode="duplicate"
                          source={script}
                          onSaved={onSaved}
                          trigger={
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              title="Duplicate"
                              aria-label={`Duplicate ${script.name}`}
                            >
                              <Copy className="size-3.5" />
                            </Button>
                          }
                        />
                      }
                    />
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </LibraryTabState>
        </TabsContent>

        <TabsContent value="policy-packs" className="space-y-3 pt-1">
          <LibraryFilterBar
            label="Policy pack filters"
            noun="policy packs"
            items={policyPacks}
            visible={visiblePolicyPacks.length}
            filters={filters}
            activeCount={activeFilterCount}
            onClear={clearFilters}
          >
            <FilterSelect
              label="Enforcement"
              value={values.enforcement}
              onChange={(value) => set("enforcement", value)}
              options={ENFORCEMENT_OPTIONS}
            />
            <FilterSelect
              label="Min severity"
              value={values.severity}
              onChange={(value) => set("severity", value)}
              options={SEVERITY_FILTER_OPTIONS}
            />
          </LibraryFilterBar>
          <LibraryTabState
            empty={visiblePolicyPacks.length === 0}
            filtered={filtersActive}
            onClear={clearFilters}
            icon={<ShieldCheck />}
            title="No policy packs yet"
            description="Policy packs supply scan defaults, floors that referencing scans may not relax, governed finding suppressions, data retention, and budgets."
            action={
              <PolicyPackEditorDialog
                mode="create"
                onSaved={onSaved}
                trigger={
                  <Button size="sm">
                    <Plus className="size-3.5" /> Create your first policy pack
                  </Button>
                }
              />
            }
          >
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-[1%] whitespace-nowrap">Name</TableHead>
                  <TableHead className="hidden w-auto lg:table-cell">Description</TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap sm:table-cell">
                    Enforced
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap md:table-cell">
                    Suppressions
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap lg:table-cell">
                    Retention / budgets
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap sm:table-cell">
                    Used by
                  </TableHead>
                  <TableHead className="w-[1%] whitespace-nowrap text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visiblePolicyPacks.map((pack) => {
                  const retention = packRetentionSummary(pack.retention);
                  const budgets = packBudgetSummary(pack.budgets);
                  return (
                    <TableRow key={pack.name} data-testid={`policy-pack-row-${pack.name}`}>
                      <NameCell
                        name={pack.name}
                        meta={
                          <>
                            <span className="basis-full truncate">{pack.description}</span>
                            <span>
                              {pack.enforced.length === 0
                                ? "advisory"
                                : `enforces ${pack.enforced.join(", ")}`}
                            </span>
                            <UsageCell item={pack} />
                          </>
                        }
                      />
                      <DescriptionCell text={pack.description} className="hidden lg:table-cell" />
                      <TableCell className="hidden w-[1%] align-top sm:table-cell">
                        {pack.enforced.length === 0 ? (
                          <span className="text-muted-foreground">none</span>
                        ) : (
                          <span className="flex flex-wrap gap-1" title="Scans may not relax these fields.">
                            {pack.enforced.map((field) => (
                              <Badge key={field} variant="secondary" className="text-[11px]">
                                {field}
                              </Badge>
                            ))}
                          </span>
                        )}
                      </TableCell>
                      <TableCell className="hidden w-[1%] whitespace-nowrap align-top text-sm text-foreground/75 md:table-cell">
                        {pack.suppressions.length === 0
                          ? "—"
                          : `${pack.suppressions.length} rule${pack.suppressions.length === 1 ? "" : "s"}`}
                      </TableCell>
                      <TableCell className="hidden w-[1%] whitespace-nowrap align-top text-[12px] text-foreground/70 lg:table-cell">
                        <div className="space-y-0.5">
                          <div>{retention ? `retention: ${retention}` : "retention: keep forever"}</div>
                          <div>{budgets ? `budgets: ${budgets}` : "budgets: unlimited"}</div>
                        </div>
                      </TableCell>
                      <TableCell className="hidden w-[1%] whitespace-nowrap align-top sm:table-cell">
                        <UsageCell item={pack} />
                      </TableCell>
                      <RowActions
                        name={pack.name}
                        onDelete={() => setPendingDelete({ kind: "policy-pack", name: pack.name })}
                        edit={
                          <PolicyPackEditorDialog
                            mode="edit"
                            source={pack}
                            onSaved={onSaved}
                            trigger={
                              <Button
                                variant="ghost"
                                size="icon-sm"
                                title="Edit"
                                aria-label={`Edit ${pack.name}`}
                              >
                                <Pencil className="size-3.5" />
                              </Button>
                            }
                          />
                        }
                      />
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </LibraryTabState>
        </TabsContent>

        <TabsContent value="programs" className="space-y-3 pt-1">
          {programsError && (
            <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
              Security programs are unavailable: {programsError}
            </div>
          )}
          <LibraryFilterBar
            label="Program filters"
            noun="programs"
            items={programs}
            visible={visiblePrograms.length}
            filters={filters}
            activeCount={activeFilterCount}
            onClear={clearFilters}
          >
            <FilterSelect
              label="Provider"
              value={values.provider}
              onChange={(value) => set("provider", value)}
              options={optionsFrom(programs.map((program) => program.provider), "Any provider")}
            />
            <FilterSelect
              label="Verification"
              value={values.verified}
              onChange={(value) => set("verified", value)}
              options={VERIFIED_OPTIONS}
            />
          </LibraryFilterBar>
          <LibraryTabState
            empty={visiblePrograms.length === 0}
            filtered={filtersActive}
            onClear={clearFilters}
            icon={<BadgeCheck />}
            title="No security programs yet"
            description="A program records an operator-verified scope policy snapshot — what is in scope, what is excluded — that scan prompts quote verbatim."
            action={
              <SecurityProgramDialog
                onSaved={onSaved}
                trigger={
                  <Button size="sm">
                    <Plus className="size-3.5" /> Create your first program
                  </Button>
                }
              />
            }
          >
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-[1%] whitespace-nowrap">Name</TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap md:table-cell">
                    Program
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap sm:table-cell">
                    Provider
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap xl:table-cell">
                    Provenance URL
                  </TableHead>
                  <TableHead className="hidden w-auto xl:table-cell">
                    Scope policy snapshot
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap lg:table-cell">
                    Repositories
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap lg:table-cell">
                    Verified
                  </TableHead>
                  <TableHead className="hidden w-[1%] whitespace-nowrap sm:table-cell">
                    Used by
                  </TableHead>
                  <TableHead className="w-[1%] whitespace-nowrap text-right">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {visiblePrograms.map((program) => (
                  <TableRow key={program.name} data-testid={`program-row-${program.name}`}>
                    <NameCell
                      name={program.name}
                      meta={
                        <>
                          <span className="basis-full truncate">{program.displayName}</span>
                          <span>{program.provider}</span>
                          <span>{programTargetLabel(program)}</span>
                          <UsageCell item={program} />
                        </>
                      }
                    />
                    <TableCell className="hidden w-[1%] align-top font-medium md:table-cell">
                      {program.displayName}
                    </TableCell>
                    <TableCell className="hidden w-[1%] whitespace-nowrap align-top sm:table-cell">
                      {program.provider}
                    </TableCell>
                    <TableCell className="hidden w-[1%] max-w-56 align-top xl:table-cell">
                      <a
                        href={program.programUrl}
                        target="_blank"
                        rel="noreferrer"
                        className="block truncate font-mono text-[12px] underline underline-offset-2"
                        title={program.programUrl}
                      >
                        {program.programUrl}
                      </a>
                    </TableCell>
                    <TableCell className="hidden w-auto align-top whitespace-pre-line text-[12px] text-foreground/75 xl:table-cell">
                      <span
                        title={program.scopePolicy}
                        tabIndex={0}
                        className="line-clamp-2 rounded-sm outline-none focus-visible:line-clamp-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                      >
                        {program.scopePolicy}
                      </span>
                    </TableCell>
                    <TableCell className="hidden w-[1%] whitespace-nowrap align-top text-[12px] text-foreground/70 lg:table-cell">
                      {programTargetLabel(program)}
                    </TableCell>
                    <TableCell className="hidden w-[1%] whitespace-nowrap align-top text-[12px] text-foreground/70 lg:table-cell">
                      {program.verifiedAt ? timestampDate(program.verifiedAt).toLocaleString() : "—"}
                    </TableCell>
                    <TableCell className="hidden w-[1%] whitespace-nowrap align-top sm:table-cell">
                      <UsageCell item={program} />
                    </TableCell>
                    <RowActions
                      name={program.name}
                      onDelete={() => setPendingDelete({ kind: "program", name: program.name })}
                      edit={
                        <SecurityProgramDialog
                          source={program}
                          onSaved={onSaved}
                          trigger={
                            <Button
                              variant="ghost"
                              size="icon-sm"
                              title="Edit"
                              aria-label={`Edit ${program.name}`}
                            >
                              <Pencil className="size-3.5" />
                            </Button>
                          }
                        />
                      }
                    />
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </LibraryTabState>
        </TabsContent>
      </Tabs>

      <ConfirmDialog
        open={pendingDelete !== null}
        onOpenChange={(nextOpen) => {
          if (!nextOpen) setPendingDelete(null);
        }}
        title={`Delete ${pendingDelete?.name ?? ""}?`}
        description="Deletion is blocked while security scans still reference this resource. Runs that already happened keep their snapshotted copy."
        confirmLabel="Delete"
        destructive
        onConfirm={handleDelete}
      />
    </ResourceListPage>
  );
}
