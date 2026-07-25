/**
 * Dialog components for every maintainer command.
 * All dialogs follow the same pattern:
 * - Disable submit while in-flight
 * - Surface server error inline
 * - On Code.Aborted: show "updated concurrently" message + refetch
 * - On success: call onSuccess() and close
 */
import { create } from "@bufbuild/protobuf";
import { useState } from "react";
import { Loader2 } from "lucide-react";
import { Code, ConnectError } from "@connectrpc/connect";

import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import {
  IssueMaintainerCommandRequestSchema,
  MaintainerAcceptedScopeInputSchema,
  MaintainerBreakdownInputSchema,
  MaintainerDispatchInputSchema,
  MaintainerFinalizeInputSchema,
  MaintainerRequestDecisionInputSchema,
  MaintainerRequestMergeInputSchema,
  MaintainerResolveDecisionInputSchema,
  MaintainerTriageInputSchema,
  type MaintainerWorkItem,
  type MaintainerWorkItemLink,
} from "@/rpc/platform/service_pb";

const ABORTED_MSG =
  "The maintainer updated this item — refreshed, please retry.";

function errorMessage(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  if (err instanceof Error) return err.message;
  return "An unexpected error occurred.";
}

function isAborted(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.Aborted;
}

type DialogSharedProps = {
  item: MaintainerWorkItem;
  trigger: React.ReactElement;
  onSuccess: () => void;
};

/* ───────────────────────── Triage / Re-triage ───────────────────────── */

