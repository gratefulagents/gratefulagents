/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { Code } from "@connectrpc/connect";
import {
  AlertTriangle,
  ChevronLeft,
  ChevronRight,
  Copy,
  SquareArrowOutUpRight,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Textarea } from "@/components/ui/textarea";
import { toast } from "@/components/ui/toaster";
import {
  DetailHeader,
  DetailSection,
  Fact,
  FactLink,
  FactList,
} from "@/components/detail-page";
import { MarkdownViewer } from "@/components/MarkdownViewer";
import { SeverityBadge } from "@/components/SecurityScanList";
import {
  FINDING_STATUSES,
  formatSeen,
  statusLabel,
} from "@/components/SecurityScanDetail";
import { BaselineBadge, ExpiryBadge, SuppressedBadge } from "@/components/security-baseline";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { client } from "@/lib/client";
import { connectCodeOf, describeRpcError } from "@/lib/rpc-errors";
import { cn } from "@/lib/utils";
import type {
  SecurityFinding,
  SecurityFindingEvent,
  SecurityScan,
} from "@/rpc/platform/service_pb";

const UNSAVED_COMMENT_WARNING =
  "You have an unsaved comment. Leave without posting it?";

const MAX_COMMENT_LEN = 10000;

const filterSelectClass =
  "h-8 rounded-md border border-border/70 bg-background px-2 text-[12.5px] text-foreground capitalize focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60";

/** Only canonical CWE identifiers become links to cwe.mitre.org. */
export function cweLinkUrl(cwe: string): string | null {
  const normalized = cwe.trim().toUpperCase();
  if (!/^CWE-\d+$/.test(normalized)) return null;
  return `https://cwe.mitre.org/data/definitions/${normalized.slice("CWE-".length)}.html`;
}

/** References render as links only for well-formed http(s) URLs. */
export function isHttpUrl(ref: string): boolean {
  try {
    const url = new URL(ref);
    return url.protocol === "http:" || url.protocol === "https:";
  } catch {
    return false;
  }
}

const GITHUB_REPO_RE =
  /^(?:https?:\/\/)?(?:www\.)?github\.com\/([A-Za-z0-9_.-]+)\/([A-Za-z0-9_.-]+?)(?:\.git)?\/?$/;

/**
 * A source link is only fabricated when the repository is unambiguously a
 * github.com repo URL and a revision is pinned; anything else stays text.
 */
export function githubBlobUrl(
  repository: string,
  revision: string,
  filePath: string,
  startLine: number,
  endLine: number,
): string | null {
  if (!repository || !revision || !filePath) return null;
  const match = GITHUB_REPO_RE.exec(repository.trim());
  if (!match) return null;
  const path = filePath
    .split("/")
    .filter((part) => part !== "")
    .map(encodeURIComponent)
    .join("/");
  if (!path) return null;
  let url = `https://github.com/${match[1]}/${match[2]}/blob/${encodeURIComponent(revision)}/${path}`;
  if (startLine > 0) {
    url += `#L${startLine}`;
    if (endLine > startLine) url += `-L${endLine}`;
  }
  return url;
}

interface EvidenceEntry {
  filePath: string;
  startLine: number;
  endLine: number;
  snippet: string;
  note: string;
}

/**
 * Extracts the evidence citations and tags from the raw agent-reported
 * finding JSON. The raw blob is model output: parse defensively and treat
 * every field as untrusted plain text.
 */
export function parseRawFinding(raw: string): {
  evidence: EvidenceEntry[];
  tags: string[];
} {
  const out: { evidence: EvidenceEntry[]; tags: string[] } = { evidence: [], tags: [] };
  if (!raw) return out;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return out;
  }
  if (typeof parsed !== "object" || parsed === null) return out;
  const record = parsed as Record<string, unknown>;
  if (Array.isArray(record.tags)) {
    out.tags = record.tags.filter((t): t is string => typeof t === "string" && t !== "");
  }
  if (Array.isArray(record.evidence)) {
    for (const entry of record.evidence) {
      if (typeof entry !== "object" || entry === null) continue;
      const e = entry as Record<string, unknown>;
      out.evidence.push({
        filePath: typeof e.file_path === "string" ? e.file_path : "",
        startLine: typeof e.start_line === "number" ? e.start_line : 0,
        endLine: typeof e.end_line === "number" ? e.end_line : 0,
        snippet: typeof e.snippet === "string" ? e.snippet : "",
        note: typeof e.note === "string" ? e.note : "",
      });
    }
  }
  return out;
}

