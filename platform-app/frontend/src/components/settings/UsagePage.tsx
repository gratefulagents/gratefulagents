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
  WalletCards,
} from "lucide-react";

import { SettingsSection } from "@/components/settings-section";
import { SettingsSubPage } from "@/components/settings/SettingsSubPage";
import { Badge } from "@/components/ui/badge";
import { Button, buttonVariants } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import type {
  AnthropicUsageLimit,
  MyAnthropicUsage,
  MyOpenAIUsage,
  OpenAIUsageLimit,
} from "@/rpc/platform/service_pb";

const CHATGPT_USAGE_URL = "https://chatgpt.com/codex/settings/usage";
const CLAUDE_USAGE_URL = "https://claude.ai/settings/usage";

type ProviderUsage<T> = { data: T | null; error: string; loading: boolean };

export default function UsagePage() {
  const [openAI, setOpenAI] = React.useState<ProviderUsage<MyOpenAIUsage>>({ data: null, error: "", loading: true });
  const [anthropic, setAnthropic] = React.useState<ProviderUsage<MyAnthropicUsage>>({ data: null, error: "", loading: true });
  const [refreshing, setRefreshing] = React.useState(false);

  const loadUsage = React.useCallback(async () => {
    const [nextOpenAI, nextAnthropic] = await Promise.allSettled([
      client.getMyOpenAIUsage({}),
      client.getMyAnthropicUsage({}),
    ]);
    setOpenAI(resultState(nextOpenAI));
    setAnthropic(resultState(nextAnthropic));
  }, []);

  const refresh = React.useCallback(async () => {
    setRefreshing(true);
    try {
      await loadUsage();
    } finally {
      setRefreshing(false);
    }
  }, [loadUsage]);

  React.useEffect(() => {
    let active = true;
    void client.getMyOpenAIUsage({})
      .then((data) => {
        if (active) setOpenAI({ data, error: "", loading: false });
      })
      .catch((error: unknown) => {
        if (active) setOpenAI(errorState(error));
      });
    void client.getMyAnthropicUsage({})
      .then((data) => {
        if (active) setAnthropic({ data, error: "", loading: false });
      })
      .catch((error: unknown) => {
        if (active) setAnthropic(errorState(error));
      });
    return () => {
      active = false;
    };
  }, []);

  return (
    <SettingsSubPage
      title="Usage"
      description="Allowances and account activity from your connected AI subscriptions."
    >
      <ProviderHeading name="Claude" />
      {anthropic.loading ? (
        <ProviderSkeleton />
      ) : anthropic.error ? (
        <ProviderError provider="Claude" error={anthropic.error} refreshing={refreshing} onRefresh={() => void refresh()} />
      ) : anthropic.data && !anthropic.data.anthropicOauthPresent ? (
        <DisconnectedState provider="Anthropic" />
      ) : anthropic.data?.reconnectRequired ? (
        <ReconnectAnthropicState />
      ) : anthropic.data ? (
        <>
          <AnthropicAccountSummary usage={anthropic.data} refreshing={refreshing} onRefresh={() => void refresh()} />
          <AnthropicAllowanceWindows usage={anthropic.data} />
          <AnthropicExtraUsage usage={anthropic.data} />
          <UsageWarnings warnings={anthropic.data.warnings} />
        </>
      ) : null}

      <ProviderHeading name="ChatGPT" />
      {openAI.loading ? (
        <ProviderSkeleton />
      ) : openAI.error ? (
        <ProviderError provider="ChatGPT" error={openAI.error} refreshing={refreshing} onRefresh={() => void refresh()} />
      ) : openAI.data && !openAI.data.openaiOauthPresent ? (
        <DisconnectedState provider="OpenAI" />
      ) : openAI.data ? (
        <>
          <OpenAIAccountSummary usage={openAI.data} refreshing={refreshing} onRefresh={() => void refresh()} />
          <OpenAIAllowanceWindows limits={openAI.data.limits} available={openAI.data.accountStatusAvailable} />
          <TokenActivity usage={openAI.data} />
          <UsageWarnings warnings={openAI.data.warnings} />
        </>
      ) : null}
    </SettingsSubPage>
  );
}

