import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  ChevronRight,
  Circle,
  ExternalLink,
  GitPullRequest,
  Loader2,
  MessageSquare,
  RefreshCw,
  Send,
  XCircle,
} from "lucide-react";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toaster";
import { InspectorSubnav } from "@/components/run-session/InspectorSubnav";
import { client } from "@/lib/client";
import {
  buildReviewFixMessage,
  pullRequestLabel,
  reviewThreadLocation,
  reviewThreadSelectionKey,
  selectableReviewThreads,
} from "@/lib/pullRequests";
import { formatPRLoopRound, formatPRLoopState, reviewVerdictTone } from "@/components/run-session/helpers";
import { useNow } from "@/hooks/useNow";
import { prLoopTone, toneSoft, toneText, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { PRLoopStatus, PullRequestCheck, PullRequestDetails } from "@/rpc/platform/service_pb";

const POLL_INTERVAL_MS = 30_000;

type PRSection = "review" | "checks" | "loop";

interface RunPullRequestPanelProps {
  namespace: string;
  name: string;
  /** Whether the viewer may steer the agent (mirrors the chat composer gate). */
  canSend?: boolean;
  /** Autonomous implementer/reviewer loop status, when this run is in one. */
  prLoop?: PRLoopStatus;
  /** Fallback PR url recorded on the run, used when the loop has none. */
  prUrl?: string;
}

function prStateTone(state: string): StatusTone {
  switch (state.toLowerCase()) {
    case "open":
      return "success";
    case "merged":
      return "purple";
    case "closed":
      return "danger";
    default:
      return "neutral";
  }
}

/** `in_progress` → "In progress", `timed_out` → "Timed out". */
function humanizeCheckLabel(raw: string): string {
  const text = raw.trim().replace(/_/g, " ").toLowerCase();
  return text ? text.charAt(0).toUpperCase() + text.slice(1) : "";
}

function relativeTime(then: number, now: number): string {
  const diff = Math.max(now - then, 0);
  const s = Math.round(diff / 1000);
  if (s < 45) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  return `${d}d ago`;
}

type CiVerdict = { tone: StatusTone; label: string; failing: number; running: number };

/** Rollup for the subject header. Warnings/cancelled checks are not failures. */
function ciVerdict(checks: PullRequestCheck[]): CiVerdict | null {
  if (checks.length === 0) return null;
  let failing = 0;
  let running = 0;
  for (const check of checks) {
    const outcome = checkOutcome(check);
    if (outcome === "failure") failing += 1;
    else if (outcome === "pending") running += 1;
  }
  if (failing > 0) return { tone: "danger", label: `CI failing · ${failing}`, failing, running };
  if (running > 0) return { tone: "running", label: `CI running · ${running}`, failing, running };
  return { tone: "success", label: "CI green", failing, running };
}

function failingChecks(checks: PullRequestCheck[]): PullRequestCheck[] {
  return checks.filter((check) => checkOutcome(check) === "failure");
}

function buildFailingChecksMessage(pr: PullRequestDetails, checks: PullRequestCheck[]): string {
  const label = prDisplayLabel(pr);
  const items = checks.map((check, i) => {
    const conclusion = humanizeCheckLabel(check.conclusion || check.status);
    const url = check.detailsUrl ? ` — ${check.detailsUrl}` : "";
    return `${i + 1}. ${check.name} (${conclusion})${url}`;
  });
  const plural = checks.length === 1 ? "check is" : "checks are";
  return [
    `The following CI ${plural} failing on ${label} (${pr.url}):`,
    items.join("\n"),
    "Please inspect the logs for each failing check, fix the underlying cause, commit, and push to the PR branch.",
  ].join("\n\n");
}

type CheckOutcome = "pending" | "success" | "failure" | "warning" | "neutral";

function checkOutcome(check: PullRequestCheck): CheckOutcome {
  if (check.status.toLowerCase() !== "completed") {
    return "pending";
  }
  switch (check.conclusion.toLowerCase()) {
    case "success":
      return "success";
    case "failure":
    case "error": // legacy commit-status error state
    case "timed_out":
    case "startup_failure":
      return "failure";
    case "cancelled":
    case "action_required":
      return "warning";
    default:
      return "neutral";
  }
}

function checkAppearance(check: PullRequestCheck): { icon: React.ReactNode; label: string; className: string } {
  switch (checkOutcome(check)) {
    case "pending":
      return {
        icon: <Loader2 className="size-3.5 animate-spin" />,
        label: humanizeCheckLabel(check.status || "pending"),
        className: toneText.running,
      };
    case "success":
      return {
        icon: <CheckCircle2 className="size-3.5" />,
        label: "Success",
        className: toneText.success,
      };
    case "failure":
      return {
        icon: <XCircle className="size-3.5" />,
        label: humanizeCheckLabel(check.conclusion),
        className: toneText.danger,
      };
    case "warning":
      return {
        icon: <AlertTriangle className="size-3.5" />,
        label: humanizeCheckLabel(check.conclusion),
        className: toneText.warning,
      };
    default:
      return {
        icon: <Circle className="size-3.5" />,
        label: humanizeCheckLabel(check.conclusion || "neutral"),
        className: toneText.neutral,
      };
  }
}

function prDisplayLabel(pr: PullRequestDetails): string {
  return pr.repository && pr.number ? `${pr.repository}#${pr.number}` : pullRequestLabel(pr.url);
}

/**
 * The PR subject line. It is sticky because everything below it (checks,
 * review threads) is meaningless without knowing which pull request you are
 * looking at, and the thread list is long.
 */
function PullRequestSubject({ pr }: { pr: PullRequestDetails }) {
  const label = prDisplayLabel(pr);
  const verdict = ciVerdict(pr.checks);
  return (
    <div className="shrink-0 border-b px-3 py-2">
      <div className="flex min-w-0 items-center gap-2">
        <GitPullRequest className="size-3.5 shrink-0 text-muted-foreground" />
        <a
          href={pr.url}
          target="_blank"
          rel="noopener noreferrer"
          className="min-w-0 truncate font-mono text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
        >
          {label}
        </a>
        {pr.state && (
          <Badge className={cn("shrink-0 px-1.5 text-[10px]", toneSoft[prStateTone(pr.state)])}>
            {pr.state.toLowerCase()}
          </Badge>
        )}
        {verdict && (
          <span
            data-testid="ci-verdict"
            className={cn("inline-flex shrink-0 items-center gap-1 rounded-full px-1.5 py-px text-[10px] font-medium", toneSoft[verdict.tone])}
          >
            {verdict.tone === "running" && <Loader2 className="size-2.5 animate-spin" />}
            {verdict.label}
          </span>
        )}
        {pr.reviewDecision && (
          <Badge variant="outline" className="shrink-0 px-1.5 text-[10px]">
            {pr.reviewDecision.toLowerCase().replaceAll("_", " ")}
          </Badge>
        )}
        <a
          href={pr.url}
          target="_blank"
          rel="noopener noreferrer"
          className="ml-auto shrink-0 text-muted-foreground transition-colors hover:text-foreground"
          aria-label={`Open ${label} on GitHub`}
        >
          <ExternalLink className="size-3.5" />
        </a>
      </div>
      {pr.title && <p className="mt-1 truncate text-sm font-medium text-foreground">{pr.title}</p>}
      {pr.error && (
        <p className={cn("mt-1 flex items-start gap-1.5 text-xs", toneText.warning)} role="alert">
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
          {pr.error}
        </p>
      )}
    </div>
  );
}

function ReviewThreadRow({
  location,
  resolved,
  outdated,
  comments,
  selectable,
  checked,
  sentAt,
  now,
  onToggle,
}: {
  location: string;
  resolved: boolean;
  outdated: boolean;
  comments: { author: string; body: string; url: string; createdAt: string }[];
  selectable: boolean;
  checked: boolean;
  sentAt?: number;
  now: number;
  onToggle: (checked: boolean) => void;
}) {
  const first = comments[0];
  const createdMs = first?.createdAt ? Date.parse(first.createdAt) : Number.NaN;
  return (
    <li
      className={cn(
        "rounded-md border px-3 py-2 transition-colors",
        checked ? "border-primary/50 bg-primary/5" : "bg-muted/30",
        resolved && "opacity-70",
      )}
    >
      <div className="flex flex-wrap items-center gap-2">
        {selectable ? (
          <input
            type="checkbox"
            className="size-3.5 shrink-0 accent-primary"
            checked={checked}
            onChange={(event) => onToggle(event.target.checked)}
            aria-label={`Select review thread ${location}`}
          />
        ) : (
          <MessageSquare className="size-3.5 shrink-0 text-muted-foreground" />
        )}
        <span className="min-w-0 flex-1 truncate font-mono text-xs text-foreground">{location}</span>
        {resolved && (
          <Badge className={cn("shrink-0 px-1.5 text-[10px]", toneSoft.success)}>resolved</Badge>
        )}
        {outdated && (
          <Badge variant="secondary" className="shrink-0 px-1.5 text-[10px]">
            outdated
          </Badge>
        )}
        {sentAt !== undefined && (
          <span className={cn("inline-flex shrink-0 items-center gap-1 rounded-full px-1.5 text-[10px]", toneSoft.info)}>
            <Send className="size-2.5" />
            sent {relativeTime(sentAt, now)}
          </span>
        )}
        {Number.isFinite(createdMs) && (
          <time dateTime={first.createdAt} className="shrink-0 text-[10px] text-muted-foreground tabular-nums">
            {relativeTime(createdMs, now)}
          </time>
        )}
        {first?.url && (
          <a
            href={first.url}
            target="_blank"
            rel="noopener noreferrer"
            className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
            aria-label={`Open thread ${location} on GitHub`}
          >
            <ExternalLink className="size-3" />
          </a>
        )}
      </div>
      <ul className="mt-1.5 space-y-1.5">
        {comments.map((comment, idx) => (
          <li key={comment.url || idx} className="min-w-0 text-xs break-words whitespace-pre-wrap text-muted-foreground">
            <span className="font-medium text-foreground">{comment.author || "reviewer"}</span>{" "}
            {comment.body}
          </li>
        ))}
      </ul>
    </li>
  );
}

/**
 * Review section — the only part of this pane that is actionable, so it is the
 * default. Unresolved threads sit at the top as a work queue; resolved ones
 * collapse into a disclosure because they are history, not work.
 */
function ReviewSection({
  pr,
  selectable,
  isSelected,
  sentAt,
  onToggleThread,
  onToggleAll,
}: {
  pr: PullRequestDetails;
  selectable: boolean;
  isSelected: (threadId: string) => boolean;
  sentAt: (threadId: string) => number | undefined;
  onToggleThread: (threadId: string, checked: boolean) => void;
  onToggleAll: (threadIds: string[], checked: boolean) => void;
}) {
  const now = useNow();
  const [showResolved, setShowResolved] = useState(false);
  const open = useMemo(() => pr.reviewThreads.filter((thread) => !thread.resolved), [pr.reviewThreads]);
  const resolved = useMemo(() => pr.reviewThreads.filter((thread) => thread.resolved), [pr.reviewThreads]);
  const candidates = selectableReviewThreads(pr);
  const allSelected = candidates.length > 0 && candidates.every((thread) => isSelected(thread.id));

  if (pr.reviewThreads.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center gap-1 px-6 py-14 text-center">
        <MessageSquare className="size-5 text-muted-foreground/60" />
        <p className="text-sm font-medium text-foreground">No review comments</p>
        <p className="max-w-xs text-xs text-muted-foreground">
          Reviewer feedback on this pull request shows up here, ready to send back to the agent.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-3 p-3">
      {open.length > 0 ? (
        <section className="space-y-2">
          <div className="flex items-center justify-between gap-2">
            <h3 className="text-xs font-medium text-muted-foreground">
              Needs attention · {open.length}
            </h3>
            {selectable && candidates.length > 1 && (
              <button
                type="button"
                className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
                onClick={() => onToggleAll(candidates.map((thread) => thread.id), !allSelected)}
              >
                {allSelected ? "Deselect all" : "Select all"}
              </button>
            )}
          </div>
          <ul className="space-y-2">
            {open.map((thread) => (
              <ReviewThreadRow
                key={thread.id}
                location={reviewThreadLocation(thread)}
                resolved={false}
                outdated={thread.outdated}
                comments={thread.comments}
                selectable={selectable}
                checked={selectable && isSelected(thread.id)}
                sentAt={sentAt(thread.id)}
                now={now}
                onToggle={(checked) => onToggleThread(thread.id, checked)}
              />
            ))}
          </ul>
        </section>
      ) : (
        <p className={cn("flex items-center gap-1.5 px-1 py-2 text-xs", toneText.success)}>
          <Check className="size-3.5" />
          All review comments resolved.
        </p>
      )}

      {resolved.length > 0 && (
        <section className="space-y-2">
          <button
            type="button"
            onClick={() => setShowResolved((prev) => !prev)}
            aria-expanded={showResolved}
            className="flex w-full items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            <ChevronRight className={cn("size-3.5 transition-transform", showResolved && "rotate-90")} />
            Resolved · {resolved.length}
          </button>
          {showResolved && (
            <ul className="space-y-2">
              {resolved.map((thread) => (
                <ReviewThreadRow
                  key={thread.id}
                  location={reviewThreadLocation(thread)}
                  resolved
                  outdated={thread.outdated}
                  comments={thread.comments}
                  selectable={false}
                  checked={false}
                  now={now}
                  onToggle={() => {}}
                />
              ))}
            </ul>
          )}
        </section>
      )}
    </div>
  );
}

/**
 * Checks section — a rollup answers the only question most people have ("is CI
 * green?"); failing and running checks stay expanded, the green wall collapses.
 */
function ChecksSection({
  pr,
  canSend,
  onSendFailing,
  sending,
}: {
  pr: PullRequestDetails;
  canSend: boolean;
  onSendFailing: (checks: PullRequestCheck[]) => void;
  sending: boolean;
}) {
  const [showPassing, setShowPassing] = useState(false);
  const groups = useMemo(() => {
    const notable: PullRequestCheck[] = [];
    const passing: PullRequestCheck[] = [];
    let failing = 0;
    let pending = 0;
    for (const check of pr.checks) {
      const outcome = checkOutcome(check);
      if (outcome === "success") {
        passing.push(check);
        continue;
      }
      notable.push(check);
      if (outcome === "failure") failing += 1;
      if (outcome === "pending") pending += 1;
    }
    return { notable, passing, failing, pending };
  }, [pr.checks]);

  if (pr.checks.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center gap-1 px-6 py-14 text-center">
        <Circle className="size-5 text-muted-foreground/60" />
        <p className="text-sm font-medium text-foreground">No checks reported</p>
        <p className="max-w-xs text-xs text-muted-foreground">CI results appear here once GitHub reports them.</p>
      </div>
    );
  }

  const renderCheck = (check: PullRequestCheck) => {
    const appearance = checkAppearance(check);
    return (
      <li key={`${check.name}-${check.startedAt}`} className="flex items-center gap-2 py-1 text-xs">
        <span className={cn("flex shrink-0 items-center", appearance.className)}>{appearance.icon}</span>
        {check.detailsUrl ? (
          <a href={check.detailsUrl} target="_blank" rel="noopener noreferrer" className="min-w-0 truncate hover:underline">
            {check.name}
          </a>
        ) : (
          <span className="min-w-0 truncate">{check.name}</span>
        )}
        <span className={cn("ml-auto shrink-0", appearance.className)}>{appearance.label}</span>
      </li>
    );
  };

  return (
    <div className="space-y-3 p-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-md border bg-muted/30 px-3 py-2 text-xs">
        {groups.failing > 0 && (
          <span className={cn("flex items-center gap-1 font-medium", toneText.danger)}>
            <XCircle className="size-3.5" />
            {groups.failing} failing
          </span>
        )}
        {groups.pending > 0 && (
          <span className={cn("flex items-center gap-1", toneText.running)}>
            <Loader2 className="size-3.5 animate-spin" />
            {groups.pending} running
          </span>
        )}
        <span className={cn("flex items-center gap-1", toneText.success)}>
          <CheckCircle2 className="size-3.5" />
          {groups.passing.length} passing
        </span>
        {canSend && groups.failing > 0 && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="ml-auto h-6 gap-1 px-2 text-[11px]"
            disabled={sending}
            onClick={() => onSendFailing(failingChecks(pr.checks))}
          >
            {sending ? <Loader2 className="size-3 animate-spin" /> : <Send className="size-3" />}
            Send failing checks to agent
          </Button>
        )}
      </div>

      {groups.notable.length > 0 && <ul className="divide-y divide-border/50">{groups.notable.map(renderCheck)}</ul>}

      {groups.passing.length > 0 && (
        <div className="space-y-1">
          <button
            type="button"
            onClick={() => setShowPassing((prev) => !prev)}
            aria-expanded={showPassing}
            className="flex w-full items-center gap-1 text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            <ChevronRight className={cn("size-3.5 transition-transform", showPassing && "rotate-90")} />
            Passing · {groups.passing.length}
          </button>
          {showPassing && <ul className="divide-y divide-border/50">{groups.passing.map(renderCheck)}</ul>}
        </div>
      )}
    </div>
  );
}