type LoadFailure = "not-found" | "forbidden" | "unsupported" | "error";

function classifyLoadError(err: unknown): LoadFailure {
  switch (connectCodeOf(err)) {
    case Code.NotFound:
      return "not-found";
    case Code.PermissionDenied:
      return "forbidden";
    case Code.FailedPrecondition:
      return "unsupported";
    default:
      return "error";
  }
}

const FAILURE_COPY: Record<LoadFailure, { title: string; description: string }> = {
  "not-found": {
    title: "Finding not found",
    description:
      "This finding does not exist under this scan. It may have been deleted with its scan, or the link may be stale.",
  },
  forbidden: {
    title: "You don't have access to this finding",
    description:
      "This finding lives in a namespace you are not authorized to view. Ask a namespace member to share it or switch accounts.",
  },
  unsupported: {
    title: "Security findings are unavailable",
    description:
      "The configured state store does not support security findings. Configure the Postgres state store to browse findings.",
  },
  error: {
    title: "Failed to load finding",
    description: "Something went wrong while loading this finding.",
  },
};

function statusChangeSummary(event: SecurityFindingEvent): string | null {
  if (event.eventType !== "status_changed" || !event.detail) return null;
  try {
    const detail = JSON.parse(event.detail) as Record<string, unknown>;
    const from = typeof detail.from === "string" ? detail.from : "";
    const to = typeof detail.to === "string" ? detail.to : "";
    if (!from && !to) return null;
    return `${statusLabel(from || "?")} → ${statusLabel(to || "?")}`;
  } catch {
    return null;
  }
}

