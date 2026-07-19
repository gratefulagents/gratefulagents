import { cn } from "@/lib/utils";
import { phaseTone, isLivePhase, toneSoft } from "@/lib/status";
import { isRunComputing, runStatusLabel, runStatusTone } from "@/lib/runStatus";
import type { AgentRun } from "@/rpc/platform/service_pb";

/**
 * Canonical run-phase badge. Maps the phase to a semantic tone and renders a
 * compact pill with a status dot (which pulses while the run is live).
 */
export function StatusBadge({
  phase,
  run,
  className,
}: {
  phase: string;
  run?: AgentRun;
  className?: string;
}) {
  const tone = run ? runStatusTone(run) : phaseTone(phase);
  const live = run ? isRunComputing(run) : isLivePhase(phase);
  const label = run ? runStatusLabel(run) : phase || "Unknown";

  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 h-[20px] pl-1.5 pr-2",
        "rounded-full text-[11px] font-medium tracking-tight whitespace-nowrap select-none",
        toneSoft[tone],
        className,
      )}
    >
      <span className="relative inline-flex size-1.5 rounded-full bg-current">
        {live && (
          <span className="absolute inset-0 rounded-full bg-current opacity-60 motion-safe:animate-ping" />
        )}
      </span>
      {label}
    </span>
  );
}