/** Autonomous implementer/reviewer loop state, as a compact key/value list. */
function LoopSection({
  loop,
  namespace,
  prUrl,
}: {
  loop: PRLoopStatus;
  namespace: string;
  prUrl?: string;
}) {
  const displayPrUrl = loop.prUrl || prUrl;
  const rows: { label: string; value: React.ReactNode }[] = [
    {
      label: "State",
      value: loop.state ? (
        <span
          className={cn(
            "inline-flex rounded-full px-2 py-0.5 text-[11px] font-medium capitalize",
            toneSoft[prLoopTone(loop.state)],
          )}
        >
          {formatPRLoopState(loop.state)}
        </span>
      ) : (
        "—"
      ),
    },
    { label: "Role", value: loop.role || "—" },
    { label: "Round", value: formatPRLoopRound(loop) },
  ];
  if (loop.reviewVerdict) {
    rows.push({
      label: "Verdict",
      value: (
        <span
          className={cn(
            "inline-flex rounded-full px-2 py-0.5 text-[11px] font-medium",
            toneSoft[reviewVerdictTone(loop.reviewVerdict)],
          )}
        >
          {loop.reviewVerdict.replace(/_/g, " ")}
        </span>
      ),
    });
  }
  if (loop.implementerRunName) {
    rows.push({
      label: "Implementer",
      value: (
        <Link
          to={`/runs/${namespace}/${loop.implementerRunName}`}
          className="text-foreground underline-offset-2 hover:text-primary hover:underline"
        >
          {loop.implementerRunName}
        </Link>
      ),
    });
  }
  if (displayPrUrl) {
    rows.push({
      label: "Pull request",
      value: (
        <a
          href={displayPrUrl}
          target="_blank"
          rel="noopener noreferrer"
          className="text-foreground underline-offset-2 hover:text-primary hover:underline"
        >
          {loop.prNumber ? `#${loop.prNumber}` : "Open on GitHub"}
        </a>
      ),
    });
  }

  return (
    <div className="space-y-3 p-3">
      <p className="text-xs text-muted-foreground">
        Autonomous implementer/reviewer progress for this pull request.
      </p>
      <dl className="grid grid-cols-[minmax(84px,auto)_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-xs">
        {rows.map((row) => (
          <div key={row.label} className="contents">
            <dt className="text-muted-foreground">{row.label}</dt>
            <dd className="min-w-0 text-foreground">{row.value}</dd>
          </div>
        ))}
      </dl>
      {loop.reviewSummary && (
        <div className="rounded-md border bg-muted/30 px-3 py-2">
          <p className="mb-1 text-xs font-medium text-muted-foreground">Review summary</p>
          <p className="break-words whitespace-pre-wrap text-xs text-foreground">{loop.reviewSummary}</p>
        </div>
      )}
    </div>
  );
}

