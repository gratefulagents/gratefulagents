/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useRef, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Copy, Pencil, Plus, Trash2, Workflow } from "lucide-react";

import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { ResourceListPage } from "@/components/list-page";
import { FlowField } from "@/components/create-flow/create-flow";
import {
  SecurityWorkflowBuilder,
  validateWorkflowTasks,
  workflowTasksFromProto,
  workflowTasksToProto,
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
import { client } from "@/lib/client";
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
  type SecurityPostScriptResource,
  type SecurityRankerResource,
  type SecurityWorkflowResource,
} from "@/rpc/platform/service_pb";

const selectClass = "h-8 rounded-md border border-input bg-background px-2 text-sm w-full";

const SNAPSHOT_COPY =
  "Scans resolve and snapshot referenced library content when each run starts, so " +
  "editing a library resource never changes runs that already happened.";

function usageBadge(usageCount: number, referencingScans: string[]) {
  if (usageCount === 0) return <span className="text-muted-foreground">unused</span>;
  return (
    <Badge variant="secondary" title={`Referenced by: ${referencingScans.join(", ")}`}>
      {usageCount} scan{usageCount === 1 ? "" : "s"}
    </Badge>
  );
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
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const validationErrors = validateWorkflowTasks(tasks);
  const parallelismInvalid =
    parallelism.trim() !== "" && (Number(parallelism) < 1 || Number(parallelism) > 16);
  const blocked = validationErrors.length > 0 || parallelismInvalid || name.trim() === "";

  function reset() {
    setName(mode === "duplicate" ? "" : (source?.name ?? ""));
    setDescription(source?.description ?? "");
    setParallelism(source?.parallelism ? String(source.parallelism) : "");
    setTasks(workflowTasksFromProto(source?.tasks ?? []));
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
      });
      // Server-side validation runs the same rules plus anything newer.
      const check = await client.validateSecurityWorkflow({
        tasks: resource.tasks,
        parallelism: resource.parallelism,
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
      <DialogContent className="flex w-full max-w-3xl flex-col gap-0 overflow-hidden p-0 sm:max-w-3xl max-h-[92vh]" showCloseButton>
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
            <FlowField id="ps-run-on" label="Runs on" hint="Which findings this post-script runs against.">
              <select
                id="ps-run-on"
                className={selectClass}
                value={runOn}
                onChange={(event) => setRunOn(event.target.value)}
              >
                <option value="all">all findings</option>
                <option value="confirmed">confirmed findings</option>
                <option value="high-and-above">high-and-above findings</option>
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

/**
 * SecurityLibraryPage lists the reusable security resources (workflows,
 * severity rankers, post-scripts) with usage counts and editors. Deleting a
 * resource that scans still reference fails with an error naming the scans.
 */
export function SecurityLibraryPage() {
  const [workflows, setWorkflows] = useState<SecurityWorkflowResource[]>([]);
  const [rankers, setRankers] = useState<SecurityRankerResource[]>([]);
  const [postScripts, setPostScripts] = useState<SecurityPostScriptResource[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [tab, setTab] = useState("workflows");
  const [pendingDelete, setPendingDelete] = useState<{ kind: string; name: string } | null>(null);
  const [scanConfigNames, setScanConfigNames] = useState<string[]>([]);
  // AI drafts live only in the client until the operator saves them through
  // the normal create flow; the counter forces the editor to remount per draft.
  const [draft, setDraft] = useState<{ id: number; value: SecurityDraft } | null>(null);
  const draftSeq = useRef(0);

  const fetchAll = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [wf, rk, ps] = await Promise.all([
        client.listSecurityWorkflows({ namespace: "" }),
        client.listSecurityRankers({ namespace: "" }),
        client.listSecurityPostScripts({ namespace: "" }),
      ]);
      setWorkflows(wf.workflows);
      setRankers(rk.rankers);
      setPostScripts(ps.postScripts);
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

  const visibleWorkflows = filterByQuery(workflows, query, (w) => [w.name, w.description]);
  const visibleRankers = filterByQuery(rankers, query, (r) => [r.name, r.description]);
  const visiblePostScripts = filterByQuery(postScripts, query, (p) => [p.name, p.description]);

  return (
    <ResourceListPage
      title="Security library"
      description="Reusable workflows, severity rankers, and post-scripts that security scans reference. Referenced content is snapshotted per run, so edits never rewrite past runs."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search the library…"
      loading={loading}
      error={error || undefined}
      onRetry={fetchAll}
      empty={false}
      skeleton={<TableRowSkeleton cols={5} />}
    >
      {actionError && (
        <p className="rounded-md border border-destructive/40 bg-destructive/5 p-2.5 text-sm text-destructive" data-testid="library-action-error">
          {actionError}
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        <ExportSecurityPackDialog
          workflows={workflows.map((w) => w.name)}
          rankers={rankers.map((r) => r.name)}
          postScripts={postScripts.map((p) => p.name)}
          scanConfigs={scanConfigNames}
        />
        <ImportSecurityPackDialog onImported={onSaved} />
      </div>
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
      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="workflows">Workflows ({workflows.length})</TabsTrigger>
          <TabsTrigger value="rankers">Rankers ({rankers.length})</TabsTrigger>
          <TabsTrigger value="post-scripts">Post-scripts ({postScripts.length})</TabsTrigger>
        </TabsList>

        <TabsContent value="workflows" className="space-y-3 pt-3">
          <div className="flex flex-wrap gap-2">
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
          </div>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Description</TableHead>
                <TableHead>Tasks</TableHead>
                <TableHead>Used by</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visibleWorkflows.length === 0 && !loading && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-sm text-muted-foreground">
                    <span className="inline-flex items-center gap-1.5">
                      <Workflow className="size-3.5" /> No reusable workflows yet.
                    </span>
                  </TableCell>
                </TableRow>
              )}
              {visibleWorkflows.map((workflow) => (
                <TableRow key={workflow.name} data-testid={`workflow-row-${workflow.name}`}>
                  <TableCell className="font-mono text-[13px]">{workflow.name}</TableCell>
                  <TableCell className="max-w-72 truncate text-muted-foreground">{workflow.description}</TableCell>
                  <TableCell>{workflow.tasks.length}</TableCell>
                  <TableCell>{usageBadge(workflow.usageCount, workflow.referencingScans)}</TableCell>
                  <TableCell className="text-right">
                    <div className="inline-flex items-center gap-1">
                      <WorkflowEditorDialog
                        mode="edit"
                        source={workflow}
                        onSaved={onSaved}
                        trigger={
                          <Button variant="ghost" size="icon-sm" aria-label={`Edit ${workflow.name}`}>
                            <Pencil className="size-3.5" />
                          </Button>
                        }
                      />
                      <WorkflowEditorDialog
                        mode="duplicate"
                        source={workflow}
                        onSaved={onSaved}
                        trigger={
                          <Button variant="ghost" size="icon-sm" aria-label={`Duplicate ${workflow.name}`}>
                            <Copy className="size-3.5" />
                          </Button>
                        }
                      />
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`Delete ${workflow.name}`}
                        onClick={() => setPendingDelete({ kind: "workflow", name: workflow.name })}
                      >
                        <Trash2 className="size-3.5" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TabsContent>

        <TabsContent value="rankers" className="space-y-3 pt-3">
          <RankerEditorDialog
            mode="create"
            onSaved={onSaved}
            trigger={
              <Button size="sm">
                <Plus className="size-3.5" /> New ranker
              </Button>
            }
          />
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Description</TableHead>
                <TableHead>Rules</TableHead>
                <TableHead>Used by</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visibleRankers.length === 0 && !loading && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-sm text-muted-foreground">
                    No reusable rankers yet.
                  </TableCell>
                </TableRow>
              )}
              {visibleRankers.map((ranker) => (
                <TableRow key={ranker.name} data-testid={`ranker-row-${ranker.name}`}>
                  <TableCell className="font-mono text-[13px]">{ranker.name}</TableCell>
                  <TableCell className="max-w-72 truncate text-muted-foreground">{ranker.description}</TableCell>
                  <TableCell>{ranker.rules.length}</TableCell>
                  <TableCell>{usageBadge(ranker.usageCount, ranker.referencingScans)}</TableCell>
                  <TableCell className="text-right">
                    <div className="inline-flex items-center gap-1">
                      <RankerEditorDialog
                        mode="edit"
                        source={ranker}
                        onSaved={onSaved}
                        trigger={
                          <Button variant="ghost" size="icon-sm" aria-label={`Edit ${ranker.name}`}>
                            <Pencil className="size-3.5" />
                          </Button>
                        }
                      />
                      <RankerEditorDialog
                        mode="duplicate"
                        source={ranker}
                        onSaved={onSaved}
                        trigger={
                          <Button variant="ghost" size="icon-sm" aria-label={`Duplicate ${ranker.name}`}>
                            <Copy className="size-3.5" />
                          </Button>
                        }
                      />
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`Delete ${ranker.name}`}
                        onClick={() => setPendingDelete({ kind: "ranker", name: ranker.name })}
                      >
                        <Trash2 className="size-3.5" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TabsContent>

        <TabsContent value="post-scripts" className="space-y-3 pt-3">
          <div className="flex flex-wrap gap-2">
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
          </div>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Description</TableHead>
                <TableHead>Runs on</TableHead>
                <TableHead>Used by</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visiblePostScripts.length === 0 && !loading && (
                <TableRow>
                  <TableCell colSpan={5} className="text-center text-sm text-muted-foreground">
                    No reusable post-scripts yet.
                  </TableCell>
                </TableRow>
              )}
              {visiblePostScripts.map((script) => (
                <TableRow key={script.name} data-testid={`post-script-row-${script.name}`}>
                  <TableCell className="font-mono text-[13px]">{script.name}</TableCell>
                  <TableCell className="max-w-72 truncate text-muted-foreground">{script.description}</TableCell>
                  <TableCell>{script.runOn || "all"}</TableCell>
                  <TableCell>{usageBadge(script.usageCount, script.referencingScans)}</TableCell>
                  <TableCell className="text-right">
                    <div className="inline-flex items-center gap-1">
                      <PostScriptEditorDialog
                        mode="edit"
                        source={script}
                        onSaved={onSaved}
                        trigger={
                          <Button variant="ghost" size="icon-sm" aria-label={`Edit ${script.name}`}>
                            <Pencil className="size-3.5" />
                          </Button>
                        }
                      />
                      <PostScriptEditorDialog
                        mode="duplicate"
                        source={script}
                        onSaved={onSaved}
                        trigger={
                          <Button variant="ghost" size="icon-sm" aria-label={`Duplicate ${script.name}`}>
                            <Copy className="size-3.5" />
                          </Button>
                        }
                      />
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        aria-label={`Delete ${script.name}`}
                        onClick={() => setPendingDelete({ kind: "post-script", name: script.name })}
                      >
                        <Trash2 className="size-3.5" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
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
