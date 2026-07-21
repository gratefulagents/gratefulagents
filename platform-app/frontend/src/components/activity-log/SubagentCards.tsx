import { memo, useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, Check, ChevronDown, ChevronRight, Clock3, Loader2, XCircle } from "lucide-react";

import { MarkdownViewer } from "@/components/MarkdownViewer";
import { CodePane } from "./DetailPanes";
import { useResolvedEntry } from "./detailContext";
import { entryIdentity, groupsToUnits, workUnitKey } from "./feedModel";
import { WorkUnitView } from "./WorkRows";
import { groupActivityEntries, formatDuration, formatTokens, subagentTitleFromPrompt, type ActivityGroup } from "@/lib/activityGrouping";
import { firstLine, formatClock, formatUsd } from "@/lib/activityLogFormat";
import { getSubagentColor } from "@/lib/subagentColors";
import { isWaitingStatus } from "@/lib/subagentGraphLayout";
import { toneSoft, toneText } from "@/lib/status";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

export function subagentLiveLine(entries: ActivityEntry[]): string {
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i];
    if (e.type !== "subagent_progress" && e.type !== "subagent_notification")
      continue;
    if (e.subagentCurrentStep) return e.subagentCurrentStep;
    if (e.recentAction) return e.recentAction;
    if (e.subagentLastTool) return `running ${e.subagentLastTool}`;
    if (e.lastToolName) return `running ${e.lastToolName}`;
  }
  return "";
}

/** Non-terminal statuses that mean the subagent is actively working. */
export function isLiveSubagentStatus(status: string): boolean {
  return status === "running" || status === "initializing" || status === "started" || !status;
}

export function SubagentStatusIcon({ status }: { status: string }) {
  if (isLiveSubagentStatus(status))
    return <Loader2 className={`size-3.5 shrink-0 animate-spin ${toneText.running}`} />;
  if (isWaitingStatus(status))
    return <Clock3 className={`size-3.5 shrink-0 ${toneText.warning}`} />;
  if (status === "failed")
    return <XCircle className={`size-3.5 shrink-0 ${toneText.danger}`} />;
  if (status === "stopped" || status === "cancelled" || status === "canceled")
    return <AlertTriangle className={`size-3.5 shrink-0 ${toneText.warning}`} />;
  return <Check className={`size-3.5 shrink-0 ${toneText.success}`} />;
}

export function SubagentShell({
  name,
  status,
  title,
  liveLine,
  resultPreview,
  metrics,
  timestamp,
  children,
  defaultOpen = false,
}: {
  name: string;
  status: string;
  title: string;
  liveLine?: string;
  /** One-line outcome shown on the collapsed card once the subagent finished. */
  resultPreview?: string;
  metrics: string[];
  timestamp: bigint;
  children: ReactNode;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const color = getSubagentColor(name || undefined);
  const isRunning = isLiveSubagentStatus(status);
  const isWaiting = isWaitingStatus(status);

  return (
    <div
      className={`overflow-hidden rounded-lg border border-border/50 border-l-2 ${color.border}`}
    >
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        title={formatClock(timestamp)}
        className="flex w-full items-start gap-2.5 px-3 py-2.5 text-left transition-colors hover:bg-muted/30 cursor-pointer"
      >
        <span className="mt-0.5">
          <SubagentStatusIcon status={status} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            {name && (
              <span
                className={`inline-flex items-center rounded-[5px] border px-1.5 py-px text-[11px] font-semibold ${color.border} ${color.bg} ${color.text}`}
              >
                {name}
              </span>
            )}
            <span className="min-w-0 truncate text-xs font-medium text-foreground/90">
              {title}
            </span>
            {(status === "failed" || status === "stopped" || isWaiting) && (
              <span
                className={`rounded-[4px] px-1.5 py-px text-[10px] font-semibold uppercase tracking-wider ${
                  status === "failed" ? toneSoft.danger : toneSoft.warning
                }`}
              >
                {isWaiting ? "waiting" : status}
              </span>
            )}
          </span>
          {isRunning && liveLine && (
            <span className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground">
              <span
                className={`size-1.5 animate-pulse rounded-full bg-current ${toneText.running}`}
              />
              {liveLine}
            </span>
          )}
          {!isRunning && !open && resultPreview && (
            <span className="mt-1 block truncate text-xs text-muted-foreground" title={resultPreview}>
              {resultPreview}
            </span>
          )}
          {metrics.length > 0 && (
            <span className="mt-1 block truncate font-mono text-[10px] tabular-nums text-muted-foreground/60">
              {metrics.join(" · ")}
            </span>
          )}
        </span>
        <ChevronDown
          className={`mt-0.5 size-3.5 shrink-0 text-muted-foreground/50 transition-transform ${
            open ? "rotate-180" : ""
          }`}
        />
      </button>
      {open && (
        <div className="space-y-3 border-t border-border/40 px-3 py-2.5">
          {children}
        </div>
      )}
    </div>
  );
}

export function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <p className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground/60">
      {children}
    </p>
  );
}

