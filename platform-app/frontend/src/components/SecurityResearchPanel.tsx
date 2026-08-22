/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { FlaskConical, Plus, RefreshCw } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { client } from "@/lib/client";
import type {
  SecurityCampaignResearchStatus,
  SecurityResearchCoverage,
  SecurityResearchDossier,
  SecurityResearchHypothesis,
  SecurityResearchScope,
  SecurityResearchVariantSweep,
  SecuritySubmissionOutcomeEvent,
} from "@/rpc/platform/service_pb";

const HYPOTHESIS_STATUSES = [
  "proposed", "investigating", "supported", "weakened", "falsified", "blocked", "superseded", "promoted",
];
const HYPOTHESIS_RESULTS = [
  "pending", "positive", "negative", "failed", "timed_out", "inconclusive", "abandoned",
];
const COVERAGE_DIMENSIONS = ["invariant", "actor", "state", "transition"];
const COVERAGE_VERDICTS = ["disproved", "adequately_tested", "inadequately_tested", "not_tested"];
const SWEEP_STATUSES = ["pending", "running"];
const SWEEP_COMPLETION_STATUSES = ["completed", "blocked"];
const SUBMISSION_OUTCOMES = ["accepted", "duplicate", "informative", "rejected", "resolved"];
const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

const fieldClass = "space-y-1.5";
const selectClass = "h-8 w-full rounded-lg border border-input bg-background px-2.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50";

function readable(value: string): string {
  return value.replace(/_/g, " ");
}

function prettyJSON(value: string, fallback: string): string {
  try {
    return JSON.stringify(JSON.parse(value || fallback), null, 2);
  } catch {
    return value;
  }
}

function jsonError(value: string, label: string): string {
  try {
    JSON.parse(value);
    return "";
  } catch {
    return `${label} must be valid JSON.`;
  }
}

function completedSweepEvidenceError(value: string): string {
  const syntax = jsonError(value, "Result");
  if (syntax) return syntax;
  const parsed: unknown = JSON.parse(value);
  if (
    parsed === null || typeof parsed !== "object" || Array.isArray(parsed)
  ) {
    return "Completed result must be a JSON object with evidence fields.";
  }
  const result = parsed as Record<string, unknown>;
  if (
    !Array.isArray(result.searched_scope) || result.searched_scope.length === 0
    || !Array.isArray(result.methods) || result.methods.length === 0
    || !Array.isArray(result.evidence) || result.evidence.length === 0
    || typeof result.summary !== "string" || !result.summary.trim()
  ) {
    return "Completed results require non-empty searched_scope, methods, evidence, and summary fields.";
  }
  return "";
}

function when(value: Timestamp | undefined): string {
  return value ? timestampDate(value).toLocaleString() : "—";
}

function ErrorMessage({ children }: { children: ReactNode }) {
  if (!children) return null;
  return <p role="alert" className="text-[12px] text-destructive">{children}</p>;
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className={fieldClass}>
      <Label>{label}</Label>
      {children}
    </div>
  );
}

function JsonField({ label, value, onChange, rows = 5 }: {
  label: string;
  value: string;
  onChange: (value: string) => void;
  rows?: number;
}) {
  return (
    <Field label={label}>
      <Textarea
        aria-label={label}
        className="font-mono text-xs"
        rows={rows}
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
    </Field>
  );
}