/**
 * RunPullRequestPanel owns the inspector's PR tab. It is organised around one
 * pull request at a time: a sticky subject line, a section switcher (review /
 * checks / loop) and a single scroller, instead of the previous flat stack of
 * every PR's every check and every thread. Unresolved review threads can be
 * selected and sent to the agent through the regular steering channel; the
 * selection survives switching PRs and sections, and the send bar is sticky.
 */
export function RunPullRequestPanel({ namespace, name, canSend = false, prLoop, prUrl }: RunPullRequestPanelProps) {
  const [pullRequests, setPullRequests] = useState<PullRequestDetails[] | null>(null);
  const [initialLoading, setInitialLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [sentAt, setSentAt] = useState<ReadonlyMap<string, number>>(new Map());
  const [sendingFixes, setSendingFixes] = useState(false);
  const [sendingChecks, setSendingChecks] = useState(false);
  const [activeUrl, setActiveUrl] = useState<string | null>(null);
  const [section, setSection] = useState<PRSection>("review");

  const load = useCallback(async () => {
    setRefreshing(true);
    try {
      const resp = await client.getAgentRunPullRequests({ namespace, name });
      setPullRequests(resp.pullRequests);
      // Drop selections whose threads disappeared or got resolved since the
      // last refresh so we never send stale feedback to the agent.
      setSelected((prev) => {
        if (prev.size === 0) {
          return prev;
        }
        const valid = new Set<string>();
        for (const pr of resp.pullRequests) {
          for (const thread of selectableReviewThreads(pr)) {
            valid.add(reviewThreadSelectionKey(pr.url, thread.id));
          }
        }
        const next = new Set([...prev].filter((key) => valid.has(key)));
        return next.size === prev.size ? prev : next;
      });
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load pull requests");
    } finally {
      setRefreshing(false);
      setInitialLoading(false);
    }
  }, [namespace, name]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial fetch, same pattern as useAgentRunUsage
    void load();
    const interval = setInterval(() => void load(), POLL_INTERVAL_MS);
    return () => clearInterval(interval);
  }, [load]);

  const toggleThreads = useCallback((prUrlKey: string, threadIds: string[], checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      for (const threadId of threadIds) {
        const key = reviewThreadSelectionKey(prUrlKey, threadId);
        if (checked) {
          next.add(key);
        } else {
          next.delete(key);
        }
      }
      return next;
    });
  }, []);

  const sendToAgent = useCallback(async () => {
    if (!pullRequests || selected.size === 0 || sendingFixes) {
      return;
    }
    const count = selected.size;
    const message = buildReviewFixMessage(pullRequests, selected);
    setSendingFixes(true);
    try {
      await client.sendAgentRunMessage({ namespace, name, message });
      toast.success(`Sent ${count} review comment${count === 1 ? "" : "s"} to the agent`);
      const at = Date.now();
      setSentAt((prev) => {
        const next = new Map(prev);
        for (const key of selected) next.set(key, at);
        return next;
      });
      setSelected(new Set());
    } catch (err) {
      toast.error("Couldn't send review comments", {
        description: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setSendingFixes(false);
    }
  }, [pullRequests, selected, sendingFixes, namespace, name]);

  const active = useMemo(() => {
    if (!pullRequests || pullRequests.length === 0) return null;
    return pullRequests.find((pr) => pr.url === activeUrl) ?? pullRequests[0];
  }, [pullRequests, activeUrl]);

  const sendFailingChecks = useCallback(
    async (checks: PullRequestCheck[]) => {
      if (!active || checks.length === 0 || sendingChecks) return;
      setSendingChecks(true);
      try {
        await client.sendAgentRunMessage({ namespace, name, message: buildFailingChecksMessage(active, checks) });
        toast.success(`Sent ${checks.length} failing check${checks.length === 1 ? "" : "s"} to the agent`);
      } catch (err) {
        toast.error("Couldn't send failing checks", {
          description: err instanceof Error ? err.message : String(err),
        });
      } finally {
        setSendingChecks(false);
      }
    },
    [active, sendingChecks, namespace, name],
  );

  const refreshButton = (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      onClick={() => void load()}
      disabled={refreshing}
      aria-label="Refresh pull requests"
      className="shrink-0 text-muted-foreground hover:text-foreground"
    >
      {refreshing ? <Loader2 className="animate-spin" /> : <RefreshCw />}
    </Button>
  );

  // A run in a review loop still has loop state worth showing when the PR
  // request itself is loading or failed, so only the loop-less case short
  // circuits to a full-pane placeholder.
  const prsUnavailable = pullRequests === null;
  const loadState = (
    <div className="flex flex-col items-center justify-center gap-1 px-6 py-14 text-center">
      {error && !initialLoading ? (
        <>
          <AlertTriangle className="size-5 text-destructive" />
          <p className="text-sm font-medium text-foreground">Couldn't load pull requests</p>
          <p className="max-w-xs text-xs text-muted-foreground" role="alert">
            {error}
          </p>
          <Button type="button" variant="outline" size="sm" className="mt-2" onClick={() => void load()}>
            Retry
          </Button>
        </>
      ) : (
        <p className="flex items-center gap-2 text-sm text-muted-foreground" role="status" aria-live="polite">
          <Loader2 className="size-4 animate-spin" />
          Loading...
        </p>
      )}
    </div>
  );

  if (!prLoop && (prsUnavailable || pullRequests.length === 0)) {
    return (
      <div className="flex h-full min-h-0 flex-col items-center justify-center">
        {prsUnavailable ? (
          loadState
        ) : (
          <div className="flex flex-col items-center justify-center gap-1 px-6 text-center">
            <GitPullRequest className="size-5 text-muted-foreground/60" />
            <p className="text-sm font-medium text-foreground">No pull requests yet</p>
            <p className="max-w-xs text-xs text-muted-foreground">
              Once the agent opens a PR, its checks and review comments show up here.
            </p>
          </div>
        )}
      </div>
    );
  }

  const unresolvedCount = active ? selectableReviewThreads(active).length : 0;
  const failingCount = active ? failingChecks(active.checks).length : 0;
  const sections: { id: PRSection; label: string; count?: number; alert?: boolean }[] = [];
  if (active) {
    sections.push(
      { id: "review", label: "Review", count: unresolvedCount, alert: unresolvedCount > 0 },
      {
        id: "checks",
        label: "Checks",
        count: failingCount > 0 ? failingCount : active.checks.length,
        alert: failingCount > 0,
      },
    );
  }
  if (prLoop) {
    sections.push({ id: "loop", label: "Loop" });
  }
  if (sections.length === 0) {
    // Unreachable in practice: the loop-less empty/failed cases returned above.
    sections.push({ id: "review", label: "Review" });
  }
  // Falls back to the first available section so a loop-only pane opens on Loop.
  const activeSection: PRSection = sections.some((s) => s.id === section) ? section : sections[0].id;

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col">
      <InspectorSubnav<PRSection>
        items={sections}
        value={activeSection}
        onChange={setSection}
        trailing={refreshButton}
      />

      {pullRequests !== null && pullRequests.length > 1 && (
        <div className="flex shrink-0 items-center gap-1 overflow-x-auto border-b px-2 py-1.5 [mask-image:linear-gradient(to_right,black_calc(100%-20px),transparent)] [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          {pullRequests.map((pr) => {
            const isActive = active?.url === pr.url;
            const pending = selectableReviewThreads(pr).length;
            return (
              <button
                key={pr.url}
                type="button"
                onClick={() => setActiveUrl(pr.url)}
                aria-pressed={isActive}
                className={cn(
                  "flex shrink-0 items-center gap-1.5 rounded-full border px-2 py-0.5 font-mono text-[11px] transition-colors",
                  isActive ? "border-border text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
                )}
              >
                {prDisplayLabel(pr)}
                {pending > 0 && (
                  <span className="rounded-full bg-[color:var(--tone-danger)]/12 px-1 text-[10px] tabular-nums text-[color:var(--tone-danger)]">
                    {pending}
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}

      {active && <PullRequestSubject pr={active} />}

      {error && !prsUnavailable && (
        <p className={cn("shrink-0 border-b bg-tone-warning/5 px-3 py-1.5 text-xs", toneText.warning)} role="alert">
          Refresh failed: {error}
        </p>
      )}

      <div className="min-h-0 min-w-0 flex-1 overflow-y-auto">
        {activeSection === "loop" && prLoop ? (
          <LoopSection loop={prLoop} namespace={namespace} prUrl={prUrl} />
        ) : prsUnavailable ? (
          loadState
        ) : !active ? (
          <div className="flex flex-col items-center justify-center gap-1 px-6 py-14 text-center">
            <GitPullRequest className="size-5 text-muted-foreground/60" />
            <p className="text-sm font-medium text-foreground">No pull request yet</p>
            <p className="max-w-xs text-xs text-muted-foreground">
              The review loop is running; PR details appear once the pull request exists.
            </p>
          </div>
        ) : activeSection === "checks" ? (
          <ChecksSection pr={active} canSend={canSend} sending={sendingChecks} onSendFailing={(checks) => void sendFailingChecks(checks)} />
        ) : (
          <ReviewSection
            pr={active}
            selectable={canSend}
            isSelected={(threadId) => selected.has(reviewThreadSelectionKey(active.url, threadId))}
            sentAt={(threadId) => sentAt.get(reviewThreadSelectionKey(active.url, threadId))}
            onToggleThread={(threadId, checked) => toggleThreads(active.url, [threadId], checked)}
            onToggleAll={(threadIds, checked) => toggleThreads(active.url, threadIds, checked)}
          />
        )}
      </div>

      {canSend && selected.size > 0 && (
        <div className="flex shrink-0 items-center justify-between gap-2 border-t bg-background px-3 py-2">
          <span className="text-xs text-muted-foreground">
            {selected.size} review comment{selected.size === 1 ? "" : "s"} selected
          </span>
          <div className="flex items-center gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => setSelected(new Set())} disabled={sendingFixes}>
              Clear
            </Button>
            <Button type="button" size="sm" onClick={() => void sendToAgent()} disabled={sendingFixes}>
              {sendingFixes ? <Loader2 className="animate-spin" /> : <Send />}
              Send to agent
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
