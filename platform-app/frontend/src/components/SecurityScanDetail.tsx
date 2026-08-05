/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useMemo, useState } from "react";
import { useParams } from "react-router-dom";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { X } from "lucide-react";

import {
  Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import { ListSearchInput } from "@/components/ui/list-search";
import {
  DetailHeader, DetailSection, StatBar, Stat, FactList, Fact, FactLink,
} from "@/components/detail-page";
import {
  SEVERITIES, SeverityBadge, severityTone,
} from "@/components/SecurityScanList";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft } from "@/lib/status";
import type { SecurityFinding, SecurityScan } from "@/rpc/platform/service_pb";

const FINDING_STATUSES = [
  "open",
  "triaged",
  "confirmed",
  "false_positive",
  "fixed",
  "accepted_risk",
] as const;

function statusLabel(status: string): string {
  return status.replace(/_/g, " ");
}

function formatSeen(ts: Timestamp | undefined): string {
  if (!ts) return "—";
  return timestampDate(ts).toLocaleString();
}

function cweUrl(cwe: string): string {
  const id = cwe.replace(/^CWE-?/i, "");
  return `https://cwe.mitre.org/data/definitions/${id}.html`;
}

const filterSelectClass =
  "h-8 rounded-md border border-border/70 bg-background px-2 text-[12.5px] text-foreground capitalize focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60";

