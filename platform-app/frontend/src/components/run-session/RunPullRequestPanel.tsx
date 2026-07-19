import { useCallback, useEffect, useState } from "react";
import {
  AlertTriangle,
  CheckCircle2,
  Circle,
  ExternalLink,
  GitPullRequest,
  Loader2,
  MessageSquare,
  RefreshCw,
  Send,
  XCircle,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toaster";
import { client } from "@/lib/client";
import {
  buildReviewFixMessage,
  pullRequestLabel,
  reviewThreadLocation,
  reviewThreadSelectionKey,
  selectableReviewThreads,
} from "@/lib/pullRequests";
import { cn } from "@/lib/utils";
import type { PullRequestCheck, PullRequestDetails } from "@/rpc/platform/service_pb";

const POLL_INTERVAL_MS = 30_000;

interface RunPullRequestPanelProps {
  namespace: string;
  name: string;
  /** Whether the viewer may steer the agent (mirrors the chat composer gate). */
  canSend?: boolean;
}

function prStateBadgeClass(state: string): string {
  switch (state.toLowerCase()) {
    case "open":
      return "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400";
    case "merged":
      return "bg-purple-500/10 text-purple-600 dark:text-purple-400";
    case "closed":
      return "bg-destructive/10 text-destructive";
    default:
      return "bg-muted text-muted-foreground";
  }
}

function checkAppearance(check: PullRequestCheck): { icon: React.ReactNode; label: string; className: string } {
  if (check.status.toLowerCase() !== "completed") {
    return {
      icon: <Loader2 className="size-3.5 animate-spin" />,
      label: check.status || "pending",
      className: "text-amber-600 dark:text-amber-400",
    };
  }
  switch (check.conclusion.toLowerCase()) {
    case "success":
      return {
        icon: <CheckCircle2 className="size-3.5" />,
        label: "success",
        className: "text-emerald-600 dark:text-emerald-400",
      };
    case "failure":
    case "error": // legacy commit-status error state
    case "timed_out":
    case "startup_failure":
      return {
        icon: <XCircle className="size-3.5" />,
        label: check.conclusion,
        className: "text-destructive",
      };
    case "cancelled":
    case "action_required":
      return {
        icon: <AlertTriangle className="size-3.5" />,
        label: check.conclusion,
        className: "text-amber-600 dark:text-amber-400",
      };
    default:
      return {
        icon: <Circle className="size-3.5" />,
        label: check.conclusion || "neutral",
        className: "text-muted-foreground",
      };
  }
}

interface PullRequestCardProps {
  pr: PullRequestDetails;
  selectable: boolean;
  isSelected: (threadId: string) => boolean;
  onToggleThread: (threadId: string, checked: boolean) => void;
  onToggleAll: (threadIds: string[], checked: boolean) => void;
}

function PullRequestCard({ pr, selectable, isSelected, onToggleThread, onToggleAll }: PullRequestCardProps) {
  const label = pr.repository && pr.number ? `${pr.repository}#${pr.number}` : pullRequestLabel(pr.url);
  const candidates = selectableReviewThreads(pr);
  const allSelected = candidates.length > 0 && candidates.every((thread) => isSelected(thread.id));
  return (
    <div className="rounded-lg border">
      <div className="flex flex-wrap items-center gap-2 border-b px-4 py-3">
        <GitPullRequest className="size-4 shrink-0 text-muted-foreground" />
        <span className="font-mono text-sm text-foreground">{label}</span>
        {pr.title && <span className="min-w-0 flex-1 truncate text-sm font-medium">{pr.title}</span>}
        {pr.state && <Badge className={prStateBadgeClass(pr.state)}>{pr.state.toLowerCase()}</Badge>}
        {pr.reviewDecision && <Badge variant="outline">{pr.reviewDecision.toLowerCase().replaceAll("_", " ")}</Badge>}
        <a
          href={pr.url}
          target="_blank"
          rel="noopener noreferrer"
          className="text-muted-foreground transition-colors hover:text-foreground"
          aria-label={`Open ${label} on GitHub`}
        >
          <ExternalLink className="size-4" />
        </a>
      </div>

      {pr.error && (
        <p className="flex items-start gap-1.5 px-4 py-2 text-xs text-amber-600 dark:text-amber-400" role="alert">
          <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
          {pr.error}
        </p>
      )}

      <div className="space-y-4 px-4 py-3">
        <section>
          <h3 className="mb-1.5 text-xs font-medium text-muted-foreground">
            Checks{pr.checks.length > 0 ? ` · ${pr.checks.length}` : ""}
          </h3>
          {pr.checks.length === 0 ? (
            <p className="text-xs text-muted-foreground/70">No checks reported.</p>
          ) : (
            <ul className="space-y-1">
              {pr.checks.map((check) => {
                const appearance = checkAppearance(check);
                return (
                  <li key={`${check.name}-${check.startedAt}`} className="flex items-center gap-2 text-xs">
                    <span className={cn("flex shrink-0 items-center", appearance.className)}>{appearance.icon}</span>
                    {check.detailsUrl ? (
                      <a
                        href={check.detailsUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="truncate hover:underline"
                      >
                        {check.name}
                      </a>
                    ) : (
                      <span className="truncate">{check.name}</span>
                    )}
                    <span className={cn("shrink-0", appearance.className)}>{appearance.label}</span>
                  </li>
                );
              })}
            </ul>
          )}
        </section>

        <section>
          <div className="mb-1.5 flex items-center justify-between gap-2">
            <h3 className="text-xs font-medium text-muted-foreground">
              Review threads{pr.reviewThreads.length > 0 ? ` · ${pr.reviewThreads.length}` : ""}
            </h3>
            {selectable && candidates.length > 1 && (
              <button
                type="button"
                className="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
                onClick={() =>
                  onToggleAll(
                    candidates.map((thread) => thread.id),
                    !allSelected,
                  )
                }
              >
                {allSelected ? "Deselect all" : "Select all"}
              </button>
            )}
          </div>
          {pr.reviewThreads.length === 0 ? (
            <p className="text-xs text-muted-foreground/70">No review threads.</p>
          ) : (
            <ul className="space-y-2">
              {pr.reviewThreads.map((thread) => {
                const threadSelectable = selectable && !thread.resolved;
                const checked = threadSelectable && isSelected(thread.id);
                return (
                  <li
                    key={thread.id}
                    className={cn("rounded-md border bg-muted/30 px-3 py-2", checked && "border-primary/50 bg-primary/5")}
                  >
                    <div className="flex flex-wrap items-center gap-2">
                      {threadSelectable ? (
                        <input
                          type="checkbox"
                          className="size-3.5 shrink-0 accent-primary"
                          checked={checked}
                          onChange={(event) => onToggleThread(thread.id, event.target.checked)}
                          aria-label={`Select review thread ${reviewThreadLocation(thread)}`}
                        />
                      ) : (
                        <MessageSquare className="size-3.5 shrink-0 text-muted-foreground" />
                      )}
                      <span className="font-mono text-xs text-foreground">{reviewThreadLocation(thread)}</span>
                      {thread.resolved && (
                        <Badge className="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400">resolved</Badge>
                      )}
                      {thread.outdated && <Badge variant="secondary">outdated</Badge>}
                    </div>
                    <ul className="mt-1.5 space-y-1.5">
                      {thread.comments.map((comment, idx) => (
                        <li key={comment.url || idx} className="text-xs">
                          <span className="font-medium text-foreground">{comment.author}</span>
                          <p className="mt-0.5 whitespace-pre-wrap break-words text-muted-foreground">{comment.body}</p>
                        </li>
                      ))}
                    </ul>
                  </li>
                );
              })}
            </ul>
          )}
        </section>
      </div>
    </div>
  );
}

// RunPullRequestPanel shows the CI checks and review threads for every pull
// request created by a run, refreshing on demand and every 30s while visible.
// Unresolved review threads can be selected and sent to the agent as a fix
// request through the regular steering-message channel.
export function RunPullRequestPanel({ namespace, name, canSend = false }: RunPullRequestPanelProps) {
  const [pullRequests, setPullRequests] = useState<PullRequestDetails[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [sendingFixes, setSendingFixes] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
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
      setLoading(false);
    }
  }, [namespace, name]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- initial fetch, same pattern as useAgentRunUsage
    void load();
    const interval = setInterval(() => void load(), POLL_INTERVAL_MS);
    return () => clearInterval(interval);
  }, [load]);

  const toggleThreads = useCallback((prUrl: string, threadIds: string[], checked: boolean) => {
    setSelected((prev) => {
      const next = new Set(prev);
      for (const threadId of threadIds) {
        const key = reviewThreadSelectionKey(prUrl, threadId);
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
      setSelected(new Set());
    } catch (err) {
      toast.error("Couldn't send review comments", {
        description: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setSendingFixes(false);
    }
  }, [pullRequests, selected, sendingFixes, namespace, name]);

  return (
    <div className="space-y-3 p-4">
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
          <GitPullRequest className="size-3.5" />
          <span>Pull requests</span>
          {pullRequests && pullRequests.length > 0 && (
            <span className="text-muted-foreground/70">· {pullRequests.length}</span>
          )}
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="size-6"
          onClick={() => void load()}
          disabled={loading}
          aria-label="Refresh pull requests"
        >
          {loading ? <Loader2 className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
        </Button>
      </div>

      {pullRequests === null ? (
        error ? (
          <p className="text-sm text-destructive" role="alert">
            Error: {error}
          </p>
        ) : (
          <p className="text-sm text-muted-foreground" role="status" aria-live="polite">
            Loading...
          </p>
        )
      ) : (
        <>
          {error && (
            <p className="text-xs text-muted-foreground" role="alert">
              Refresh failed: {error}
            </p>
          )}
          {pullRequests.length === 0 ? (
            <p className="text-sm text-muted-foreground">No pull requests yet.</p>
          ) : (
            pullRequests.map((pr) => (
              <PullRequestCard
                key={pr.url}
                pr={pr}
                selectable={canSend}
                isSelected={(threadId) => selected.has(reviewThreadSelectionKey(pr.url, threadId))}
                onToggleThread={(threadId, checked) => toggleThreads(pr.url, [threadId], checked)}
                onToggleAll={(threadIds, checked) => toggleThreads(pr.url, threadIds, checked)}
              />
            ))
          )}
          {canSend && selected.size > 0 && (
            <div className="sticky bottom-2 flex items-center justify-between gap-2 rounded-lg border bg-background/95 px-3 py-2 shadow-md backdrop-blur">
              <span className="text-xs text-muted-foreground">
                {selected.size} review comment{selected.size === 1 ? "" : "s"} selected
              </span>
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => setSelected(new Set())}
                  disabled={sendingFixes}
                >
                  Clear
                </Button>
                <Button type="button" size="sm" onClick={() => void sendToAgent()} disabled={sendingFixes}>
                  {sendingFixes ? <Loader2 className="size-3.5 animate-spin" /> : <Send className="size-3.5" />}
                  Send to agent
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}
