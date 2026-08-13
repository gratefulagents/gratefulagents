import * as React from "react";
import { Activity, ChevronRight, Trash2 } from "lucide-react";

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

function formatBody(body: string): string {
  try {
    return JSON.stringify(JSON.parse(body), null, 2);
  } catch {
    return body;
  }
}

function DetailSection({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <div className="mb-1 text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </div>
      {children}
    </div>
  );
}

function HeaderList({ headers }: { headers: [string, string][] }) {
  if (headers.length === 0) {
    return <div className="text-[11px] text-muted-foreground">(none captured)</div>;
  }
  return (
    <div className="space-y-0.5">
      {headers.map(([name, value], i) => (
        <div key={`${name}-${i}`} className="break-all font-mono text-[11px]">
          <span className="text-muted-foreground">{name}:</span> {value}
        </div>
      ))}
    </div>
  );
}

function BodyBlock({ body }: { body: string | null }) {
  if (body == null) {
    return <div className="text-[11px] text-muted-foreground">(empty)</div>;
  }
  return (
    <pre className="max-h-56 overflow-auto whitespace-pre-wrap break-all rounded bg-muted/50 p-2 font-mono text-[11px]">
      {formatBody(body)}
    </pre>
  );
}

function CallDetails({ call }: { call: ApiCallRecord }) {
  return (
    <div className="space-y-3 border-t bg-muted/20 px-4 py-3">
      <DetailSection label="General">
        <div className="space-y-0.5 font-mono text-[11px]">
          <div className="break-all">
            <span className="text-muted-foreground">URL:</span> {call.url}
          </div>
          <div>
            <span className="text-muted-foreground">Status:</span>{" "}
            <span className={statusClass(call)}>
              {call.status ?? "-"}
              {call.statusText ? ` ${call.statusText}` : ""}
            </span>
          </div>
          <div>
            <span className="text-muted-foreground">Duration:</span>{" "}
            {formatDuration(call)}
          </div>
          {call.error && (
            <div className="break-all text-destructive">
              <span className="text-muted-foreground">Error:</span> {call.error}
            </div>
          )}
        </div>
      </DetailSection>
      <DetailSection label="Request Headers">
        <HeaderList headers={call.requestHeaders} />
      </DetailSection>
      {call.requestBody != null && (
        <DetailSection label="Request Body">
          <BodyBlock body={call.requestBody} />
        </DetailSection>
      )}
      <DetailSection label="Response Headers">
        <HeaderList headers={call.responseHeaders} />
      </DetailSection>
      <DetailSection label="Response Body">
        <BodyBlock body={call.responseBody} />
      </DetailSection>
    </div>
  );
}

export function ApiMonitorSidebar() {
  const calls = React.useSyncExternalStore(
    subscribeApiCalls,
    getApiCallsSnapshot,
    getApiCallsSnapshot,
  );
  const [expandedId, setExpandedId] = React.useState<number | null>(null);

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
      <SheetContent className="w-[min(480px,92vw)] gap-0 p-0 pt-safe pb-safe sm:max-w-[540px]">
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
            onClick={() => {
              clearApiCalls();
              setExpandedId(null);
            }}
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
              {calls.map((call) => {
                const expanded = expandedId === call.id;
                return (
                  <div key={call.id}>
                    <button
                      type="button"
                      className="w-full px-4 py-3 text-left transition-colors hover:bg-muted/40"
                      onClick={() =>
                        setExpandedId(expanded ? null : call.id)
                      }
                      aria-expanded={expanded}
                    >
                      <div className="flex items-center gap-2">
                        <ChevronRight
                          className={cn(
                            "size-3 shrink-0 text-muted-foreground transition-transform",
                            expanded && "rotate-90",
                          )}
                        />
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
                    </button>
                    {expanded && <CallDetails call={call} />}
                  </div>
                );
              })}
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}
