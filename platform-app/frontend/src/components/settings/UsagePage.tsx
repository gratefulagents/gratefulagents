import * as React from "react";
import { Link } from "react-router-dom";
import {
  Activity,
  ArrowUpRight,
  Clock3,
  Code2,
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import type {
  AnthropicUsageLimit,
  CopilotUsageQuota,
  MyAnthropicUsage,
  MyCopilotUsage,
  MyOpenAIUsage,
  OpenAIUsageLimit,
} from "@/rpc/platform/service_pb";

const CHATGPT_USAGE_URL = "https://chatgpt.com/codex/settings/usage";
const CLAUDE_USAGE_URL = "https://claude.ai/settings/usage";
const COPILOT_USAGE_URL = "https://github.com/settings/copilot/features";

type ProviderUsage<T> = { data: T | null; error: string; loading: boolean };
type UsageProvider = "anthropic" | "openai" | "copilot";

const EMPTY_USAGE = { data: null, error: "", loading: false };

export default function UsagePage() {
  const [activeProvider, setActiveProvider] = React.useState<UsageProvider>("anthropic");
  const [openAI, setOpenAI] = React.useState<ProviderUsage<MyOpenAIUsage>>(EMPTY_USAGE);
  const [copilot, setCopilot] = React.useState<ProviderUsage<MyCopilotUsage>>(EMPTY_USAGE);
  const [anthropic, setAnthropic] = React.useState<ProviderUsage<MyAnthropicUsage>>(EMPTY_USAGE);
  const [refreshing, setRefreshing] = React.useState(false);
  const requestedProviders = React.useRef(new Set<UsageProvider>());
  const mounted = React.useRef(true);

  React.useEffect(() => () => {
    mounted.current = false;
  }, []);

  const loadProvider = React.useCallback(async (provider: UsageProvider, refresh = false) => {
    if (!refresh && requestedProviders.current.has(provider)) return;
    requestedProviders.current.add(provider);
    if (refresh) setRefreshing(true);

    try {
      if (provider === "anthropic") {
        if (!refresh) setAnthropic((current) => ({ ...current, error: "", loading: true }));
        const data = await client.getMyAnthropicUsage({});
        if (mounted.current) setAnthropic({ data, error: "", loading: false });
      } else if (provider === "openai") {
        if (!refresh) setOpenAI((current) => ({ ...current, error: "", loading: true }));
        const data = await client.getMyOpenAIUsage({});
        if (mounted.current) setOpenAI({ data, error: "", loading: false });
      } else {
        if (!refresh) setCopilot((current) => ({ ...current, error: "", loading: true }));
        const data = await client.getMyCopilotUsage({});
        if (mounted.current) setCopilot({ data, error: "", loading: false });
      }
    } catch (error: unknown) {
      if (!mounted.current) return;
      if (provider === "anthropic") setAnthropic(errorState(error));
      else if (provider === "openai") setOpenAI(errorState(error));
      else setCopilot(errorState(error));
    } finally {
      if (refresh && mounted.current) setRefreshing(false);
    }
  }, []);

  React.useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- fetch only when the selected provider tab opens
    void loadProvider(activeProvider);
  }, [activeProvider, loadProvider]);

  const refresh = React.useCallback(() => loadProvider(activeProvider, true), [activeProvider, loadProvider]);

  return (
    <SettingsSubPage
      title="Usage"
      description="Allowances and account activity from your connected AI subscriptions."
    >
      <Tabs value={activeProvider} onValueChange={(value) => setActiveProvider(value as UsageProvider)}>
        <TabsList className="w-full sm:w-fit" aria-label="Usage provider">
          <TabsTrigger value="anthropic" className="px-4">Anthropic</TabsTrigger>
          <TabsTrigger value="openai" className="px-4">OpenAI</TabsTrigger>
          <TabsTrigger value="copilot" className="px-4">Copilot</TabsTrigger>
        </TabsList>

        <TabsContent value="anthropic" className="space-y-5 pt-3">
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
        </TabsContent>

        <TabsContent value="openai" className="space-y-5 pt-3">
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
        </TabsContent>

        <TabsContent value="copilot" className="space-y-5 pt-3">
          {copilot.loading ? (
            <ProviderSkeleton />
          ) : copilot.error ? (
            <ProviderError provider="GitHub Copilot" error={copilot.error} refreshing={refreshing} onRefresh={() => void refresh()} />
          ) : copilot.data && !copilot.data.copilotOauthPresent ? (
            <DisconnectedState provider="Copilot" />
          ) : copilot.data ? (
            <>
              <CopilotUsage usage={copilot.data} refreshing={refreshing} onRefresh={() => void refresh()} />
              <UsageWarnings warnings={copilot.data.warnings} />
            </>
          ) : null}
        </TabsContent>
      </Tabs>
    </SettingsSubPage>
  );
}

