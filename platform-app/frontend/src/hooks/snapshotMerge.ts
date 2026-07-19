import type { ActivityEntry, AgentRun, ChatMessage, SubagentGraph } from "@/rpc/platform/service_pb";

/**
 * Helpers for the full-snapshot watch streams: the backend re-sends the whole
 * entries array / conversation every frame, so we reuse previous object
 * references for the stable prefix (entries before the last one are
 * effectively immutable) and bail out of setState when nothing changed.
 * Received protobuf messages are never mutated — only references are reused.
 */

function sameEntryIdentity(prev: ActivityEntry, next: ActivityEntry): boolean {
  return (
    prev.timestampUnix === next.timestampUnix &&
    prev.type === next.type &&
    prev.toolUseId === next.toolUseId
  );
}

function sameEntryContent(prev: ActivityEntry, next: ActivityEntry): boolean {
  return (
    sameEntryIdentity(prev, next) &&
    (prev.message?.length ?? 0) === (next.message?.length ?? 0) &&
    (prev.output?.length ?? 0) === (next.output?.length ?? 0)
  );
}

/**
 * Merge a full activity-log snapshot into the previous entries array.
 * Returns `prev` unchanged when the snapshot carries nothing new (so React
 * can bail out), reuses previous entry references for the stable prefix, and
 * falls back to the raw snapshot on any reset/rewrite.
 */
export function mergeActivityEntries(prev: ActivityEntry[], next: ActivityEntry[]): ActivityEntry[] {
  if (prev.length === 0 || next.length < prev.length) {
    return next;
  }
  for (let i = 0; i < prev.length - 1; i++) {
    if (!sameEntryIdentity(prev[i], next[i])) {
      return next;
    }
    // assistant_thinking entries grow in place while a model streams
    // reasoning, so a non-last one can still change content: adopt the
    // snapshot when its message length differs.
    if (
      prev[i].type === "assistant_thinking" &&
      (prev[i].message?.length ?? 0) !== (next[i].message?.length ?? 0)
    ) {
      return next;
    }
  }
  const lastIdx = prev.length - 1;
  if (!sameEntryContent(prev[lastIdx], next[lastIdx])) {
    return next;
  }
  if (next.length === prev.length) {
    return prev;
  }
  return [...prev, ...next.slice(prev.length)];
}

/** Cheap fingerprint for a subagent graph so unchanged graphs skip setState. */
export function subagentGraphFingerprint(graph: SubagentGraph | undefined): string {
  if (!graph) {
    return "";
  }
  const nodes = graph.nodes ?? [];
  const edges = graph.edges ?? [];
  return `${nodes.length}|${edges.length}|${nodes.map((n) => n.status).join(",")}`;
}

function sameMessage(prev: ChatMessage, next: ChatMessage, isLastPrev: boolean): boolean {
  if (
    prev.id !== next.id ||
    prev.role !== next.role ||
    prev.timestampUnix !== next.timestampUnix ||
    prev.pending !== next.pending ||
    prev.deliveredAtUnix !== next.deliveredAtUnix ||
    prev.queueMode !== next.queueMode ||
    prev.deliverySequence !== next.deliverySequence ||
    prev.deliveryState !== next.deliveryState
  ) {
    return false;
  }
  if (isLastPrev) {
    return (prev.content?.length ?? 0) === (next.content?.length ?? 0);
  }
  return true;
}

/**
 * Reuse previous conversation message references for the stable prefix of a
 * full conversation snapshot. Falls back to the snapshot array on mismatch.
 */
export function mergeConversation(prev: ChatMessage[], next: ChatMessage[]): ChatMessage[] {
  if (prev.length === 0 || next.length < prev.length) {
    return next;
  }
  for (let i = 0; i < prev.length; i++) {
    if (!sameMessage(prev[i], next[i], i === prev.length - 1)) {
      return next;
    }
  }
  if (next.length === prev.length) {
    return prev;
  }
  return [...prev, ...next.slice(prev.length)];
}

/**
 * Cheap fingerprint over the user-visible AgentRun fields; when it is
 * unchanged between two full snapshots the update is skipped entirely.
 */
export function agentRunFingerprint(run: AgentRun): string {
  const conversation = run.conversation ?? [];
  const last = conversation[conversation.length - 1];
  return [
    run.phase,
    run.queueState,
    run.blockedReason,
    run.currentStep,
    run.lastError,
    run.displayName,
    run.intentTitle,
    run.costUsd,
    run.contextTokens,
    run.inputTokens,
    run.outputTokens,
    run.sendReady,
    run.sendReadinessReason,
    run.planUpdatedAt,
    run.currentPlan?.length ?? 0,
    run.modeName,
    run.modeRefName,
    run.modeInstructions,
    run.model,
    run.resolvedModel,
    run.overseer?.modeRefName,
    run.overseer?.modeRefVersion,
    run.overseer?.modeRefChannel,
    run.overseer?.model,
    run.overseer?.authority,
    run.overseer?.intervalMinutes,
    run.overseer?.maxInterventions,
    run.overseerDetaching,
    run.overseerSummary?.runName,
    run.overseerSummary?.state,
    run.overseerSummary?.checkpointsHandled,
    run.overseerSummary?.interventionsUsed,
    run.overseerSummary?.completionRejectionsUsed,
    run.overseerSummary?.lastVerdict,
    run.overseerSummary?.lastSummary,
    run.overseerSummary?.lastVerdictAtUnix,
    run.myPermission,
    run.reviewArtifactKind,
    run.reviewArtifactName,
    run.pullRequestUrl,
    run.pendingActions?.length ?? 0,
    run.userInputRequest?.type ?? "",
    run.userInputRequest?.message ?? "",
    run.userInputRequest?.actions?.map((action) => [action.id, action.label, action.mode, action.style].join(":")) ?? [],
    run.pullRequestUrls?.length ?? 0,
    run.children?.length ?? 0,
    run.completedAtUnix,
    conversation.length,
    conversation.map((message) => [
      message.id,
      message.pending,
      message.deliveredAtUnix,
      message.queueMode,
      message.deliverySequence,
      message.deliveryState,
      message.content?.length ?? 0,
      message.imageDataUrls?.length ?? 0,
    ].join(":")),
    last?.content?.length ?? 0,
  ].join("|");
}

/**
 * Merge a full AgentRun snapshot into the previous one: returns `prev` when
 * nothing user-visible changed, otherwise the new run with conversation
 * message references reused for the stable prefix.
 */
export function mergeAgentRun(prev: AgentRun | null, next: AgentRun): AgentRun {
  if (!prev) {
    return next;
  }
  if (agentRunFingerprint(prev) === agentRunFingerprint(next)) {
    return prev;
  }
  const conversation = mergeConversation(prev.conversation ?? [], next.conversation ?? []);
  if (!next.conversation || conversation === next.conversation) {
    return next;
  }
  return { ...next, conversation };
}