export function SecurityFindingDetail() {
  const { namespace, runName, findingId } = useParams<{
    namespace: string;
    runName: string;
    findingId: string;
  }>();
  const [searchParams] = useSearchParams();

  const [scan, setScan] = useState<SecurityScan | null>(null);
  const [finding, setFinding] = useState<SecurityFinding | null>(null);
  const [events, setEvents] = useState<SecurityFindingEvent[]>([]);
  const [siblingIds, setSiblingIds] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [failure, setFailure] = useState<LoadFailure | null>(null);
  const [failureMessage, setFailureMessage] = useState("");

  const [statusDraft, setStatusDraft] = useState("");
  const [statusNote, setStatusNote] = useState("");
  const [statusSaving, setStatusSaving] = useState(false);
  const [expiryDraft, setExpiryDraft] = useState("");

  const [assigneeDraft, setAssigneeDraft] = useState<string | null>(null);
  const [assigneeSaving, setAssigneeSaving] = useState(false);

  const [ticketUrlDraft, setTicketUrlDraft] = useState("");
  const [ticketProviderDraft, setTicketProviderDraft] = useState("github");
  const [ticketRepoDraft, setTicketRepoDraft] = useState("");
  const [ticketBusy, setTicketBusy] = useState(false);

  const [comment, setComment] = useState("");
  const [commentSaving, setCommentSaving] = useState(false);
  const commentRef = useRef(comment);
  useEffect(() => {
    commentRef.current = comment;
  }, [comment]);

  // Filter context carried over from the scan table keeps prev/next
  // deterministic and lets the back link restore the same view.
  const severity = searchParams.get("severity") ?? "";
  const status = searchParams.get("status") ?? "actionable";
  const category = searchParams.get("category") ?? "";
  const search = searchParams.get("q") ?? "";
  const filterQuery = searchParams.toString();

  const scanHref = `/security/${namespace}/${runName}${filterQuery ? `?${filterQuery}` : ""}`;
  const findingHref = useCallback(
    (id: string) =>
      `/security/${namespace}/${runName}/findings/${id}${filterQuery ? `?${filterQuery}` : ""}`,
    [namespace, runName, filterQuery],
  );

  const fetchAll = useCallback(async () => {
    if (!namespace || !runName || !findingId) return;
    setLoading(true);
    setFailure(null);
    try {
      // The scan is loaded first so the finding lookup can assert scan
      // ownership server-side (scanName must match the finding's scan).
      const scanResp = await client.getSecurityScan({ namespace, runName });
      const [findingResp, siblingsResp] = await Promise.all([
        client.getSecurityFinding({
          id: findingId,
          namespace,
          scanName: scanResp.scanName,
        }),
        client.listSecurityFindings({
          namespace,
          runName,
          severity,
          status: status === "all" ? "" : status,
          category,
          search,
        }),
      ]);
      setScan(scanResp);
      setFinding(findingResp.finding ?? null);
      setEvents(findingResp.events);
      setSiblingIds(siblingsResp.findings.map((f) => f.id));
      if (!findingResp.finding) {
        setFailure("not-found");
      }
    } catch (e: unknown) {
      setFailure(classifyLoadError(e));
      setFailureMessage(describeRpcError(e, "load the finding"));
    } finally {
      setLoading(false);
    }
  }, [namespace, runName, findingId, severity, status, category, search]);

  useEffect(() => {
    void fetchAll();
  }, [fetchAll]);

  useEffect(() => {
    setStatusDraft("");
    setStatusNote("");
    setExpiryDraft("");
    setAssigneeDraft(null);
    setTicketUrlDraft("");
    setTicketRepoDraft("");
  }, [findingId]);

  // Warn before the tab closes while a comment draft would be lost.
  useEffect(() => {
    const onBeforeUnload = (e: BeforeUnloadEvent) => {
      if (commentRef.current.trim()) {
        e.preventDefault();
        e.returnValue = "";
      }
    };
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, []);

  // Warn before any in-app link on this page (back, prev/next, agent run,
  // duplicate-of) navigates away from a non-empty comment draft.
  const guardLinkClicks = useCallback((e: React.MouseEvent) => {
    if (!commentRef.current.trim()) return;
    const anchor = (e.target as HTMLElement).closest("a");
    if (!anchor) return;
    if (!window.confirm(UNSAVED_COMMENT_WARNING)) {
      e.preventDefault();
      e.stopPropagation();
    }
  }, []);

  const refreshEvents = useCallback(async () => {
    if (!namespace || !findingId || !scan) return;
    try {
      const resp = await client.listSecurityFindingEvents({
        id: findingId,
        namespace,
        scanName: scan.scanName,
      });
      setEvents(resp.events);
    } catch {
      // Non-fatal: the trail refreshes on the next full load.
    }
  }, [namespace, findingId, scan]);

  async function applyStatus() {
    if (!finding || !statusDraft || statusDraft === finding.status) return;
    if (statusDraft === "accepted_risk" && expiryDraft) {
      const at = new Date(expiryDraft);
      if (Number.isNaN(at.getTime()) || at.getTime() <= Date.now()) {
        toast.error("The accepted-risk expiry must be in the future");
        return;
      }
    }
    setStatusSaving(true);
    try {
      const updated = await client.updateSecurityFindingStatus({
        id: finding.id,
        status: statusDraft,
        note: statusNote.trim(),
        namespace: namespace ?? "",
        acceptedRiskExpiresAt:
          statusDraft === "accepted_risk" && expiryDraft
            ? timestampFromDate(new Date(expiryDraft))
            : undefined,
      });
      setFinding(updated);
      setStatusDraft("");
      setStatusNote("");
      setExpiryDraft("");
      toast.success(`Status set to ${statusLabel(updated.status)}`);
      void refreshEvents();
    } catch (e: unknown) {
      // The finding may have been re-triaged or deleted elsewhere: reload
      // the authoritative state instead of keeping a stale view.
      toast.error(describeRpcError(e, "update the finding status"));
      void fetchAll();
    } finally {
      setStatusSaving(false);
    }
  }

  async function applyAssignee() {
    if (!finding || assigneeDraft === null) return;
    setAssigneeSaving(true);
    try {
      const updated = await client.updateSecurityFindingAssignee({
        id: finding.id,
        namespace: namespace ?? "",
        assignee: assigneeDraft.trim(),
      });
      setFinding(updated);
      setAssigneeDraft(null);
      toast.success(updated.assignee ? `Assigned to ${updated.assignee}` : "Assignee cleared");
      void refreshEvents();
    } catch (e: unknown) {
      toast.error(describeRpcError(e, "update the assignee"));
    } finally {
      setAssigneeSaving(false);
    }
  }

  async function linkTicket() {
    if (!finding || !ticketUrlDraft.trim()) return;
    setTicketBusy(true);
    try {
      const updated = await client.updateSecurityFindingTicket({
        id: finding.id,
        namespace: namespace ?? "",
        ticketUrl: ticketUrlDraft.trim(),
        ticketProvider: ticketProviderDraft,
      });
      setFinding(updated);
      setTicketUrlDraft("");
      toast.success("Ticket linked");
      void refreshEvents();
    } catch (e: unknown) {
      toast.error(describeRpcError(e, "link the ticket"));
    } finally {
      setTicketBusy(false);
    }
  }

  async function unlinkTicket() {
    if (!finding) return;
    setTicketBusy(true);
    try {
      const updated = await client.updateSecurityFindingTicket({
        id: finding.id,
        namespace: namespace ?? "",
        ticketUrl: "",
      });
      setFinding(updated);
      toast.success("Ticket unlinked");
      void refreshEvents();
    } catch (e: unknown) {
      toast.error(describeRpcError(e, "unlink the ticket"));
    } finally {
      setTicketBusy(false);
    }
  }

  async function createGitHubTicket() {
    if (!finding || !ticketRepoDraft.trim()) return;
    setTicketBusy(true);
    try {
      const updated = await client.createSecurityFindingTicket({
        id: finding.id,
        namespace: namespace ?? "",
        provider: "github",
        repositoryRef: ticketRepoDraft.trim(),
      });
      setFinding(updated);
      setTicketRepoDraft("");
      toast.success("GitHub issue created and linked");
      void refreshEvents();
    } catch (e: unknown) {
      toast.error(describeRpcError(e, "create the GitHub issue"));
    } finally {
      setTicketBusy(false);
    }
  }

  async function submitComment() {
    if (!finding || !comment.trim()) return;
    setCommentSaving(true);
    try {
      await client.addSecurityFindingComment({
        id: finding.id,
        namespace: namespace ?? "",
        scanName: scan?.scanName ?? "",
        body: comment.trim(),
      });
      setComment("");
      toast.success("Comment added");
      void refreshEvents();
    } catch (e: unknown) {
      toast.error(describeRpcError(e, "add the comment"));
    } finally {
      setCommentSaving(false);
    }
  }

  function copyEvidence() {
    if (!finding?.raw) return;
    void navigator.clipboard
      .writeText(finding.raw)
      .then(() => toast.success("Evidence copied"))
      .catch(() => toast.error("Couldn't copy to the clipboard"));
  }

  if (loading) {
    return (
      <div aria-label="Loading finding" className="space-y-4">
        <Skeleton className="h-7 w-2/5" />
        <Skeleton className="h-4 w-3/5" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  if (failure || !finding) {
    const copy = FAILURE_COPY[failure ?? "not-found"];
    return (
      <div className="space-y-3">
        <Link
          to={scanHref}
          className="inline-flex items-center gap-0.5 text-[12px] text-muted-foreground hover:text-foreground"
        >
          <ChevronLeft className="size-3" />
          Back to scan
        </Link>
        <div role="alert" className="rounded-xl border border-border/70 bg-muted/20 px-4 py-6">
          <h1 className="text-[15px] font-semibold">{copy.title}</h1>
          <p className="mt-1 max-w-[70ch] text-[13px] text-muted-foreground">{copy.description}</p>
          {failure === "error" && failureMessage && (
            <p className="mt-2 text-[12.5px] text-muted-foreground">{failureMessage}</p>
          )}
          {failure === "error" && (
            <Button variant="outline" size="sm" className="mt-3" onClick={() => void fetchAll()}>
              Retry
            </Button>
          )}
        </div>
      </div>
    );
  }

  const { evidence, tags } = parseRawFinding(finding.raw);
  const index = siblingIds.indexOf(finding.id);
  const prevId = index > 0 ? siblingIds[index - 1] : null;
  const nextId = index >= 0 && index < siblingIds.length - 1 ? siblingIds[index + 1] : null;
  const location = finding.filePath
    ? `${finding.filePath}${finding.startLine > 0 ? `:${finding.startLine}${finding.endLine > finding.startLine ? `-${finding.endLine}` : ""}` : ""}`
    : "";
  const sourceUrl = githubBlobUrl(
    finding.repository,
    finding.revision,
    finding.filePath,
    finding.startLine,
    finding.endLine,
  );

  return (
    <div className="space-y-7" onClickCapture={guardLinkClicks}>
      <DetailHeader
        parentLabel={`Scan ${runName ?? ""}`}
        parentTo={scanHref}
        title={finding.title}
        meta={
          <>
            <SeverityBadge severity={finding.severity} />
            <Badge variant="outline" className="capitalize">
              {statusLabel(finding.status)}
            </Badge>
            <BaselineBadge state={finding.baselineState} />
            {finding.status === "accepted_risk" && (
              <ExpiryBadge ts={finding.acceptedRiskExpiresAt} />
            )}
            <SuppressedBadge finding={finding} />
          </>
        }
        subtitle={
          location ? (
            <span className="font-mono text-[12.5px] text-muted-foreground">{location}</span>
          ) : undefined
        }
        actions={
          <>
            {scan && (
              <Button
                variant="outline"
                size="sm"
                nativeButton={false}
                render={<Link to={`/runs/${scan.namespace}/${scan.runName}`} />}
              >
                <SquareArrowOutUpRight />
                Agent run
              </Button>
            )}
            <nav aria-label="Finding navigation" className="flex items-center gap-1">
              <Button
                variant="outline"
                size="sm"
                aria-label="Previous finding"
                disabled={!prevId}
                nativeButton={!prevId}
                render={prevId ? <Link to={findingHref(prevId)} /> : undefined}
              >
                <ChevronLeft />
                Prev
              </Button>
              {index >= 0 && (
                <span className="px-1 text-[12px] tabular-nums text-muted-foreground">
                  {index + 1} of {siblingIds.length}
                </span>
              )}
              <Button
                variant="outline"
                size="sm"
                aria-label="Next finding"
                disabled={!nextId}
                nativeButton={!nextId}
                render={nextId ? <Link to={findingHref(nextId)} /> : undefined}
              >
                Next
                <ChevronRight />
              </Button>
            </nav>
          </>
        }
      />

      {finding.suppressedBy && (
        <section
          aria-label="Suppression"
          data-testid="suppression-details"
          className="space-y-1 rounded-lg border border-violet-500/40 bg-violet-500/10 px-3 py-2.5 text-[12.5px]"
        >
          <p className="font-medium">
            Suppressed by policy rule <span className="font-mono">{finding.suppressedBy}</span>
          </p>
          <p className="text-muted-foreground">
            {finding.suppressedReason && (
              <>Reason: {finding.suppressedReason}. </>
            )}
            {finding.suppressedOwner && <>Owner: {finding.suppressedOwner}. </>}
            {finding.suppressedAt && <>Suppressed since {formatSeen(finding.suppressedAt)}. </>}
            {finding.suppressionExpiresAt
              ? `Unsuppresses automatically on ${formatSeen(finding.suppressionExpiresAt)}.`
              : "No expiry — suppressed until the pack rule is removed."}
          </p>
          <p className="text-muted-foreground">
            Suppressed findings are excluded from fail-on-severity gating and default listings,
            but are never deleted; every suppression transition is audited.
          </p>
        </section>
      )}

      <DetailSection title="Overview">
        <FactList>
          <Fact label="Severity" value={<SeverityBadge severity={finding.severity} />} />
          <Fact label="Status" value={statusLabel(finding.status)} />
          <Fact label="Confidence" value={finding.confidence || ""} />
          <Fact label="Score" mono value={finding.score.toFixed(1)} />
          <Fact label="Category" value={finding.category || ""} />
          <Fact
            label="CWE"
            value={
              finding.cwe.length > 0 ? (
                <span className="flex flex-wrap gap-x-3 gap-y-1">
                  {finding.cwe.map((cwe) => {
                    const url = cweLinkUrl(cwe);
                    return url ? (
                      <FactLink key={cwe} href={url}>{cwe}</FactLink>
                    ) : (
                      <span key={cwe}>{cwe}</span>
                    );
                  })}
                </span>
              ) : (
                ""
              )
            }
          />
          <Fact label="Repository" mono value={finding.repository || ""} />
          <Fact label="Revision" mono value={finding.revision || ""} />
          <Fact
            label="Location"
            mono
            value={
              location ? (
                sourceUrl ? (
                  <FactLink href={sourceUrl}>{location}</FactLink>
                ) : (
                  location
                )
              ) : (
                ""
              )
            }
          />
          <Fact label="Symbol" mono value={finding.symbol || ""} />
          <Fact label="Occurrences" mono value={String(finding.occurrences)} />
          <Fact label="First seen" value={formatSeen(finding.firstSeenAt)} />
          <Fact label="Last seen" value={formatSeen(finding.lastSeenAt)} />
          <Fact label="Triaged" value={finding.triagedAt ? formatSeen(finding.triagedAt) : ""} />
          <Fact label="Resolved" value={finding.resolvedAt ? formatSeen(finding.resolvedAt) : ""} />
          <Fact
            label="Baseline"
            value={finding.baselineState ? <BaselineBadge state={finding.baselineState} /> : ""}
          />
          <Fact label="Assignee" value={finding.assignee || ""} />
          <Fact
            label="Ticket"
            value={
              finding.ticketUrl ? (
                <FactLink href={finding.ticketUrl}>
                  {finding.ticketProvider ? `${finding.ticketProvider}: ` : ""}
                  {finding.ticketUrl}
                </FactLink>
              ) : (
                ""
              )
            }
          />
          <Fact label="Source agent" mono value={finding.sourceAgent || ""} />
          <Fact label="Scan step" mono value={finding.scanStep || ""} />
          <Fact
            label="Source"
            value={
              finding.sourceKind === "scanner" ? (
                <Badge variant="outline" className="text-[11px]">deterministic scanner</Badge>
              ) : (
                <Badge variant="outline" className="text-[11px]">agent</Badge>
              )
            }
          />
          <Fact
            label="Tool"
            mono
            value={
              finding.tool
                ? `${finding.tool}${finding.toolVersion ? ` ${finding.toolVersion}` : ""}`
                : ""
            }
          />
          <Fact label="Rule ID" mono value={finding.ruleId || ""} />
          <Fact
            label="Correlated sources"
            value={
              finding.correlatedFingerprints.length > 0 ? (
                <span className="flex flex-col gap-0.5">
                  {finding.correlatedFingerprints.map((fp) => (
                    <span key={fp} className="break-all font-mono text-[12px]">{fp}</span>
                  ))}
                </span>
              ) : (
                ""
              )
            }
          />
          <Fact label="Fingerprint" mono value={finding.fingerprint || ""} />
          <Fact
            label="Duplicate of"
            value={
              finding.duplicateOf ? (
                <Link
                  to={findingHref(finding.duplicateOf)}
                  className="font-mono text-[12.5px] underline-offset-2 hover:text-primary hover:underline"
                >
                  {finding.duplicateOf}
                </Link>
              ) : (
                ""
              )
            }
          />
          <Fact
            label="Tags"
            value={
              tags.length > 0 ? (
                <span className="flex flex-wrap gap-1">
                  {tags.map((tag) => (
                    <Badge key={tag} variant="outline" className="text-[11px]">
                      {tag}
                    </Badge>
                  ))}
                </span>
              ) : (
                ""
              )
            }
          />
          <Fact
            label="References"
            value={
              finding.references.length > 0 ? (
                <span className="flex flex-col gap-1">
                  {finding.references.map((ref) =>
                    isHttpUrl(ref) ? (
                      <FactLink key={ref} href={ref}>{ref}</FactLink>
                    ) : (
                      <span key={ref} className="break-all">{ref}</span>
                    ),
                  )}
                </span>
              ) : (
                ""
              )
            }
          />
        </FactList>
      </DetailSection>

      <DetailSection title="Details">
        <div className="space-y-5">
          <FindingMarkdownSection label="Description" content={finding.description} />
          <FindingMarkdownSection label="Impact" content={finding.impact} />
          <FindingMarkdownSection label="Attack vector" content={finding.attackVector} />
          <FindingMarkdownSection label="Remediation" content={finding.remediation} />
        </div>
      </DetailSection>

      <DetailSection
        title="Evidence & PoC"
        aside={
          finding.raw ? (
            <Button variant="outline" size="sm" onClick={copyEvidence}>
              <Copy />
              Copy raw
            </Button>
          ) : undefined
        }
      >
        <div className="space-y-3">
          <p
            role="note"
            className="flex items-start gap-2 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-[12.5px]"
          >
            <AlertTriangle aria-hidden className="mt-0.5 size-4 shrink-0 text-amber-600" />
            <span>
              Evidence and proof-of-concept content below is untrusted agent output shown
              verbatim. Never execute it without careful review.
            </span>
          </p>

          {evidence.length === 0 && !finding.raw ? (
            <p className="text-[12.5px] text-muted-foreground">Not provided.</p>
          ) : (
            <>
              {evidence.map((entry, i) => (
                <div key={i} className="space-y-1">
                  {(entry.filePath || entry.note) && (
                    <p className="text-[12px] text-muted-foreground">
                      {entry.filePath && (
                        <span className="font-mono">
                          {entry.filePath}
                          {entry.startLine > 0 &&
                            `:${entry.startLine}${entry.endLine > entry.startLine ? `-${entry.endLine}` : ""}`}
                        </span>
                      )}
                      {entry.filePath && entry.note && " — "}
                      {entry.note}
                    </p>
                  )}
                  {entry.snippet && (
                    <pre className="max-h-64 overflow-auto rounded-md border border-border/60 bg-muted/30 p-2 font-mono text-[11.5px] leading-relaxed whitespace-pre-wrap break-all">
                      {entry.snippet}
                    </pre>
                  )}
                </div>
              ))}
              {finding.raw && (
                <details className="text-[12px]">
                  <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
                    Raw finding JSON
                  </summary>
                  <pre className="mt-2 max-h-80 overflow-auto rounded-md border border-border/60 bg-muted/30 p-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap break-all">
                    {finding.raw}
                  </pre>
                </details>
              )}
            </>
          )}
        </div>
      </DetailSection>

      <DetailSection title="Triage">
        <div className="space-y-4">
          <div className="flex flex-wrap items-end gap-2">
            <div className="space-y-1">
              <label
                htmlFor="finding-status"
                className="block text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70"
              >
                Status
              </label>
              <select
                id="finding-status"
                className={filterSelectClass}
                value={statusDraft || finding.status}
                disabled={statusSaving}
                onChange={(e) => setStatusDraft(e.target.value)}
              >
                {FINDING_STATUSES.map((s) => (
                  <option key={s} value={s}>{statusLabel(s)}</option>
                ))}
              </select>
            </div>
            {(statusDraft || finding.status) === "accepted_risk" && (
              <div className="space-y-1">
                <label
                  htmlFor="finding-risk-expiry"
                  className="block text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70"
                >
                  Accepted until (optional)
                </label>
                <input
                  id="finding-risk-expiry"
                  type="datetime-local"
                  value={expiryDraft}
                  disabled={statusSaving}
                  onChange={(e) => setExpiryDraft(e.target.value)}
                  className="h-8 rounded-md border border-border/70 bg-background px-2 text-[12.5px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                />
              </div>
            )}
            <div className="min-w-0 flex-1 space-y-1">
              <label
                htmlFor="finding-status-note"
                className="block text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70"
              >
                Note (optional)
              </label>
              <input
                id="finding-status-note"
                type="text"
                value={statusNote}
                disabled={statusSaving}
                onChange={(e) => setStatusNote(e.target.value)}
                placeholder="Why is the status changing?"
                className="h-8 w-full rounded-md border border-border/70 bg-background px-2 text-[12.5px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
              />
            </div>
            <Button
              size="sm"
              disabled={statusSaving || !statusDraft || statusDraft === finding.status}
              onClick={() => void applyStatus()}
            >
              {statusSaving ? "Saving…" : "Update status"}
            </Button>
          </div>

          <div className="flex flex-wrap items-end gap-2">
            <div className="space-y-1">
              <label
                htmlFor="finding-assignee"
                className="block text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70"
              >
                Assignee
              </label>
              <input
                id="finding-assignee"
                type="text"
                value={assigneeDraft ?? finding.assignee}
                disabled={assigneeSaving}
                onChange={(e) => setAssigneeDraft(e.target.value)}
                placeholder="Unassigned"
                className="h-8 w-56 rounded-md border border-border/70 bg-background px-2 text-[12.5px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
              />
            </div>
            <Button
              size="sm"
              variant="outline"
              disabled={assigneeSaving || assigneeDraft === null || assigneeDraft.trim() === finding.assignee}
              onClick={() => void applyAssignee()}
            >
              {assigneeSaving ? "Saving…" : "Set assignee"}
            </Button>
            {finding.assignee && (
              <Button
                size="sm"
                variant="ghost"
                disabled={assigneeSaving}
                onClick={() => {
                  setAssigneeDraft("");
                  void (async () => {
                    setAssigneeSaving(true);
                    try {
                      const updated = await client.updateSecurityFindingAssignee({
                        id: finding.id,
                        namespace: namespace ?? "",
                        assignee: "",
                      });
                      setFinding(updated);
                      setAssigneeDraft(null);
                      toast.success("Assignee cleared");
                      void refreshEvents();
                    } catch (e: unknown) {
                      toast.error(describeRpcError(e, "clear the assignee"));
                    } finally {
                      setAssigneeSaving(false);
                    }
                  })();
                }}
              >
                Clear
              </Button>
            )}
          </div>
        </div>
      </DetailSection>

      <DetailSection
        title="Ticket"
        description="Track remediation in an external issue tracker. Created issues never include raw evidence."
      >
        {finding.ticketUrl ? (
          <div className="flex flex-wrap items-center gap-2 text-[12.5px]">
            {finding.ticketProvider && (
              <Badge variant="outline" className="text-[11px] capitalize">{finding.ticketProvider}</Badge>
            )}
            <FactLink href={finding.ticketUrl}>{finding.ticketUrl}</FactLink>
            <Button variant="outline" size="sm" disabled={ticketBusy} onClick={() => void unlinkTicket()}>
              {ticketBusy ? "Working…" : "Unlink"}
            </Button>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="flex flex-wrap items-end gap-2">
              <div className="min-w-0 flex-1 space-y-1">
                <label
                  htmlFor="finding-ticket-url"
                  className="block text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70"
                >
                  Link an existing ticket
                </label>
                <input
                  id="finding-ticket-url"
                  type="url"
                  value={ticketUrlDraft}
                  disabled={ticketBusy}
                  onChange={(e) => setTicketUrlDraft(e.target.value)}
                  placeholder="https://github.com/org/repo/issues/123 or a Linear issue URL"
                  className="h-8 w-full rounded-md border border-border/70 bg-background px-2 text-[12.5px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                />
              </div>
              <select
                aria-label="Ticket provider"
                className={filterSelectClass}
                value={ticketProviderDraft}
                disabled={ticketBusy}
                onChange={(e) => setTicketProviderDraft(e.target.value)}
              >
                <option value="github">GitHub</option>
                <option value="linear">Linear</option>
                <option value="other">Other</option>
              </select>
              <Button size="sm" variant="outline" disabled={ticketBusy || !ticketUrlDraft.trim()} onClick={() => void linkTicket()}>
                Link ticket
              </Button>
            </div>
            <div className="flex flex-wrap items-end gap-2">
              <div className="space-y-1">
                <label
                  htmlFor="finding-ticket-repo"
                  className="block text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70"
                >
                  Create a GitHub issue via a configured repository
                </label>
                <input
                  id="finding-ticket-repo"
                  type="text"
                  value={ticketRepoDraft}
                  disabled={ticketBusy}
                  onChange={(e) => setTicketRepoDraft(e.target.value)}
                  placeholder="GitHubRepository resource name"
                  className="h-8 w-72 rounded-md border border-border/70 bg-background px-2 text-[12.5px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                />
              </div>
              <Button size="sm" disabled={ticketBusy || !ticketRepoDraft.trim()} onClick={() => void createGitHubTicket()}>
                {ticketBusy ? "Working…" : "Create GitHub issue"}
              </Button>
            </div>
            <p className="text-[12px] text-muted-foreground">
              Linear issues are link-only: create the issue in Linear, then paste its URL above.
            </p>
          </div>
        )}
      </DetailSection>

      <DetailSection title="Comments & History">
        <div className="space-y-4">
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void submitComment();
            }}
            className="space-y-2"
          >
            <label
              htmlFor="finding-comment"
              className="block text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70"
            >
              Add a comment
            </label>
            <Textarea
              id="finding-comment"
              value={comment}
              maxLength={MAX_COMMENT_LEN}
              disabled={commentSaving}
              onChange={(e) => setComment(e.target.value)}
              placeholder="Share triage notes, validation results, or remediation plans…"
              className="min-h-20"
            />
            <div className="flex items-center justify-between gap-2">
              <span
                className={cn(
                  "text-[11.5px] text-muted-foreground/70",
                  comment.length >= MAX_COMMENT_LEN && "text-destructive",
                )}
              >
                {comment.length} / {MAX_COMMENT_LEN}
              </span>
              <Button type="submit" size="sm" disabled={commentSaving || !comment.trim()}>
                {commentSaving ? "Posting…" : "Comment"}
              </Button>
            </div>
          </form>

          {events.length === 0 ? (
            <p className="text-[12.5px] text-muted-foreground">No history recorded.</p>
          ) : (
            <ol aria-label="Finding history" className="space-y-3">
              {events.map((event) => (
                <li key={String(event.id)} className="text-[12.5px] leading-relaxed">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                    <span className="font-medium capitalize">{statusLabel(event.eventType)}</span>
                    {statusChangeSummary(event) && (
                      <span className="capitalize text-muted-foreground">
                        {statusChangeSummary(event)}
                      </span>
                    )}
                    {event.actor && <span className="text-muted-foreground">· {event.actor}</span>}
                    {event.createdAt && (
                      <span className="text-muted-foreground">· {formatSeen(event.createdAt)}</span>
                    )}
                  </div>
                  {event.note && (
                    <p className="mt-0.5 whitespace-pre-wrap break-words text-foreground/90">
                      {event.note}
                    </p>
                  )}
                </li>
              ))}
            </ol>
          )}
        </div>
      </DetailSection>
    </div>
  );
}

function FindingMarkdownSection({ label, content }: { label: string; content: string }) {
  return (
    <section aria-label={label} className="space-y-1">
      <h3 className="text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
        {label}
      </h3>
      {content ? (
        <MarkdownViewer content={content} />
      ) : (
        <p className="text-[12.5px] text-muted-foreground">Not provided.</p>
      )}
    </section>
  );
}
