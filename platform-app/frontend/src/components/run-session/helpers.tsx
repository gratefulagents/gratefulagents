import type React from "react";
import { Link } from "react-router-dom";

import { MarkdownViewer } from "@/components/MarkdownViewer";
import { DetailSection, Fact, FactLink, FactList } from "@/components/detail-page";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog";
import { prLoopTone, toneSoft, toneText } from "@/lib/status";
import type { StatusTone } from "@/lib/status";
import { runStepLabel } from "@/lib/runStatus";
import { cn } from "@/lib/utils";
import type { ActivityEntry, ChatMessage, PRLoopStatus } from "@/rpc/platform/service_pb";

export type QuickAction = {
  id: string;
  label: string;
  mode?: string;
  style?: string;
};

export type TimelineItem =
  | { kind: "user-request"; key: string; content: string }
  | { kind: "message"; key: string; role: string; content: string; timestamp: bigint; imageDataUrls?: string[] }
  | { kind: "activity"; key: string; entries: ActivityEntry[]; isLive: boolean }
  | { kind: "pending"; key: string; content: string; actions?: QuickAction[] }
  | { kind: "thinking"; key: string; phase: string };

export type BannerConfig = {
  label: string;
  textColor: string;
  dotColor: string;
};

export type MainView = "chat" | "graph" | "diff" | "pr" | "errors" | "trace";

export function isMainView(value: string | null): value is MainView {
  return value === "chat" || value === "graph" || value === "diff" || value === "pr" || value === "errors" || value === "trace";
}

export const autoChatKickoffRequest =
  "Please ask me what feature or fix I want to work on, then help me produce an implementation plan.";
export const autoExecutionKickoffRequest =
  "Please help me implement the requested change in this project. Ask focused follow-up questions only when needed, then continue execution in this same run.";

export const pendingBannerConfig: Record<string, BannerConfig> = {
  question: {
    label: "Waiting for your input",
    textColor: toneText.warning,
    dotColor: "bg-[color:var(--tone-warning)]",
  },
  approval: {
    label: "Approval required",
    textColor: toneText.purple,
    dotColor: "bg-[color:var(--tone-purple)]",
  },
  plan_review: {
    label: "Plan ready for review",
    textColor: toneText.purple,
    dotColor: "bg-[color:var(--tone-purple)]",
  },
  turn_limit: {
    label: "Turn limit reached",
    textColor: toneText.warning,
    dotColor: "bg-[color:var(--tone-warning)]",
  },
  circuit_breaker: {
    label: "Agent needs help",
    textColor: toneText.danger,
    dotColor: "bg-[color:var(--tone-danger)]",
  },
  stopped: {
    label: "Stopped",
    textColor: toneText.neutral,
    dotColor: "bg-muted-foreground",
  },
  idle: {
    label: "Ready for another message",
    textColor: "text-muted-foreground",
    dotColor: "bg-muted-foreground/40",
  },
};

export const runtimeExtensionPresets = [
  { label: "30m", value: "30m" },
  { label: "1h", value: "1h" },
  { label: "2h", value: "2h" },
  { label: "4h", value: "4h" },
];

export function fmtTokens(n: number): string {
  if (n >= 1_000_000) {
    return `${(n / 1_000_000).toFixed(1)}M`;
  }
  if (n >= 1_000) {
    return `${(n / 1_000).toFixed(1)}k`;
  }
  return String(n);
}

export function fmtUsd(n: number): string {
  return n.toFixed(4);
}

/** Route for a run's source resource, based on its project/trigger kind. */
export function sourceHref(kind: string, namespace: string, name: string): string {
  switch (kind) {
    case "LinearProject":
      return `/linear/${namespace}/${name}`;
    case "GitHubRepository":
      return `/github/${namespace}/${name}`;
    case "Cron":
      return `/cron/${namespace}/${name}`;
    case "SlackAgent":
      return `/slack/${namespace}/${name}`;
    case "Project":
    default:
      return `/projects/${namespace}/${name}`;
  }
}

export function parseUsd(raw?: string): number | null {
  if (!raw) return null;
  const parsed = Number(raw);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
}

export function thinkingLabel(phase: string, step: string): string {
  // Pending is still scheduler-owned. A pre-seeded `starting` step does not
  // mean the worker has begun, so phase takes precedence here.
  if (phase === "Pending") return "Queued to start…";
  const stepLabel = runStepLabel(step);
  if (stepLabel) return `${stepLabel}…`;
  if (phase === "Admitted" || phase === "Provisioning") return "Provisioning workspace…";
  if (phase === "Running") return "Working…";
  return "Waiting for a status update…";
}