export function PromptToggle({ prompt }: { prompt: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="flex items-center gap-1.5 text-[10px] font-medium uppercase tracking-wider text-muted-foreground/60 transition-colors hover:text-muted-foreground cursor-pointer"
      >
        <ChevronRight className={`size-3 transition-transform ${open ? "rotate-90" : ""}`} />
        Prompt
        {!open && (
          <span className="normal-case tracking-normal font-normal text-muted-foreground/50 truncate max-w-[24rem]">
            {prompt.replace(/\s+/g, " ").slice(0, 80)}…
          </span>
        )}
      </button>
      {open && (
        <div className="mt-1.5">
          <CodePane text={prompt} maxHeight={320} />
        </div>
      )}
    </div>
  );
}

export const SubagentCard = memo(function SubagentCard({
  group,
}: {
  group: Extract<ActivityGroup, { kind: "subagent" }>;
}) {
  const firstEntry = group.entries[0];
  const startedEntry = group.entries.find((e) => e.type === "subagent_started");
  const parentToolUseId = startedEntry?.toolUseId ?? "";
  const parentToolEntry = parentToolUseId
    ? group.entries.find(
        (e) => e.type === "tool_use" && e.toolUseId === parentToolUseId,
      )
    : group.entries.find((e) => e.type === "tool_use" && e.tool === "Agent");

  const promptContent =
    parentToolEntry?.input || parentToolEntry?.inputRaw || group.subagentPrompt || "";

  const toolUseEntries = group.entries.filter(
    (e) =>
      e.type === "tool_use" &&
      e.tool !== "Agent" &&
      (!parentToolUseId || e.toolUseId !== parentToolUseId),
  );
  const resultsByUseId = new Map<string, ActivityEntry>();
  for (const e of group.entries) {
    if (e.type === "tool_result" && e.toolUseId)
      resultsByUseId.set(e.toolUseId, e);
  }
  const stepEntries =
    toolUseEntries.length > 0
      ? toolUseEntries
      : group.entries.filter((e) => e.type === "subagent_progress");

  const childEntries: ActivityEntry[] = [];
  for (const u of toolUseEntries) {
    childEntries.push(u);
    const r = u.toolUseId ? resultsByUseId.get(u.toolUseId) : undefined;
    if (r) childEntries.push(r);
  }
  const childUnits =
    childEntries.length > 0
      ? groupsToUnits(
          groupActivityEntries(childEntries, { skipSubagentGrouping: true }),
        )
      : [];

  const assistantTexts = group.entries.filter(
    (e) => e.type === "assistant_text" && e.message,
  );
  const notification = group.entries.find(
    (e) => e.type === "subagent_notification",
  );
  const agentResultId = parentToolUseId || parentToolEntry?.toolUseId;
  const agentResult = agentResultId
    ? group.entries.find(
        (e) => e.type === "tool_result" && e.toolUseId === agentResultId,
      )
    : null;
  const resultContent =
    agentResult?.output ||
    notification?.message ||
    group.subagentResultText ||
    "";
  const resultIsError = agentResult?.isError ?? false;

  const isRunning = isLiveSubagentStatus(group.subagentStatus);
  const liveLine = isRunning ? subagentLiveLine(group.entries) : "";

  const liveEntry = [
    ...group.entries.filter((e) => e.type === "subagent_progress"),
    ...(notification ? [notification] : []),
  ].at(-1);
  const waitingOn = liveEntry?.subagentWaitingOn ?? [];
  const dependsOn = liveEntry?.subagentDependsOn ?? [];

  const metrics: string[] = [];
  if (group.subagentModel) metrics.push(group.subagentModel);
  if (group.toolCount > 0) metrics.push(`${group.toolCount} tools`);
  if (group.subagentNumTurns > 0) metrics.push(`${group.subagentNumTurns} turns`);
  if (group.totalTokens > 0n) metrics.push(`${formatTokens(group.totalTokens)} tok`);
  if (group.durationMs > 0n) metrics.push(formatDuration(group.durationMs));
  if (group.subagentCostKnown) metrics.push(formatUsd(group.subagentCostUsd));

  const title =
    (group.subagentDescription && group.subagentDescription !== "spawned"
      ? group.subagentDescription
      : "") ||
    subagentTitleFromPrompt(group.subagentPrompt) ||
    (group.toolCount > 0
      ? `${group.toolCount} tool ${group.toolCount === 1 ? "call" : "calls"}`
      : "Subagent task");

  return (
    <SubagentShell
      name={group.subagentType}
      status={group.subagentStatus}
      title={title}
      liveLine={liveLine}
      resultPreview={resultIsError ? "" : firstLine(resultContent)}
      metrics={metrics}
      timestamp={firstEntry.timestampUnix}
    >
      {(dependsOn.length > 0 || waitingOn.length > 0) && (
        <div className="flex flex-wrap items-center gap-1.5">
          <SectionLabel>Depends on</SectionLabel>
          {dependsOn.map((d) => (
            <span
              key={`dep-${d}`}
              className={`rounded-[4px] px-1.5 py-px font-mono text-[10px] ${toneSoft.neutral}`}
            >
              {d}
            </span>
          ))}
          {waitingOn.map((w) => (
            <span
              key={`wait-${w}`}
              className={`rounded-[4px] px-1.5 py-px font-mono text-[10px] ${toneSoft.warning}`}
            >
              waiting {w}
            </span>
          ))}
        </div>
      )}

      {promptContent && <PromptToggle prompt={promptContent} />}

      {childUnits.length > 0 ? (
        <div>
          <SectionLabel>Steps</SectionLabel>
          <div className="mt-1 space-y-px">
            {childUnits.map((u, i) => (
              <WorkUnitView key={workUnitKey(u, i)} unit={u} />
            ))}
          </div>
        </div>
      ) : stepEntries.length > 0 ? (
        <div>
          <SectionLabel>Progress</SectionLabel>
          <div className="mt-1 space-y-px">
            {stepEntries.map((e) => (
              <div
                key={entryIdentity(e)}
                className="flex items-center gap-2 px-2 py-0.5 text-xs text-muted-foreground"
              >
                <span className="size-1 shrink-0 rounded-full bg-muted-foreground/40" />
                <span className="truncate">
                  {e.subagentCurrentStep ||
                    e.recentAction ||
                    firstLine(e.message || "") ||
                    "progress"}
                </span>
              </div>
            ))}
          </div>
        </div>
      ) : null}

      {assistantTexts.map((e) => (
        <div key={`at-${entryIdentity(e)}`}>
          <SectionLabel>Response</SectionLabel>
          <div className="mt-1 text-sm">
            <MarkdownViewer content={e.message} />
          </div>
        </div>
      ))}

      {resultContent && (
        <SubagentResultSection
          entry={agentResult ?? undefined}
          fallbackText={resultContent}
          isError={resultIsError}
        />
      )}
    </SubagentShell>
  );
});

