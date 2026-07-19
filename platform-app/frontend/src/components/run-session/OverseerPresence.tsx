import { useEffect, useId, useRef, useState } from "react";
import { Link } from "react-router-dom";

import { isDonePhase, toneSolid, toneText, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { AgentRun } from "@/rpc/platform/service_pb";

const INACTIVE_STATES = new Set(["cancelled", "detaching", "unavailable"]);

const STATE_PRESENTATION: Record<string, { label: string; tone: StatusTone; copy: string; symbol: string }> = {
  active: { label: "Active", tone: "running", copy: "Guarding this run", symbol: "✓" },
  observing: { label: "Observing", tone: "info", copy: "Watching without intervening", symbol: "○" },
  checking: { label: "Checking", tone: "info", copy: "Reviewing the latest checkpoint", symbol: "…" },
  capped: { label: "Cap reached", tone: "warning", copy: "Monitoring with interventions paused", symbol: "‖" },
  degraded: { label: "Degraded", tone: "warning", copy: "Supervision is operating fail-open", symbol: "!" },
  escalated: { label: "Escalated", tone: "danger", copy: "This run needs attention", symbol: "!" },
};

const VERDICT_PRESENTATION: Record<string, { label: string; feedback: FeedbackTone }> = {
  all_clear: { label: "All clear", feedback: "success" },
  steer: { label: "Guidance sent", feedback: "warning" },
  reject_completion: { label: "Completion held", feedback: "warning" },
  resolve_input: { label: "Input resolved", feedback: "success" },
  escalate: { label: "Escalated", feedback: "danger" },
};

type FeedbackTone = "none" | "success" | "warning" | "danger";

function normalize(value?: string): string {
  return value?.trim().toLowerCase() ?? "";
}

function isOverseerPresenceVisible(run: AgentRun): boolean {
  const state = normalize(run.overseerSummary?.state);
  return Boolean(run.overseer) && !run.overseerDetaching && !isDonePhase(run.phase) && !INACTIVE_STATES.has(state);
}

function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(() =>
    typeof window !== "undefined" && typeof window.matchMedia === "function"
      ? window.matchMedia("(prefers-reduced-motion: reduce)").matches
      : false,
  );

  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReduced(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  return reduced;
}

function overseerCopy(run: AgentRun, stateCopy: string): string {
  const state = normalize(run.overseerSummary?.state);
  const lastSummary = run.overseerSummary?.lastSummary.trim();
  if ((state === "degraded" || state === "escalated") && lastSummary) return lastSummary;
  const interval = run.overseer?.intervalMinutes;
  if (state === "checking") return stateCopy;
  if (interval && interval > 0) return `${stateCopy} · checks every ${interval}m`;
  return stateCopy;
}

/* How far the lids open per state (1 = wide). Sustained aperture is drawn in
   the path geometry so the iris stays round; only the transient blink squashes
   the eye group via CSS. */
const STATE_APERTURE: Record<string, number> = {
  active: 0.72,
  observing: 0.5,
  checking: 0.95,
  capped: 0.28,
  degraded: 0.6,
  escalated: 1,
};

/* The overseer mark: a procedural, tone-tinted eye.
   Primary   — lid aperture per lifecycle state (+ settle beat on change).
   Secondary — iris scans side to side while a checkpoint is under review.
   Ambient   — slow blink while guarding; halo pulse when escalated.
   All motion is CSS-driven (see index.css) and honours reduced motion. */
export function OverseerIris({ state }: { state: string }) {
  const aperture = STATE_APERTURE[state] ?? STATE_APERTURE.active;
  const lid = 11 * aperture;
  const escalated = state === "escalated";
  const irisR = escalated ? 5.6 : 5;
  const pupilR = escalated ? 3 : 2.2;
  return (
    <svg
      key={state}
      viewBox="0 0 32 32"
      fill="none"
      aria-hidden="true"
      data-iris-state={state}
      className="overseer-iris relative size-8"
    >
      <g className="overseer-iris-eye">
        <path
          d={`M3.5 16 Q16 ${16 - lid} 28.5 16 Q16 ${16 + lid} 3.5 16 Z`}
          stroke="var(--overseer-color)"
          strokeWidth="1.8"
          strokeLinejoin="round"
          fill="color-mix(in oklch, var(--color-background) 82%, transparent)"
        />
        <g className="overseer-iris-pupil">
          <circle cx="16" cy="16" r={irisR} stroke="var(--overseer-color)" strokeWidth="1.8" />
          <circle cx="16" cy="16" r={pupilR} fill="var(--overseer-color)" />
        </g>
      </g>
      {state === "capped" && (
        <line
          className="overseer-iris-cap"
          x1="9" y1="22" x2="23" y2="22"
          stroke="var(--overseer-color)"
          strokeWidth="1.6"
          strokeLinecap="round"
          opacity="0.75"
        />
      )}
      {escalated && (
        <g className="overseer-iris-rays" stroke="var(--overseer-color)" strokeWidth="1.5" strokeLinecap="round" opacity="0.85">
          <line x1="16" y1="2.2" x2="16" y2="5.2" />
          <line x1="5.2" y1="6.5" x2="7.4" y2="8.7" />
          <line x1="26.8" y1="6.5" x2="24.6" y2="8.7" />
        </g>
      )}
    </svg>
  );
}

export function OverseerPresence({ run, href }: { run: AgentRun; href?: string }) {
  const reducedMotion = usePrefersReducedMotion();
  const statusId = useId();
  const [feedback, setFeedback] = useState<FeedbackTone>("none");
  const feedbackTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const verdictAt = run.overseerSummary?.lastVerdictAtUnix ?? 0n;
  const previousVerdictAt = useRef(verdictAt);
  const state = normalize(run.overseerSummary?.state) || "active";
  const presentation = STATE_PRESENTATION[state] ?? STATE_PRESENTATION.active;
  const verdict = VERDICT_PRESENTATION[normalize(run.overseerSummary?.lastVerdict)];
  const visible = isOverseerPresenceVisible(run);
  const motionEnabled = visible && !reducedMotion;

  useEffect(() => {
    const previous = previousVerdictAt.current;
    previousVerdictAt.current = verdictAt;
    if (!previous || !verdictAt || previous === verdictAt || !verdict) return;

    if (feedbackTimer.current) clearTimeout(feedbackTimer.current);
    setFeedback(verdict.feedback);
    feedbackTimer.current = setTimeout(() => setFeedback("none"), 1400);
  }, [verdictAt, verdict]);

  useEffect(
    () => () => {
      if (feedbackTimer.current) clearTimeout(feedbackTimer.current);
    },
    [],
  );

  const resolvedHref =
    href ||
    (run.overseerSummary?.runName
      ? `/runs/${encodeURIComponent(run.namespace)}/${encodeURIComponent(run.overseerSummary.runName)}`
      : "");
  if (!visible) return null;

  const statusCopy = overseerCopy(run, presentation.copy);
  const statusDetail = /[.!?]$/.test(statusCopy) ? statusCopy : `${statusCopy}.`;
  const statusAnnouncement = `Overseer ${presentation.label.toLowerCase()}. ${statusDetail}${verdict ? ` Last verdict: ${verdict.label}.` : ""}`;
  const controlClassName = "overseer-presence-control group relative inline-flex size-10 shrink-0 items-center justify-center gap-1.5 overflow-visible rounded-xl border bg-background/90 px-1 shadow-sm backdrop-blur-sm transition-colors hover:bg-muted/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 md:size-9 xl:w-auto xl:max-w-40 xl:justify-start xl:pr-2.5";
  const controlContent = (
    <>
      <span className="overseer-iris-shell relative flex size-8 shrink-0 items-center justify-center" aria-hidden="true">
        <span className="overseer-iris-halo absolute inset-1 rounded-full bg-[color:var(--overseer-color)] opacity-15 blur-md transition-opacity group-hover:opacity-25" />
        <span className="overseer-check-focus pointer-events-none absolute inset-0 rounded-full" />
        <span key={verdictAt.toString()} className="overseer-verdict-ripple pointer-events-none absolute inset-1 rounded-full" />
        <OverseerIris state={state} />
        <span className={cn("absolute -right-0.5 -top-0.5 flex size-3.5 items-center justify-center rounded-full text-[8px] font-bold leading-none text-white ring-2 ring-background", toneSolid[presentation.tone])}>
          {presentation.symbol}
        </span>
      </span>
      <span className="hidden min-w-0 items-baseline gap-1.5 xl:flex">
        <span className="text-[10px] font-semibold uppercase tracking-[0.12em] text-foreground">Overseer</span>
        <span className={cn("truncate text-[10px] font-medium", toneText[presentation.tone])}>{presentation.label}</span>
      </span>
    </>
  );
  const controlProps = {
    className: controlClassName,
    "data-state": state,
    "data-feedback": feedback,
    "data-motion": motionEnabled ? "playing" : "paused",
    "aria-describedby": statusId,
    title: `${presentation.label}: ${statusCopy}`,
  } as const;

  return (
    <span className="relative inline-flex shrink-0">
      <span
        key={`overseer-status-${verdictAt}`}
        id={statusId}
        className="sr-only"
        role="status"
        aria-live="polite"
        aria-atomic="true"
        aria-label={statusAnnouncement}
      >
        {statusAnnouncement}
      </span>
      {resolvedHref ? (
        <Link
          to={resolvedHref}
          {...controlProps}
          aria-label={`Open overseer chat — ${presentation.label}`}
        >
          {controlContent}
        </Link>
      ) : (
        <span
          {...controlProps}
          role="button"
          tabIndex={0}
          aria-disabled="true"
          aria-label={`Overseer chat unavailable — ${presentation.label}`}
        >
          {controlContent}
        </span>
      )}
    </span>
  );
}
