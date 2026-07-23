import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import { FileText, Loader2 } from "lucide-react";

import { UnifiedDiffViewer } from "@/components/diff/UnifiedDiffViewer";
import { DiffRepoSelector } from "@/components/diff/DiffRepoSelector";
import { NewFilesBrowser } from "@/components/diff/NewFilesBrowser";
import { MarkdownViewer } from "@/components/MarkdownViewer";
import { PlanApprovalPanel } from "@/components/PlanApprovalPanel";
import { SubagentGraphView } from "@/components/SubagentGraphView";
import { EvidenceGatesCard } from "@/components/VerificationEvidenceCard";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { toast } from "@/components/ui/toaster";
import { useAgentRun } from "@/hooks/useAgentRun";
import { useAgentRunErrors } from "@/hooks/useAgentRunErrors";
import { useAgentRunLogs } from "@/hooks/useAgentRunLogs";
import { useAgentTrace } from "@/hooks/useAgentTrace";
import { useImageAttachments } from "@/hooks/useImageAttachments";
import { useDiff } from "@/hooks/useDiff";
import { useRepositories } from "@/hooks/useRepositories";
import { useRunActivityLog } from "@/hooks/useRunActivityLog";
import { useActivityEntryDetail } from "@/hooks/useActivityEntryDetail";
import { ActivityDetailProvider } from "@/components/activity-log/detailContext";
import { usePresence } from "@/hooks/usePresence";
import { useAgentRunUsage } from "@/hooks/useAgentRunUsage";
import { client } from "@/lib/client";
import { aggregateTraceUsage } from "@/lib/traceUsage";
import { currentContextTokens } from "@/lib/contextUsage";
import { toneSoft, toneText } from "@/lib/status";
import { runPullRequestUrls } from "@/lib/pullRequests";
import { cn } from "@/lib/utils";
import { AgentRunMessageMode, type ChatMessage } from "@/rpc/platform/service_pb";
import { RunSessionFooter } from "@/components/run-session/RunSessionFooter";
import { RunSessionHeader } from "@/components/run-session/RunSessionHeader";
import { RunSessionTracePane } from "@/components/run-session/RunSessionTracePane";
import { RunSessionErrorsPane } from "@/components/run-session/RunSessionErrorsPane";
import { RunSessionLogsPane } from "@/components/run-session/RunSessionLogsPane";
import { RunPullRequestPanel } from "@/components/run-session/RunPullRequestPanel";
import { ChatScrollControls } from "@/components/run-session/ChatScrollControls";
import { buildSlashCommands, type SlashCommand } from "@/components/run-session/slashCommands";
import { useAvailableModes } from "@/hooks/useAvailableModes";
import { activityGroupKey, autoChatKickoffRequest, autoExecutionKickoffRequest, bucketActivityByMessage, findLatestPlanPresentation, getActionButtonVariant, isMainView, mapPendingAction, messageDeliveryTimestamp, messageTimelineKey, orderDeliveredMessages, parseUsd, partitionConversation, pendingBannerConfig, planContentForPresentationGroup, PRLoopCard, renderPlanDialogButton, type MainView, type QuickAction, type TimelineItem } from "@/components/run-session/helpers";
import { isActionableInputType, isRunComputing, visibleInputType } from "@/lib/runStatus";
import { TimelineRow } from "@/components/run-session/TimelineRow";
import { PendingMessages } from "@/components/run-session/PendingMessages";
import { ActiveSubagentsDock } from "@/components/run-session/ActiveSubagentsDock";
import { messageForQuickAction } from "@/components/quickActions";

const runnableSandboxPhases = new Set(["Running", "Question", "Blocked", "WaitingApproval"]);

function hasRunnableSandbox(phase: string, sandboxRef?: string): boolean {
  return runnableSandboxPhases.has(phase) && Boolean(sandboxRef?.trim());
}

function sandboxStartupMessage(sandboxRef?: string): string {
  return sandboxRef?.trim()
    ? "Sandbox pod is starting… repositories will appear once it is ready."
    : "Provisioning sandbox… repositories will appear once the pod is ready.";
}

function persistDraft(key: string, value: string) {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    // Ignore quota / storage failures.
  }
}

const DRAFT_PERSIST_DEBOUNCE_MS = 300;

// Top padding for the virtualized list (the old container's py-3); bottom
// spacing comes from each row's pb-3. Defined at module level so Virtuoso
// doesn't remount it on every render. Doubles as the "loading older page"
// indicator for reverse infinite scroll.
type TimelineContext = { loadingOlder: boolean };
const TimelineListHeader = ({ context }: { context?: TimelineContext }) =>
  context?.loadingOlder ? (
    <div className="flex items-center justify-center gap-2 py-2 text-xs text-muted-foreground">
      <Loader2 className="size-3 animate-spin" />
      <span>Loading earlier activity…</span>
    </div>
  ) : (
    <div className="h-3" />
  );
const timelineComponents = { Header: TimelineListHeader };
// Virtuoso reverse-infinite-scroll anchor: firstItemIndex starts at a large
// base and decreases by the number of prepended timeline items so existing
// items keep their (virtual) indices and the scroll position is preserved.
const FIRST_ITEM_INDEX_BASE = 100000;