function resultState<T>(result: PromiseSettledResult<T>): ProviderUsage<T> {
  if (result.status === "fulfilled") return { data: result.value, error: "", loading: false };
  return errorState(result.reason);
}

function errorState(error: unknown): ProviderUsage<never> {
  return {
    data: null,
    error: error instanceof Error ? error.message : "Usage could not be loaded.",
    loading: false,
  };
}

function ProviderHeading({ name }: { name: string }) {
  return (
    <div className="flex items-center gap-3 pt-1 first:pt-0" aria-label={`${name} usage`}>
      <span className="text-[10px] font-semibold uppercase tracking-[0.16em] text-muted-foreground">{name}</span>
      <span className="h-px flex-1 bg-border/70" />
    </div>
  );
}

function AnthropicAccountSummary({
  usage,
  refreshing,
  onRefresh,
}: {
  usage: MyAnthropicUsage;
  refreshing: boolean;
  onRefresh: () => void;
}) {
  return (
    <SettingsSection
      icon={<Sparkles />}
      title="Claude account"
      description="The Anthropic OAuth sign-in saved under Credentials."
      aside={<RefreshButton refreshing={refreshing} onRefresh={onRefresh} />}
    >
      <div className="grid gap-4 border-t border-border/60 pt-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-end">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-[17px] font-semibold tracking-[-0.02em]">Claude</span>
            <Badge variant="secondary">OAuth</Badge>
          </div>
          <p className="mt-1 truncate font-mono text-[11.5px] text-muted-foreground">
            {usage.accountEmail || "Connected Anthropic account"}
          </p>
          {usage.accountUuid && (
            <p className="mt-1 truncate font-mono text-[10.5px] text-muted-foreground/80" title={usage.accountUuid}>
              Account {usage.accountUuid}
            </p>
          )}
        </div>
        <div className="flex flex-wrap items-end gap-x-5 gap-y-2 sm:justify-end sm:text-right">
          <CredentialDate label="Token expires" value={usage.credentialExpiresAtUnix} />
          <CredentialDate label="Last refreshed" value={usage.credentialLastRefreshedAtUnix} />
          <ExternalUsageLink href={CLAUDE_USAGE_URL} label="Open Claude" />
        </div>
      </div>
    </SettingsSection>
  );
}

function OpenAIAccountSummary({
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
      aside={<RefreshButton refreshing={refreshing} onRefresh={onRefresh} />}
    >
      <div className="flex flex-wrap items-end justify-between gap-4 border-t border-border/60 pt-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-[17px] font-semibold tracking-[-0.02em]">{displayPlan(usage.planType)}</span>
            <Badge variant="secondary">OAuth</Badge>
          </div>
          <p className="mt-1 truncate font-mono text-[11.5px] text-muted-foreground">
            {usage.accountEmail || "Connected OpenAI account"}
          </p>
        </div>
        <div className="flex items-center gap-4 text-right">
          {usage.credits && <StatValue label="Credits" value={usage.credits} />}
          <ExternalUsageLink href={CHATGPT_USAGE_URL} label="Open ChatGPT" />
        </div>
      </div>
    </SettingsSection>
  );
}

function RefreshButton({ refreshing, onRefresh }: { refreshing: boolean; onRefresh: () => void }) {
  return (
    <Button variant="ghost" size="sm" onClick={onRefresh} disabled={refreshing} aria-label="Refresh usage">
      <RefreshCw className={cn(refreshing && "animate-spin")} />
      Refresh
    </Button>
  );
}

