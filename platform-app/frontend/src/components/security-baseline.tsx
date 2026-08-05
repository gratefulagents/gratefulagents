import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export const BASELINE_STATES = [
  "new",
  "recurring",
  "regressed",
  "resolved",
  "reopened",
] as const;

const baselineTone: Record<string, string> = {
  new: "bg-sky-500/15 text-sky-700 dark:text-sky-300",
  recurring: "bg-muted text-muted-foreground",
  regressed: "bg-red-500/15 text-red-700 dark:text-red-300",
  resolved: "bg-emerald-500/15 text-emerald-700 dark:text-emerald-300",
  reopened: "bg-amber-500/15 text-amber-700 dark:text-amber-300",
};

/** BaselineBadge renders the scan-to-scan baseline state as a chip. */
export function BaselineBadge({ state }: { state: string }) {
  if (!state) return null;
  return (
    <Badge
      variant="outline"
      className={cn("border-transparent text-[11px] capitalize", baselineTone[state] ?? "")}
    >
      {state}
    </Badge>
  );
}

/**
 * acceptedRiskExpiry describes an accepted-risk expiry relative to now:
 * "expires in Xd" while in the future, "expired" once past.
 */
export function acceptedRiskExpiry(
  ts: Timestamp | undefined,
  now: number = Date.now(),
): { label: string; expired: boolean } | null {
  if (!ts) return null;
  const at = timestampDate(ts).getTime();
  if (at <= now) return { label: "expired", expired: true };
  const days = Math.ceil((at - now) / 86400000);
  return { label: `expires in ${days}d`, expired: false };
}

/** ExpiryBadge renders the accepted-risk expiry chip, or nothing. */
export function ExpiryBadge({ ts, now }: { ts: Timestamp | undefined; now?: number }) {
  const info = acceptedRiskExpiry(ts, now);
  if (!info) return null;
  return (
    <Badge
      variant="outline"
      className={cn(
        "border-transparent text-[11px]",
        info.expired
          ? "bg-red-500/15 text-red-700 dark:text-red-300"
          : "bg-amber-500/15 text-amber-700 dark:text-amber-300",
      )}
    >
      {info.label}
    </Badge>
  );
}

/** formatDurationSeconds renders trend durations like "3.5d" or "6h". */
export function formatDurationSeconds(seconds: number): string {
  if (!seconds || seconds <= 0) return "—";
  if (seconds < 3600) return `${Math.max(1, Math.round(seconds / 60))}m`;
  if (seconds < 86400) return `${(seconds / 3600).toFixed(1).replace(/\.0$/, "")}h`;
  return `${(seconds / 86400).toFixed(1).replace(/\.0$/, "")}d`;
}