function errorState(error: unknown): ProviderUsage<never> {
  return {
    data: null,
    error: error instanceof Error ? error.message : "Usage could not be loaded.",
    loading: false,
  };
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

function CopilotUsage({
  usage,
  refreshing,
  onRefresh,
}: {
  usage: MyCopilotUsage;
  refreshing: boolean;
  onRefresh: () => void;
}) {
  return (
    <SettingsSection
      icon={<Code2 />}
      title="GitHub Copilot"
      description="Premium request quotas from the GitHub OAuth sign-in saved under Credentials."
      aside={<RefreshButton refreshing={refreshing} onRefresh={onRefresh} />}
    >
      <div className="flex flex-wrap items-end justify-between gap-4 border-t border-border/60 pt-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-[17px] font-semibold tracking-[-0.02em]">
              {displayCopilotPlan(usage.plan)}
            </span>
            <Badge variant="secondary">OAuth</Badge>
          </div>
          <p className="mt-1 truncate font-mono text-[11.5px] text-muted-foreground">
            {usage.accountLogin ? `@${usage.accountLogin}` : "Connected GitHub account"}
          </p>
        </div>
        <a
          href={COPILOT_USAGE_URL}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 text-[12px] font-medium text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          Open GitHub
          <ArrowUpRight className="size-3.5" />
        </a>
      </div>
      {!usage.usageAvailable ? (
        <UnavailableCopy>GitHub did not return current Copilot quota data.</UnavailableCopy>
      ) : usage.quotas.length === 0 ? (
        <UnavailableCopy>No Copilot quotas were returned for this plan.</UnavailableCopy>
      ) : (
        <div className="divide-y divide-border/60 rounded-lg border border-border/70">
          {usage.quotas.map((quota) => (
            <CopilotQuotaRail key={quota.name} quota={quota} resetDate={usage.quotaResetDate} />
          ))}
        </div>
      )}
    </SettingsSection>
  );
}

function CopilotQuotaRail({ quota, resetDate }: { quota: CopilotUsageQuota; resetDate: string }) {
  const entitlement = Number(quota.entitlement);
  const remaining = Number(quota.remaining);
  const usedPercent = entitlement > 0 ? Math.max(0, Math.min(100, ((entitlement - remaining) / entitlement) * 100)) : 0;
  const label = displayQuotaName(quota.name);
  const remainingLabel = quota.unlimited ? "Unlimited" : `${new Intl.NumberFormat().format(remaining)} left`;
  const details = quota.unlimited
    ? "No fixed allowance"
    : `${new Intl.NumberFormat().format(entitlement)} included${quota.overageCount > 0n ? ` · ${quota.overageCount.toString()} overage` : ""}`;
  return (
    <div className="grid gap-3 px-3.5 py-3 sm:grid-cols-[150px_1fr_120px] sm:items-center">
      <div>
        <div className="text-[12.5px] font-medium">{label}</div>
        <div className="mt-0.5 text-[10.5px] text-muted-foreground">{details}</div>
      </div>
      {quota.unlimited ? (
        <div className="h-2 rounded-full bg-muted" aria-hidden="true" />
      ) : (
        <div
          className="relative h-2 overflow-hidden rounded-full bg-muted"
          role="progressbar"
          aria-label={`${label} used`}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={Math.round(usedPercent)}
        >
          <div
            className={cn(
              "absolute inset-y-0 left-0 rounded-full",
              usedPercent >= 90 ? "bg-destructive" : usedPercent >= 70 ? "bg-amber-500" : "bg-foreground/75",
            )}
            style={{ width: `${usedPercent}%` }}
          />
        </div>
      )}
      <div className="flex items-baseline justify-between gap-2 sm:block sm:text-right">
        <span className="font-mono text-[12px] font-medium tabular-nums">{remainingLabel}</span>
        {resetDate && <span className="block text-[10.5px] text-muted-foreground">Resets {formatCopilotReset(resetDate)}</span>}
      </div>
    </div>
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

function DisconnectedState({ provider }: { provider: "Anthropic" | "OpenAI" | "Copilot" }) {
  const account = provider === "Anthropic" ? "Claude" : provider === "OpenAI" ? "ChatGPT" : "GitHub Copilot";
  return (
    <SettingsSection
      icon={<KeyRound />}
      title={`Connect ${provider} to see usage`}
      description={`Usage is read through the ${account} OAuth sign-in saved under Credentials. API-key credentials do not expose subscription allowances.`}
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

function displayCopilotPlan(plan: string): string {
  const normalized = plan.trim().replaceAll("_", " ");
  if (!normalized) return "GitHub Copilot";
  return `GitHub Copilot ${normalized.replace(/\b\w/g, (letter) => letter.toUpperCase())}`;
}

function displayQuotaName(name: string): string {
  const labels: Record<string, string> = {
    chat: "Chat requests",
    completions: "Code completions",
    premium_interactions: "Premium requests",
  };
  return labels[name] ?? name.replaceAll("_", " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function formatCopilotReset(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", timeZone: "UTC" }).format(date);
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
