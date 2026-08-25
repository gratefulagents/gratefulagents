/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Code } from "@connectrpc/connect";
import {
  AlertTriangle,
  Check,
  ChevronLeft,
  ChevronRight,
  Copy,
  SquareArrowOutUpRight,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
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
import { timestampDate, timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";
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

const fieldInputClass =
  "h-8 rounded-md border border-border/70 bg-background px-2 text-[12.5px] text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:opacity-100 disabled:border-border/50 disabled:bg-muted/40 disabled:text-muted-foreground";

const fieldLabelClass =
  "block text-[10.5px] font-medium uppercase tracking-[0.07em] text-muted-foreground";

/**
 * Disabled controls keep full opacity and switch to muted-on-muted instead of
 * fading out, so "nothing to do here" never reads as "this page is broken".
 */
const disabledControlClass =
  "disabled:opacity-100 disabled:bg-muted/60 disabled:text-foreground/70";

/** Single wording for an unset value, everywhere on the page. */
const EMPTY_VALUE = "Not set";

/** Sentence-cased status wording ("Open", "Accepted risk") for display. */
function findingStatusLabel(status: string): string {
  const text = statusLabel(status);
  if (!text) return "";
  return text.charAt(0).toUpperCase() + text.slice(1);
}

function relativeAge(atMs: number, nowMs: number): string {
  const seconds = Math.round((nowMs - atMs) / 1000);
  const abs = Math.abs(seconds);
  const amount =
    abs < 60
      ? `${abs}s`
      : abs < 3600
        ? `${Math.floor(abs / 60)}m`
        : abs < 86400
          ? `${Math.floor(abs / 3600)}h`
          : abs < 2592000
            ? `${Math.floor(abs / 86400)}d`
            : abs < 31536000
              ? `${Math.floor(abs / 2592000)}mo`
              : `${Math.floor(abs / 31536000)}y`;
  return seconds >= 0 ? `${amount} ago` : `in ${amount}`;
}

/**
 * Compact absolute stamp plus a relative age: "2 Feb, 00:00" / "9mo ago".
 * The year is only spelled out when it isn't the current one, and the full
 * locale string stays available as the tooltip.
 */
function formatWhen(
  ts: Timestamp | undefined,
  nowMs = Date.now(),
): { absolute: string; relative: string; exact: string } | null {
  if (!ts) return null;
  const date = timestampDate(ts);
  const absolute = date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    ...(date.getFullYear() === new Date(nowMs).getFullYear() ? {} : { year: "numeric" }),
    hour: "2-digit",
    minute: "2-digit",
    hourCycle: "h23",
  });
  return {
    absolute,
    relative: relativeAge(date.getTime(), nowMs),
    exact: date.toLocaleString(),
  };
}

function TimeValue({ ts }: { ts: Timestamp | undefined }) {
  const when = formatWhen(ts);
  if (!when) return <span className="text-muted-foreground">{EMPTY_VALUE}</span>;
  return (
    <span className="inline-flex flex-wrap items-baseline gap-x-1.5" title={when.exact}>
      <span className="tabular-nums">{when.absolute}</span>
      <span className="text-[11.5px] text-muted-foreground">{when.relative}</span>
    </span>
  );
}

function copyText(value: string, what: string) {
  void navigator.clipboard
    .writeText(value)
    .then(() => toast.success(`${what} copied`))
    .catch(() => toast.error("Couldn't copy to the clipboard"));
}

/**
 * Long identifiers (repository URLs, revisions, fingerprints) truncate to the
 * column instead of overflowing it; the full value stays in the tooltip and
 * one click away in the clipboard.
 */
function CopyValue({ value, label }: { value: string; label: string }) {
  return (
    <span className="group/copy inline-flex min-w-0 max-w-full items-center gap-1">
      <span className="min-w-0 truncate font-mono" title={value}>
        {value}
      </span>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label={`Copy ${label}`}
        title={`Copy ${label}`}
        className="shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover/copy:opacity-100 focus-visible:opacity-100"
        onClick={() => copyText(value, label)}
      >
        <Copy />
      </Button>
    </span>
  );
}

