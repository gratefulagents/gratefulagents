import { memo, useMemo, useState, type ReactNode } from "react";
import { AlertTriangle, Check, ChevronRight, Clock3, HelpCircle, Loader2, XCircle } from "lucide-react";

import { MarkdownViewer } from "@/components/MarkdownViewer";
import { AgentTypeChip } from "@/components/ui/agent-type-chip";
import { LiveDot } from "@/components/ui/live-dot";
import { Collapse } from "./Collapse";
import { CodePane } from "./DetailPanes";
import { useResolvedEntry } from "./detailContext";
import { useSubagentContext } from "./subagentContext";
import { entryIdentity, groupsToUnits, workUnitKey } from "./feedModel";
import { WorkUnitView } from "./WorkRows";
import { cleanSubagentDescription, groupActivityEntries, formatDuration, formatTokens, subagentPromptMarkdown, subagentTitleFromPrompt, type ActivityGroup } from "@/lib/activityGrouping";
import { firstLine, formatClock, formatUsd } from "@/lib/activityLogFormat";
import { getSubagentColor } from "@/lib/subagentColors";
import {
  classifySubagentStatus,
  isLiveSubagentStatus as isLiveStatus,
  subagentStatusLabel,
} from "@/lib/subagentStatus";
import { toneSoft, toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
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
  return isLiveStatus(status);
}

export function SubagentStatusIcon({ status }: { status: string }) {
  const label = subagentStatusLabel(status);
  switch (classifySubagentStatus(status)) {
    case "live":
      return (
        <span
          className="flex size-3.5 shrink-0 items-center justify-center"
          role="img"
          aria-label={label}
        >
          <LiveDot tone="running" pulse />
        </span>
      );
    case "waiting":
      return <Clock3 className={`size-3.5 shrink-0 ${toneText.warning}`} aria-label={label} />;
    case "failed":
      return <XCircle className={`size-3.5 shrink-0 ${toneText.danger}`} aria-label={label} />;
    case "stopped":
      return (
        <AlertTriangle className={`size-3.5 shrink-0 ${toneText.warning}`} aria-label={label} />
      );
    case "succeeded":
      return <Check className={`size-3.5 shrink-0 ${toneText.success}`} aria-label={label} />;
    default:
      // A status this build does not recognise: show it as such rather than
      // as a green check.
      return (
        <span className="flex size-3.5 shrink-0 items-center justify-center" title={label}>
          <HelpCircle className="size-3.5 text-muted-foreground" aria-label={label} />
        </span>
      );
  }
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
  ordinal,
}: {
  name: string;
  status: string;
  title: string;
  /** Run-wide `#n`, when the run's graph knows this task. */
  ordinal?: number;
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
  const category = classifySubagentStatus(status);
  const isWaiting = category === "waiting";
  const showBadge =
    category === "failed" || category === "stopped" || category === "unknown" || isWaiting;

  return (
    <div
      className={`overflow-hidden rounded-lg border border-border/50 border-l-2 ${color.border}`}
    >
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        title={formatClock(timestamp)}
        className="flex w-full items-start gap-2.5 px-3 py-2.5 text-left transition-colors hover:bg-muted/30 cursor-pointer focus-visible:outline-2 focus-visible:outline-ring focus-visible:-outline-offset-2"
      >
        <span className="mt-0.5">
          <SubagentStatusIcon status={status} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            {ordinal !== undefined && (
              <span className="shrink-0 font-mono text-3xs font-semibold tabular-nums text-muted-foreground/80">
                #{ordinal}
              </span>
            )}
            {name && <AgentTypeChip type={name} />}
            <span className="min-w-0 truncate text-xs font-medium text-foreground/90">
              {title}
            </span>
            {showBadge && (
              <span
                className={`rounded-sm px-1.5 py-px text-3xs font-semibold uppercase tracking-wider ${
                  category === "failed"
                    ? toneSoft.danger
                    : category === "unknown"
                      ? toneSoft.neutral
                      : toneSoft.warning
                }`}
              >
                {subagentStatusLabel(status)}
              </span>
            )}
          </span>
          {isRunning && liveLine && (
            <span
              className="mt-1 flex items-center gap-1.5 text-xs text-muted-foreground"
              title={liveLine}
            >
              <LiveDot tone="running" pulse size="xs" />
              <span className="min-w-0 line-clamp-1">{liveLine}</span>
            </span>
          )}
          {!isRunning && !open && resultPreview && (
            <span className="mt-1 block truncate text-xs text-muted-foreground" title={resultPreview}>
              {resultPreview}
            </span>
          )}
          {metrics.length > 0 && (
            <span className="mt-1 block truncate font-mono text-3xs tabular-nums text-muted-foreground/60">
              {metrics.join(" · ")}
            </span>
          )}
        </span>
        <ChevronRight
          aria-hidden="true"
          className={cn(
            "mt-0.5 size-3.5 shrink-0 text-muted-foreground/50 transition-transform",
            open && "rotate-90",
          )}
        />
      </button>
      <Collapse open={open} className="space-y-3 border-t border-border/40 px-3 py-2.5">
        {children}
      </Collapse>
    </div>
  );
}

