import { useEffect, useRef } from "react";
import { FileText } from "lucide-react";

import { Button } from "@/components/ui/button";
import { renderPlanDialogButton } from "@/components/run-session/helpers";

type PlanApprovalPanelProps = {
  planContent: string;
  disabled?: boolean;
  onSendMessage: (message: string) => void | Promise<void>;
};

// Compact inline bar shown while a run has a plan ready for approval. The plan
// itself is read in the shared Plan dialog (the same one opened from the header)
// rather than being embedded here, so the bar stays small.
export function PlanApprovalPanel({
  planContent,
  disabled = false,
  onSendMessage,
}: PlanApprovalPanelProps) {
  const approveButtonRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    approveButtonRef.current?.focus();
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
