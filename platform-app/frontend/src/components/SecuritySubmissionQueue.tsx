/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { Download, Inbox, MoreHorizontal, RefreshCw, Send, Stamp } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { TableRowSkeleton } from "@/components/ui/list-state";
import { ResourceListPage } from "@/components/list-page";
import { SecurityNav } from "@/components/SecurityNav";
import { SeverityBadge } from "@/components/SecurityScanList";
import { client } from "@/lib/client";
import { downloadBlob } from "@/lib/download";
import { formatAge } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useNow } from "@/hooks/useNow";
import type {
  SecuritySubmissionPrecision,
  SecuritySubmissionPrecisionRollup,
  SecuritySubmissionQueueItem,
} from "@/rpc/platform/service_pb";

const SUBMISSION_OUTCOMES = ["accepted", "duplicate", "informative", "rejected", "resolved"];
const selectClass = "h-8 rounded-lg border border-input bg-background px-2.5 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring/50";

type QueueFilter = "ready" | "submitted" | "all";

/** Rows a human can still hand to a program. */
export function awaitingHandoff(item: SecuritySubmissionQueueItem): boolean {
  return item.submissionId !== "" && ["candidate", "reserved", "packaged"].includes(item.submissionStatus);
}

/** Rows a human already filed; they stay listed (greyed) for outcome recording. */
export function handedOff(item: SecuritySubmissionQueueItem): boolean {
  return item.submissionStatus === "submitted" || item.submissionStatus === "resolved";
}

export function statusLabel(item: SecuritySubmissionQueueItem): string {
  switch (item.submissionStatus) {
    case "packaged": return "Ready to submit";
    case "submitted": return "Submitted";
    case "resolved": return "Resolved";
    case "reserved": return "Reserved (bundle ready)";
    case "candidate": return "Candidate (bundle ready)";
    default: return "Bundle only";
  }
}

export function acceptanceRate(precision: SecuritySubmissionPrecision | undefined): string {
  if (!precision) return "—";
  const adjudicated = Number(precision.accepted + precision.duplicate + precision.informative + precision.rejected);
  if (adjudicated === 0) return "—";
  return `${Math.round((Number(precision.accepted) / adjudicated) * 100)}%`;
}

function when(value: Timestamp | undefined): string {
  return value ? timestampDate(value).toLocaleString() : "—";
}

function ageOf(value: Timestamp | undefined, nowMs: number): string {
  return value ? formatAge(value.seconds, nowMs) : "—";
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      {children}
    </div>
  );
}

function PrecisionCell({ label, precision }: { label: string; precision: SecuritySubmissionPrecision | undefined }) {
  return (
    <div className="rounded-lg border bg-background/60 p-3 text-xs">
      <div className="flex items-center justify-between gap-2">
        <span className="truncate font-medium">{label}</span>
        <span className="tabular-nums text-muted-foreground" title="Accepted over adjudicated outcomes">acceptance {acceptanceRate(precision)}</span>
      </div>
      <dl className="mt-2 grid grid-cols-4 gap-2 tabular-nums text-muted-foreground">
        <div><dt>submitted</dt><dd className="text-foreground">{String(precision?.submitted ?? 0n)}</dd></div>
        <div><dt>accepted</dt><dd className="text-foreground">{String(precision?.accepted ?? 0n)}</dd></div>
        <div><dt>duplicate</dt><dd className="text-foreground">{String(precision?.duplicate ?? 0n)}</dd></div>
        <div><dt>rejected</dt><dd className="text-foreground">{String(precision?.rejected ?? 0n)}</dd></div>
      </dl>
    </div>
  );
}

