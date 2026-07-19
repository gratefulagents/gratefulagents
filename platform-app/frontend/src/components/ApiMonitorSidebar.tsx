import * as React from "react";
import { Activity, Trash2 } from "lucide-react";

import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import {
  clearApiCalls,
  getApiCallsSnapshot,
  subscribeApiCalls,
  type ApiCallRecord,
} from "@/lib/api-monitor";

function formatDuration(record: ApiCallRecord): string {
  if (record.durationMs == null) return "pending";
  if (record.durationMs < 1000) return `${Math.round(record.durationMs)} ms`;
  return `${(record.durationMs / 1000).toFixed(2)} s`;
}

function statusClass(record: ApiCallRecord): string {
  if (record.state === "pending") return "text-muted-foreground";
  if (record.state === "success") return toneText.success;
  return "text-destructive";
}

export function ApiMonitorSidebar() {
  const calls = React.useSyncExternalStore(
    subscribeApiCalls,
    getApiCallsSnapshot,
    getApiCallsSnapshot,
  );

  const pendingCount = calls.filter((call) => call.state === "pending").length;
  const lastDuration = calls.find((call) => call.durationMs != null);

  return (
    <Sheet>
      <SheetTrigger
        render={
          <Button
            variant="ghost"
            size="icon-sm"
            className="relative size-10 md:size-7"
            title="API calls"
            aria-label="Open API call monitor"
          />
        }
      >
        <Activity className="size-4" />
        {pendingCount > 0 && (
          <span className="absolute -right-0.5 -top-0.5 size-2 rounded-full bg-primary" />
        )}
      </SheetTrigger>
      <SheetContent className="w-[min(360px,92vw)] gap-0 p-0 pt-safe pb-safe sm:max-w-[420px]">
        <SheetHeader className="border-b pr-12">
          <SheetTitle>API Calls</SheetTitle>
          <SheetDescription>
            {calls.length === 0
              ? "No calls recorded yet."
              : `${calls.length} recent calls${lastDuration ? ` - latest ${formatDuration(lastDuration)}` : ""}`}
          </SheetDescription>
        </SheetHeader>

        <div className="flex items-center justify-between border-b px-4 py-2">
          <span className="text-[11px] font-mono text-muted-foreground">
            {pendingCount} pending
          </span>
          <Button
            variant="ghost"
            size="sm"
            onClick={clearApiCalls}
            disabled={calls.length === 0}
          >
            <Trash2 className="size-3.5" />
            Clear
          </Button>
        </div>

        <div className="min-h-0 flex-1 overflow-auto">
          {calls.length === 0 ? (
            <div className="px-4 py-8 text-center text-sm text-muted-foreground">
              Make a request and it will show up here.
            </div>
          ) : (
            <div className="divide-y">
              {calls.map((call) => (
                <div key={call.id} className="px-4 py-3">
                  <div className="flex items-center gap-2">
                    <span className="rounded bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                      {call.method}
                    </span>
                    <span className={cn("font-mono text-[11px]", statusClass(call))}>
                      {call.status ?? (call.state === "pending" ? "..." : "ERR")}
                    </span>
                    <span className="ml-auto font-mono text-[11px] text-muted-foreground">
                      {formatDuration(call)}
                    </span>
                  </div>
                  <div className="mt-1 truncate font-mono text-[12px]" title={call.url}>
                    {call.path}
                  </div>
                  <div className="mt-1 text-[11px] text-muted-foreground">
                    {new Date(call.startedAt).toLocaleTimeString()}
                    {call.error ? ` - ${call.error}` : ""}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
