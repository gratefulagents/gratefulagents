import { Activity } from "lucide-react";

import { ObservabilityOverview } from "@/components/ObservabilityOverview";

export function ObservabilityPage() {
  return (
    <div className="space-y-5 pb-8">
      <header className="flex items-center gap-3">
        <div className="grid size-9 place-items-center rounded-xl bg-primary/12 text-primary ring-1 ring-inset ring-primary/20">
          <Activity className="size-[18px]" />
        </div>
        <div>
          <h1 className="text-[24px] font-semibold leading-none tracking-[-0.025em]">Observability</h1>
          <p className="mt-1 text-[12.5px] text-muted-foreground">
            Track historical usage, cost, and reliability across visible runs.
          </p>
        </div>
      </header>

      <ObservabilityOverview />
    </div>
  );
}
