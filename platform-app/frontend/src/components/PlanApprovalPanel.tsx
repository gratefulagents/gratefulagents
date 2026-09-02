import { useEffect, useRef } from "react";
import { FileText } from "lucide-react";

import { Button } from "@/components/ui/button";
import { renderPlanDialogButton } from "@/components/run-session/helpers";

type PlanApprovalPanelProps = {
  planContent: string;
  disabled?: boolean;
  onSendMessage: (message: string) => void | Promise<void>;
  /** When provided, renders a secondary "Request changes" action (e.g. to focus the composer). */
  onRequestChanges?: () => void;
};

// Compact inline bar shown while a run has a plan ready for approval. The plan
// itself is read in the shared Plan dialog (the same one opened from the header)
// rather than being embedded here, so the bar stays small.
export function PlanApprovalPanel({
  planContent,
  disabled = false,
  onSendMessage,
  onRequestChanges,
}: PlanApprovalPanelProps) {
  const approveButtonRef = useRef<HTMLButtonElement>(null);

  // Only steal focus when nothing else has it — never yank it out of the composer.
  useEffect(() => {
    if (document.activeElement === document.body) {
      approveButtonRef.current?.focus({ preventScroll: true });
    }
  }, []);

  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
      <span className="text-xs text-muted-foreground">
        <span className="font-medium text-foreground">Plan ready</span> · review the plan, then
        approve to continue in this mode.
      </span>
      <div className="ml-auto flex items-center gap-2">
        {planContent
          ? renderPlanDialogButton(
              planContent,
              <Button variant="ghost" size="sm" className="gap-1.5">
                <FileText className="size-3.5" />
                View plan
              </Button>,
            )
          : null}
        {onRequestChanges && (
          <Button type="button" variant="outline" size="sm" onClick={onRequestChanges} disabled={disabled}>
            Request changes
          </Button>
        )}
        <Button
          ref={approveButtonRef}
          type="button"
          size="sm"
          onClick={() => void onSendMessage("__action:accept_plan")}
          disabled={disabled}
        >
          Approve &amp; continue
        </Button>
      </div>
    </div>
  );
}
