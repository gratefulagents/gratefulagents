import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";

/**
 * Readiness pill for resources exposing a k8s-style Ready condition.
 * Uses the semantic tone system (success/danger/neutral) rather than
 * brand/destructive Badge variants so "Ready" reads as healthy-green.
 */
export function ReadyBadge({ status }: { status: string }) {
  const [tone, label]: [StatusTone, string] =
    status === "True"
      ? ["success", "Ready"]
      : status === "False"
        ? ["danger", "Not Ready"]
        : ["neutral", "Unknown"];
  return (
    <span
      className={cn(
        "inline-flex h-5 w-fit shrink-0 items-center rounded-full px-2 text-[11px] font-medium whitespace-nowrap",
        toneSoft[tone],
      )}
    >
      {label}
    </span>
  );
}