export function TriageDialog({ item, trigger, onSuccess }: DialogSharedProps) {
  const [open, setOpen] = useState(false);
  const [disposition, setDisposition] = useState(item.disposition || "Bounded");
  const [evidenceSummary, setEvidenceSummary] = useState(item.evidenceSummary);
  const [statement, setStatement] = useState(item.acceptedScopeStatement);
  const [criteria, setCriteria] = useState(item.acceptedScopeCriteria.join("\n"));
  const [closeReason, setCloseReason] = useState(item.closeReason || "not_planned");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setDisposition(item.disposition || "Bounded");
    setEvidenceSummary(item.evidenceSummary);
    setStatement(item.acceptedScopeStatement);
    setCriteria(item.acceptedScopeCriteria.join("\n"));
    setCloseReason(item.closeReason || "not_planned");
    setError(null);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await client.issueMaintainerCommand(
        create(IssueMaintainerCommandRequestSchema, {
          namespace: item.namespace,
          repositoryName: item.repositoryName,
          workItemName: item.name,
          expectedProjectionSequence: item.projectionSequence,
          type: "TriageIssue",
          triage: create(MaintainerTriageInputSchema, {
            disposition,
            evidenceSummary,
            acceptedScope:
              disposition !== "NotActionable"
                ? create(MaintainerAcceptedScopeInputSchema, {
                    statement,
                    acceptanceCriteria: criteria
                      .split("\n")
                      .map((s) => s.trim())
                      .filter(Boolean),
                  })
                : undefined,
            closeReason: disposition === "NotActionable" ? closeReason : "",
          }),
        }),
      );
      setOpen(false);
      reset();
      onSuccess();
    } catch (err) {
      setError(isAborted(err) ? ABORTED_MSG : errorMessage(err));
      if (isAborted(err)) onSuccess();
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        // Card components stay mounted while polling replaces `item`. Refresh
        // defaults at open time so a current projection sequence can never be
        // submitted with triage fields captured from an older projection.
        reset();
        setOpen(next);
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent className="flex w-full max-w-lg flex-col gap-0 overflow-hidden p-0" showCloseButton>
        <form onSubmit={handleSubmit} className="flex flex-col">
          <DialogHeader className="border-b px-5 py-4">
            <DialogTitle>Triage #{item.issueNumber}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 px-5 py-4">
            <label className="block space-y-1.5">
              <span className="text-[12.5px] font-medium">Disposition</span>
              <select
                value={disposition}
                onChange={(e) => setDisposition(e.target.value)}
                className="w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm"
              >
                <option value="Bounded">Bounded</option>
                <option value="Decomposable">Decomposable</option>
                <option value="Discovery">Discovery</option>
                <option value="Escalated">Escalated</option>
                <option value="NotActionable">Not actionable</option>
              </select>
            </label>

            <label className="block space-y-1.5">
              <span className="text-[12.5px] font-medium">Evidence summary</span>
              <Textarea
                value={evidenceSummary}
                onChange={(e) => setEvidenceSummary(e.target.value)}
                placeholder="What did the maintainer observe about this issue?"
                className="min-h-20"
              />
            </label>

            {disposition === "NotActionable" ? (
              <label className="block space-y-1.5">
                <span className="text-[12.5px] font-medium">Close reason</span>
                <select
                  value={closeReason}
                  onChange={(e) => setCloseReason(e.target.value)}
                  className="w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm"
                >
                  <option value="not_planned">Not planned</option>
                  <option value="completed">Already completed</option>
                </select>
              </label>
            ) : (
              <>
                <label className="block space-y-1.5">
                  <span className="text-[12.5px] font-medium">Scope statement</span>
                  <Textarea
                    value={statement}
                    onChange={(e) => setStatement(e.target.value)}
                    placeholder="What will be delivered?"
                    className="min-h-16"
                  />
                </label>
                <label className="block space-y-1.5">
                  <span className="text-[12.5px] font-medium">Acceptance criteria</span>
                  <Textarea
                    value={criteria}
                    onChange={(e) => setCriteria(e.target.value)}
                    placeholder="One criterion per line"
                    className="min-h-16 font-mono text-[12px]"
                  />
                </label>
              </>
            )}

            {error && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            )}
          </div>
          <div className="flex items-center justify-end gap-2 border-t px-5 py-3">
            <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
              Cancel
            </DialogClose>
            <Button type="submit" size="sm" disabled={submitting}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : null}
              {submitting ? "Saving…" : "Save triage"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/* ───────────────────────── Configure graph ───────────────────────── */

export function ConfigureGraphDialog({
  item,
  allItems,
  trigger,
  onSuccess,
}: DialogSharedProps & { allItems: MaintainerWorkItem[] }) {
  const [open, setOpen] = useState(false);
  const [childNames, setChildNames] = useState<string[]>(
    item.children.map((c) => c.workItemName),
  );
  const [depNames, setDepNames] = useState<string[]>(
    item.dependencies.map((d) => d.workItemName),
  );
  const [leafConfirm, setLeafConfirm] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const others = allItems.filter((i) => i.name !== item.name);

  function toggleLink(
    name: string,
    list: string[],
    setList: (l: string[]) => void,
  ) {
    setList(list.includes(name) ? list.filter((n) => n !== name) : [...list, name]);
  }

  function reset() {
    setChildNames(item.children.map((c) => c.workItemName));
    setDepNames(item.dependencies.map((d) => d.workItemName));
    setLeafConfirm(false);
    setError(null);
  }

  const isLeaf = childNames.length === 0 && depNames.length === 0;

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (isLeaf && !leafConfirm) {
      setError("Confirm this is a leaf (no children or dependencies) before saving.");
      return;
    }
    setError(null);
    setSubmitting(true);
    try {
      await client.issueMaintainerCommand(
        create(IssueMaintainerCommandRequestSchema, {
          namespace: item.namespace,
          repositoryName: item.repositoryName,
          workItemName: item.name,
          expectedProjectionSequence: item.projectionSequence,
          type: "BreakdownIssue",
          breakdown: create(MaintainerBreakdownInputSchema, {
            childWorkItemNames: childNames,
            dependencyWorkItemNames: depNames,
          }),
        }),
      );
      setOpen(false);
      reset();
      onSuccess();
    } catch (err) {
      setError(isAborted(err) ? ABORTED_MSG : errorMessage(err));
      if (isAborted(err)) onSuccess();
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent className="flex w-full max-w-lg flex-col gap-0 overflow-hidden p-0" showCloseButton>
        <form onSubmit={handleSubmit} className="flex flex-col">
          <DialogHeader className="border-b px-5 py-4">
            <DialogTitle>Configure graph for #{item.issueNumber}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 px-5 py-4 max-h-[60vh] overflow-y-auto">
            {others.length === 0 ? (
              <p className="text-sm text-muted-foreground">No other work items to link.</p>
            ) : (
              <>
                <WorkItemLinkPicker
                  label="Children"
                  hint="Sub-issues this item must deliver."
                  items={others}
                  selected={childNames}
                  onToggle={(name) => toggleLink(name, childNames, setChildNames)}
                />
                <WorkItemLinkPicker
                  label="Dependencies"
                  hint="Items that must be delivered before this one."
                  items={others}
                  selected={depNames}
                  onToggle={(name) => toggleLink(name, depNames, setDepNames)}
                />
              </>
            )}

            {isLeaf && (
              <label className="flex items-start gap-2.5 rounded-md border border-border/60 bg-muted/30 px-3 py-2.5 text-[12.5px]">
                <input
                  type="checkbox"
                  className="mt-0.5 shrink-0"
                  checked={leafConfirm}
                  onChange={(e) => setLeafConfirm(e.target.checked)}
                />
                <span>
                  This is a leaf issue — no children or dependencies. The maintainer can dispatch
                  it directly once triage is complete.
                </span>
              </label>
            )}

            {error && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            )}
          </div>
          <div className="flex items-center justify-end gap-2 border-t px-5 py-3">
            <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
              Cancel
            </DialogClose>
            <Button type="submit" size="sm" disabled={submitting}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : null}
              {submitting ? "Saving…" : "Save graph"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function WorkItemLinkPicker({
  label,
  hint,
  items,
  selected,
  onToggle,
}: {
  label: string;
  hint: string;
  items: MaintainerWorkItem[];
  selected: string[];
  onToggle: (name: string) => void;
}) {
  return (
    <div className="space-y-1.5">
      <div>
        <p className="text-[12.5px] font-medium">{label}</p>
        <p className="text-[11px] text-muted-foreground">{hint}</p>
      </div>
      <div className="max-h-40 overflow-y-auto rounded-md border border-border/60 divide-y divide-border/40">
        {items.map((i) => (
          <label
            key={i.name}
            className="flex cursor-pointer items-center gap-2.5 px-3 py-1.5 text-[12.5px] hover:bg-muted/30"
          >
            <input
              type="checkbox"
              className="shrink-0"
              checked={selected.includes(i.name)}
              onChange={() => onToggle(i.name)}
            />
            <span className="font-mono text-muted-foreground">#{i.issueNumber}</span>
            <span className="truncate">{i.issueTitle}</span>
          </label>
        ))}
      </div>
    </div>
  );
}

/* ───────────────────────── Request decision ───────────────────────── */

export function RequestDecisionDialog({ item, trigger, onSuccess }: DialogSharedProps) {
  const [open, setOpen] = useState(false);
  const [decisionId, setDecisionId] = useState("");
  const [question, setQuestion] = useState("");
  const [options, setOptions] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setDecisionId("");
    setQuestion("");
    setOptions("");
    setError(null);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmedDecisionId = decisionId.trim();
    const trimmedQuestion = question.trim();
    if (!trimmedDecisionId || !trimmedQuestion) {
      setError("Decision ID and question are required.");
      return;
    }

    setError(null);
    setSubmitting(true);
    try {
      await client.issueMaintainerCommand(
        create(IssueMaintainerCommandRequestSchema, {
          namespace: item.namespace,
          repositoryName: item.repositoryName,
          workItemName: item.name,
          expectedProjectionSequence: item.projectionSequence,
          type: "RequestDecision",
          requestDecision: create(MaintainerRequestDecisionInputSchema, {
            decisionId: trimmedDecisionId,
            question: trimmedQuestion,
            options: options
              .split("\n")
              .map((option) => option.trim())
              .filter(Boolean),
          }),
        }),
      );
      setOpen(false);
      reset();
      onSuccess();
    } catch (err) {
      setError(isAborted(err) ? ABORTED_MSG : errorMessage(err));
      if (isAborted(err)) onSuccess();
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent className="flex w-full max-w-md flex-col gap-0 overflow-hidden p-0" showCloseButton>
        <form onSubmit={handleSubmit} className="flex flex-col">
          <DialogHeader className="border-b px-5 py-4">
            <DialogTitle>Ask a question</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 px-5 py-4">
            <label className="block space-y-1.5">
              <span className="text-[12.5px] font-medium">Decision ID</span>
              <Input
                value={decisionId}
                onChange={(e) => setDecisionId(e.target.value)}
                placeholder="e.g. deployment-strategy"
                disabled={submitting}
              />
            </label>
            <label className="block space-y-1.5">
              <span className="text-[12.5px] font-medium">Question</span>
              <Textarea
                value={question}
                onChange={(e) => setQuestion(e.target.value)}
                placeholder="What decision needs human input?"
                className="min-h-20"
                disabled={submitting}
              />
            </label>
            <label className="block space-y-1.5">
              <span className="text-[12.5px] font-medium">Options (optional)</span>
              <Textarea
                value={options}
                onChange={(e) => setOptions(e.target.value)}
                placeholder="One option per line"
                className="min-h-16"
                disabled={submitting}
              />
            </label>
            {error && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            )}
          </div>
          <div className="flex items-center justify-end gap-2 border-t px-5 py-3">
            <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
              Cancel
            </DialogClose>
            <Button type="submit" size="sm" disabled={submitting}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : null}
              {submitting ? "Sending…" : "Ask question"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/* ───────────────────────── Answer decision ───────────────────────── */

export function AnswerDialog({ item, trigger, onSuccess }: DialogSharedProps) {
  const decision = item.pendingDecision;
  const [open, setOpen] = useState(false);
  const [answer, setAnswer] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!decision) return null;

  function reset() {
    setAnswer("");
    setError(null);
  }

  async function submitAnswer(selectedAnswer: string) {
    if (!decision) return;
    setError(null);
    setSubmitting(true);
    try {
      await client.issueMaintainerCommand(
        create(IssueMaintainerCommandRequestSchema, {
          namespace: item.namespace,
          repositoryName: item.repositoryName,
          workItemName: item.name,
          expectedProjectionSequence: item.projectionSequence,
          type: "ResolveDecision",
          resolveDecision: create(MaintainerResolveDecisionInputSchema, {
            decisionId: decision.id,
            answer: selectedAnswer,
          }),
        }),
      );
      setOpen(false);
      reset();
      onSuccess();
    } catch (err) {
      setError(isAborted(err) ? ABORTED_MSG : errorMessage(err));
      if (isAborted(err)) onSuccess();
    } finally {
      setSubmitting(false);
    }
  }

  async function handleFreeText(e: React.FormEvent) {
    e.preventDefault();
    if (!answer.trim()) return;
    await submitAnswer(answer.trim());
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent className="flex w-full max-w-md flex-col gap-0 overflow-hidden p-0" showCloseButton>
        <DialogHeader className="border-b px-5 py-4">
          <DialogTitle>Answer decision</DialogTitle>
        </DialogHeader>
        <div className="space-y-4 px-5 py-4">
          <p className="text-[13px] leading-relaxed">{decision.question}</p>

          {decision.options.length > 0 && (
            <div className="flex flex-wrap gap-2">
              {decision.options.map((opt) => (
                <Button
                  key={opt}
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={submitting}
                  onClick={() => void submitAnswer(opt)}
                >
                  {opt}
                </Button>
              ))}
            </div>
          )}

          <form onSubmit={handleFreeText} className="flex gap-2">
            <Input
              value={answer}
              onChange={(e) => setAnswer(e.target.value)}
              placeholder="Or type a custom answer…"
              className="flex-1 text-sm"
              disabled={submitting}
              aria-label="Custom answer"
            />
            <Button type="submit" size="sm" disabled={submitting || !answer.trim()}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : "Send"}
            </Button>
          </form>

          {error && (
            <p role="alert" className={cn("text-sm", toneText.danger)}>
              {error}
            </p>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

/* ───────────────────────── Dispatch now ───────────────────────── */

export function DispatchDialog({ item, trigger, onSuccess }: DialogSharedProps) {
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setMode("");
    setError(null);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await client.issueMaintainerCommand(
        create(IssueMaintainerCommandRequestSchema, {
          namespace: item.namespace,
          repositoryName: item.repositoryName,
          workItemName: item.name,
          expectedProjectionSequence: item.projectionSequence,
          type: "DispatchWorkItem",
          dispatch: create(MaintainerDispatchInputSchema, { mode: mode.trim() }),
        }),
      );
      setOpen(false);
      reset();
      onSuccess();
    } catch (err) {
      setError(isAborted(err) ? ABORTED_MSG : errorMessage(err));
      if (isAborted(err)) onSuccess();
    } finally {
      setSubmitting(false);
    }
  }

  const disabled = !item.readyToDispatch;

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent className="flex w-full max-w-sm flex-col gap-0 overflow-hidden p-0" showCloseButton>
        <form onSubmit={handleSubmit} className="flex flex-col">
          <DialogHeader className="border-b px-5 py-4">
            <DialogTitle>Dispatch #{item.issueNumber}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 px-5 py-4">
            {disabled && item.unmetRequirements.length > 0 && (
              <div className={cn("rounded-md border border-border/60 bg-muted/30 px-3 py-2 text-[12.5px]", toneText.warning)}>
                <p className="font-medium mb-1">Not ready to dispatch</p>
                <ul className="list-disc list-inside space-y-0.5 text-muted-foreground">
                  {item.unmetRequirements.map((req) => (
                    <li key={req}>{req}</li>
                  ))}
                </ul>
              </div>
            )}
            <label className="block space-y-1.5">
              <span className="text-[12.5px] font-medium">Mode (optional)</span>
              <Input
                value={mode}
                onChange={(e) => setMode(e.target.value)}
                placeholder="repository default"
                disabled={disabled}
              />
            </label>
            {error && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            )}
          </div>
          <div className="flex items-center justify-end gap-2 border-t px-5 py-3">
            <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
              Cancel
            </DialogClose>
            <Button type="submit" size="sm" disabled={submitting || disabled}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : null}
              {submitting ? "Dispatching…" : "Dispatch now"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/* ───────────────────────── Merge ───────────────────────── */

export function MergeDialog({ item, trigger, onSuccess }: DialogSharedProps) {
  const prs = item.pullRequests;
  const [open, setOpen] = useState(false);
  const [prNumber, setPrNumber] = useState(prs[0]?.number ?? 0);
  const [mergeMethod, setMergeMethod] = useState("squash");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const selectedPr = prs.find((p) => p.number === prNumber) ?? prs[0];

  function reset() {
    setPrNumber(prs[0]?.number ?? 0);
    setMergeMethod("squash");
    setError(null);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!selectedPr) return;
    setError(null);
    setSubmitting(true);
    try {
      await client.issueMaintainerCommand(
        create(IssueMaintainerCommandRequestSchema, {
          namespace: item.namespace,
          repositoryName: item.repositoryName,
          workItemName: item.name,
          expectedProjectionSequence: item.projectionSequence,
          type: "RequestMerge",
          requestMerge: create(MaintainerRequestMergeInputSchema, {
            pullRequestNumber: selectedPr.number,
            expectedHeadSha: selectedPr.headSha,
            mergeMethod,
          }),
        }),
      );
      setOpen(false);
      reset();
      onSuccess();
    } catch (err) {
      setError(isAborted(err) ? ABORTED_MSG : errorMessage(err));
      if (isAborted(err)) onSuccess();
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent className="flex w-full max-w-sm flex-col gap-0 overflow-hidden p-0" showCloseButton>
        <form onSubmit={handleSubmit} className="flex flex-col">
          <DialogHeader className="border-b px-5 py-4">
            <DialogTitle>Merge PR for #{item.issueNumber}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 px-5 py-4">
            {prs.length > 1 && (
              <label className="block space-y-1.5">
                <span className="text-[12.5px] font-medium">Pull request</span>
                <select
                  value={prNumber}
                  onChange={(e) => setPrNumber(Number(e.target.value))}
                  className="w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm"
                >
                  {prs.map((pr) => (
                    <option key={pr.number} value={pr.number}>
                      {pr.repository}#{pr.number}
                    </option>
                  ))}
                </select>
              </label>
            )}

            {selectedPr && (
              <p className="text-[12px] font-mono text-muted-foreground">
                HEAD: {selectedPr.headSha ? selectedPr.headSha.slice(0, 7) : "unknown"}
              </p>
            )}

            <label className="block space-y-1.5">
              <span className="text-[12.5px] font-medium">Merge method</span>
              <select
                value={mergeMethod}
                onChange={(e) => setMergeMethod(e.target.value)}
                className="w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm"
              >
                <option value="squash">Squash and merge</option>
                <option value="merge">Create a merge commit</option>
                <option value="rebase">Rebase and merge</option>
              </select>
            </label>

            {error && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            )}
          </div>
          <div className="flex items-center justify-end gap-2 border-t px-5 py-3">
            <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
              Cancel
            </DialogClose>
            <Button type="submit" size="sm" disabled={submitting || !selectedPr}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : null}
              {submitting ? "Merging…" : "Merge"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/* ───────────────────────── Finalize ───────────────────────── */

export function FinalizeDialog({ item, trigger, onSuccess }: DialogSharedProps) {
  const [open, setOpen] = useState(false);
  const [deliverySummary, setDeliverySummary] = useState(item.deliverySummary);
  const [deliveryEvidence, setDeliveryEvidence] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setDeliverySummary(item.deliverySummary);
    setDeliveryEvidence("");
    setError(null);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      await client.issueMaintainerCommand(
        create(IssueMaintainerCommandRequestSchema, {
          namespace: item.namespace,
          repositoryName: item.repositoryName,
          workItemName: item.name,
          expectedProjectionSequence: item.projectionSequence,
          type: "FinalizeWorkItem",
          finalize: create(MaintainerFinalizeInputSchema, {
            deliverySummary,
            deliveryEvidence,
          }),
        }),
      );
      setOpen(false);
      reset();
      onSuccess();
    } catch (err) {
      setError(isAborted(err) ? ABORTED_MSG : errorMessage(err));
      if (isAborted(err)) onSuccess();
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent className="flex w-full max-w-md flex-col gap-0 overflow-hidden p-0" showCloseButton>
        <form onSubmit={handleSubmit} className="flex flex-col">
          <DialogHeader className="border-b px-5 py-4">
            <DialogTitle>Finalize #{item.issueNumber}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4 px-5 py-4">
            <label className="block space-y-1.5">
              <span className="text-[12.5px] font-medium">Delivery summary</span>
              <Textarea
                value={deliverySummary}
                onChange={(e) => setDeliverySummary(e.target.value)}
                placeholder="What was delivered?"
                className="min-h-20"
              />
            </label>
            <label className="block space-y-1.5">
              <span className="text-[12.5px] font-medium">Delivery evidence</span>
              <Textarea
                value={deliveryEvidence}
                onChange={(e) => setDeliveryEvidence(e.target.value)}
                placeholder="Links, commit SHAs, screenshots…"
                className="min-h-16"
              />
            </label>
            {error && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            )}
          </div>
          <div className="flex items-center justify-end gap-2 border-t px-5 py-3">
            <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
              Cancel
            </DialogClose>
            <Button type="submit" size="sm" disabled={submitting}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : null}
              {submitting ? "Finalizing…" : "Finalize"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

// Re-export link type for convenience
export type { MaintainerWorkItemLink };
