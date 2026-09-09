import { useState } from "react";
import { Eye, ShieldAlert, ShieldCheck, Zap } from "lucide-react";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  APPROVAL_MODES,
  APPROVAL_MODE_META,
  setComputerUseApprovalMode,
  useComputerUseApprovalMode,
  type ComputerUseApprovalMode,
} from "@/lib/computer-use-preferences";
import { toneSoft, toneText, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";

const APPROVAL_MODE_TONE: Record<ComputerUseApprovalMode, StatusTone> = {
  manual: "success",
  assisted: "info",
  auto: "warning",
};

export function ApprovalModeIcon({ mode, className }: { mode: ComputerUseApprovalMode; className?: string }) {
  const Icon = mode === "manual" ? ShieldCheck : mode === "assisted" ? Eye : ShieldAlert;
  return <Icon className={className} />;
}

/** Compact pill naming the current approval mode; used in the run panel header. */
export function ApprovalModeBadge({ mode, className }: { mode: ComputerUseApprovalMode; className?: string }) {
  return (
    <span className={cn("inline-flex h-5 items-center gap-1 rounded-full px-2 text-[11px] font-medium", toneSoft[APPROVAL_MODE_TONE[mode]], className)}
      title={APPROVAL_MODE_META[mode].description}>
      {mode === "auto" ? <Zap className="size-3" /> : <ApprovalModeIcon mode={mode} className="size-3" />}
      {APPROVAL_MODE_META[mode].short}
    </span>
  );
}

const SKIP_ALL_WARNING =
  "Agent requests in a supervised session will execute in the approved window without asking you first. " +
  "Nothing prevents a send, submit, deletion, or purchase once the agent decides to do it, and on-screen content " +
  "may steer it. Use only with test windows that contain no private data, keep the emergency stop within reach, " +
  "and remember that you remain responsible for every action taken.";

/**
 * Graduated approval-mode picker. Stepping up to "Skip all approvals" always
 * goes through a confirmation dialog first (as Copilot and Claude do); stepping
 * down applies immediately.
 */
export function ApprovalModeControl({ variant = "cards", disabled = false, className }: {
  variant?: "cards" | "compact";
  disabled?: boolean;
  className?: string;
}) {
  const mode = useComputerUseApprovalMode();
  const [confirmAuto, setConfirmAuto] = useState(false);

  const select = (next: ComputerUseApprovalMode) => {
    if (next === mode) return;
    if (next === "auto") setConfirmAuto(true);
    else setComputerUseApprovalMode(next);
  };

  const dialog = (
    <ConfirmDialog
      open={confirmAuto}
      onOpenChange={setConfirmAuto}
      title="Skip all approvals?"
      description={SKIP_ALL_WARNING}
      confirmLabel="Skip all approvals"
      destructive
      onConfirm={() => setComputerUseApprovalMode("auto")}
    />
  );

  if (variant === "compact") {
    return (
      <>
        <label className={cn("inline-flex items-center gap-1.5 text-xs", className)}>
          <span className="text-muted-foreground">Approvals</span>
          <select aria-label="Approval mode" value={mode} disabled={disabled}
            className={cn("h-7 rounded-md border bg-background px-1.5 text-xs font-medium disabled:opacity-50", toneText[APPROVAL_MODE_TONE[mode]])}
            onChange={(event) => select(event.target.value as ComputerUseApprovalMode)}>
            {APPROVAL_MODES.map((option) => <option key={option} value={option}>{APPROVAL_MODE_META[option].short}</option>)}
          </select>
        </label>
        {dialog}
      </>
    );
  }

  return (
    <>
      <div role="radiogroup" aria-label="Approval mode" className={cn("grid gap-2 sm:grid-cols-3", className)}>
        {APPROVAL_MODES.map((option) => {
          const active = option === mode;
          const tone = APPROVAL_MODE_TONE[option];
          return (
            <button key={option} type="button" role="radio" aria-checked={active} disabled={disabled}
              onClick={() => select(option)}
              className={cn(
                "flex flex-col items-start gap-1.5 rounded-lg border p-3 text-left transition-colors",
                "hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50",
                active && option === "manual" && "border-[color-mix(in_oklch,var(--tone-success)_45%,transparent)] bg-[color-mix(in_oklch,var(--tone-success)_6%,transparent)]",
                active && option === "assisted" && "border-[color-mix(in_oklch,var(--tone-info)_45%,transparent)] bg-[color-mix(in_oklch,var(--tone-info)_6%,transparent)]",
                active && option === "auto" && "border-[color-mix(in_oklch,var(--tone-warning)_45%,transparent)] bg-[color-mix(in_oklch,var(--tone-warning)_6%,transparent)]",
              )}>
              <span className="flex items-center gap-2">
                <span className={cn("grid size-6 shrink-0 place-items-center rounded-md [&_svg]:size-3.5", active ? toneSoft[tone] : "bg-muted/60 text-muted-foreground ring-1 ring-inset ring-border/60")}>
                  {option === "auto" ? <Zap /> : <ApprovalModeIcon mode={option} />}
                </span>
                <span className="text-[13px] font-medium">{APPROVAL_MODE_META[option].label}</span>
              </span>
              <span className="text-[11.5px] leading-relaxed text-muted-foreground">{APPROVAL_MODE_META[option].description}</span>
              {option === "manual" && <span className={cn("text-[11px] font-medium", toneText.success)}>Recommended</span>}
              {option === "auto" && <span className={cn("text-[11px] font-medium", toneText.warning)}>Asks for confirmation before turning on</span>}
            </button>
          );
        })}
      </div>
      {dialog}
    </>
  );
}
