/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
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
import {
  DetailErrorState,
  classifyDetailError,
  type DetailErrorKind,
} from "@/components/ui/detail-state";
import { useUrlFilters } from "@/hooks/useUrlFilters";
import { repoLabel } from "@/lib/securityFilters";
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

/**
 * Canonical finding-filter contract, identical to the scan detail and
 * configuration detail lists. The whole set matters here: prev/next must walk
 * exactly the list the user filtered, so every key that narrows the table also
 * narrows the sibling list.
 */
const FILTER_SPEC = {
  q: "",
  severity: "all",
  status: "actionable",
  category: "all",
  tool: "all",
  file: "",
  baseline: "all",
  assignee: "",
  suppressed: "exclude",
  dupes: "hide",
  selected: "",
} as const;

const RECOVERY_LINKS = [
  { to: "/security/runs", label: "Scan runs" },
  { to: "/security", label: "Security overview" },
];

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

/** Map a load failure to a typed dead-end state, code first, message second. */
function classifyLoadError(err: unknown, message: string): DetailErrorKind {
  switch (connectCodeOf(err)) {
    case Code.NotFound:
      return "not-found";
    case Code.PermissionDenied:
    case Code.Unauthenticated:
      return "forbidden";
    case Code.FailedPrecondition:
    case Code.Unimplemented:
      return "unsupported";
    default:
      return classifyDetailError(message);
  }
}

