import { useState } from "react";
import { Check, ChevronDown, CornerDownRight, FileText, Layers, MessageCircleQuestion, Sparkles } from "lucide-react";

import { MarkdownViewer } from "@/components/MarkdownViewer";
import { renderPlanDialogButton } from "@/components/run-session/helpers";
import { Button } from "@/components/ui/button";
import { extractUserAnswer, firstLine, formatClock, formatUsd, formatWall, isCostKnown, parsePlan, parseQuestion, wallSeconds } from "@/lib/activityLogFormat";
import { formatDuration, formatTokens } from "@/lib/activityGrouping";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

export function QuestionCard({
  use,
  result,
}: {
  use: ActivityEntry;
  result?: ActivityEntry;
}) {
  const { question, choices } = parseQuestion(use);
  const answer = result ? extractUserAnswer(result) : null;
  const answerIsChoice =
    answer != null && choices.some((c) => c.trim() === answer.trim());

  return (
    <div className="space-y-2 py-0.5">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <MessageCircleQuestion className="size-3.5" />
        Question
      </div>
      {question && (
        <div className="text-sm leading-relaxed text-foreground">
          <MarkdownViewer content={question} />
        </div>
      )}
      {choices.length > 0 && (
        <div className="flex flex-wrap gap-1.5">
          {choices.map((c) => {
            const isPicked = answer != null && answer.trim() === c.trim();
            return (
              <span
                key={c}
                className={`rounded-md border px-2 py-0.5 text-xs ${
                  isPicked
                    ? "border-foreground/30 bg-muted/50 font-medium text-foreground"
                    : "border-border/50 text-muted-foreground"
                }`}
              >
                {isPicked && <Check className="mr-1 inline size-3 align-[-2px]" />}
                {c}
              </span>
            );
          })}
        </div>
      )}
      {answer && !answerIsChoice && (
        <div className="flex items-start gap-1.5 text-sm">
          <CornerDownRight className="mt-0.5 size-3.5 shrink-0 text-muted-foreground/50" />
          <span className="min-w-0 whitespace-pre-wrap break-words text-foreground/90">
            {answer}
          </span>
        </div>
      )}
      {!answer && (
        <p className="text-xs italic text-muted-foreground/60">
          Awaiting response…
        </p>
      )}
    </div>
  );
}

export function PlanCard({
  use,
  planContent,
}: {
  use: ActivityEntry;
  planContent?: string;
}) {
  const { summary, plan, capturedPlan, recommended } = parsePlan(use);
  const popupContent = capturedPlan || planContent || plan;

  return (
    <div className="space-y-2 py-0.5">
      <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
        <FileText className="size-3.5" />
        Plan
        {popupContent &&
          renderPlanDialogButton(
            popupContent,
            <Button variant="ghost" size="xs" className="ml-auto gap-1.5">
              <FileText className="size-3" />
              View plan
            </Button>,
          )}
      </div>
      {summary && (
        <div className="text-sm leading-relaxed text-foreground">
          <MarkdownViewer content={summary} />
        </div>
      )}
      {!summary && plan && (
        <div className="border-l-2 border-border/50 pl-3 text-sm">
          <MarkdownViewer content={plan} />
        </div>
      )}
      {recommended && (
        <p className="text-xs text-muted-foreground">
          <span className="font-medium text-foreground/80">Recommended:</span>{" "}
          {recommended}
        </p>
      )}
    </div>
  );
}

// ─── Reasoning ──────────────────────────────────────────────────────────────

export function ReasoningCard({ entries }: { entries: ActivityEntry[] }) {
  const [open, setOpen] = useState(true);
  const text = entries
    .map((e) => e.message)
    .filter(Boolean)
    .join("\n\n")
    .trim();
  if (!text) return null;
  const preview = firstLine(text);
  const duration = formatWall(wallSeconds(entries));

  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        title={formatClock(entries[0].timestampUnix)}
        className="group flex w-full items-center gap-1.5 py-0.5 text-left cursor-pointer"
      >
        <ChevronDown
          className={`size-3 shrink-0 text-muted-foreground/40 transition-transform group-hover:text-muted-foreground ${
            open ? "" : "-rotate-90"
          }`}
        />
        <span className="shrink-0 text-xs italic text-muted-foreground/70 transition-colors group-hover:text-muted-foreground">
          {duration ? `Thought for ${duration}` : "Thought"}
        </span>
        {!open && (
          <span className="min-w-0 flex-1 truncate text-xs italic text-muted-foreground/50">
            {preview}
          </span>
        )}
      </button>
      {open && (
        <div className="mt-1 border-l-2 border-border/50 pl-3 text-sm leading-relaxed opacity-65">
          <MarkdownViewer content={text} />
        </div>
      )}
    </div>
  );
}

// ─── Dividers & meta ────────────────────────────────────────────────────────

export function PhaseDivider({ entry }: { entry: ActivityEntry }) {
  const label = entry.message || entry.step || "Phase";
  return (
    <div
      className="flex items-center gap-3 py-1"
      title={formatClock(entry.timestampUnix)}
    >
      <div className="h-px flex-1 bg-border/70" />
      <span className="flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
        <Layers className="size-3 text-cyan-500/80" />
        {label}
      </span>
      <div className="h-px flex-1 bg-border/70" />
    </div>
  );
}

export function MetaLine({ entry }: { entry: ActivityEntry }) {
  const parts: string[] = ["Session complete"];
  if (isCostKnown(entry)) parts.push(formatUsd(entry.costUsd));
  if (entry.numTurns) parts.push(`${entry.numTurns} turns`);
  if (entry.durationMs) parts.push(formatDuration(entry.durationMs));
  if (entry.inputTokens || entry.outputTokens) {
    parts.push(
      `${formatTokens(entry.inputTokens)} in / ${formatTokens(entry.outputTokens)} out`,
    );
  }
  return (
    <div
      className="flex items-center justify-center gap-2 py-0.5"
      title={formatClock(entry.timestampUnix)}
    >
      <Sparkles className="size-3 text-muted-foreground/50" />
      <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
        {parts.join(" · ")}
      </span>
    </div>
  );
}
