import type { EvidenceGateResult } from '../rpc/platform/service_pb';
import { Badge } from "@/components/ui/badge";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { useState } from "react";
import { ChevronRight, CheckCircle2, XCircle, SkipForward } from "lucide-react";
import { toneText } from "@/lib/status";

interface Props {
  gates: EvidenceGateResult[];
  finishAttempts: number;
}

export function EvidenceGatesCard({ gates, finishAttempts }: Props) {
  const [open, setOpen] = useState(false);

  if (gates.length === 0) return null;

  const allPassed = gates.every(g => g.passed || g.skipped);
  const passedCount = gates.filter(g => g.passed).length;
  const skippedCount = gates.filter(g => g.skipped).length;

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger>
        <button
          type="button"
          className="w-full flex items-center gap-2 text-left py-1 hover:bg-muted/30 rounded -mx-1 px-1 transition-colors"
        >
          <ChevronRight className={`w-3 h-3 shrink-0 text-muted-foreground transition-transform duration-150 ${open ? "rotate-90" : ""}`} />
          {allPassed ? (
            <CheckCircle2 className={`w-3.5 h-3.5 ${toneText.success} shrink-0`} />
          ) : (
            <XCircle className={`w-3.5 h-3.5 ${toneText.danger} shrink-0`} />
          )}
          <span className="text-xs font-medium text-foreground">Evidence Gates</span>
          <Badge variant="secondary" className="text-[10px] font-mono">
            {passedCount}/{gates.length} passed
          </Badge>
          {skippedCount > 0 && (
            <Badge variant="outline" className="text-[10px] font-mono text-muted-foreground">
              {skippedCount} skipped
            </Badge>
          )}
          <span className="ml-auto text-[10px] text-muted-foreground font-mono">
            attempt {finishAttempts}
          </span>
        </button>
      </CollapsibleTrigger>

      <CollapsibleContent>
        <div className="ml-5 mt-1 space-y-0.5 pb-1">
          {gates.map((gate, i) => (
            <div
              key={i}
              className={`flex items-center gap-2 py-0.5 text-xs ${gate.skipped ? "opacity-50" : ""}`}
            >
              {gate.skipped ? (
                <SkipForward className="w-3 h-3 text-muted-foreground shrink-0" />
              ) : gate.passed ? (
                <CheckCircle2 className={`w-3 h-3 ${toneText.success} shrink-0`} />
              ) : (
                <XCircle className={`w-3 h-3 ${toneText.danger} shrink-0`} />
              )}
              <span className="font-medium text-foreground shrink-0">{gate.gate}</span>
              <span className="text-muted-foreground truncate">{gate.reason || '—'}</span>
            </div>
          ))}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}