export function getActionButtonVariant(style?: string): "default" | "destructive" | "outline" {
  if (style === "primary") {
    return "default";
  }
  if (style === "destructive") {
    return "destructive";
  }
  return "outline";
}

export function mapPendingAction(action: { id: string; label: string; mode?: string; style?: string }): QuickAction {
  return {
    id: action.id,
    label: action.label,
    mode: action.mode || undefined,
    style: action.style || undefined,
  };
}

export function formatPRLoopState(state: string): string {
  return state.replace(/_/g, " ");
}

export function formatPRLoopRound(loop: PRLoopStatus): string {
  if (!loop.reviewRound) {
    return "—";
  }
  return `${loop.reviewRound} of ${loop.maxRounds || 3}`;
}

export function reviewVerdictTone(verdict: string): StatusTone {
  if (verdict === "approve") return "success";
  if (verdict === "request_changes") return "warning";
  return "neutral";
}

export function PRLoopCard({
  loop,
  namespace,
  prUrl,
}: {
  loop: PRLoopStatus;
  namespace: string;
  prUrl: string;
}) {
  const displayPrUrl = loop.prUrl || prUrl;
  return (
    <div className="shrink-0 border-b px-4 py-3">
      <DetailSection
        title="PR review loop"
        description="Autonomous implementer/reviewer progress for this pull request."
      >
        <div className="rounded-lg border border-border/60 bg-muted/20 px-3 py-2.5">
          <FactList className="grid-cols-[minmax(100px,140px)_minmax(0,1fr)] gap-y-1.5">
            <Fact
              label="State"
              value={
                loop.state ? (
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
                )
              }
            />
            <Fact label="Role" value={loop.role || "—"} />
            <Fact label="Round" value={formatPRLoopRound(loop)} />
            <Fact
              label="Pull request"
              value={
                displayPrUrl ? (
                  <FactLink href={displayPrUrl}>
                    {loop.prNumber ? `#${loop.prNumber}` : "Pull request"}
                  </FactLink>
                ) : loop.prNumber ? (
                  `#${loop.prNumber}`
                ) : (
                  "—"
                )
              }
            />
            {loop.implementerRunName && (
              <Fact
                label="Implementer"
                value={
                  <Link
                    to={`/runs/${namespace}/${loop.implementerRunName}`}
                    className="text-foreground underline-offset-2 hover:text-primary hover:underline"
                  >
                    {loop.implementerRunName}
                  </Link>
                }
              />
            )}
            {loop.reviewVerdict && (
              <Fact
                label="Verdict"
                value={
                  <span
                    className={cn(
                      "inline-flex rounded-full px-2 py-0.5 text-[11px] font-medium",
                      toneSoft[reviewVerdictTone(loop.reviewVerdict)],
                    )}
                  >
                    {loop.reviewVerdict.replace(/_/g, " ")}
                  </span>
                }
              />
            )}
            {loop.reviewSummary && (
              <Fact label="Summary" value={loop.reviewSummary} wrap />
            )}
          </FactList>
        </div>
      </DetailSection>
    </div>
  );
}

export function messageTimelineKey(message: ChatMessage, occurrence: number): string {
  if (message.id !== 0n) {
    return `message:${message.id.toString()}`;
  }
  return `message:${message.timestampUnix.toString()}:${message.role}:${occurrence}`;
}

/**
 * Splits the conversation into the delivered transcript and pending user
 * messages. A pending message has been accepted by the platform but not yet
 * handed to the agent loop — it renders as a queued/steering chip above the
 * composer instead of as a chat bubble, and joins the transcript only once
 * the agent actually consumes it. Pending messages with no visible content
 * are dropped entirely.
 */
export function partitionConversation(messages: ChatMessage[]): {
  delivered: ChatMessage[];
  pending: ChatMessage[];
} {
  const delivered: ChatMessage[] = [];
  const pending: ChatMessage[] = [];
  for (const message of messages) {
    if (!message.pending) {
      delivered.push(message);
    } else if (message.content.trim() !== "" || message.imageDataUrls.length > 0) {
      pending.push(message);
    }
  }
  return { delivered, pending };
}

/**
 * Timestamp used to slot a message into the activity timeline. User messages
 * anchor to the moment the agent consumed them (when known) rather than when
 * they were typed, so activity that ran while a message sat queued renders
 * before the bubble instead of after it.
 */
export function messageDeliveryTimestamp(message: ChatMessage): bigint {
  if (message.role === "user" && message.deliveredAtUnix > 0n) {
    return message.deliveredAtUnix;
  }
  return message.timestampUnix;
}