function FormDialog({
  open,
  onOpenChange,
  title,
  description,
  submitLabel,
  busy,
  error,
  onSubmit,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description: string;
  submitLabel: string;
  busy: boolean;
  error: string;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  children: ReactNode;
}) {
  return (
    <Dialog open={open} onOpenChange={(next) => !busy && onOpenChange(next)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <form className="space-y-4" onSubmit={onSubmit}>
          {children}
          <ErrorMessage>{error}</ErrorMessage>
          <DialogFooter>
            <Button type="button" variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button type="submit" disabled={busy}>{busy ? "Saving…" : submitLabel}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function CountGroup({ label, values }: { label: string; values: Record<string, number> }) {
  const entries = Object.entries(values);
  return (
    <div className="space-y-1.5">
      <p className="text-[10.5px] font-medium uppercase tracking-[0.07em] text-muted-foreground">{label}</p>
      <div className="flex flex-wrap gap-1.5">
        {entries.length === 0 ? <span className="text-xs text-muted-foreground">None</span> : entries.map(([key, count]) => (
          <Badge key={key} variant="outline" className="capitalize">{readable(key)} · {count}</Badge>
        ))}
      </div>
    </div>
  );
}

function EmptyRow({ children }: { children: ReactNode }) {
  return <p className="rounded-md border border-dashed px-3 py-4 text-center text-xs text-muted-foreground">{children}</p>;
}

function permissionCanWrite(permission: string): boolean {
  return permission === "owner" || permission === "collaborator" || permission === "admin";
}

export function SecurityResearchPanel({ namespace, targetKey, revision, workflow, permission }: {
  namespace: string;
  targetKey: string;
  revision: string;
  workflow: string;
  permission: string;
}) {
  const scope = useMemo<SecurityResearchScope>(() => ({
    $typeName: "platform.v1.SecurityResearchScope",
    namespace,
    targetKey,
    revision,
  }), [namespace, targetKey, revision]);
  const scopeKey = `${namespace}\u0000${targetKey}\u0000${revision}`;
  const loadGeneration = useRef(0);
  const previousScopeKey = useRef("");
  const canWrite = permissionCanWrite(permission);
  const [status, setStatus] = useState<SecurityCampaignResearchStatus | null>(null);
  const [dossier, setDossier] = useState<SecurityResearchDossier | null>(null);
  const [hypotheses, setHypotheses] = useState<SecurityResearchHypothesis[]>([]);
  const [coverage, setCoverage] = useState<SecurityResearchCoverage[]>([]);
  const [sweeps, setSweeps] = useState<SecurityResearchVariantSweep[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [dialog, setDialog] = useState<"dossier" | "hypothesis" | "transition" | "coverage" | "sweep" | "complete" | "outcome" | "correction" | null>(null);
  const [selectedHypothesis, setSelectedHypothesis] = useState<SecurityResearchHypothesis | null>(null);
  const [selectedSweep, setSelectedSweep] = useState<SecurityResearchVariantSweep | null>(null);
  const [selectedEvent, setSelectedEvent] = useState<SecuritySubmissionOutcomeEvent | null>(null);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState("");

  const [dossierContent, setDossierContent] = useState("{}");
  const [changeSummary, setChangeSummary] = useState("");
  const [hypothesisKey, setHypothesisKey] = useState("");
  const [hypothesisTitle, setHypothesisTitle] = useState("");
  const [invariant, setInvariant] = useState("");
  const [detailJSON, setDetailJSON] = useState("{}");
  const [nextStatus, setNextStatus] = useState("investigating");
  const [result, setResult] = useState("pending");
  const [rationale, setRationale] = useState("");
  const [coverageHypothesis, setCoverageHypothesis] = useState("");
  const [dimension, setDimension] = useState("invariant");
  const [subjectKey, setSubjectKey] = useState("");
  const [verdict, setVerdict] = useState("not_tested");
  const [boundsJSON, setBoundsJSON] = useState("{}");
  const [evidenceJSON, setEvidenceJSON] = useState("[]");
  const [findingId, setFindingId] = useState("");
  const [rootHypothesisId, setRootHypothesisId] = useState("");
  const [rootCause, setRootCause] = useState("");
  const [sweepScopeJSON, setSweepScopeJSON] = useState("{}");
  const [sweepStatus, setSweepStatus] = useState("pending");
  const [completionStatus, setCompletionStatus] = useState("completed");
  const [resultJSON, setResultJSON] = useState('{\n  "searched_scope": [],\n  "methods": [],\n  "evidence": [],\n  "summary": ""\n}');
  const [submissionId, setSubmissionId] = useState("");
  const [outcomeEvents, setOutcomeEvents] = useState<SecuritySubmissionOutcomeEvent[]>([]);
  const [outcomeLoading, setOutcomeLoading] = useState(false);
  const [outcomeError, setOutcomeError] = useState("");
  const [outcome, setOutcome] = useState("accepted");
  const [externalReference, setExternalReference] = useState("");

  const load = useCallback(async () => {
    if (!revision) return;
    const requestScopeKey = scopeKey;
    const generation = ++loadGeneration.current;
    setLoading(true);
    setLoadError("");
    const results = await Promise.allSettled([
      client.getSecurityCampaignResearchStatus({ scope, workflow }),
      client.getSecurityResearchDossier({ scope }),
      client.listSecurityResearchHypotheses({ scope, limit: 200 }),
      client.listSecurityResearchCoverage({ scope, limit: 200 }),
      client.listSecurityResearchVariantSweeps({ scope, limit: 200 }),
    ]);
    if (generation !== loadGeneration.current || previousScopeKey.current !== requestScopeKey) return;
    const [statusResult, dossierResult, hypothesesResult, coverageResult, sweepsResult] = results;
    setStatus(statusResult.status === "fulfilled" ? statusResult.value : null);
    setDossier(dossierResult.status === "fulfilled" ? dossierResult.value : null);
    setHypotheses(hypothesesResult.status === "fulfilled" ? hypothesesResult.value.hypotheses : []);
    setCoverage(coverageResult.status === "fulfilled" ? coverageResult.value.coverage : []);
    setSweeps(sweepsResult.status === "fulfilled" ? sweepsResult.value.sweeps : []);
    const failed = [statusResult, hypothesesResult, coverageResult, sweepsResult].find((item) => item.status === "rejected");
    setLoadError(failed?.status === "rejected"
      ? failed.reason instanceof Error ? failed.reason.message : "Failed to load security research"
      : "");
    setLoading(false);
  }, [revision, scope, scopeKey, workflow]);

  useEffect(() => {
    if (previousScopeKey.current !== scopeKey) {
      loadGeneration.current += 1;
      previousScopeKey.current = scopeKey;
      setStatus(null);
      setDossier(null);
      setHypotheses([]);
      setCoverage([]);
      setSweeps([]);
      setLoading(false);
      setOutcomeEvents([]);
      setOutcomeLoading(false);
      setOutcomeError("");
      setDialog(null);
      setSelectedHypothesis(null);
      setSelectedSweep(null);
      setSelectedEvent(null);
    }
    void load();
  }, [load, scopeKey]);

  async function refreshOutcomeHistory(id = submissionId) {
    const requestScopeKey = scopeKey;
    const trimmed = id.trim();
    if (!UUID_PATTERN.test(trimmed)) {
      setOutcomeError("Submission ID must be a valid UUID.");
      return;
    }
    setOutcomeLoading(true);
    setOutcomeError("");
    try {
      const response = await client.listSecuritySubmissionOutcomeHistory({ scope, submissionId: trimmed, limit: 200 });
      if (previousScopeKey.current !== requestScopeKey) return;
      setOutcomeEvents(response.events);
    } catch (error: unknown) {
      if (previousScopeKey.current !== requestScopeKey) return;
      setOutcomeEvents([]);
      setOutcomeError(error instanceof Error ? error.message : "Failed to load submission outcome history");
    } finally {
      if (previousScopeKey.current === requestScopeKey) setOutcomeLoading(false);
    }
  }

  function openDialog(next: typeof dialog) {
    setFormError("");
    setDialog(next);
  }

  function mutationError(error: unknown, fallback: string) {
    setFormError(error instanceof Error ? error.message : fallback);
  }

  async function submitDossier(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const validation = jsonError(dossierContent, "Dossier content");
    if (validation) return setFormError(validation);
    if (!changeSummary.trim()) return setFormError("Change summary is required.");
    setBusy(true);
    try {
      await client.amendSecurityResearchDossier({
        scope,
        expectedVersion: dossier?.version ?? 0,
        expectedParentId: dossier?.id ?? "",
        contentJson: dossierContent,
        changeSummary: changeSummary.trim(),
        idempotencyKey: crypto.randomUUID(),
      });
      setDialog(null);
      await load();
    } catch (error: unknown) {
      mutationError(error, "Failed to amend dossier");
    } finally {
      setBusy(false);
    }
  }

  async function submitHypothesis(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!hypothesisKey.trim() || !hypothesisTitle.trim() || !invariant.trim()) return setFormError("Key, title, and invariant are required.");
    const validation = jsonError(detailJSON, "Detail");
    if (validation) return setFormError(validation);
    setBusy(true);
    try {
      await client.createSecurityResearchHypothesis({
        scope,
        hypothesisKey: hypothesisKey.trim(),
        title: hypothesisTitle.trim(),
        invariant: invariant.trim(),
        detailJson: detailJSON,
        idempotencyKey: crypto.randomUUID(),
      });
      setHypothesisKey("");
      setHypothesisTitle("");
      setInvariant("");
      setDetailJSON("{}");
      setDialog(null);
      await load();
    } catch (error: unknown) {
      mutationError(error, "Failed to create hypothesis");
    } finally {
      setBusy(false);
    }
  }

  async function submitTransition(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedHypothesis) return;
    if (!rationale.trim()) return setFormError("Rationale is required.");
    const validation = jsonError(detailJSON, "Detail");
    if (validation) return setFormError(validation);
    setBusy(true);
    try {
      await client.transitionSecurityResearchHypothesis({
        scope,
        hypothesisId: selectedHypothesis.id,
        expectedVersion: selectedHypothesis.version,
        toStatus: nextStatus,
        result,
        rationale: rationale.trim(),
        detailJson: detailJSON,
        idempotencyKey: crypto.randomUUID(),
      });
      setRationale("");
      setDetailJSON("{}");
      setDialog(null);
      await load();
    } catch (error: unknown) {
      mutationError(error, "Failed to transition hypothesis");
    } finally {
      setBusy(false);
    }
  }

  async function submitCoverage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!subjectKey.trim()) return setFormError("Subject is required.");
    const validation = jsonError(boundsJSON, "Bounds") || jsonError(evidenceJSON, "Evidence");
    if (validation) return setFormError(validation);
    setBusy(true);
    try {
      await client.recordSecurityResearchCoverage({
        scope,
        hypothesisId: coverageHypothesis,
        dimension,
        subjectKey: subjectKey.trim(),
        verdict,
        boundsJson: boundsJSON,
        evidenceJson: evidenceJSON,
        idempotencyKey: crypto.randomUUID(),
      });
      setSubjectKey("");
      setDialog(null);
      await load();
    } catch (error: unknown) {
      mutationError(error, "Failed to record coverage");
    } finally {
      setBusy(false);
    }
  }

  async function submitSweep(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!rootCause.trim()) return setFormError("Root cause is required.");
    if (findingId && !UUID_PATTERN.test(findingId.trim())) return setFormError("Finding ID must be a valid UUID.");
    const validation = jsonError(sweepScopeJSON, "Sweep scope");
    if (validation) return setFormError(validation);
    setBusy(true);
    try {
      await client.createSecurityResearchVariantSweep({
        scope,
        findingId: findingId.trim(),
        rootHypothesisId,
        rootCause: rootCause.trim(),
        scopeJson: sweepScopeJSON,
        status: sweepStatus,
        idempotencyKey: crypto.randomUUID(),
      });
      setFindingId("");
      setRootCause("");
      setDialog(null);
      await load();
    } catch (error: unknown) {
      mutationError(error, "Failed to create variant sweep");
    } finally {
      setBusy(false);
    }
  }

  async function submitCompletion(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selectedSweep) return;
    const validation = completionStatus === "completed" ? completedSweepEvidenceError(resultJSON) : jsonError(resultJSON, "Result");
    if (validation) return setFormError(validation);
    setBusy(true);
    try {
      await client.completeSecurityResearchVariantSweep({
        scope,
        sweepId: selectedSweep.id,
        status: completionStatus,
        resultJson: resultJSON,
        idempotencyKey: crypto.randomUUID(),
      });
      setDialog(null);
      await load();
    } catch (error: unknown) {
      mutationError(error, "Failed to complete variant sweep");
    } finally {
      setBusy(false);
    }
  }

  async function submitOutcome(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!UUID_PATTERN.test(submissionId.trim())) return setFormError("Submission ID must be a valid UUID.");
    if (!rationale.trim()) return setFormError("Rationale is required.");
    setBusy(true);
    try {
      const request = {
        scope,
        submissionId: submissionId.trim(),
        outcome,
        externalReference: externalReference.trim(),
        rationale: rationale.trim(),
        idempotencyKey: crypto.randomUUID(),
      };
      if (dialog === "correction" && selectedEvent) {
        await client.correctSecuritySubmissionOutcome({ ...request, correctionOf: selectedEvent.id });
      } else {
        await client.recordSecuritySubmissionOutcome(request);
      }
      setRationale("");
      setDialog(null);
      await Promise.all([refreshOutcomeHistory(), load()]);
    } catch (error: unknown) {
      mutationError(error, "Failed to record submission outcome");
    } finally {
      setBusy(false);
    }
  }

  if (!revision) {
    return (
      <section aria-label="Security research" className="rounded-lg border border-border/70 bg-muted/20 p-4">
        <h2 className="font-medium">Security research</h2>
        <p className="mt-1 text-sm text-muted-foreground">Research controls require an immutable scan revision.</p>
      </section>
    );
  }

  const mutationTitle = canWrite ? undefined : "Viewer access is read-only. Ask the scan owner for collaborator access.";

  return (
    <section aria-label="Security research" className="space-y-4 rounded-lg border border-border/70 bg-muted/10 p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <FlaskConical className="size-4 text-muted-foreground" />
            <h2 className="font-medium">Security research</h2>
            {permission && <Badge variant="outline" className="capitalize">{permission}</Badge>}
          </div>
          <p className="mt-1 text-xs text-muted-foreground">
            Durable research for <span className="font-mono">{targetKey}@{revision.slice(0, 12)}</span> · workflow {workflow}
          </p>
          {!canWrite && <p className="mt-1 text-xs text-muted-foreground">Viewer access can inspect research but cannot change it.</p>}
        </div>
        <Button variant="ghost" size="sm" disabled={loading} onClick={() => void load()}>
          <RefreshCw className={loading ? "animate-spin" : ""} />
          Refresh
        </Button>
      </div>

      <ErrorMessage>{loadError}</ErrorMessage>

      {status && (
        <div className="grid gap-3 rounded-lg border bg-background/60 p-3 md:grid-cols-2 xl:grid-cols-4">
          <CountGroup label="Hypotheses" values={status.hypothesisStatusCounts} />
          <CountGroup label="Results" values={status.hypothesisResultCounts} />
          <CountGroup label="Coverage" values={status.coverageVerdictCounts} />
          <CountGroup label="Variant sweeps" values={status.variantSweepStatusCounts} />
          <div className="md:col-span-2 xl:col-span-4 text-xs text-muted-foreground">
            Dossier v{status.dossierVersion} · submissions {String(status.precision?.submitted ?? 0n)} · accepted {String(status.precision?.accepted ?? 0n)} · duplicates {String(status.precision?.duplicate ?? 0n)} · rejected {String(status.precision?.rejected ?? 0n)}
          </div>
        </div>
      )}

      <details open className="group rounded-lg border bg-background/60">
        <summary className="flex cursor-pointer list-none items-center justify-between gap-3 px-3 py-2.5">
          <span className="text-sm font-medium">Dossier {dossier ? `· v${dossier.version}` : ""}</span>
          <Button
            size="sm"
            variant="outline"
            disabled={!canWrite}
            title={mutationTitle}
            onClick={(event) => {
              event.preventDefault();
              setDossierContent(prettyJSON(dossier?.contentJson ?? "{}", "{}"));
              setChangeSummary("");
              openDialog("dossier");
            }}
          >{dossier ? "Amend" : "Create dossier"}</Button>
        </summary>
        <div className="border-t p-3">
          {dossier ? (
            <>
              <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/40 p-3 font-mono text-xs">{prettyJSON(dossier.contentJson, "{}")}</pre>
              <p className="mt-2 text-xs text-muted-foreground">{dossier.changeSummary || "No change summary"} · {dossier.actor || "unknown actor"} · {when(dossier.createdAt)}</p>
            </>
          ) : <EmptyRow>No dossier recorded for this revision.</EmptyRow>}
        </div>
      </details>

      <details open className="rounded-lg border bg-background/60">
        <summary className="flex cursor-pointer list-none items-center justify-between gap-3 px-3 py-2.5">
          <span className="text-sm font-medium">Hypotheses · {hypotheses.length}</span>
          <Button size="sm" variant="outline" disabled={!canWrite} title={mutationTitle} onClick={(event) => { event.preventDefault(); openDialog("hypothesis"); }}><Plus />Create</Button>
        </summary>
        <div className="space-y-2 border-t p-3">
          {hypotheses.length === 0 ? <EmptyRow>No hypotheses recorded.</EmptyRow> : hypotheses.map((hypothesis) => (
            <article key={hypothesis.id} className="flex flex-wrap items-start justify-between gap-3 rounded-md border p-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="font-mono text-[11px] text-muted-foreground">{hypothesis.hypothesisKey}</span>
                  <Badge variant="outline" className="capitalize">{readable(hypothesis.status)}</Badge>
                  <Badge variant="secondary" className="capitalize">{readable(hypothesis.result)}</Badge>
                </div>
                <h3 className="mt-1 text-sm font-medium">{hypothesis.title}</h3>
                <p className="mt-1 text-xs text-muted-foreground">{hypothesis.invariant}</p>
              </div>
              <Button size="sm" variant="ghost" disabled={!canWrite} title={mutationTitle} onClick={() => {
                setSelectedHypothesis(hypothesis);
                setNextStatus(hypothesis.status === "proposed" ? "investigating" : hypothesis.status);
                setResult(hypothesis.result || "pending");
                setRationale("");
                setDetailJSON(prettyJSON(hypothesis.detailJson, "{}"));
                openDialog("transition");
              }}>Transition</Button>
            </article>
          ))}
        </div>
      </details>

      <details className="rounded-lg border bg-background/60">
        <summary className="flex cursor-pointer list-none items-center justify-between gap-3 px-3 py-2.5">
          <span className="text-sm font-medium">Coverage · {coverage.length}</span>
          <Button size="sm" variant="outline" disabled={!canWrite} title={mutationTitle} onClick={(event) => { event.preventDefault(); openDialog("coverage"); }}><Plus />Record</Button>
        </summary>
        <div className="space-y-2 border-t p-3">
          {coverage.length === 0 ? <EmptyRow>No coverage recorded.</EmptyRow> : coverage.map((item) => (
            <article key={item.id} className="rounded-md border p-3 text-xs">
              <div className="flex flex-wrap items-center gap-2">
                <Badge variant="outline" className="capitalize">{readable(item.dimension)}</Badge>
                <strong>{item.subjectKey}</strong>
                <Badge variant="secondary" className="capitalize">{readable(item.verdict)}</Badge>
              </div>
              <p className="mt-1 text-muted-foreground">{item.actor || "unknown actor"} · {when(item.createdAt)}</p>
            </article>
          ))}
        </div>
      </details>

      <details className="rounded-lg border bg-background/60">
        <summary className="flex cursor-pointer list-none items-center justify-between gap-3 px-3 py-2.5">
          <span className="text-sm font-medium">Variant sweeps · {sweeps.length}</span>
          <Button size="sm" variant="outline" disabled={!canWrite} title={mutationTitle} onClick={(event) => { event.preventDefault(); openDialog("sweep"); }}><Plus />Create</Button>
        </summary>
        <div className="space-y-2 border-t p-3">
          {sweeps.length === 0 ? <EmptyRow>No variant sweeps recorded.</EmptyRow> : sweeps.map((sweep) => (
            <article key={sweep.id} className="flex flex-wrap items-start justify-between gap-3 rounded-md border p-3 text-xs">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2"><Badge variant="outline" className="capitalize">{readable(sweep.status)}</Badge><span className="font-mono text-muted-foreground">{sweep.id.slice(0, 8)}</span></div>
                <p className="mt-1 font-medium">{sweep.rootCause}</p>
                {sweep.findingId && <p className="mt-1 text-muted-foreground">Finding {sweep.findingId}</p>}
              </div>
              {!sweep.completedAt && (
                <Button size="sm" variant="ghost" disabled={!canWrite} title={mutationTitle} onClick={() => {
                  setSelectedSweep(sweep);
                  setCompletionStatus("completed");
                  setResultJSON('{\n  "searched_scope": [],\n  "methods": [],\n  "evidence": [],\n  "summary": ""\n}');
                  openDialog("complete");
                }}>Complete</Button>
              )}
            </article>
          ))}
        </div>
      </details>

      <details className="rounded-lg border bg-background/60">
        <summary className="cursor-pointer list-none px-3 py-2.5 text-sm font-medium">Submission outcomes</summary>
        <div className="space-y-3 border-t p-3">
          <div className="flex flex-wrap items-end gap-2">
            <Field label="Submission ID">
              <Input aria-label="Submission ID" className="w-80 font-mono" placeholder="UUID" value={submissionId} onChange={(event) => setSubmissionId(event.target.value)} />
            </Field>
            <Button variant="outline" size="sm" disabled={outcomeLoading} onClick={() => void refreshOutcomeHistory()}>{outcomeLoading ? "Loading…" : "Load history"}</Button>
            <Button size="sm" disabled={!canWrite} title={mutationTitle} onClick={() => { setSelectedEvent(null); setRationale(""); openDialog("outcome"); }}><Plus />Record outcome</Button>
          </div>
          <ErrorMessage>{outcomeError}</ErrorMessage>
          {outcomeEvents.length === 0 ? <EmptyRow>No outcome history loaded.</EmptyRow> : outcomeEvents.map((event) => (
            <article key={String(event.id)} className="flex flex-wrap items-start justify-between gap-3 rounded-md border p-3 text-xs">
              <div>
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant="outline" className="capitalize">{readable(event.outcome)}</Badge>
                  <span>Event {String(event.id)}</span>
                  {event.correctionOf > 0n && <span className="text-muted-foreground">corrects {String(event.correctionOf)}</span>}
                </div>
                <p className="mt-1 text-muted-foreground">{event.rationale || "No rationale"} · {event.actor || "unknown actor"} · {when(event.createdAt)}</p>
                {event.externalReference && <p className="mt-1 font-mono text-muted-foreground">{event.externalReference}</p>}
              </div>
              <Button size="sm" variant="ghost" disabled={!canWrite} title={mutationTitle} onClick={() => {
                setSelectedEvent(event);
                setOutcome(event.outcome);
                setExternalReference(event.externalReference);
                setRationale("");
                openDialog("correction");
              }}>Correct</Button>
            </article>
          ))}
        </div>
      </details>

      <FormDialog open={dialog === "dossier"} onOpenChange={(open) => setDialog(open ? "dossier" : null)} title={dossier ? "Amend research dossier" : "Create research dossier"} description="Save a new immutable dossier version for this revision." submitLabel={dossier ? "Amend dossier" : "Create dossier"} busy={busy} error={formError} onSubmit={submitDossier}>
        <JsonField label="Dossier content (JSON)" value={dossierContent} onChange={setDossierContent} rows={10} />
        <Field label="Change summary"><Input aria-label="Change summary" value={changeSummary} onChange={(event) => setChangeSummary(event.target.value)} /></Field>
      </FormDialog>

      <FormDialog open={dialog === "hypothesis"} onOpenChange={(open) => setDialog(open ? "hypothesis" : null)} title="Create hypothesis" description="Record a testable security invariant for this exact revision." submitLabel="Create hypothesis" busy={busy} error={formError} onSubmit={submitHypothesis}>
        <Field label="Hypothesis key"><Input aria-label="Hypothesis key" value={hypothesisKey} onChange={(event) => setHypothesisKey(event.target.value)} /></Field>
        <Field label="Title"><Input aria-label="Title" value={hypothesisTitle} onChange={(event) => setHypothesisTitle(event.target.value)} /></Field>
        <Field label="Invariant"><Textarea aria-label="Invariant" value={invariant} onChange={(event) => setInvariant(event.target.value)} /></Field>
        <JsonField label="Detail (JSON)" value={detailJSON} onChange={setDetailJSON} />
      </FormDialog>

      <FormDialog open={dialog === "transition"} onOpenChange={(open) => setDialog(open ? "transition" : null)} title={`Transition ${selectedHypothesis?.hypothesisKey ?? "hypothesis"}`} description="Record the next research state and its evidence-backed result." submitLabel="Save transition" busy={busy} error={formError} onSubmit={submitTransition}>
        <Field label="Status"><select aria-label="Status" className={selectClass} value={nextStatus} onChange={(event) => setNextStatus(event.target.value)}>{HYPOTHESIS_STATUSES.map((value) => <option key={value} value={value}>{readable(value)}</option>)}</select></Field>
        <Field label="Result"><select aria-label="Result" className={selectClass} value={result} onChange={(event) => setResult(event.target.value)}>{HYPOTHESIS_RESULTS.map((value) => <option key={value} value={value}>{readable(value)}</option>)}</select></Field>
        <Field label="Rationale"><Textarea aria-label="Rationale" value={rationale} onChange={(event) => setRationale(event.target.value)} /></Field>
        <JsonField label="Detail (JSON)" value={detailJSON} onChange={setDetailJSON} />
      </FormDialog>

      <FormDialog open={dialog === "coverage"} onOpenChange={(open) => setDialog(open ? "coverage" : null)} title="Record coverage" description="Persist one bounded invariant, actor, state, or transition result." submitLabel="Record coverage" busy={busy} error={formError} onSubmit={submitCoverage}>
        <Field label="Hypothesis"><select aria-label="Hypothesis" className={selectClass} value={coverageHypothesis} onChange={(event) => setCoverageHypothesis(event.target.value)}><option value="">None</option>{hypotheses.map((value) => <option key={value.id} value={value.id}>{value.hypothesisKey} · {value.title}</option>)}</select></Field>
        <Field label="Dimension"><select aria-label="Dimension" className={selectClass} value={dimension} onChange={(event) => setDimension(event.target.value)}>{COVERAGE_DIMENSIONS.map((value) => <option key={value}>{readable(value)}</option>)}</select></Field>
        <Field label="Subject"><Input aria-label="Subject" value={subjectKey} onChange={(event) => setSubjectKey(event.target.value)} /></Field>
        <Field label="Verdict"><select aria-label="Verdict" className={selectClass} value={verdict} onChange={(event) => setVerdict(event.target.value)}>{COVERAGE_VERDICTS.map((value) => <option key={value}>{readable(value)}</option>)}</select></Field>
        <JsonField label="Bounds (JSON)" value={boundsJSON} onChange={setBoundsJSON} />
        <JsonField label="Evidence (JSON)" value={evidenceJSON} onChange={setEvidenceJSON} />
      </FormDialog>

      <FormDialog open={dialog === "sweep"} onOpenChange={(open) => setDialog(open ? "sweep" : null)} title="Create variant sweep" description="Bind a root cause to an explicit search scope." submitLabel="Create sweep" busy={busy} error={formError} onSubmit={submitSweep}>
        <Field label="Finding ID (optional)"><Input aria-label="Finding ID (optional)" className="font-mono" value={findingId} onChange={(event) => setFindingId(event.target.value)} /></Field>
        <Field label="Root hypothesis"><select aria-label="Root hypothesis" className={selectClass} value={rootHypothesisId} onChange={(event) => setRootHypothesisId(event.target.value)}><option value="">None</option>{hypotheses.map((value) => <option key={value.id} value={value.id}>{value.hypothesisKey} · {value.title}</option>)}</select></Field>
        <Field label="Root cause"><Textarea aria-label="Root cause" value={rootCause} onChange={(event) => setRootCause(event.target.value)} /></Field>
        <Field label="Initial status"><select aria-label="Initial status" className={selectClass} value={sweepStatus} onChange={(event) => setSweepStatus(event.target.value)}>{SWEEP_STATUSES.map((value) => <option key={value}>{value}</option>)}</select></Field>
        <JsonField label="Sweep scope (JSON)" value={sweepScopeJSON} onChange={setSweepScopeJSON} />
      </FormDialog>

      <FormDialog open={dialog === "complete"} onOpenChange={(open) => setDialog(open ? "complete" : null)} title="Complete variant sweep" description="Record the bounded search result and evidence." submitLabel="Complete sweep" busy={busy} error={formError} onSubmit={submitCompletion}>
        <Field label="Completion status"><select aria-label="Completion status" className={selectClass} value={completionStatus} onChange={(event) => setCompletionStatus(event.target.value)}>{SWEEP_COMPLETION_STATUSES.map((value) => <option key={value}>{value}</option>)}</select></Field>
        <JsonField label="Result (JSON)" value={resultJSON} onChange={setResultJSON} rows={9} />
      </FormDialog>

      <FormDialog open={dialog === "outcome" || dialog === "correction"} onOpenChange={(open) => setDialog(open ? (selectedEvent ? "correction" : "outcome") : null)} title={dialog === "correction" ? `Correct outcome event ${String(selectedEvent?.id ?? "")}` : "Record submission outcome"} description="Persist external adjudication for the selected submission." submitLabel={dialog === "correction" ? "Record correction" : "Record outcome"} busy={busy} error={formError} onSubmit={submitOutcome}>
        <Field label="Outcome"><select aria-label="Outcome" className={selectClass} value={outcome} onChange={(event) => setOutcome(event.target.value)}>{SUBMISSION_OUTCOMES.map((value) => <option key={value}>{value}</option>)}</select></Field>
        <Field label="External reference"><Input aria-label="External reference" value={externalReference} onChange={(event) => setExternalReference(event.target.value)} /></Field>
        <Field label="Rationale"><Textarea aria-label="Rationale" value={rationale} onChange={(event) => setRationale(event.target.value)} /></Field>
      </FormDialog>
    </section>
  );
}
