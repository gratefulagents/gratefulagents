/**
 * Pull request URL helpers for runs that create multiple PRs
 * (one per workspace repository).
 */

import type { PullRequestDetails, PullRequestReviewThread } from "@/rpc/platform/service_pb";

/**
 * pullRequestLabel renders a compact human label for a PR URL.
 * GitHub-style URLs (https://github.com/owner/repo/pull/123) become
 * "owner/repo#123"; anything else falls back to the URL's path or the raw URL.
 */
export function pullRequestLabel(url: string): string {
  try {
    const parsed = new URL(url);
    const parts = parsed.pathname.split("/").filter(Boolean);
    const pullIdx = parts.findIndex((part) => part === "pull" || part === "pulls" || part === "merge_requests");
    if (pullIdx >= 2 && pullIdx + 1 < parts.length && /^\d+$/.test(parts[pullIdx + 1])) {
      return `${parts[pullIdx - 2]}/${parts[pullIdx - 1]}#${parts[pullIdx + 1]}`;
    }
    return parts.join("/") || url;
  } catch {
    return url;
  }
}

/**
 * runPullRequestUrls returns every PR URL recorded on a run, falling back to
 * the legacy single-URL field for runs created before multi-PR support.
 */
export function runPullRequestUrls(run: {
  pullRequestUrls?: string[];
  pullRequestUrl?: string;
}): string[] {
  const urls = (run.pullRequestUrls ?? []).filter((url) => url.trim() !== "");
  if (urls.length > 0) {
    return [...new Set(urls)];
  }
  const legacy = run.pullRequestUrl?.trim();
  return legacy ? [legacy] : [];
}

/**
 * reviewThreadSelectionKey identifies a review thread uniquely across every
 * PR of a run (thread ids are only unique within a repository).
 */
export function reviewThreadSelectionKey(prUrl: string, threadId: string): string {
  return `${prUrl}::${threadId}`;
}

/** reviewThreadLocation renders "path:line" (or just the path) for a thread. */
export function reviewThreadLocation(thread: PullRequestReviewThread): string {
  return thread.line > 0 ? `${thread.path}:${thread.line}` : thread.path;
}

/**
 * selectableReviewThreads returns the threads that can be sent to the agent
 * as fix requests: unresolved ones (resolved feedback needs no action).
 */
export function selectableReviewThreads(pr: PullRequestDetails): PullRequestReviewThread[] {
  return pr.reviewThreads.filter((thread) => !thread.resolved);
}

/**
 * buildReviewFixMessage renders the selected review threads as a steering
 * message asking the agent to address each one. `selected` holds
 * reviewThreadSelectionKey entries.
 */
export function buildReviewFixMessage(pullRequests: PullRequestDetails[], selected: ReadonlySet<string>): string {
  const sections: string[] = [];
  let count = 0;
  for (const pr of pullRequests) {
    const threads = pr.reviewThreads.filter((thread) => selected.has(reviewThreadSelectionKey(pr.url, thread.id)));
    if (threads.length === 0) {
      continue;
    }
    const label = pr.repository && pr.number ? `${pr.repository}#${pr.number}` : pullRequestLabel(pr.url);
    const items = threads.map((thread) => {
      count += 1;
      const comments = thread.comments
        .map((comment) => `${comment.author ? `@${comment.author}` : "reviewer"}: ${comment.body}`.trim())
        .join("\n");
      const outdated = thread.outdated ? " (marked outdated — re-check against the current code)" : "";
      return `${count}. \`${reviewThreadLocation(thread)}\` — review thread ${thread.id}${outdated}\n${comments}`;
    });
    sections.push(`${label} (${pr.url}):\n\n${items.join("\n\n")}`);
  }
  const plural = count === 1 ? "comment" : "comments";
  return [
    `Please address the following PR review ${plural}:`,
    ...sections,
    "For each item, make the fix, commit, and push to the PR branch. Then reply to and resolve each addressed review thread.",
  ].join("\n\n");
}
