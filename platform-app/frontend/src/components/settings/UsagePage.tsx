import * as React from "react";
import { Link } from "react-router-dom";
import {
  Activity,
  ArrowUpRight,
  Clock3,
  Gauge,
  KeyRound,
  RefreshCw,
  Sparkles,
} from "lucide-react";

import { SettingsSection } from "@/components/settings-section";
import { SettingsSubPage } from "@/components/settings/SettingsSubPage";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import type {
  MyOpenAIUsage,
  OpenAIUsageLimit,
} from "@/rpc/platform/service_pb";

const CHATGPT_USAGE_URL = "https://chatgpt.com/codex/settings/usage";

export default function UsagePage() {
  const [usage, setUsage] = React.useState<MyOpenAIUsage | null>(null);
  const [error, setError] = React.useState("");
  const [loading, setLoading] = React.useState(true);
  const [refreshing, setRefreshing] = React.useState(false);

  const refresh = React.useCallback(async () => {
    setRefreshing(true);
    try {
      setUsage(await client.getMyOpenAIUsage({}));
      setError("");
    } catch (nextError) {
      setError(nextError instanceof Error ? nextError.message : "Usage could not be loaded.");
    } finally {
      setRefreshing(false);
    }
  }, []);

  React.useEffect(() => {
    let active = true;
    void client
      .getMyOpenAIUsage({})
      .then((nextUsage) => {
        if (active) setUsage(nextUsage);
      })
      .catch((nextError: unknown) => {
        if (active) setError(nextError instanceof Error ? nextError.message : "Usage could not be loaded.");
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);

  return (
    <SettingsSubPage
      title="Usage"
      description="Allowances and token activity from your current ChatGPT OAuth account."
    >
      {loading ? (
        <UsageSkeleton />
      ) : error ? (
        <SettingsSection icon={<Activity />} title="Usage unavailable" description={error}>
          <Button variant="outline" size="sm" onClick={() => void refresh()} disabled={refreshing}>
            <RefreshCw data-icon="inline-start" className={cn(refreshing && "animate-spin")} />
            {refreshing ? "Trying again…" : "Try again"}
          </Button>
        </SettingsSection>
      ) : usage && !usage.openaiOauthPresent ? (
        <DisconnectedState />
      ) : usage ? (
        <>
          <AccountSummary usage={usage} refreshing={refreshing} onRefresh={() => void refresh()} />
          <AllowanceWindows limits={usage.limits} available={usage.accountStatusAvailable} />
          <TokenActivity usage={usage} />
          {usage.warnings.length > 0 && (
            <div role="status" className="rounded-lg border border-border/70 bg-muted/25 px-3 py-2 text-[12px] text-muted-foreground">
              {usage.warnings.join(" ")}
            </div>
          )}
        </>
      ) : null}
    </SettingsSubPage>
  );
}

function AccountSummary({
  usage,
  refreshing,
  onRefresh,
}: {
  usage: MyOpenAIUsage;
  refreshing: boolean;
  onRefresh: () => void;
}) {
  return (
    <SettingsSection
      icon={<Sparkles />}
      title="ChatGPT account"
      description="The OpenAI OAuth sign-in saved under Credentials."
      aside={
        <Button variant="ghost" size="sm" onClick={onRefresh} disabled={refreshing} aria-label="Refresh usage">
          <RefreshCw className={cn(refreshing && "animate-spin")} />
          Refresh
        </Button>
      }
    >
      <div className="flex flex-wrap items-end justify-between gap-4 border-t border-border/60 pt-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-[17px] font-semibold tracking-[-0.02em]">
              {displayPlan(usage.planType)}
            </span>
            <Badge variant="secondary">OAuth</Badge>
          </div>
          <p className="mt-1 truncate font-mono text-[11.5px] text-muted-foreground">
            {usage.accountEmail || "Connected OpenAI account"}
          </p>
        </div>
        <div className="flex items-center gap-4 text-right">
          {usage.credits && (
            <div>
              <div className="text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">Credits</div>
              <div className="mt-0.5 font-mono text-[13px] tabular-nums">{usage.credits}</div>
            </div>
          )}
          <a
            href={CHATGPT_USAGE_URL}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 text-[12px] font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            Open ChatGPT
            <ArrowUpRight className="size-3.5" />
          </a>
        </div>
      </div>
    </SettingsSection>
  );
}

function AllowanceWindows({ limits, available }: { limits: OpenAIUsageLimit[]; available: boolean }) {
  return (
    <SettingsSection
      icon={<Gauge />}
      title="Allowance windows"
      description="How much of each ChatGPT Codex allowance has been used."
    >
      {!available ? (
        <UnavailableCopy>ChatGPT did not return current allowance data.</UnavailableCopy>
      ) : limits.length === 0 ? (
        <UnavailableCopy>No allowance windows were returned for this plan.</UnavailableCopy>
      ) : (
        <div className="divide-y divide-border/60 rounded-lg border border-border/70">
          {limits.map((limit, index) => (
            <QuotaRail key={`${limit.label}-${index}`} limit={limit} />
          ))}
        </div>
      )}
    </SettingsSection>
  );
}

function QuotaRail({ limit }: { limit: OpenAIUsageLimit }) {
  const used = Math.max(0, Math.min(100, limit.usedPercent));
  const remaining = Math.max(0, 100 - used);
  const reset = limit.resetAtUnix > 0n ? formatTimestamp(limit.resetAtUnix) : "Reset time unavailable";
  return (
    <div className="grid gap-3 px-3.5 py-3 sm:grid-cols-[112px_1fr_120px] sm:items-center">
      <div>
        <div className="text-[12.5px] font-medium">{limit.label || "Usage"}</div>
        {limit.details && <div className="mt-0.5 text-[10.5px] text-muted-foreground">{limit.details}</div>}
      </div>
      <div
        className="relative h-2 overflow-hidden rounded-full bg-muted"
        role="progressbar"
        aria-label={`${limit.label || "Allowance"} used`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(used)}
        aria-valuetext={`${used.toFixed(0)}% used, ${remaining.toFixed(0)}% left`}
      >
        <div
          className={cn(
            "absolute inset-y-0 left-0 rounded-full transition-[width] duration-500 motion-reduce:transition-none",
            used >= 90 ? "bg-destructive" : used >= 70 ? "bg-amber-500" : "bg-foreground/75",
          )}
          style={{ width: `${used}%` }}
        />
      </div>
      <div className="flex items-baseline justify-between gap-2 sm:block sm:text-right">
        <span className="font-mono text-[12px] font-medium tabular-nums">{remaining.toFixed(0)}% left</span>
        <span className="block text-[10.5px] text-muted-foreground">{reset}</span>
      </div>
    </div>
  );
}

function TokenActivity({ usage }: { usage: MyOpenAIUsage }) {
  const stats = [
    ["Last 30 days", formatTokens(usage.last30DaysTokens)],
    ["Lifetime", formatOptionalTokens(usage.lifetimeTokens)],
    ["Peak day", formatOptionalTokens(usage.peakDailyTokens)],
    ["Current streak", formatDays(usage.currentStreakDays)],
    ["Longest turn", formatDuration(usage.longestRunningTurnSeconds)],
  ];
  return (
    <SettingsSection
      icon={<Clock3 />}
      title="Token activity"
      description="Account activity reported by ChatGPT, independent of API billing."
    >
      {!usage.tokenActivityAvailable ? (
        <UnavailableCopy>ChatGPT did not return token activity for this account.</UnavailableCopy>
      ) : (
        <div className="grid grid-cols-2 overflow-hidden rounded-lg border border-border/70 sm:grid-cols-5">
          {stats.map(([label, value], index) => (
            <div
              key={label}
              className={cn(
                "min-w-0 px-3 py-3",
                index % 2 === 1 && "border-l border-border/60",
                index >= 2 && "border-t border-border/60",
                index === 4 && "col-span-2 border-l-0",
                index > 0 && "sm:border-l sm:border-border/60",
                index >= 2 && "sm:border-t-0",
                index === 4 && "sm:col-span-1",
              )}
            >
              <div className="truncate font-mono text-[15px] font-semibold tabular-nums tracking-[-0.02em]">{value}</div>
              <div className="mt-1 text-[10.5px] text-muted-foreground">{label}</div>
            </div>
          ))}
        </div>
      )}
    </SettingsSection>
  );
}

function DisconnectedState() {
  return (
    <SettingsSection
      icon={<KeyRound />}
      title="Connect OpenAI to see usage"
      description="Usage is read through the ChatGPT OAuth sign-in saved under Credentials. API-key credentials do not expose subscription allowances."
    >
      <Link to="/settings/credentials" className={buttonVariants({ size: "sm" })}>
        Open Credentials
      </Link>
    </SettingsSection>
  );
}

function UsageSkeleton() {
  return (
    <div role="status" aria-live="polite" aria-busy="true" className="space-y-5">
      <span className="sr-only">Loading usage</span>
      {[112, 180, 138].map((height) => (
        <Skeleton key={height} className="w-full rounded-xl" style={{ height }} />
      ))}
    </div>
  );
}

function UnavailableCopy({ children }: { children: React.ReactNode }) {
  return <p className="rounded-lg border border-dashed border-border px-3 py-4 text-center text-[12px] text-muted-foreground">{children}</p>;
}

function displayPlan(plan: string): string {
  const normalized = plan.trim().toLowerCase();
  if (!normalized) return "ChatGPT";
  if (["team", "business", "self_serve_business_usage_based"].includes(normalized)) return "ChatGPT Business";
  if (["enterprise", "enterprise_cbp_usage_based"].includes(normalized)) return "ChatGPT Enterprise";
  if (normalized === "prolite") return "ChatGPT Pro Lite";
  return `ChatGPT ${normalized.charAt(0).toUpperCase()}${normalized.slice(1)}`;
}

function formatTokens(value: bigint): string {
  return new Intl.NumberFormat().format(value);
}

function formatOptionalTokens(value: bigint | undefined): string {
  return value === undefined ? "—" : formatTokens(value);
}

function formatDays(value: bigint | undefined): string {
  return value === undefined ? "—" : `${value.toString()}d`;
}

function formatDuration(value: bigint | undefined): string {
  if (value === undefined) return "—";
  const seconds = Number(value);
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  return minutes ? `${hours}h ${minutes}m` : `${hours}h`;
}

function formatTimestamp(value: bigint): string {
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(new Date(Number(value) * 1000));
}