function ExternalUsageLink({ href, label }: { href: string; label: string }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="inline-flex items-center gap-1 text-[12px] font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      {label}
      <ArrowUpRight className="size-3.5" />
    </a>
  );
}

function CredentialDate({ label, value }: { label: string; value: bigint }) {
  if (value <= 0n) return null;
  return <StatValue label={label} value={formatTimestamp(value)} />;
}

function StatValue({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">{label}</div>
      <div className="mt-0.5 font-mono text-[12px] tabular-nums">{value}</div>
    </div>
  );
}

function AnthropicAllowanceWindows({ usage }: { usage: MyAnthropicUsage }) {
  return (
    <SettingsSection icon={<Gauge />} title="Allowance windows" description="How much of each Claude allowance has been used.">
      {!usage.usageAvailable ? (
        <UnavailableCopy>Claude did not return current allowance data.</UnavailableCopy>
      ) : usage.limits.length === 0 ? (
        <UnavailableCopy>No allowance windows were returned for this account.</UnavailableCopy>
      ) : (
        <QuotaList limits={usage.limits} />
      )}
    </SettingsSection>
  );
}

function OpenAIAllowanceWindows({ limits, available }: { limits: OpenAIUsageLimit[]; available: boolean }) {
  return (
    <SettingsSection icon={<Gauge />} title="Allowance windows" description="How much of each ChatGPT Codex allowance has been used.">
      {!available ? (
        <UnavailableCopy>ChatGPT did not return current allowance data.</UnavailableCopy>
      ) : limits.length === 0 ? (
        <UnavailableCopy>No allowance windows were returned for this plan.</UnavailableCopy>
      ) : (
        <QuotaList limits={limits} />
      )}
    </SettingsSection>
  );
}

function QuotaList({ limits }: { limits: Array<OpenAIUsageLimit | AnthropicUsageLimit> }) {
  return (
    <div className="divide-y divide-border/60 rounded-lg border border-border/70">
      {limits.map((limit, index) => <QuotaRail key={`${limit.label}-${index}`} limit={limit} />)}
    </div>
  );
}