export function RunSessionView({ namespace, name }: { namespace: string; name: string }) {
  const { run, loading, error, starting } = useAgentRun(namespace, name);
  const activityRefreshKey = run
    ? [namespace, name, run.sessionNumber, run.retryCount].join("::")
    : "";
  const { entries: activityEntries, subagentGraph, isComplete: activityComplete, hasMoreBefore, loadOlder } =
    useRunActivityLog(namespace, name, run?.phase ?? "", activityRefreshKey);
  const fetchActivityEntryDetail = useActivityEntryDetail(namespace, name);
  const draftKey = `draft:${namespace}/${name}`;
  const draftKeyRef = useRef(draftKey);
  const skipNextDraftPersistRef = useRef(false);
  function readDraft(key: string): string {
    try { return localStorage.getItem(key) ?? ""; } catch { return ""; }
  }
  const [reply, setReply] = useState(() => {
    return readDraft(draftKey);
  });
  useEffect(() => {
    if (draftKeyRef.current === draftKey) return;
    draftKeyRef.current = draftKey;
    skipNextDraftPersistRef.current = true;
    setReply(readDraft(draftKey));
  }, [draftKey]);
  useEffect(() => {
    if (skipNextDraftPersistRef.current) {
      skipNextDraftPersistRef.current = false;
      return;
    }
    const timeout = setTimeout(
      () => persistDraft(draftKey, reply),
      DRAFT_PERSIST_DEBOUNCE_MS,
    );
    return () => clearTimeout(timeout);
  }, [reply, draftKey]);
  // Flush the latest draft on unmount so a pending debounce isn't lost.
  const draftFlushRef = useRef({ key: draftKey, value: reply });
  useEffect(() => {
    draftFlushRef.current = { key: draftKey, value: reply };
  }, [reply, draftKey]);
  useEffect(
    () => () => persistDraft(draftFlushRef.current.key, draftFlushRef.current.value),
    [],
  );
  const [sending, setSending] = useState(false);
  // Steering (deliver into the in-flight turn) is the default; "Queue" is the
  // opt-in for messages that should wait for the next turn boundary. The
  // chosen mode is sticky across sends.
  const [sendMode, setSendMode] = useState<AgentRunMessageMode>(AgentRunMessageMode.IMMEDIATE);
  // Guards the cancel/edit actions on pending (not yet consumed) messages.
  const [pendingOpBusy, setPendingOpBusy] = useState(false);
  const attachments = useImageAttachments();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [mainView, setMainView] = useState<MainView>(() => {
    try {
      const stored = localStorage.getItem("gratefulagents.mainView");
      if (isMainView(stored)) {
        return stored;
      }
    } catch {
      // Ignore storage read failures.
    }
    return "chat";
  });
  useEffect(() => {
    try {
      localStorage.setItem("gratefulagents.mainView", mainView);
    } catch {
      // Ignore storage write failures.
    }
  }, [mainView]);
  const virtuosoRef = useRef<VirtuosoHandle>(null);
  const [firstItemIndex, setFirstItemIndex] = useState(FIRST_ITEM_INDEX_BASE);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const prependPendingRef = useRef(false);
  const prevTimelineLengthRef = useRef(0);
  const [isChatPinnedToBottom, setIsChatPinnedToBottom] = useState(true);
  const [isChatPinnedToTop, setIsChatPinnedToTop] = useState(true);
  const [diffRepoPath, setDiffRepoPath] = useState("");
  const { repositories: workspaceRepos } = useRepositories(
    namespace,
    name,
    "AgentRun",
    hasRunnableSandbox(run?.phase ?? "", run?.sandboxRef),
  );
  // Only honor the selection while that repo exists in the workspace; fall
  // back to the primary repo otherwise (e.g. after navigating to another run).
  const selectedDiffRepo = workspaceRepos.find((repo) => repo.path === diffRepoPath);
  const diffRepoParam = selectedDiffRepo && !selectedDiffRepo.isPrimary ? selectedDiffRepo.path : "";
  // Diff/trace streams only run while their tab is open; the hooks keep the
  // last data cached across tab switches.
  const diffState = useDiff(namespace, name, run?.phase ?? "", "AgentRun", diffRepoParam, {
    enabled: mainView === "diff",
  });
  const {
    trace,
    loading: traceLoading,
    error: traceError,
  } = useAgentTrace(namespace, name, run?.traceId, run?.phase, {
    enabled: mainView === "trace",
  });
  const runErrors = useAgentRunErrors(namespace, name, run?.phase, {
    enabled: mainView === "errors",
  });
  const runLogs = useAgentRunLogs(namespace, name, run?.phase, {
    enabled: mainView === "logs" && Boolean(run),
  });
  const traceSpans = trace?.spans;
  const sessionMetrics = useMemo(() => {
    if (!traceSpans?.length) {
      return null;
    }
    const usage = aggregateTraceUsage(traceSpans);
    return usage.hasUsage ? usage : null;
  }, [traceSpans]);
  // Current context size: prefer the agent-published gauge (works without a
  // tracing backend); fall back to trace-derived usage for older workers.
  const publishedContextTokens = Number(run?.contextTokens ?? 0);
  const contextTokens = useMemo(() => {
    if (publishedContextTokens > 0) return publishedContextTokens;
    return currentContextTokens(traceSpans);
  }, [publishedContextTokens, traceSpans]);
  const { viewers } = usePresence("agent_run", name, namespace);
  const { modes: availableModes } = useAvailableModes(namespace);
  const runModeName = run?.modeName;
  const slashCommands = useMemo<SlashCommand[]>(
    () =>
      runModeName === undefined
        ? []
        : buildSlashCommands({ modeName: runModeName }, availableModes),
    [runModeName, availableModes],
  );
  const { usage, loading: usageLoading, error: usageError } = useAgentRunUsage(namespace, name, Boolean(run));
  const [shareOpen, setShareOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [promoting, setPromoting] = useState(false);
  const [interrupting, setInterrupting] = useState(false);
  const [retrying, setRetrying] = useState(false);
  const [extendRuntimeOpen, setExtendRuntimeOpen] = useState(false);
  const [runtimeExtension, setRuntimeExtension] = useState("1h");
  const [extendingRuntime, setExtendingRuntime] = useState(false);
  const [updatingRuntimeConfig, setUpdatingRuntimeConfig] = useState(false);
  const [confirmAction, setConfirmAction] = useState<null | { kind: "delete" | "stop" | "retry" | "promote" }>(null);
  const composerTextareaRef = useRef<HTMLTextAreaElement>(null);
  const navigate = useNavigate();

  const phase = run?.phase ?? "";
  const isTerminal = phase === "Succeeded" || phase === "Failed" || phase === "Cancelled";
  const isPaused = phase === "Paused";
  const isThinking = Boolean(
    run &&
    (run.phase === "Pending" ||
      (["Admitted", "Provisioning", "Running"].includes(run.phase) && isRunComputing(run))),
  );
  const isActive = phase !== "" && !isTerminal && !isPaused;
  const isOwnerOrAdmin = run?.myPermission === "owner" || run?.myPermission === "admin";
  const isViewer = run?.myPermission === "viewer";
  const backendSendReady = run?.sendReady ?? false;
  const startupCopy = run?.sendReadinessReason || "Preparing run…";
  const sandboxReady = hasRunnableSandbox(phase, run?.sandboxRef);
  const canSendMessage = isActive && !isViewer && backendSendReady;
  const hasDiff = useMemo(
    () => /\S/.test(diffState.diff) || diffState.newFiles.length > 0,
    [diffState.diff, diffState.newFiles.length],
  );
  const canDelete = isOwnerOrAdmin;
  const canStop = isOwnerOrAdmin && !isTerminal;
  const canPromote = isOwnerOrAdmin && !isTerminal;
  // Stop the in-flight turn (model call, tools, sub-agents) without killing
  // the run — shown in the composer's send slot while the agent is working.
  const canInterrupt = isThinking && !isViewer;
  const canRetry = isOwnerOrAdmin && (phase === "Failed" || phase === "Cancelled");
  const canExtendRuntime = phase !== "" && !isTerminal && !isViewer;
  const canRename = !isViewer;
  const canUpdateRuntimeConfig = phase !== "" && !isTerminal && !isViewer;
  const userInputRequest = run?.userInputRequest;
  const pendingQuestion = userInputRequest?.message || "";
  const pendingInputType = run ? visibleInputType(run) : "";
  const showInputBanner = isActionableInputType(pendingInputType) || pendingInputType === "circuit_breaker";
  const pendingActions: QuickAction[] = (userInputRequest?.actions ?? run?.pendingActions ?? []).map(mapPendingAction);
  const pendingBanner = pendingBannerConfig[pendingInputType] ?? pendingBannerConfig.question;
  const runCostUsd = parseUsd(run?.costUsd);
  const displayCostUsd = sessionMetrics?.hasCost ? sessionMetrics.costUsd : runCostUsd;

  // Pending user messages (queued/steering, not yet consumed by the agent)
  // render above the composer instead of in the transcript; they join the
  // chat feed only once the agent actually picks them up.
  const conversation = run?.conversation;
  const conversationParts = useMemo(
    () => partitionConversation(conversation ?? []),
    [conversation],
  );
  const pendingMessages = conversationParts.pending;

  const hasRun = Boolean(run);
  const intentTitle = run?.intentTitle ?? "";
  const currentStep = run?.currentStep ?? "";
  const runPendingActions = run?.pendingActions;
  const timelinePlanContent = run?.currentPlan || run?.planSummary || "";

  const timeline = useMemo<TimelineItem[]>(() => {
    if (!hasRun) {
      return [];
    }

    const items: TimelineItem[] = [];
    const messages = orderDeliveredMessages(conversationParts.delivered);
    const messageOccurrences = new Map<string, number>();
    const latestPlanPresentation = findLatestPlanPresentation(activityEntries);
    // The original user request is seeded as the first Postgres conversation
    // message (intentTitle is always empty), so only render a dedicated
    // user-request bubble when a non-empty title is actually present. Without
    // this guard an empty bubble renders at the start of every run.
    if (
      intentTitle.trim() !== "" &&
      intentTitle !== autoChatKickoffRequest &&
      intentTitle !== autoExecutionKickoffRequest
    ) {
      items.push({ kind: "user-request", key: `user-request:${intentTitle}`, content: intentTitle });
    }

    // A sub-agent task's events can span user-message boundaries (the user
    // types while tasks run); anchor each task's entries to where it started
    // so its whole lifecycle renders in one segment (see bucketActivityByMessage).
    const { segments: segmentBuckets, trailing: trailingBucket } = bucketActivityByMessage(
      activityEntries,
      messages.map(messageDeliveryTimestamp),
    );

    for (let i = 0; i < messages.length; i += 1) {
      const msg = messages[i];
      const group = segmentBuckets[i];

      if (group.length > 0) {
        // The agent's final text often arrives both as an assistant_text
        // activity entry and as a conversation message; drop the duplicate so
        // the feed doesn't render the same prose twice. Trim once per message
        // and gate on length so the comparison stays cheap for big bodies.
        let deduped = group;
        if (msg.role === "assistant") {
          const target = msg.content.trim();
          deduped = group.filter(
            (e) =>
              !(
                e.type === "assistant_text" &&
                e.message.length >= target.length &&
                e.message.trim() === target
              ),
          );
        }
        if (deduped.length > 0) {
          items.push({
            kind: "activity",
            key: activityGroupKey(deduped),
            entries: deduped,
            isLive: false,
            planContent: planContentForPresentationGroup(
              deduped,
              latestPlanPresentation,
              timelinePlanContent,
            ),
          });
        }
      }

      const messageIdentity = `${msg.timestampUnix.toString()}:${msg.role}`;
      const occurrence = messageOccurrences.get(messageIdentity) ?? 0;
      messageOccurrences.set(messageIdentity, occurrence + 1);

      items.push({
        kind: "message",
        key: messageTimelineKey(msg, occurrence),
        role: msg.role,
        content: msg.content,
        timestamp: messageDeliveryTimestamp(msg),
        imageDataUrls: msg.imageDataUrls,
      });
    }

    if (trailingBucket.length > 0) {
      items.push({
        kind: "activity",
        key: activityGroupKey(trailingBucket),
        entries: trailingBucket,
        isLive: isActive && !activityComplete,
        planContent: planContentForPresentationGroup(
          trailingBucket,
          latestPlanPresentation,
          timelinePlanContent,
        ),
      });
    }

    if (
      userInputRequest?.type &&
      !["idle", "stopped", "circuit_breaker"].includes(userInputRequest.type)
    ) {
      const actions: QuickAction[] = (userInputRequest.actions ?? runPendingActions ?? []).map(mapPendingAction);

      items.push({
        kind: "pending",
        key: `pending:${userInputRequest.type}:${userInputRequest.message || ""}`,
        content: userInputRequest.message || "",
        actions: actions.length > 0 ? actions : undefined,
      });
    }

    if (isThinking) {
      items.push({ kind: "thinking", key: `thinking:${phase}:${currentStep}`, phase });
    }

    return items;
  }, [
    hasRun,
    intentTitle,
    conversationParts,
    activityEntries,
    isActive,
    activityComplete,
    isThinking,
    userInputRequest,
    runPendingActions,
    phase,
    currentStep,
    timelinePlanContent,
  ]);

  const liveAnnouncement = useMemo(() => {
    const lastItem = timeline[timeline.length - 1];
    if (!lastItem) {
      return "";
    }
    if (lastItem.kind === "message" && lastItem.role === "assistant") {
      return "Assistant replied";
    }
    if (lastItem.kind === "pending") {
      const banner = pendingBannerConfig[pendingInputType] ?? pendingBannerConfig.question;
      return banner.label;
    }
    if (lastItem.kind === "activity" && lastItem.isLive) {
      return "Assistant activity updated";
    }
    return "";
  }, [pendingInputType, timeline]);

  // Reverse infinite scroll: when an older page is prepended the timeline
  // grows at the front; decrease firstItemIndex by the growth so Virtuoso
  // keeps the current items anchored (entries -> timeline items is not 1:1,
  // so the delta is measured on the derived timeline, not on entries).
  useEffect(() => {
    const prevLength = prevTimelineLengthRef.current;
    prevTimelineLengthRef.current = timeline.length;
    if (prependPendingRef.current && timeline.length > prevLength) {
      prependPendingRef.current = false;
      setLoadingOlder(false);
      setFirstItemIndex((index) => index - (timeline.length - prevLength));
    }
  }, [timeline]);

  const handleStartReached = useCallback(() => {
    if (!hasMoreBefore || prependPendingRef.current) {
      return;
    }
    prependPendingRef.current = true;
    setLoadingOlder(true);
    void loadOlder().finally(() => {
      // Safety net for pages that don't grow the timeline (no-op loads):
      // clear the flag after the prepend effect had a chance to consume it.
      setTimeout(() => {
        prependPendingRef.current = false;
        setLoadingOlder(false);
      }, 0);
    });
  }, [hasMoreBefore, loadOlder]);

  const scrollChatTo = useCallback((where: "top" | "bottom") => {
    const virtuoso = virtuosoRef.current;
    if (!virtuoso) return;
    virtuoso.scrollToIndex(
      where === "top"
        ? { index: 0, align: "start", behavior: "smooth" }
        : { index: "LAST", align: "end", behavior: "smooth" },
    );
  }, []);

  if (loading) {
    return (
      <p className="p-8 text-muted-foreground" role="status" aria-live="polite">
        {starting ? "Run is starting…" : "Loading..."}
      </p>
    );
  }
  if (error || !run) {
    return (
      <p className="p-8 text-destructive" role="alert">
        Error: {error || "Not found"}
      </p>
    );
  }

  const prUrl =
    run.pullRequestUrl || (run.reviewArtifactKind === "PullRequest" ? run.reviewArtifactName : "");
  const prUrls = runPullRequestUrls(run);
  if (prUrls.length === 0 && prUrl) {
    prUrls.push(prUrl);
  }
  const showCreatePRButton = hasDiff && run.reviewArtifactKind !== "PullRequest";
  const planContent = timelinePlanContent;
  const hasPlan = Boolean(planContent);
  const inPlanMode = run.modeName === "plan";
  const showPlanApprovalPanel = inPlanMode && hasPlan;
  const showPlanningBanner = inPlanMode && !hasPlan;
  const activeMainView: MainView =
    (mainView === "trace" && !run.traceId) || (mainView === "pr" && prUrls.length === 0)
      ? "chat"
      : mainView;
  const mainTabs: Array<{ value: MainView; label: string }> = [
    { value: "chat", label: "Chat" },
    { value: "graph", label: "Graph" },
    { value: "diff", label: "Diff" },
    ...(prUrls.length > 0 ? [{ value: "pr" as const, label: "PR" }] : []),
    { value: "errors", label: "Errors" },
    { value: "logs", label: "Logs" },
    ...(run.traceId ? [{ value: "trace" as const, label: "Trace" }] : []),
  ];

  async function handleSend() {
    const hasImages = attachments.images.length > 0;
    if ((!reply.trim() && !hasImages) || sending || !canSendMessage) {
      return;
    }

    setSending(true);
    try {
      await client.sendAgentRunMessage({
        namespace,
        name,
        message: reply.trim(),
        messageMode: sendMode,
        imageDataUrls: attachments.dataUrls(),
      });
      setReply("");
      attachments.clear();
      toast.success("Message sent");
      composerTextareaRef.current?.focus();
    } catch (e) {
      toast.error("Couldn't send message", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setSending(false);
    }
  }

  // Withdraw a queued/steering message the agent hasn't consumed yet. The
  // chip disappears from the pending strip on the next snapshot.
  async function handleCancelPending(message: ChatMessage) {
    if (pendingOpBusy) {
      return;
    }
    setPendingOpBusy(true);
    try {
      await client.cancelAgentRunMessage({ namespace, name, messageId: message.id });
    } catch (e) {
      toast.error("Couldn't cancel message", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setPendingOpBusy(false);
    }
  }

  // Pull a not-yet-consumed message back into the composer: cancel it, then
  // restore its text and image attachments so it can be revised and re-sent.
  async function handleEditPending(message: ChatMessage) {
    if (pendingOpBusy) {
      return;
    }
    setPendingOpBusy(true);
    try {
      await client.cancelAgentRunMessage({ namespace, name, messageId: message.id });
      setReply((current) =>
        current.trim() ? `${message.content}\n\n${current}` : message.content,
      );
      attachments.addDataUrls(message.imageDataUrls);
      composerTextareaRef.current?.focus();
    } catch (e) {
      toast.error("Couldn't edit message", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setPendingOpBusy(false);
    }
  }

  async function handleAction(action: QuickAction, freeform?: string) {
    if (sending) {
      return;
    }

    setSending(true);
    try {
      const trimmedFreeform = freeform?.trim();
      const message = trimmedFreeform
        ? `${messageForQuickAction(action)} ${trimmedFreeform}`
        : messageForQuickAction(action);
      await client.sendAgentRunMessage({ namespace, name, message });
    } catch (e) {
      toast.error("Couldn't send action", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setSending(false);
    }
  }

  async function handleControlMessage(message: string) {
    if (sending || !canSendMessage) {
      return;
    }

    setSending(true);
    try {
      await client.sendAgentRunMessage({ namespace, name, message });
      toast.success("Message sent");
      composerTextareaRef.current?.focus();
    } catch (e) {
      toast.error("Couldn't send message", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setSending(false);
    }
  }

  // executeSlashCommand runs a palette command as a control action. Every
  // command is a mode switch (plan included — it is a regular ModeTemplate).
  // The raw command is never persisted as a chat message.
  async function executeSlashCommand(command: SlashCommand) {
    if (sending || !canSendMessage) {
      return;
    }

    setSending(true);
    try {
      const resp = await client.switchAgentRunMode({
        namespace,
        name,
        targetMode: command.action.target,
        source: "ui",
      });
      if (resp.result === "denied") {
        toast.error("Mode switch denied", {
          description: resp.denialReason || "You don't have permission to switch modes.",
        });
        return;
      }
      composerTextareaRef.current?.focus();
    } catch (e) {
      toast.error("Couldn't run command", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setSending(false);
    }
  }

  async function handleDelete() {
    if (!canDelete || deleting) {
      return;
    }
    setConfirmAction({ kind: "delete" });
  }

  async function performDelete() {
    setDeleting(true);
    try {
      await client.deleteAgentRun({ namespace, name });
      toast.success(`Deleted ${name}`);
      navigate("/");
    } catch (e) {
      toast.error("Couldn't delete run", {
        description: e instanceof Error ? e.message : String(e),
      });
      setDeleting(false);
    }
  }

  async function handleStop() {
    if (!canStop || stopping) {
      return;
    }
    setConfirmAction({ kind: "stop" });
  }

  async function performStop() {
    setStopping(true);
    try {
      await client.cancelAgentRun({ namespace, name });
      toast.success(`Stopping ${name}`);
    } catch (e) {
      toast.error("Couldn't stop run", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setStopping(false);
    }
  }

  async function handlePromote() {
    if (!canPromote || promoting) {
      return;
    }
    setConfirmAction({ kind: "promote" });
  }

  async function performPromote() {
    setPromoting(true);
    try {
      await client.promoteAgentRun({ namespace, name });
      toast.success(`Marked ${name} as succeeded`);
    } catch (e) {
      toast.error("Couldn't mark run as succeeded", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setPromoting(false);
    }
  }

  async function handleInterrupt() {
    if (!canInterrupt || interrupting) {
      return;
    }
    setInterrupting(true);
    try {
      await client.interruptAgentRun({ namespace, name });
      toast.success("Stopping the current turn");
    } catch (e) {
      toast.error("Couldn't stop the current turn", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setInterrupting(false);
    }
  }

  async function handleRename(displayName: string) {
    const next = displayName.trim();
    if (!next) {
      return;
    }
    try {
      await client.renameAgentRun({ namespace, name, displayName: next });
      toast.success("Renamed run");
    } catch (e) {
      toast.error("Couldn't rename run", {
        description: e instanceof Error ? e.message : String(e),
      });
    }
  }

  async function handleRetry() {
    if (!canRetry || retrying) {
      return;
    }
    setConfirmAction({ kind: "retry" });
  }

  async function performRetry() {
    setRetrying(true);
    try {
      await client.retryAgentRun({ namespace, name, idempotencyKey: crypto.randomUUID() });
      toast.success(`Retrying ${name}`);
    } catch (e) {
      toast.error("Couldn't retry run", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setRetrying(false);
    }
  }

  async function handleConfirmAction() {
    if (confirmAction?.kind === "delete") {
      await performDelete();
    } else if (confirmAction?.kind === "stop") {
      await performStop();
    } else if (confirmAction?.kind === "retry") {
      await performRetry();
    } else if (confirmAction?.kind === "promote") {
      await performPromote();
    }
  }

  async function handleExtendRuntime(event?: React.FormEvent<HTMLFormElement>) {
    event?.preventDefault();
    if (!canExtendRuntime || extendingRuntime) {
      return;
    }

    const additionalRuntime = runtimeExtension.trim();
    if (!additionalRuntime) {
      toast.error("Enter a runtime extension");
      return;
    }

    setExtendingRuntime(true);
    try {
      await client.extendAgentRunRuntime({
        namespace,
        name,
        additionalRuntime,
      });
      setExtendRuntimeOpen(false);
      toast.success(isPaused ? "Runtime extended; resuming run" : "Runtime extended");
    } catch (e) {
      toast.error("Couldn't extend runtime", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setExtendingRuntime(false);
    }
  }

  async function handleUpdateRuntimeConfig(update: { provider: string; model: string; reasoningLevel: string }) {
    if (!canUpdateRuntimeConfig || updatingRuntimeConfig) {
      return;
    }
    setUpdatingRuntimeConfig(true);
    try {
      await client.updateAgentRunRuntimeConfig({
        namespace,
        name,
        provider: update.provider,
        model: update.model,
        updateReasoningLevel: true,
        reasoningLevel: update.reasoningLevel,
      });
      toast.success("Runtime config updated");
    } catch (e) {
      toast.error("Couldn't update runtime config", {
        description: e instanceof Error ? e.message : String(e),
      });
      throw e;
    } finally {
      setUpdatingRuntimeConfig(false);
    }
  }

  const confirmDialog = confirmAction
    ? {
        delete: {
          title: "Delete run?",
          description: isActive
            ? `Delete run ${namespace}/${name}? If it is still active, this will stop it. This cannot be undone.`
            : `Delete run ${namespace}/${name}? This cannot be undone.`,
          confirmLabel: "Delete",
          destructive: true,
        },
        stop: {
          title: "Stop run?",
          description: `Stop run ${namespace}/${name}? The pod is terminated; history and diff are preserved.`,
          confirmLabel: "Stop",
          destructive: true,
        },
        retry: {
          title: "Retry run?",
          description: `Retry run ${namespace}/${name}? The run will resume from its persisted session.`,
          confirmLabel: "Retry",
          destructive: false,
        },
        promote: {
          title: "Mark run as succeeded?",
          description: `Mark run ${namespace}/${name} as Succeeded? Anything still running is stopped and the run completes with a success status.`,
          confirmLabel: "Mark as succeeded",
          destructive: false,
        },
      }[confirmAction.kind]
    : null;

  return (
    <ActivityDetailProvider value={fetchActivityEntryDetail}>
    <div className="flex h-full gap-px overflow-hidden bg-muted/30">
      {confirmDialog && (
        <ConfirmDialog
          open={confirmAction !== null}
          onOpenChange={(open) => {
            if (!open) setConfirmAction(null);
          }}
          title={confirmDialog.title}
          description={confirmDialog.description}
          confirmLabel={confirmDialog.confirmLabel}
          destructive={confirmDialog.destructive}
          onConfirm={handleConfirmAction}
        />
      )}
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden bg-background">
        <RunSessionHeader
          namespace={namespace}
          name={name}
          run={run}
          viewers={viewers}
          showRepositories={isActive}
          sandboxReady={sandboxReady}
          sandboxStartupMessage={sandboxStartupMessage(run.sandboxRef)}
          mainTabs={mainTabs}
          activeMainView={activeMainView}
          setMainView={setMainView}
          prUrls={prUrls}
          showCreatePRButton={showCreatePRButton}
          canExtendRuntime={canExtendRuntime}
          isPaused={isPaused}
          extendingRuntime={extendingRuntime}
          extendRuntimeOpen={extendRuntimeOpen}
          setExtendRuntimeOpen={setExtendRuntimeOpen}
          runtimeExtension={runtimeExtension}
          setRuntimeExtension={setRuntimeExtension}
          handleExtendRuntime={handleExtendRuntime}
          hasPlan={hasPlan}
          planContent={planContent}
          shareOpen={shareOpen}
          setShareOpen={setShareOpen}
          isOwnerOrAdmin={isOwnerOrAdmin}
          isViewer={isViewer}
          canRetry={canRetry}
          handleRetry={handleRetry}
          retrying={retrying}
          canStop={canStop}
          handleStop={handleStop}
          stopping={stopping}
          canPromote={canPromote}
          handlePromote={handlePromote}
          promoting={promoting}
          canDelete={canDelete}
          handleDelete={handleDelete}
          deleting={deleting}
          displayCostUsd={displayCostUsd}
          sessionMetrics={sessionMetrics}
          canRename={canRename}
          onRename={handleRename}
        />
        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
          {run.gateResults && run.gateResults.length > 0 && (
            <div className="shrink-0 border-b px-3 py-2 md:px-4">
              <EvidenceGatesCard gates={run.gateResults} finishAttempts={run.finishAttempts} />
            </div>
          )}
          {run.prLoop && (
            <PRLoopCard loop={run.prLoop} namespace={namespace} prUrl={prUrl} />
          )}
          {activeMainView === "chat" && (
            <div className="relative isolate flex h-full min-h-0 min-w-0 flex-col overflow-hidden">
              <div className="relative isolate flex min-h-0 flex-1 flex-col overflow-hidden">
                <div aria-live="polite" className="sr-only">{liveAnnouncement}</div>
                <Virtuoso<TimelineItem, TimelineContext>
                  ref={virtuosoRef}
                  className="flex-1 overscroll-y-contain"
                  data={timeline}
                  context={{ loadingOlder }}
                  computeItemKey={(_, item) => item.key}
                  components={timelineComponents}
                  increaseViewportBy={600}
                  followOutput="auto"
                  firstItemIndex={firstItemIndex}
                  startReached={handleStartReached}
                  atBottomThreshold={48}
                  atBottomStateChange={setIsChatPinnedToBottom}
                  atTopThreshold={48}
                  atTopStateChange={setIsChatPinnedToTop}
                  initialTopMostItemIndex={{ index: "LAST", align: "end" }}
                  itemContent={(_, item) => (
                    <div className="px-3 pb-3 md:px-4">
                      <TimelineRow
                        item={item}
                        thinkingStep={item.kind === "thinking" ? currentStep : ""}
                      />
                    </div>
                  )}
                />

              <ChatScrollControls
                show={!(isChatPinnedToTop && isChatPinnedToBottom)}
                isPinnedToTop={isChatPinnedToTop}
                isPinnedToBottom={isChatPinnedToBottom}
                onScrollTo={scrollChatTo}
              />
              </div>

              {showPlanApprovalPanel && (
                <div className="border-t px-3 py-2 md:px-4">
                  <PlanApprovalPanel
                    planContent={planContent}
                    disabled={sending || isViewer || !canSendMessage}
                    onSendMessage={handleControlMessage}
                  />
                </div>
              )}

              {showPlanningBanner && (
                <div className="flex items-center gap-2 border-t px-3 py-2 text-xs text-muted-foreground md:px-4">
                  <span className={cn("flex size-5 items-center justify-center rounded-full", toneSoft.info)}>
                    <FileText className={cn("size-3", toneText.info)} />
                  </span>
                  <span>
                    <span className="font-medium text-foreground">Plan mode</span> · the agent is
                    preparing a plan. You can review and approve it here before implementation.
                  </span>
                </div>
              )}

              {showInputBanner && !isViewer && !showPlanApprovalPanel && (
                <div className="shrink-0 space-y-2 border-t px-3 py-3 md:px-4">
                  <div className="flex items-center gap-2">
                    <span className="relative flex size-1.5">
                      <span
                        className={`absolute inline-flex size-full animate-ping rounded-full ${pendingBanner.dotColor} opacity-75`}
                      />
                      <span
                        className={`relative inline-flex size-1.5 rounded-full ${pendingBanner.dotColor}`}
                      />
                    </span>
                    <span className={`text-xs font-medium ${pendingBanner.textColor}`}>
                      {pendingBanner.label}
                    </span>
                  </div>
                  {pendingQuestion && (
                    <div className="max-h-64 overflow-y-auto text-sm leading-relaxed">
                      <MarkdownViewer content={pendingQuestion} />
                    </div>
                  )}
                  {pendingActions.length > 0 && (
                    <div className="flex flex-wrap items-center gap-2">
                      {pendingActions.map((action) => (
                        <Button
                          key={action.id}
                          variant={getActionButtonVariant(action.style)}
                          size="sm"
                          onClick={() => handleAction(action)}
                          disabled={sending}
                        >
                          {action.label}
                        </Button>
                      ))}
                      {pendingInputType === "approval" &&
                        hasPlan &&
                        renderPlanDialogButton(
                          planContent,
                          <Button variant="ghost" size="sm" className="gap-1.5">
                            <FileText className="size-3.5" />
                            View plan
                          </Button>,
                        )}
                      <span className="text-xs text-muted-foreground">or type below</span>
                    </div>
                  )}
                </div>
              )}

              <PendingMessages
                messages={pendingMessages}
                terminal={isTerminal}
                onEdit={canSendMessage ? handleEditPending : undefined}
                onCancel={canSendMessage ? handleCancelPending : undefined}
                busy={pendingOpBusy}
              />

              <ActiveSubagentsDock
                graph={subagentGraph}
                onOpenGraph={() => setMainView("graph")}
              />

              <RunSessionFooter
                isActive={isActive}
                isViewer={isViewer}
                sending={sending}
                canSendMessage={canSendMessage}
                startupCopy={startupCopy}
                attachments={attachments}
                fileInputRef={fileInputRef}
                reply={reply}
                setReply={setReply}
                handleSend={handleSend}
                sendMode={sendMode}
                setSendMode={setSendMode}
                slashCommands={slashCommands}
                onRunSlashCommand={executeSlashCommand}
                phase={run.phase}
                blockedReason={run.blockedReason}
                canExtendRuntime={canExtendRuntime}
                setExtendRuntimeOpen={setExtendRuntimeOpen}
                extendingRuntime={extendingRuntime}
                canRetry={canRetry}
                handleRetry={handleRetry}
                retrying={retrying}
                canInterrupt={canInterrupt}
                interrupting={interrupting}
                onInterrupt={handleInterrupt}
                textareaRef={composerTextareaRef}
                namespace={namespace}
                name={name}
                resourceType="AgentRun"
                contextTokens={contextTokens}
                contextTriggerTokens={Number(run.contextTriggerTokens)}
                contextTargetTokens={Number(run.contextTargetTokens)}
                run={run}
                canUpdateRuntimeConfig={canUpdateRuntimeConfig}
                updatingRuntimeConfig={updatingRuntimeConfig}
                onUpdateRuntimeConfig={handleUpdateRuntimeConfig}
              />
            </div>
          )}

          {activeMainView === "graph" && (
            <div className="flex-1 min-h-0 min-w-0 overflow-hidden">
              <SubagentGraphView graph={subagentGraph} entries={activityEntries} />
            </div>
          )}

          {activeMainView === "diff" && (
            <div className="flex min-h-0 min-w-0 flex-1 overflow-hidden p-2 md:p-4">
              <UnifiedDiffViewer
                diff={diffState.diff}
                loading={diffState.loading}
                error={diffState.error}
                isComplete={diffState.isComplete}
                truncated={diffState.truncated}
                source={diffState.source}
                bodyWrapper={(body) => (
                  <NewFilesBrowser
                    key={`${namespace}/${name}/${diffRepoParam}`}
                    namespace={namespace}
                    name={name}
                    resourceType="AgentRun"
                    repoPath={diffRepoParam}
                    files={diffState.newFiles}
                    filesTruncated={diffState.newFilesTruncated}
                  >
                    {body}
                  </NewFilesBrowser>
                )}
                toolbar={
                  <DiffRepoSelector
                    repositories={workspaceRepos}
                    value={diffRepoParam}
                    onChange={setDiffRepoPath}
                  />
                }
              />
            </div>
          )}

          {activeMainView === "pr" && (
            <div className="flex-1 min-h-0 min-w-0 overflow-y-auto">
              <RunPullRequestPanel namespace={namespace} name={name} canSend={canSendMessage} />
            </div>
          )}

          {activeMainView === "errors" && (
            <RunSessionErrorsPane
              errors={runErrors.errors}
              loading={runErrors.loading}
              error={runErrors.error}
              truncated={runErrors.truncated}
            />
          )}

          {activeMainView === "logs" && (
            <RunSessionLogsPane
              content={runLogs.content}
              podName={runLogs.podName}
              available={runLogs.available}
              loading={runLogs.loading}
              error={runLogs.error}
              truncated={runLogs.truncated}
              lastUpdated={runLogs.lastUpdated}
              onRefresh={runLogs.refresh}
            />
          )}

          {activeMainView === "trace" && (
            <RunSessionTracePane
              trace={trace}
              traceError={traceError}
              traceLoading={traceLoading}
              usage={usage}
              usageLoading={usageLoading}
              usageError={usageError}
            />
          )}
        </div>
      </div>
    </div>
    </ActivityDetailProvider>
  );
}