export function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <p className="text-3xs font-medium uppercase tracking-wider text-muted-foreground/60">
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
        className="flex min-h-6 items-center gap-1.5 rounded-sm text-3xs font-medium uppercase tracking-wider text-muted-foreground/60 transition-colors hover:text-muted-foreground cursor-pointer focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2"
      >
        <ChevronRight
          aria-hidden="true"
          className={`size-3 transition-transform ${open ? "rotate-90" : ""}`}
        />
        Prompt
        {!open && (
          <span className="normal-case tracking-normal font-normal text-muted-foreground/50 truncate max-w-[24rem]">
            {prompt.replace(/\s+/g, " ").slice(0, 80)}…
          </span>
        )}
      </button>
      <Collapse
        open={open}
        className="mt-1.5 max-h-80 overflow-y-auto rounded-md border border-border/50 bg-muted/15 px-3 py-2 text-sm"
      >
        <MarkdownViewer content={prompt} />
      </Collapse>
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

  // Prefer the task's own prompt over the parent tool call's raw JSON input:
  // a DAG batch shares one tool call whose payload lists every task.
  const promptContent =
    group.subagentPrompt ||
    subagentPromptMarkdown(parentToolEntry?.input || parentToolEntry?.inputRaw || "");

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

  const shared = useSubagentContext();
  const metrics: string[] = [];
  if (group.subagentModel) metrics.push(group.subagentModel);
  if (group.toolCount > 0) metrics.push(`${group.toolCount} tools`);
  if (group.subagentNumTurns > 0) metrics.push(`${group.subagentNumTurns} turns`);
  if (group.totalTokens > 0n) metrics.push(`${formatTokens(group.totalTokens)} tok`);
  if (group.durationMs > 0n) metrics.push(formatDuration(group.durationMs));
  if (group.subagentCostKnown) metrics.push(formatUsd(group.subagentCostUsd));

  const title =
    cleanSubagentDescription(group.subagentDescription) ||
    subagentTitleFromPrompt(group.subagentPrompt) ||
    (group.toolCount > 0
      ? `${group.toolCount} tool ${group.toolCount === 1 ? "call" : "calls"}`
      : "Subagent task");
  const ordinal = group.taskId ? shared.ordinalByTaskId.get(group.taskId) : undefined;

  return (
    <SubagentShell
      name={group.subagentType}
      status={group.subagentStatus}
      title={title}
      ordinal={ordinal}
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
              className={`rounded-sm px-1.5 py-px font-mono text-3xs ${toneSoft.neutral}`}
            >
              {d}
            </span>
          ))}
          {waitingOn.map((w) => (
            <span
              key={`wait-${w}`}
              className={`rounded-sm px-1.5 py-px font-mono text-3xs ${toneSoft.warning}`}
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

  const promptContent = subagentPromptMarkdown(
    parentEntry.input || parentEntry.inputRaw || parentEntry.message,
  );

  return (
    <SubagentShell
      name={agentName}
      status={isComplete ? "completed" : "running"}
      title={
        firstLine(
          parentEntry.message ||
            subagentPromptMarkdown(parentEntry.input || parentEntry.inputRaw),
        ) || agentName
      }
      liveLine={isComplete ? "" : "working…"}
      resultPreview={group.resultEntry?.isError ? "" : firstLine(group.resultEntry?.output || "")}
      metrics={metrics}
      timestamp={parentEntry.timestampUnix}
    >
      {promptContent && <PromptToggle prompt={promptContent} />}
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
          <div className="flex items-center gap-1.5 text-2xs text-muted-foreground">
            <Loader2 className="size-3 animate-spin" />
            <span>Loading full payload…</span>
          </div>
        )}
        {resolved.failed && (
          <p className="text-2xs text-muted-foreground">
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