/** One labelled value on the header metadata line. */
function MetaItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      <span className="text-[10.5px] font-medium uppercase tracking-[0.07em] text-muted-foreground">
        {label}
      </span>
      <span className="inline-flex min-w-0 items-center gap-1.5 text-foreground">{children}</span>
    </span>
  );
}

/** In-place confirmation that a triage write landed. */
function SavedFlash() {
  return (
    <span
      role="status"
      className="inline-flex items-center gap-1 text-[12px] font-medium text-emerald-700 dark:text-emerald-400"
    >
      <Check className="size-3.5" />
      Saved
    </span>
  );
}

/**
 * Wraps a disabled submit so hovering it explains what is missing; the button
 * itself cannot, because disabled buttons drop their pointer events.
 */
function GuardedAction({
  enabled,
  reason,
  children,
}: {
  enabled: boolean;
  reason: string;
  children: React.ReactNode;
}) {
  if (enabled) return <>{children}</>;
  return (
    <span className="inline-flex" title={reason}>
      {children}
    </span>
  );
}

/**
 * One end of the prev/next pager. A disabled step keeps a legible, honest
 * disabled look and explains itself on hover through a wrapper (a disabled
 * button swallows its own pointer events, tooltip included).
 */
function NavStep({
  label,
  to,
  disabledReason,
  children,
}: {
  label: string;
  to: string;
  disabledReason: string;
  children: React.ReactNode;
}) {
  const button = (
    <Button
      variant="ghost"
      size="sm"
      aria-label={label}
      className={cn("rounded-none border-0 px-2.5", disabledControlClass)}
      disabled={!to}
      nativeButton={!to}
      render={to ? <Link to={to} /> : undefined}
    >
      {children}
    </Button>
  );
  if (to) return button;
  return (
    <span className="inline-flex" title={disabledReason}>
      {button}
    </span>
  );
}

/**
 * Domain-specific stand-in for an absent fact value, styled like the generic
 * "Not set" fallback the Fact component renders on its own.
 */
function Missing({ text }: { text: string }) {
  return <span className="text-muted-foreground/50">{text}</span>;
}