export function SecurityScanDetail() {
  const { namespace, runName } = useParams<{ namespace: string; runName: string }>();

  const [scan, setScan] = useState<SecurityScan | null>(null);
  const [summary, setSummary] = useState<Record<string, number>>({});
  const [findings, setFindings] = useState<SecurityFinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [findingsLoading, setFindingsLoading] = useState(true);
  const [error, setError] = useState("");
  const [actionError, setActionError] = useState<string | null>(null);

  const [severity, setSeverity] = useState("");
  const [status, setStatus] = useState("");
  const [category, setCategory] = useState("");
  const [search, setSearch] = useState("");

  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [statusSaving, setStatusSaving] = useState(false);

  const fetchScan = useCallback(async () => {
    if (!namespace || !runName) return;
    setLoading(true);
    setError("");
    try {
      const [scanResp, summaryResp] = await Promise.all([
        client.getSecurityScan({ namespace, runName }),
        client.getSecurityFindingSummary({ namespace, runName }),
      ]);
      setScan(scanResp);
      setSummary(summaryResp.counts);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to load security scan");
    } finally {
      setLoading(false);
    }
  }, [namespace, runName]);

  const fetchFindings = useCallback(async () => {
    if (!namespace || !runName) return;
    setFindingsLoading(true);
    try {
      const resp = await client.listSecurityFindings({
        namespace,
        runName,
        severity,
        status,
        category,
        search,
      });
      setFindings(resp.findings);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : "Failed to load security findings");
    } finally {
      setFindingsLoading(false);
    }
  }, [namespace, runName, severity, status, category, search]);

  useEffect(() => {
    void fetchScan();
  }, [fetchScan]);

  useEffect(() => {
    void fetchFindings();
  }, [fetchFindings]);

  const categories = useMemo(() => {
    const set = new Set<string>();
    for (const finding of findings) {
      if (finding.category) set.add(finding.category);
    }
    if (category) set.add(category);
    return [...set].sort();
  }, [findings, category]);

  const selected = findings.find((f) => f.id === selectedId) ?? null;

  async function changeStatus(finding: SecurityFinding, nextStatus: string) {
    setActionError(null);
    setStatusSaving(true);
    const previous = findings;
    // Optimistic update; the authoritative refresh follows below.
    setFindings((current) =>
      current.map((f) => (f.id === finding.id ? { ...f, status: nextStatus } : f)),
    );
    try {
      await client.updateSecurityFindingStatus({ id: finding.id, status: nextStatus, note: "" });
      await Promise.all([fetchFindings(), fetchScan()]);
    } catch (e: unknown) {
      setFindings(previous);
      setActionError(e instanceof Error ? e.message : "Failed to update finding status");
    } finally {
      setStatusSaving(false);
    }
  }

  return (
    <ListState
      loading={loading}
      error={error}
      empty={!scan}
      skeleton={<ListRowSkeleton rows={4} />}
      emptyTitle="Security scan not found"
      emptyDescription="This scan may have been removed or you may not have access."
    >
      {scan && (
        <div className="space-y-7">
          <DetailHeader
            parentLabel="Security Scans"
            parentTo="/security"
            title={scan.runName}
            meta={
              <Badge variant="outline" className="capitalize">
                {scan.status || "unknown"}
              </Badge>
            }
            subtitle={
              <span className="font-mono text-[12.5px] text-muted-foreground">
                {scan.repository}
                {scan.revision && ` @ ${scan.revision.slice(0, 12)}`}
              </span>
            }
          />

          <StatBar>
            <Stat label="Total" value={summary["total"] ?? 0} />
            <Stat label="Open" value={summary["open"] ?? 0} />
            {SEVERITIES.map((s) => (
              <Stat
                key={s}
                label={s}
                value={
                  <span className={cn(toneSoft[severityTone(s)], "rounded-md px-2 py-0.5")}>
                    {summary[s] ?? 0}
                  </span>
                }
                mono={false}
              />
            ))}
          </StatBar>

          {scan.summary && (
            <DetailSection title="Scan Summary">
              <p className="max-w-[90ch] whitespace-pre-wrap text-[13px] leading-relaxed text-muted-foreground">
                {scan.summary}
              </p>
            </DetailSection>
          )}

          {actionError && (
            <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/5 px-3 py-2 text-[12.5px]">
              {actionError}
            </div>
          )}

          <DetailSection
            title="Findings"
            aside={
              <div className="flex flex-wrap items-center gap-2">
                <ListSearchInput
                  value={search}
                  onChange={setSearch}
                  placeholder="Search findings…"
                />
                <select
                  aria-label="Filter by severity"
                  className={filterSelectClass}
                  value={severity}
                  onChange={(e) => setSeverity(e.target.value)}
                >
                  <option value="">All severities</option>
                  {SEVERITIES.map((s) => (
                    <option key={s} value={s}>{s}</option>
                  ))}
                </select>
                <select
                  aria-label="Filter by status"
                  className={filterSelectClass}
                  value={status}
                  onChange={(e) => setStatus(e.target.value)}
                >
                  <option value="">All statuses</option>
                  {FINDING_STATUSES.map((s) => (
                    <option key={s} value={s}>{statusLabel(s)}</option>
                  ))}
                </select>
                <select
                  aria-label="Filter by category"
                  className={filterSelectClass}
                  value={category}
                  onChange={(e) => setCategory(e.target.value)}
                >
                  <option value="">All categories</option>
                  {categories.map((c) => (
                    <option key={c} value={c}>{c}</option>
                  ))}
                </select>
              </div>
            }
          >
            <div className={cn("grid gap-4", selected && "lg:grid-cols-[minmax(0,1fr)_minmax(320px,420px)]")}>
              <ListState
                loading={findingsLoading}
                empty={!findings.length}
                skeleton={<ListRowSkeleton rows={4} />}
                emptyTitle="No findings"
                emptyDescription={
                  severity || status || category || search
                    ? "No findings match the current filters."
                    : "This scan reported no findings."
                }
              >
                <Table>
                  <TableCaption className="sr-only">Security findings</TableCaption>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Title</TableHead>
                      <TableHead>Severity</TableHead>
                      <TableHead>Category</TableHead>
                      <TableHead>Status</TableHead>
                      <TableHead className="text-right">Score</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {findings.map((finding) => (
                      <TableRow
                        key={finding.id}
                        data-state={finding.id === selectedId ? "selected" : undefined}
                        className="cursor-pointer"
                        onClick={() => setSelectedId(finding.id)}
                      >
                        <TableCell>
                          <button
                            type="button"
                            className="text-left font-medium text-primary hover:underline"
                            onClick={() => setSelectedId(finding.id)}
                          >
                            {finding.title}
                          </button>
                          <div className="mt-0.5 truncate font-mono text-[11.5px] text-muted-foreground">
                            {finding.filePath}
                            {finding.startLine > 0 && `:${finding.startLine}`}
                          </div>
                        </TableCell>
                        <TableCell>
                          <SeverityBadge severity={finding.severity} />
                        </TableCell>
                        <TableCell className="text-sm text-muted-foreground">
                          {finding.category || "—"}
                        </TableCell>
                        <TableCell className="text-sm capitalize text-muted-foreground">
                          {statusLabel(finding.status)}
                        </TableCell>
                        <TableCell className="text-right font-mono tabular-nums">
                          {finding.score.toFixed(1)}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </ListState>

              {selected && (
                <aside
                  aria-label="Finding details"
                  className="surface-card h-fit space-y-4 rounded-xl border border-border/60 bg-muted/10 p-4"
                >
                  <div className="flex items-start justify-between gap-2">
                    <div className="min-w-0 space-y-1">
                      <h3 className="text-[14px] font-semibold leading-snug">{selected.title}</h3>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <SeverityBadge severity={selected.severity} />
                        {selected.category && (
                          <Badge variant="outline" className="text-[11px]">{selected.category}</Badge>
                        )}
                      </div>
                    </div>
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label="Close finding details"
                      onClick={() => setSelectedId(null)}
                    >
                      <X className="size-4" />
                    </Button>
                  </div>

                  <div className="flex items-center gap-2">
                    <label
                      htmlFor="finding-status"
                      className="text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70"
                    >
                      Status
                    </label>
                    <select
                      id="finding-status"
                      className={filterSelectClass}
                      value={selected.status}
                      disabled={statusSaving}
                      onChange={(e) => void changeStatus(selected, e.target.value)}
                    >
                      {FINDING_STATUSES.map((s) => (
                        <option key={s} value={s}>{statusLabel(s)}</option>
                      ))}
                    </select>
                  </div>

                  {selected.description && (
                    <FindingText label="Description" text={selected.description} />
                  )}
                  {selected.impact && <FindingText label="Impact" text={selected.impact} />}
                  {selected.attackVector && (
                    <FindingText label="Attack Vector" text={selected.attackVector} />
                  )}
                  {selected.remediation && (
                    <FindingText label="Remediation" text={selected.remediation} />
                  )}

                  <FactList>
                    <Fact
                      label="Location"
                      mono
                      value={
                        selected.filePath
                          ? `${selected.filePath}${selected.startLine > 0 ? `:${selected.startLine}${selected.endLine > selected.startLine ? `-${selected.endLine}` : ""}` : ""}`
                          : "—"
                      }
                    />
                    {selected.symbol && <Fact label="Symbol" mono value={selected.symbol} />}
                    <Fact label="Score" mono value={selected.score.toFixed(1)} />
                    <Fact label="Confidence" value={selected.confidence || "—"} />
                    <Fact label="Source Agent" mono value={selected.sourceAgent || "—"} />
                    <Fact label="Occurrences" mono value={String(selected.occurrences)} />
                    <Fact label="First Seen" value={formatSeen(selected.firstSeenAt)} />
                    <Fact label="Last Seen" value={formatSeen(selected.lastSeenAt)} />
                    {selected.cwe.length > 0 && (
                      <Fact
                        label="CWE"
                        value={
                          <span className="flex flex-wrap gap-x-3 gap-y-1">
                            {selected.cwe.map((cwe) => (
                              <FactLink key={cwe} href={cweUrl(cwe)}>{cwe}</FactLink>
                            ))}
                          </span>
                        }
                      />
                    )}
                    {selected.references.length > 0 && (
                      <Fact
                        label="References"
                        value={
                          <span className="flex flex-col gap-1">
                            {selected.references.map((ref) => (
                              <FactLink key={ref} href={ref}>{ref}</FactLink>
                            ))}
                          </span>
                        }
                      />
                    )}
                  </FactList>

                  {selected.raw && (
                    <details className="text-[12px]">
                      <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
                        Evidence
                      </summary>
                      <pre className="mt-2 max-h-64 overflow-auto rounded-md border border-border/60 bg-muted/30 p-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-all">
                        {selected.raw}
                      </pre>
                    </details>
                  )}
                </aside>
              )}
            </div>
          </DetailSection>
        </div>
      )}
    </ListState>
  );
}

function FindingText({ label, text }: { label: string; text: string }) {
  return (
    <div className="space-y-1">
      <h4 className="text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
        {label}
      </h4>
      <p className="whitespace-pre-wrap text-[12.5px] leading-relaxed text-foreground/90">{text}</p>
    </div>
  );
}