/**
 * Orders transcript messages by when they became part of the agent-visible
 * conversation. Queued user messages may be created before an older turn's
 * assistant reply but delivered afterwards, so database ID order is not a
 * valid display order. Durable IDs provide a stable tie-breaker.
 */
export function orderDeliveredMessages(messages: ChatMessage[]): ChatMessage[] {
  return messages
    .map((message, sourceIndex) => ({ message, sourceIndex }))
    .sort((a, b) => {
      if (a.message.deliverySequence > 0n && b.message.deliverySequence > 0n && a.message.deliverySequence !== b.message.deliverySequence) {
        return a.message.deliverySequence < b.message.deliverySequence ? -1 : 1;
      }
      const aTimestamp = messageDeliveryTimestamp(a.message);
      const bTimestamp = messageDeliveryTimestamp(b.message);
      if (aTimestamp !== bTimestamp) return aTimestamp < bTimestamp ? -1 : 1;
      if (a.message.role !== b.message.role && (a.message.role === "user" || b.message.role === "user")) {
        const user = a.message.role === "user" ? a.message : b.message;
        // For a user row with an explicit delivery stamp, an assistant/system
        // row created in that same second may belong to the older turn; keep
        // it first. Non-user role ties fall through to durable ID/source order.
        const userFirst = user.deliveredAtUnix === 0n;
        return (a.message.role === "user") === userFirst ? -1 : 1;
      }
      if (a.message.id !== 0n && b.message.id !== 0n && a.message.id !== b.message.id) {
        return a.message.id < b.message.id ? -1 : 1;
      }
      return a.sourceIndex - b.sourceIndex;
    })
    .map(({ message }) => message);
}

/**
 * Assigns activity entries to conversation-message segments. A sub-agent
 * task's events can span user-message boundaries (the user types while tasks
 * run); slicing strictly by timestamp splits the task — the earlier segment's
 * card never sees the completion (stuck "running") and the later segment
 * renders a duplicate. Every task-tagged entry is therefore anchored to the
 * timestamp of the task's FIRST entry, keeping a task's whole lifecycle in
 * one segment. Entries after the last message land in `trailing`.
 */
export function bucketActivityByMessage(
  entries: ActivityEntry[],
  messageTimestamps: bigint[],
): { segments: ActivityEntry[][]; trailing: ActivityEntry[] } {
  const segments: ActivityEntry[][] = messageTimestamps.map(() => []);
  const trailing: ActivityEntry[] = [];
  // Entries and messages are both time-ordered, so a single monotone cursor
  // replaces a per-entry scan. A task's segment is decided once, at its first
  // entry (where the cursor sits at that moment), and cached for the rest of
  // its lifecycle.
  const taskSegment = new Map<string, number>();
  let cursor = 0;
  for (const e of entries) {
    let seg: number;
    const cached = e.taskId ? taskSegment.get(e.taskId) : undefined;
    if (cached !== undefined) {
      seg = cached;
    } else {
      while (cursor < messageTimestamps.length && e.timestampUnix > messageTimestamps[cursor]) {
        cursor += 1;
      }
      seg = cursor;
      if (e.taskId) taskSegment.set(e.taskId, seg);
    }
    if (seg >= messageTimestamps.length) trailing.push(e);
    else segments[seg].push(e);
  }
  return { segments, trailing };
}

export function activityEntryIdentity(entry: ActivityEntry): string {
  return [
    entry.timestampUnix.toString(),
    entry.session,
    entry.type,
    entry.toolUseId || "-",
    entry.taskId || "-",
    entry.parentCallId || "-",
    entry.step || "-",
    entry.tool || "-",
  ].join(":");
}

export function activityGroupKey(entries: ActivityEntry[]): string {
  const firstEntry = entries[0];
  return `activity:${activityEntryIdentity(firstEntry)}`;
}

export function PlanDialogContent({ planContent }: { planContent: string }) {
  return (
    <DialogContent className="flex max-h-[85vh] w-[min(92vw,48rem)] max-w-[min(92vw,48rem)] flex-col overflow-hidden sm:max-w-[min(92vw,48rem)]">
      <DialogHeader>
        <DialogTitle className="text-sm">Plan</DialogTitle>
      </DialogHeader>
      <div className="min-h-0 flex-1 overflow-y-auto rounded-lg border border-border/60 bg-muted/20">
        <div className="px-6 py-5">
          <MarkdownViewer content={planContent} />
        </div>
      </div>
    </DialogContent>
  );
}

export function renderPlanDialogButton(planContent: string, trigger: React.ReactElement) {
  return (
    <Dialog>
      <DialogTrigger render={trigger} />
      <PlanDialogContent planContent={planContent} />
    </Dialog>
  );
}