/** Bordered, headed group of related facts; several sit side by side. */
function FactGroup({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section
      aria-label={title}
      className="min-w-0 space-y-2.5 rounded-lg border border-border/60 bg-muted/10 px-3.5 py-3"
    >
      <h3 className="text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground">
        {title}
      </h3>
      <FactList className="gap-x-4 sm:grid-cols-[minmax(88px,120px)_minmax(0,1fr)]">
        {children}
      </FactList>
    </section>
  );
}

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
    return `${findingStatusLabel(from || "?")} → ${findingStatusLabel(to || "?")}`;
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
  // Which triage control just saved, so the bar can confirm in place instead
  // of relying on a toast that may already have gone.
  const [savedFlash, setSavedFlash] = useState<"" | "status" | "assignee">("");

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
    setSavedFlash("");
  }, [findingId]);

  useEffect(() => {
    if (!savedFlash) return;
    const timer = setTimeout(() => setSavedFlash(""), 4000);
    return () => clearTimeout(timer);
  }, [savedFlash]);

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
      setSavedFlash("status");
      toast.success(`Status set to ${findingStatusLabel(updated.status)}`);
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
      setSavedFlash("assignee");
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
    copyText(finding.raw, "Evidence");
  }

  function copyLocation() {
    if (!finding) return;
    copyText(
      `${finding.filePath}${finding.startLine > 0 ? `:${finding.startLine}` : ""}`,
      "File path",
    );
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

  const statusText = findingStatusLabel(finding.status);
  const statusDirty = Boolean(statusDraft) && statusDraft !== finding.status;
  const assigneeDirty = assigneeDraft !== null && assigneeDraft.trim() !== finding.assignee;
  const outsideList = position < 0;
  const prevReason = prevId
    ? ""
    : outsideList
      ? "This finding is not in the current filtered list, so there is nothing to page back to."
      : "This is the first finding in the filtered list.";
  const nextReason = nextId
    ? ""
    : outsideList
      ? "This finding is not in the current filtered list, so there is nothing to page forward to."
      : "This is the last finding in the filtered list.";

  const headerMeta: { key: string; label: string; node: React.ReactNode }[] = [
    {
      key: "status",
      label: "Status",
      node: (
        <>
          <Badge variant="outline">{statusText}</Badge>
          {finding.status === "accepted_risk" && (
            <ExpiryBadge ts={finding.acceptedRiskExpiresAt} />
          )}
          <SuppressedBadge finding={finding} />
        </>
      ),
    },
    {
      key: "score",
      label: "Score",
      node: <span className="font-medium tabular-nums">{finding.score.toFixed(1)}</span>,
    },
  ];
  if (finding.baselineState) {
    headerMeta.push({
      key: "baseline",
      label: "Baseline",
      node: <BaselineBadge state={finding.baselineState} />,
    });
  }
  if (finding.repository) {
    headerMeta.push({
      key: "repository",
      label: "Repository",
      node: (
        <span className="truncate font-mono" title={finding.repository}>
          {repoLabel(finding.repository)}
        </span>
      ),
    });
  }
  if (location) {
    headerMeta.push({
      key: "file",
      label: "File",
      node: (
        <>
          {sourceUrl ? (
            <a
              href={sourceUrl}
              target="_blank"
              rel="noreferrer noopener"
              className="truncate font-mono underline-offset-2 hover:text-primary hover:underline"
              title={location}
            >
              {location}
            </a>
          ) : (
            <span className="truncate font-mono" title={location}>
              {location}
            </span>
          )}
          <Button
            variant="ghost"
            size="xs"
            aria-label="Copy file and line"
            className="shrink-0 text-foreground/80 hover:text-foreground"
            onClick={copyLocation}
          >
            <Copy />
            Copy
          </Button>
        </>
      ),
    });
  }

  return (
    <div className="space-y-6" onClickCapture={guardLinkClicks}>
      <DetailHeader
        parentLabel={`Scan ${runName ?? ""}`}
        parentTo={scanHref}
        title={finding.title}
        meta={<SeverityBadge severity={finding.severity} />}
        subtitle={
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 text-[12.5px] text-muted-foreground">
            {headerMeta.map((item, i) => (
              <span key={item.key} className="inline-flex min-w-0 items-center gap-2.5">
                {i > 0 && <span aria-hidden className="h-3.5 w-px shrink-0 bg-border" />}
                <MetaItem label={item.label}>{item.node}</MetaItem>
              </span>
            ))}
          </div>
        }
        actions={
          scan ? (
            <Button
              variant="outline"
              size="sm"
              nativeButton={false}
              render={<Link to={`/runs/${scan.namespace}/${scan.runName}`} />}
            >
              <SquareArrowOutUpRight />
              Agent run
            </Button>
          ) : undefined
        }
      />

      {/* The queue control sits with the finding it pages through, not in the
          header's action corner: one segmented group, its ordering spelled
          out, and the keyboard equivalents beside it. */}
      <nav
        aria-label="Finding navigation"
        className="flex flex-wrap items-center gap-x-3 gap-y-2 border-y border-border/60 py-2"
      >
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
          <div className="inline-flex items-stretch overflow-hidden rounded-lg border border-border/70">
            <NavStep
              label="Previous finding"
              to={prevId ? findingHref(prevId) : ""}
              disabledReason={prevReason}
            >
              <ChevronLeft />
              Prev
            </NavStep>
            <span
              data-testid="finding-position"
              aria-live="polite"
              className="flex items-center border-x border-border/70 bg-muted/30 px-2.5 text-[12px] tabular-nums text-foreground"
            >
              {position >= 0
                ? `${position + 1} of ${siblingIds.length}`
                : "Not in the filtered list"}
            </span>
            <NavStep
              label="Next finding"
              to={nextId ? findingHref(nextId) : ""}
              disabledReason={nextReason}
            >
              Next
              <ChevronRight />
            </NavStep>
          </div>
          <p
            className="text-[12px] text-muted-foreground"
            title="Prev/next walk the scan list in its current order: highest score first, then severity."
          >
            Ordered by score, as filtered
          </p>
        </div>
        <span aria-hidden className="hidden h-3.5 w-px bg-border sm:block" />
        <p className="flex items-center gap-1 text-[12px] text-muted-foreground">
          <Kbd>j</Kbd>
          <Kbd>k</Kbd>
          <span>or</span>
          <Kbd>←</Kbd>
          <Kbd>→</Kbd>
          <span>to move between findings</span>
        </p>
      </nav>

      {/* Sticky so the triage decision stays reachable while reading long
          evidence: status and assignment are the actions this page exists for.
          One row on one baseline — the fields line up with the submit that
          commits them, and the assignee sits beside them instead of alone. */}
      <section
        aria-label="Triage actions"
        className="sticky top-0 z-20 rounded-lg border border-border/70 bg-background/95 px-3 py-2 backdrop-blur supports-[backdrop-filter]:bg-background/80"
      >
        <h2 className="sr-only">Triage</h2>
        <div className="flex flex-wrap items-end gap-x-3 gap-y-2">
          <div className="space-y-1">
            <label htmlFor="finding-status" className={fieldLabelClass}>
              Status
            </label>
            <select
              id="finding-status"
              className={cn(filterSelectClass, "normal-case", disabledControlClass)}
              value={statusDraft || finding.status}
              disabled={statusSaving}
              onChange={(e) => setStatusDraft(e.target.value)}
            >
              {FINDING_STATUSES.map((s) => (
                <option key={s} value={s}>{findingStatusLabel(s)}</option>
              ))}
            </select>
          </div>
          {(statusDraft || finding.status) === "accepted_risk" && (
            <div className="space-y-1">
              <label htmlFor="finding-risk-expiry" className={fieldLabelClass}>
                Accepted until
              </label>
              <input
                id="finding-risk-expiry"
                type="datetime-local"
                value={expiryDraft}
                disabled={statusSaving}
                onChange={(e) => setExpiryDraft(e.target.value)}
                className={fieldInputClass}
              />
            </div>
          )}
          <div className="min-w-[14rem] max-w-sm flex-1 space-y-1">
            <label htmlFor="finding-status-note" className={fieldLabelClass}>
              Note (optional)
            </label>
            <input
              id="finding-status-note"
              type="text"
              value={statusNote}
              disabled={statusSaving}
              onChange={(e) => setStatusNote(e.target.value)}
              placeholder="Why is the status changing?"
              className={cn(fieldInputClass, "w-full")}
            />
          </div>
          <div className="flex items-center gap-2">
            <GuardedAction
              enabled={statusDirty}
              reason="Pick a different status to enable this."
            >
              <Button
                size="sm"
                className={cn("h-8", disabledControlClass)}
                disabled={statusSaving || !statusDirty}
                onClick={() => void applyStatus()}
              >
                {statusSaving ? "Saving…" : "Update status"}
              </Button>
            </GuardedAction>
            {savedFlash === "status" && <SavedFlash />}
          </div>

          <span aria-hidden className="hidden h-8 w-px self-end bg-border/60 lg:block" />

          <div className="space-y-1">
            <label htmlFor="finding-assignee" className={fieldLabelClass}>
              Assignee
            </label>
            <input
              id="finding-assignee"
              type="text"
              value={assigneeDraft ?? finding.assignee}
              disabled={assigneeSaving}
              onChange={(e) => setAssigneeDraft(e.target.value)}
              placeholder={EMPTY_VALUE}
              className={cn(fieldInputClass, "w-48")}
            />
          </div>
          <div className="flex items-center gap-2">
            <GuardedAction enabled={assigneeDirty} reason="Change the assignee to enable this.">
              <Button
                size="sm"
                variant="outline"
                className={cn("h-8", disabledControlClass)}
                disabled={assigneeSaving || !assigneeDirty}
                onClick={() => void applyAssignee()}
              >
                {assigneeSaving ? "Saving…" : "Set assignee"}
              </Button>
            </GuardedAction>
            {finding.assignee && (
              <Button
                size="sm"
                variant="ghost"
                className={cn("h-8", disabledControlClass)}
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
                      setSavedFlash("assignee");
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
            {savedFlash === "assignee" && <SavedFlash />}
          </div>
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
            {finding.suppressedAt && (
              <>
                Suppressed since <TimeValue ts={finding.suppressedAt} />.{" "}
              </>
            )}
            {finding.suppressionExpiresAt ? (
              <>
                Unsuppresses automatically on{" "}
                <TimeValue ts={finding.suppressionExpiresAt} />.
              </>
            ) : (
              "No expiry — suppressed until the pack rule is removed."
            )}
          </p>
          <p className="text-muted-foreground">
            Suppressed findings are excluded from fail-on-severity gating and default listings,
            but are never deleted; every suppression transition is audited.
          </p>
        </section>
      )}

      {/* The facts are grouped and framed instead of poured into one narrow
          two-column list: five small blocks fill the page width and each one
          answers a different question. */}
      <DetailSection title="Overview">
        <div className="gap-3 lg:columns-2 2xl:columns-3 [&>section]:mb-3 [&>section]:break-inside-avoid">
          <FactGroup title="Classification">
            <Fact label="Status" value={statusText} />
            <Fact label="Confidence" value={finding.confidence || ""} />
            <Fact label="Category" value={finding.category || <Missing text="No category" />} />
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
                  <Missing text="No CWE assigned" />
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
          </FactGroup>

          <FactGroup title="Code">
            <Fact
              label="Repository"
              value={
                finding.repository ? (
                  <CopyValue value={finding.repository} label="Repository" />
                ) : (
                  <Missing text="Not recorded" />
                )
              }
            />
            <Fact
              label="Revision"
              value={
                finding.revision ? (
                  <CopyValue value={finding.revision} label="Revision" />
                ) : (
                  <Missing text="Not recorded" />
                )
              }
            />
            <Fact
              label="File"
              value={
                location ? (
                  <CopyValue value={location} label="File path" />
                ) : (
                  <Missing text="No code location" />
                )
              }
            />
            <Fact label="Symbol" mono value={finding.symbol || ""} />
          </FactGroup>

          <FactGroup title="Provenance">
            <Fact
              label="Source"
              value={
                <Badge variant="outline" className="text-[11px]">
                  {finding.sourceKind === "scanner" ? "deterministic scanner" : "agent"}
                </Badge>
              }
            />
            <Fact
              label="Source agent"
              mono
              value={finding.sourceAgent || <Missing text="Not recorded" />}
            />
            <Fact label="Scan step" mono value={finding.scanStep || ""} />
            <Fact
              label="Tool"
              value={
                finding.tool ? (
                  <CopyValue
                    value={`${finding.tool}${finding.toolVersion ? ` ${finding.toolVersion}` : ""}`}
                    label="Tool"
                  />
                ) : (
                  <Missing text="Not recorded" />
                )
              }
            />
            <Fact
              label="Rule ID"
              value={finding.ruleId ? <CopyValue value={finding.ruleId} label="Rule ID" /> : ""}
            />
          </FactGroup>

          <FactGroup title="Identity">
            <Fact
              label="Fingerprint"
              value={
                finding.fingerprint ? (
                  <CopyValue value={finding.fingerprint} label="Fingerprint" />
                ) : (
                  ""
                )
              }
            />
            <Fact
              label="Correlated"
              value={
                finding.correlatedFingerprints.length > 0 ? (
                  <span className="flex flex-col gap-0.5">
                    {finding.correlatedFingerprints.map((fp) => (
                      <CopyValue key={fp} value={fp} label="Fingerprint" />
                    ))}
                  </span>
                ) : (
                  ""
                )
              }
            />
            <Fact
              label="Duplicate of"
              value={
                finding.duplicateOf ? (
                  <Link
                    to={findingHref(finding.duplicateOf)}
                    title={finding.duplicateOf}
                    className="block truncate font-mono text-[12.5px] underline-offset-2 hover:text-primary hover:underline"
                  >
                    {finding.duplicateOf}
                  </Link>
                ) : (
                  ""
                )
              }
            />
            <Fact label="Occurrences" mono value={String(finding.occurrences)} />
          </FactGroup>

          <FactGroup title="Triage state">
            <Fact label="Assignee" value={finding.assignee || <Missing text="Unassigned" />} />
            <Fact
              label="Baseline"
              value={finding.baselineState ? <BaselineBadge state={finding.baselineState} /> : ""}
            />
            <Fact
              label="Ticket"
              value={
                finding.ticketUrl ? (
                  <FactLink href={finding.ticketUrl}>
                    {finding.ticketProvider ? `${finding.ticketProvider}: ` : ""}
                    {finding.ticketUrl}
                  </FactLink>
                ) : (
                  <Missing text="No ticket linked" />
                )
              }
            />
          </FactGroup>

          <FactGroup title="Timeline">
            <Fact label="First seen" value={<TimeValue ts={finding.firstSeenAt} />} />
            <Fact label="Last seen" value={<TimeValue ts={finding.lastSeenAt} />} />
            <Fact label="Triaged" value={<TimeValue ts={finding.triagedAt} />} />
            <Fact label="Resolved" value={<TimeValue ts={finding.resolvedAt} />} />
          </FactGroup>
        </div>
      </DetailSection>

      <DetailSection title="Details">
        <div className="grid items-start gap-3 lg:grid-cols-2">
          <FindingMarkdownSection label="Description" content={finding.description} />
          <FindingMarkdownSection label="Impact" content={finding.impact} />
          <FindingMarkdownSection label="Attack vector" content={finding.attackVector} />
          <FindingMarkdownSection label="Remediation" content={finding.remediation} />
          <section
            aria-label="References"
            className="space-y-1.5 rounded-lg border border-border/60 bg-muted/10 px-3.5 py-3 lg:col-span-2"
          >
            <h3 className="text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground">
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
          {/* The untrusted-content warning belongs to content: with nothing to
              show it is a warning about an empty box. */}
          {(evidence.length > 0 || finding.raw) && (
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
          )}

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
            <span className="min-w-0 max-w-full flex-1 truncate">
              <FactLink href={finding.ticketUrl}>{finding.ticketUrl}</FactLink>
            </span>
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
                  className={fieldLabelClass}
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
                  className={cn(fieldInputClass, "w-full")}
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
                  className={fieldLabelClass}
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
                  className={cn(fieldInputClass, "w-72")}
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
              className={fieldLabelClass}
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
            <div className="flex items-center justify-end gap-3">
              <span
                className={cn(
                  "text-[11.5px] text-muted-foreground",
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
                    <span className="font-medium">{findingStatusLabel(event.eventType)}</span>
                    {statusChangeSummary(event) && (
                      <span className="text-muted-foreground">{statusChangeSummary(event)}</span>
                    )}
                    {event.actor && <span className="text-muted-foreground">· {event.actor}</span>}
                    {event.createdAt && (
                      <span className="text-muted-foreground">
                        · <TimeValue ts={event.createdAt} />
                      </span>
                    )}
                  </div>
                  {event.note && (
                    <p className="mt-0.5 whitespace-pre-wrap break-words text-foreground">
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
    <section
      aria-label={label}
      className="min-w-0 space-y-1.5 rounded-lg border border-border/60 bg-muted/10 px-3.5 py-3"
    >
      <h3 className="text-[11px] font-medium uppercase tracking-[0.07em] text-muted-foreground">
        {label}
      </h3>
      {content ? (
        <div className="min-w-0 break-words">
          <MarkdownViewer content={content} />
        </div>
      ) : (
        <p className="text-[12.5px] text-muted-foreground">Not provided.</p>
      )}
    </section>
  );
}