export function SecuritySubmissionQueue() {
  const [items, setItems] = useState<SecuritySubmissionQueueItem[]>([]);
  const [rollup, setRollup] = useState<SecuritySubmissionPrecisionRollup | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<QueueFilter>("all");
  const [notice, setNotice] = useState("");
  const [busyId, setBusyId] = useState("");

  const [dialog, setDialog] = useState<"submit" | "outcome" | null>(null);
  const [selected, setSelected] = useState<SecuritySubmissionQueueItem | null>(null);
  const [program, setProgram] = useState("");
  const [externalReference, setExternalReference] = useState("");
  const [outcome, setOutcome] = useState(SUBMISSION_OUTCOMES[0]);
  const [rationale, setRationale] = useState("");
  const [formError, setFormError] = useState("");
  const [saving, setSaving] = useState(false);
  const nowMs = useNow();

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const [queue, precision] = await Promise.all([
        client.listSecuritySubmissionQueue({ namespace: "", limit: 200 }),
        client.getSecuritySubmissionPrecisionRollup({ namespace: "" }),
      ]);
      setItems(queue.items);
      setRollup(precision);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to load the submission queue");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const visible = useMemo(() => {
    const needle = query.trim().toLowerCase();
    return items.filter((item) => {
      if (filter === "ready" && !awaitingHandoff(item)) return false;
      if (filter === "submitted" && !handedOff(item)) return false;
      if (!needle) return true;
      return [item.title, item.scanName, item.program, item.externalReference, item.fingerprint, item.repository]
        .some((value) => value.toLowerCase().includes(needle));
    });
  }, [items, query, filter]);

  const readyCount = items.filter(awaitingHandoff).length;

  async function downloadBundle(item: SecuritySubmissionQueueItem) {
    setBusyId(item.findingId);
    setNotice("");
    try {
      const resp = await client.getSecurityFindingSubmissionBundle({ namespace: item.namespace, findingId: item.findingId });
      if (resp.status !== "ready" || resp.content.length === 0) {
        setNotice(resp.error || `Bundle for ${item.title} is ${resp.status || "unavailable"}.`);
        return;
      }
      downloadBlob(resp.filename || item.bundleFilename || `${item.fingerprint}-bounty-submission.zip`, resp.content, "application/zip");
      setNotice(resp.sha256 ? `Downloaded ${resp.filename}. SHA-256: ${resp.sha256}` : "Downloaded.");
    } catch (e: unknown) {
      setNotice(e instanceof Error ? e.message : "Failed to fetch the bounty bundle");
    } finally {
      setBusyId("");
    }
  }

  function openSubmit(item: SecuritySubmissionQueueItem) {
    setSelected(item);
    setProgram(item.program);
    setExternalReference(item.externalReference);
    setFormError("");
    setDialog("submit");
  }

  function openOutcome(item: SecuritySubmissionQueueItem) {
    setSelected(item);
    setOutcome(SUBMISSION_OUTCOMES[0]);
    setExternalReference(item.externalReference);
    setRationale("");
    setFormError("");
    setDialog("outcome");
  }

  async function submitHandoff(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected) return;
    if (!program.trim()) return setFormError("Program is required: where was the report filed?");
    setSaving(true);
    try {
      await client.markSecuritySubmissionSubmitted({
        namespace: selected.namespace,
        submissionId: selected.submissionId,
        program: program.trim(),
        externalReference: externalReference.trim(),
        idempotencyKey: crypto.randomUUID(),
      });
      setDialog(null);
      await load();
    } catch (e: unknown) {
      setFormError(e instanceof Error ? e.message : "Failed to mark the submission submitted");
    } finally {
      setSaving(false);
    }
  }

  async function submitOutcome(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!selected) return;
    if (!rationale.trim()) return setFormError("Rationale is required.");
    setSaving(true);
    try {
      await client.recordSecuritySubmissionOutcome({
        scope: { namespace: selected.namespace, targetKey: selected.targetKey, revision: selected.revision },
        submissionId: selected.submissionId,
        outcome,
        externalReference: externalReference.trim(),
        rationale: rationale.trim(),
        idempotencyKey: crypto.randomUUID(),
      });
      setDialog(null);
      await load();
    } catch (e: unknown) {
      setFormError(e instanceof Error ? e.message : "Failed to record the submission outcome");
    } finally {
      setSaving(false);
    }
  }

  const dialogOpen = dialog !== null;

  return (
    <ResourceListPage
      title="Submission queue"
      description="Bounty bundles agents packaged across every scan. Download a bundle, file it with the program, then mark it submitted so precision feedback counts real reports."
      query={query}
      onQuery={setQuery}
      searchPlaceholder="Search findings, scans, programs…"
      hideSearch={!items.length}
      loading={loading}
      error={error}
      onRetry={() => void load()}
      empty={!visible.length}
      skeleton={<TableRowSkeleton rows={4} />}
      emptyIcon={<Inbox className="size-6" />}
      emptyTitle={items.length ? "No bundles match this view" : "No bundles are ready to submit"}
      emptyDescription={items.length
        ? "Change the status filter or clear the search to see the rest of the queue."
        : "Bundles appear here once a confirmed or triaged finding has a ready bounty submission bundle."}
      actions={(
        <Button variant="outline" size="sm" disabled={loading} onClick={() => void load()}>
          <RefreshCw className={loading ? "animate-spin" : undefined} />
          Refresh
        </Button>
      )}
      nav={<SecurityNav counts={{ "/security/queue": loading ? undefined : readyCount }} />}
      toolbar={(
        <div className="space-y-4">
          <section aria-label="Submission precision" className="space-y-2 rounded-lg border border-border/70 bg-muted/10 p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h2 className="text-sm font-medium">Precision by program</h2>
              <span className="text-xs text-muted-foreground">
                Counts only reports a human marked submitted; packaged bundles never enter the denominator.
              </span>
            </div>
            {rollup && rollup.byProgram.length > 0 ? (
              <div className="grid gap-2 md:grid-cols-2 xl:grid-cols-3">
                {rollup.byProgram.map((group) => (
                  <PrecisionCell key={group.key} label={group.key || "(no program)"} precision={group.precision} />
                ))}
                {rollup.byProgram.length > 1 && <PrecisionCell label="All programs" precision={rollup.total} />}
              </div>
            ) : (
              <p className="rounded-md border border-dashed px-3 py-3 text-center text-xs text-muted-foreground">
                No reports have been marked submitted yet. Precision feedback starts after the first handoff is recorded.
              </p>
            )}
          </section>
          {items.length > 0 && (
            <div role="group" aria-label="Queue filters" className="flex flex-wrap items-center gap-2 text-xs">
              <label className="flex items-center gap-2">
                <span className="text-muted-foreground">Status</span>
                <select aria-label="Status filter" className={selectClass} value={filter} onChange={(event) => setFilter(event.target.value as QueueFilter)}>
                  <option value="all">All</option>
                  <option value="ready">Ready to submit</option>
                  <option value="submitted">Submitted</option>
                </select>
              </label>
              <span aria-live="polite" className="text-muted-foreground">{visible.length} of {items.length} bundles · {readyCount} ready to submit</span>
            </div>
          )}
          {notice && <p role="status" className="text-xs text-muted-foreground">{notice}</p>}
        </div>
      )}
    >
      <div className="overflow-x-auto rounded-md border">
        <table className="w-full text-xs">
          <thead className="bg-muted/40 text-left text-muted-foreground">
            <tr>
              <th scope="col" className="px-3 py-2 font-medium">Severity</th>
              <th scope="col" className="px-3 py-2 font-medium">Finding</th>
              <th scope="col" className="px-3 py-2 font-medium">Scan</th>
              <th scope="col" className="px-3 py-2 font-medium">Program</th>
              <th scope="col" className="px-3 py-2 font-medium">Bundle ready</th>
              <th scope="col" className="px-3 py-2 font-medium">Status</th>
              <th scope="col" className="px-3 py-2"><span className="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {visible.map((item) => {
              const filed = handedOff(item);
              return (
                <tr
                  key={item.findingId}
                  data-status={item.submissionStatus || "none"}
                  className={cn("border-t", filed && "bg-muted/20 text-muted-foreground")}
                >
                  <td className="px-3 py-2"><SeverityBadge severity={item.severity} className={filed ? "opacity-60" : undefined} /></td>
                  <td className="max-w-md px-3 py-2">
                    <Link
                      to={`/security/${item.namespace}/${item.runName}/findings/${item.findingId}`}
                      className={cn("block truncate font-medium hover:underline", !filed && "text-foreground")}
                    >
                      {item.title || item.fingerprint}
                    </Link>
                    <span className="block truncate font-mono text-[11px] text-muted-foreground">{item.fingerprint}</span>
                  </td>
                  <td className="px-3 py-2">
                    <Link to={`/security/${item.namespace}/${item.runName}`} className="hover:underline">{item.scanName}</Link>
                    <span className="block truncate text-[11px] text-muted-foreground">{item.repository}</span>
                  </td>
                  <td className="px-3 py-2">
                    {item.program ? (
                      <>
                        <span>{item.program}</span>
                        {item.externalReference && <span className="block truncate font-mono text-[11px] text-muted-foreground">{item.externalReference}</span>}
                      </>
                    ) : <span className="text-muted-foreground">—</span>}
                  </td>
                  <td className="px-3 py-2 tabular-nums" title={when(item.bundleReadyAt)}>{ageOf(item.bundleReadyAt, nowMs)} ago</td>
                  <td className="px-3 py-2">
                    <Badge variant={filed ? "secondary" : "outline"}>{statusLabel(item)}</Badge>
                    {item.latestOutcome && <span className="ml-1.5 capitalize text-muted-foreground">· {item.latestOutcome}</span>}
                    {filed && item.submittedBy && (
                      <span className="block text-[11px] text-muted-foreground">by {item.submittedBy} · {when(item.submittedAt)}</span>
                    )}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <DropdownMenu>
                      <DropdownMenuTrigger
                        render={<Button size="icon-sm" variant="ghost" aria-label={`Actions for ${item.title || item.fingerprint}`} disabled={busyId === item.findingId} />}
                      >
                        <MoreHorizontal />
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem onClick={() => void downloadBundle(item)}><Download />Download bundle</DropdownMenuItem>
                        <DropdownMenuItem disabled={!awaitingHandoff(item)} onClick={() => openSubmit(item)}><Send />Mark submitted</DropdownMenuItem>
                        <DropdownMenuItem disabled={!filed} onClick={() => openOutcome(item)}><Stamp />Record outcome</DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      <Dialog open={dialogOpen} onOpenChange={(open) => !saving && !open && setDialog(null)}>
        <DialogContent className="sm:max-w-lg">
          {dialog === "submit" ? (
            <>
              <DialogHeader>
                <DialogTitle>Mark submitted</DialogTitle>
                <DialogDescription>
                  Record that you filed “{selected?.title}” with a bug-bounty program. This is the only transition that counts toward submission precision.
                </DialogDescription>
              </DialogHeader>
              <form className="space-y-4" onSubmit={submitHandoff}>
                <Field label="Program">
                  <Input aria-label="Program" placeholder="e.g. Immunefi, HackerOne, vendor VDP" value={program} onChange={(event) => setProgram(event.target.value)} />
                </Field>
                <Field label="External reference">
                  <Input aria-label="External reference" placeholder="Report ID or URL (optional)" value={externalReference} onChange={(event) => setExternalReference(event.target.value)} />
                </Field>
                {formError && <p role="alert" className="text-[12px] text-destructive">{formError}</p>}
                <DialogFooter>
                  <Button type="button" variant="outline" disabled={saving} onClick={() => setDialog(null)}>Cancel</Button>
                  <Button type="submit" disabled={saving}>{saving ? "Saving…" : "Mark submitted"}</Button>
                </DialogFooter>
              </form>
            </>
          ) : (
            <>
              <DialogHeader>
                <DialogTitle>Record outcome</DialogTitle>
                <DialogDescription>Persist the program’s adjudication for “{selected?.title}”.</DialogDescription>
              </DialogHeader>
              <form className="space-y-4" onSubmit={submitOutcome}>
                <Field label="Outcome">
                  <select aria-label="Outcome" className={cn(selectClass, "w-full")} value={outcome} onChange={(event) => setOutcome(event.target.value)}>
                    {SUBMISSION_OUTCOMES.map((value) => <option key={value}>{value}</option>)}
                  </select>
                </Field>
                <Field label="External reference">
                  <Input aria-label="Outcome external reference" value={externalReference} onChange={(event) => setExternalReference(event.target.value)} />
                </Field>
                <Field label="Rationale">
                  <Textarea aria-label="Rationale" value={rationale} onChange={(event) => setRationale(event.target.value)} />
                </Field>
                {formError && <p role="alert" className="text-[12px] text-destructive">{formError}</p>}
                <DialogFooter>
                  <Button type="button" variant="outline" disabled={saving} onClick={() => setDialog(null)}>Cancel</Button>
                  <Button type="submit" disabled={saving}>{saving ? "Saving…" : "Record outcome"}</Button>
                </DialogFooter>
              </form>
            </>
          )}
        </DialogContent>
      </Dialog>
    </ResourceListPage>
  );
}