function QuotaRail({ limit }: { limit: OpenAIUsageLimit | AnthropicUsageLimit }) {
  const used = Math.max(0, Math.min(100, limit.usedPercent));
  const remaining = Math.max(0, 100 - used);
  const reset = limit.resetAtUnix > 0n ? formatTimestamp(limit.resetAtUnix) : "Reset time unavailable";
  const details = "details" in limit ? limit.details : "";
  return (
    <div className="grid gap-3 px-3.5 py-3 sm:grid-cols-[112px_1fr_120px] sm:items-center">
      <div>
        <div className="text-[12.5px] font-medium">{limit.label || "Usage"}</div>
        {details && <div className="mt-0.5 text-[10.5px] text-muted-foreground">{details}</div>}
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

function AnthropicExtraUsage({ usage }: { usage: MyAnthropicUsage }) {
  if (!usage.usageAvailable || !usage.extraUsageAvailable) return null;
  const stats = [
    ["Status", usage.extraUsageEnabled ? "Enabled" : "Disabled"],
    ["Monthly limit", formatOptionalCreditAmount(usage.extraUsageMonthlyLimitUsdCents)],
    ["Credits used", formatOptionalCreditAmount(usage.extraUsageUsedCreditsUsdCents)],
    ["Utilization", usage.extraUsageUtilization === undefined ? "—" : `${usage.extraUsageUtilization.toFixed(0)}%`],
  ];
  return (
    <SettingsSection
      icon={<WalletCards />}
      title="Extra usage"
      description="Pay-as-you-go capacity reported by Claude after subscription allowances."
    >
      <div className="grid grid-cols-2 overflow-hidden rounded-lg border border-border/70 sm:grid-cols-4">
        {stats.map(([label, value], index) => (
          <div key={label} className={cn("min-w-0 px-3 py-3", index % 2 === 1 && "border-l border-border/60", index >= 2 && "border-t border-border/60", index > 0 && "sm:border-l sm:border-border/60", index >= 2 && "sm:border-t-0")}>
            <div className="truncate font-mono text-[15px] font-semibold tabular-nums tracking-[-0.02em]">{value}</div>
            <div className="mt-1 text-[10.5px] text-muted-foreground">{label}</div>
          </div>
        ))}
      </div>
    </SettingsSection>
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
    <SettingsSection icon={<Clock3 />} title="Token activity" description="Account activity reported by ChatGPT, independent of API billing.">
      {!usage.tokenActivityAvailable ? (
        <UnavailableCopy>ChatGPT did not return token activity for this account.</UnavailableCopy>
      ) : (
        <div className="grid grid-cols-2 overflow-hidden rounded-lg border border-border/70 sm:grid-cols-5">
          {stats.map(([label, value], index) => (
            <div key={label} className={cn("min-w-0 px-3 py-3", index % 2 === 1 && "border-l border-border/60", index >= 2 && "border-t border-border/60", index === 4 && "col-span-2 border-l-0", index > 0 && "sm:border-l sm:border-border/60", index >= 2 && "sm:border-t-0", index === 4 && "sm:col-span-1")}>
              <div className="truncate font-mono text-[15px] font-semibold tabular-nums tracking-[-0.02em]">{value}</div>
              <div className="mt-1 text-[10.5px] text-muted-foreground">{label}</div>
            </div>
          ))}
        </div>
      )}
    </SettingsSection>
  );
}

function ProviderError({ provider, error, refreshing, onRefresh }: { provider: string; error: string; refreshing: boolean; onRefresh: () => void }) {
  return (
    <SettingsSection icon={<Activity />} title={`${provider} usage unavailable`} description={error}>
      <Button variant="outline" size="sm" onClick={onRefresh} disabled={refreshing}>
        <RefreshCw data-icon="inline-start" className={cn(refreshing && "animate-spin")} />
        {refreshing ? "Trying again…" : "Try again"}
      </Button>
    </SettingsSection>
  );
}

function DisconnectedState({ provider }: { provider: "Anthropic" | "OpenAI" }) {
  return (
    <SettingsSection
      icon={<KeyRound />}
      title={`Connect ${provider} to see usage`}
      description={`Usage is read through the ${provider === "Anthropic" ? "Claude" : "ChatGPT"} OAuth sign-in saved under Credentials. API-key credentials do not expose subscription allowances.`}
    >
      <Link to="/settings/credentials" className={buttonVariants({ size: "sm" })}>Open Credentials</Link>
    </SettingsSection>
  );
}

function ReconnectAnthropicState() {
  return (
    <SettingsSection
      icon={<KeyRound />}
      title="Reconnect Anthropic to see usage"
      description="Claude rejected the saved OAuth credential. Sign in again under Credentials to restore subscription allowances."
    >
      <Link to="/settings/credentials" className={buttonVariants({ size: "sm" })}>Open Credentials</Link>
    </SettingsSection>
  );
}

function UsageWarnings({ warnings }: { warnings: string[] }) {
  if (warnings.length === 0) return null;
  return <div role="status" className="rounded-lg border border-border/70 bg-muted/25 px-3 py-2 text-[12px] text-muted-foreground">{warnings.join(" ")}</div>;
}

function ProviderSkeleton() {
  return (
    <div role="status" aria-live="polite" aria-busy="true" className="space-y-5">
      <span className="sr-only">Loading provider usage</span>
      {[112, 180].map((height) => <Skeleton key={height} className="w-full rounded-xl" style={{ height }} />)}
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

function formatOptionalCreditAmount(value: number | undefined): string {
  if (value === undefined) return "—";
  return new Intl.NumberFormat(undefined, {
    style: "currency",
    currency: "USD",
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(value / 100);
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
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" }).format(new Date(Number(value) * 1000));
}