const FAILURE_COPY: Record<DetailErrorKind, { title: string; description: string }> = {
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
  const navigate = useNavigate();

  const [scan, setScan] = useState<SecurityScan | null>(null);
  const [finding, setFinding] = useState<SecurityFinding | null>(null);
  const [events, setEvents] = useState<SecurityFindingEvent[]>([]);
  const [siblingIds, setSiblingIds] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [failure, setFailure] = useState<DetailErrorKind | null>(null);
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

  // The full filter context carried over from the scan table. Prev/next walks
  // exactly this list, and the back link hands it straight back.
  const { values, queryString } = useUrlFilters(FILTER_SPEC);
  const {
    severity,
    status,
    category,
    tool,
    file,
    baseline,
    assignee: assigneeFilter,
    suppressed,
    dupes,
  } = values;
  const search = values.q;

  // Every hop keeps the filter query string and points `selected` at the
  // finding on screen, so returning to the scan restores the same row.
  const hrefFor = useCallback(
    (path: string, selectedId: string) => {
      const params = new URLSearchParams(queryString);
      if (selectedId) params.set("selected", selectedId);
      const text = params.toString();
      return `${path}${text ? `?${text}` : ""}`;
    },
    [queryString],
  );

  const scanHref = hrefFor(`/security/${namespace}/${runName}`, findingId ?? "");
  const findingHref = useCallback(
    (id: string) => hrefFor(`/security/${namespace}/${runName}/findings/${id}`, id),
    [hrefFor, namespace, runName],
  );

  const fetchAll = useCallback(async () => {
    if (!namespace || !runName || !findingId) return;
    setLoading(true);
    setFailure(null);
    setFailureMessage("");
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
          severity: severity === "all" ? "" : severity,
          status: status === "all" ? "" : status,
          category: category === "all" ? "" : category,
          search,
          baselineState: baseline === "all" ? "" : baseline,
          assignee: assigneeFilter,
          suppressed,
          includeDuplicates: dupes === "include",
        }),
      ]);
      // `tool` and `file` have no server-side equivalent on
      // listSecurityFindings, so they narrow the page it returned — the same
      // way the scan table narrows it.
      const needle = file.trim().toLowerCase();
      const siblings = siblingsResp.findings.filter((f) => {
        if (tool !== "all" && f.sourceAgent !== tool) return false;
        if (needle && !f.filePath.toLowerCase().includes(needle)) return false;
        return true;
      });
      setScan(scanResp);
      setFinding(findingResp.finding ?? null);
      setEvents(findingResp.events);
      setSiblingIds(siblings.map((f) => f.id));
      if (!findingResp.finding) {
        setFailure("not-found");
      }
    } catch (e: unknown) {
      const message = describeRpcError(e, "load the finding");
      setFailure(classifyLoadError(e, message));
      setFailureMessage(message);
    } finally {
      setLoading(false);
    }
  }, [
    namespace, runName, findingId, severity, status, category, search, tool, file,
    baseline, assigneeFilter, suppressed, dupes,
  ]);

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
  const confirmLeave = useCallback(
    () => !commentRef.current.trim() || window.confirm(UNSAVED_COMMENT_WARNING),
    [],
  );

  const guardLinkClicks = useCallback(
    (e: React.MouseEvent) => {
      if (!commentRef.current.trim()) return;
      const anchor = (e.target as HTMLElement).closest("a");
      if (!anchor) return;
      if (!confirmLeave()) {
        e.preventDefault();
        e.stopPropagation();
      }
    },
    [confirmLeave],
  );

  const position = finding ? siblingIds.indexOf(finding.id) : -1;
  const prevId = position > 0 ? siblingIds[position - 1] : null;
  const nextId =
    position >= 0 && position < siblingIds.length - 1 ? siblingIds[position + 1] : null;

  // Triage is a queue: j/k (and the arrow keys) step through the filtered list
  // without leaving the keyboard. Typing in a field always wins.
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.metaKey || e.ctrlKey || e.altKey || e.shiftKey) return;
      const target = e.target as HTMLElement | null;
      if (target?.isContentEditable) return;
      const tag = target?.tagName;
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
      const back = e.key === "k" || e.key === "ArrowLeft";
      const forward = e.key === "j" || e.key === "ArrowRight";
      if (!back && !forward) return;
      const destination = back ? prevId : nextId;
      if (!destination) return;
      e.preventDefault();
      if (!confirmLeave()) return;
      navigate(findingHref(destination));
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [prevId, nextId, navigate, findingHref, confirmLeave]);

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

  function copyLocation() {
    if (!finding) return;
    const target = `${finding.filePath}${finding.startLine > 0 ? `:${finding.startLine}` : ""}`;
    void navigator.clipboard
      .writeText(target)
      .then(() => toast.success("File path copied"))
      .catch(() => toast.error("Couldn't copy to the clipboard"));
  }

  if (!namespace || !runName || !findingId) {
    return (
      <DetailErrorState
        kind="not-found"
        title="Finding not found"
        description="This link is missing the namespace, the scan run, or the finding id."
        links={RECOVERY_LINKS}
      />
    );
  }

  if (loading) {
    return (
      <div role="status" aria-label="Loading finding" className="space-y-4">
        <Skeleton className="h-7 w-2/5" />
        <Skeleton className="h-4 w-3/5" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  if (failure || !finding) {
    const kind = failure ?? "not-found";
    const copy = FAILURE_COPY[kind];
    return (
      <DetailErrorState
        kind={kind}
        title={copy.title}
        description={copy.description}
        detail={failureMessage || undefined}
        onRetry={() => void fetchAll()}
        links={[{ to: scanHref, label: "Back to scan" }, ...RECOVERY_LINKS]}
      />
    );
  }

  const { evidence, tags } = parseRawFinding(finding.raw);
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
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[12.5px] text-muted-foreground">
            <span className="tabular-nums">
              Score <span className="font-medium text-foreground">{finding.score.toFixed(1)}</span>
            </span>
            {finding.repository && (
              <span className="font-mono" title={finding.repository}>
                {repoLabel(finding.repository)}
              </span>
            )}
            {location && (
              <span className="inline-flex min-w-0 items-center gap-1">
                {sourceUrl ? (
                  <a
                    href={sourceUrl}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="truncate font-mono underline-offset-2 hover:text-foreground hover:underline"
                  >
                    {location}
                  </a>
                ) : (
                  <span className="truncate font-mono">{location}</span>
                )}
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="Copy file and line"
                  onClick={copyLocation}
                >
                  <Copy />
                </Button>
              </span>
            )}
          </div>
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
            <nav
              aria-label="Finding navigation"
              className="flex flex-col items-start gap-0.5 sm:items-end"
            >
              <div className="flex items-center gap-1">
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
                <span
                  data-testid="finding-position"
                  aria-live="polite"
                  className="px-1 text-[12px] tabular-nums text-muted-foreground"
                >
                  {position >= 0
                    ? `${position + 1} of ${siblingIds.length}`
                    : "Not in the filtered list"}
                </span>
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
              </div>
              <p className="text-[11px] text-muted-foreground/70">
                Press <kbd className="font-mono">j</kbd>/<kbd className="font-mono">k</kbd> or{" "}
                <kbd className="font-mono">←</kbd>/<kbd className="font-mono">→</kbd> to move
                between findings
              </p>
            </nav>
          </>
        }
      />

      {/* Sticky so the triage decision stays reachable while reading long
          evidence: status and assignment are the actions this page exists for. */}
      <section
        aria-label="Triage actions"
        className="sticky top-0 z-20 space-y-3 rounded-xl border border-border/70 bg-background/95 px-3 py-2.5 backdrop-blur supports-[backdrop-filter]:bg-background/80"
      >
        <h2 className="sr-only">Triage</h2>
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
      </section>

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
          <Fact label="Status" value={statusLabel(finding.status)} />
          <Fact label="Confidence" value={finding.confidence || ""} />
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
        </FactList>
      </DetailSection>

      <DetailSection title="Details">
        <div className="space-y-5">
          <FindingMarkdownSection label="Description" content={finding.description} />
          <FindingMarkdownSection label="Impact" content={finding.impact} />
          <FindingMarkdownSection label="Attack vector" content={finding.attackVector} />
          <FindingMarkdownSection label="Remediation" content={finding.remediation} />
          <section aria-label="References" className="space-y-1">
            <h3 className="text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
              References
            </h3>
            {finding.references.length > 0 ? (
              <ul className="space-y-0.5 text-[12.5px]">
                {finding.references.map((ref) => (
                  <li key={ref} className="break-all">
                    {isHttpUrl(ref) ? <FactLink href={ref}>{ref}</FactLink> : ref}
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-[12.5px] text-muted-foreground">Not provided.</p>
            )}
          </section>
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