export const InlineSubagentCard = memo(function InlineSubagentCard({
  group,
}: {
  group: Extract<ActivityGroup, { kind: "inline-subagent" }>;
}) {
  const parentEntry = group.parentEntry;
  const agentName =
    parentEntry.agentName || parentEntry.tool?.replace("agent_", "") || "sub-agent";
  const toolCount = group.children.filter((c) => c.type === "tool_use").length;
  const isComplete = Boolean(group.resultEntry);

  const childUnits = useMemo(
    () =>
      groupsToUnits(
        groupActivityEntries(group.children, { skipSubagentGrouping: true }),
      ),
    [group.children],
  );

  const metrics: string[] = [];
  if (toolCount > 0) metrics.push(`${toolCount} tools`);

  return (
    <SubagentShell
      name={agentName}
      status={isComplete ? "completed" : "running"}
      title={firstLine(parentEntry.message || parentEntry.input || "") || agentName}
      liveLine={isComplete ? "" : "working…"}
      resultPreview={group.resultEntry?.isError ? "" : firstLine(group.resultEntry?.output || "")}
      metrics={metrics}
      timestamp={parentEntry.timestampUnix}
    >
      {childUnits.length > 0 && (
        <div>
          <SectionLabel>Steps</SectionLabel>
          <div className="mt-1 space-y-px">
            {childUnits.map((u, i) => (
              <WorkUnitView key={workUnitKey(u, i)} unit={u} />
            ))}
          </div>
        </div>
      )}
      {group.resultEntry?.output && (
        <SubagentResultSection
          entry={group.resultEntry}
          fallbackText={group.resultEntry.output}
          isError={group.resultEntry.isError}
        />
      )}
    </SubagentShell>
  );
});

/**
 * Result pane that lazily loads the full output when the server sent a
 * truncated preview (output_truncated).
 */
function SubagentResultSection({
  entry,
  fallbackText,
  isError,
}: {
  entry?: ActivityEntry;
  fallbackText: string;
  isError: boolean;
}) {
  const resolved = useResolvedEntry(entry, true);
  const text = resolved.entry?.output || fallbackText;
  return (
    <div>
      <SectionLabel>{isError ? "Error" : "Result"}</SectionLabel>
      <div className="mt-1 space-y-1.5">
        {resolved.loading && (
          <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <Loader2 className="size-3 animate-spin" />
            <span>Loading full payload…</span>
          </div>
        )}
        {resolved.failed && (
          <p className="text-[11px] text-muted-foreground">
            Couldn't load the full payload — showing the truncated preview.
          </p>
        )}
        {isError ? (
          <CodePane text={text} tone="error" />
        ) : (
          <div className="rounded-md border border-border/50 bg-muted/15 px-3 py-2 text-sm">
            <MarkdownViewer content={text} />
          </div>
        )}
      </div>
    </div>
  );
}
