import * as React from "react";
import { AlertTriangle, RefreshCw } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useObservabilityOverview, type ObservabilityRange } from "@/hooks/useObservabilityOverview";
import type {
  ObservabilityBreakdown,
  ObservabilityBucket,
  ObservabilityTotals,
} from "@/rpc/platform/service_pb";
import { cn } from "@/lib/utils";

const RANGES: ObservabilityRange[] = ["24h", "7d", "30d", "90d"];

// Page-local chart accents. The rest of the page stays on the app's neutral
// tokens; these four hues are reserved for data so color always means the
// same thing: spend, tokens, activity, failure.
const ACCENT = {
  spend: "var(--primary)",
  tokens: "oklch(0.78 0.13 75)",
  activity: "oklch(0.74 0.11 160)",
  failure: "oklch(0.66 0.19 25)",
} as const;

const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });
const money = new Intl.NumberFormat(undefined, { style: "currency", currency: "USD", maximumFractionDigits: 2 });
const percent = new Intl.NumberFormat(undefined, { style: "percent", maximumFractionDigits: 1 });

function formatCount(value: bigint | number): string {
  return compact.format(typeof value === "bigint" ? value : Math.round(value));
}

function formatDuration(ms: number): string {
  if (ms <= 0) return "—";
  if (ms < 1_000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1_000).toFixed(1)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

function bucketDate(bucket: ObservabilityBucket): Date | null {
  return bucket.start ? new Date(Number(bucket.start.seconds) * 1_000) : null;
}

function bucketLabel(bucket: ObservabilityBucket, range: ObservabilityRange): string {
  const date = bucketDate(bucket);
  if (!date) return "Unknown time";
  return range === "24h"
    ? date.toLocaleString(undefined, { hour: "numeric" })
    : date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

type Series = { label: string; value: number; display: string };

function toSeries(
  buckets: ObservabilityBucket[],
  range: ObservabilityRange,
  select: (totals: ObservabilityTotals) => number,
  display: (value: number) => string,
): Series[] {
  return buckets.map((bucket) => {
    const value = bucket.totals ? select(bucket.totals) : 0;
    return { label: bucketLabel(bucket, range), value, display: display(value) };
  });
}

function linePoints(series: Series[], width: number, top: number, baseline: number): string {
  const max = Math.max(...series.map((point) => point.value), 0);
  return series
    .map((point, index) => {
      const x = series.length < 2 ? width / 2 : (index / (series.length - 1)) * width;
      const y = max === 0 ? baseline : baseline - (point.value / max) * (baseline - top);
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    })
    .join(" ");
}

function AccessibleSeriesTable({ series }: { series: Series[] }) {
  return (
    <details className="mt-1.5 text-[10.5px] text-muted-foreground">
      <summary className="cursor-pointer select-none rounded-sm focus-visible:outline-2 focus-visible:outline-ring">
        View data
      </summary>
      <table className="mt-2 w-full text-left">
        <thead>
          <tr>
            <th className="font-medium">Time</th>
            <th className="text-right font-medium">Value</th>
          </tr>
        </thead>
        <tbody>
          {series.map((point, index) => (
            <tr key={index}>
              <td>{point.label}</td>
              <td className="text-right font-mono tabular-nums">{point.display}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </details>
  );
}

// The signature element: a full-width spend ledger. The period's cost readout
// sits inside the chart itself, over an area fill of generation-attributed
// spend, with a tick-marked baseline and real time labels.
function SpendLedger({
  series,
  range,
  totalDisplay,
  peakDisplay,
  secondary,
}: {
  series: Series[];
  range: ObservabilityRange;
  totalDisplay: string;
  peakDisplay: string;
  secondary: { label: string; value: string }[];
}) {
  const points = linePoints(series, 100, 6, 34);
  const axis =
    series.length > 1
      ? [0, 0.25, 0.5, 0.75, 1].map((f) => series[Math.round(f * (series.length - 1))].label)
      : series.map((point) => point.label);
  return (
    <figure
      className="relative overflow-hidden rounded-xl border border-border/60 bg-background/40 p-4"
      style={{ boxShadow: "var(--elevation-low)" }}
    >
      <figcaption className="flex flex-wrap items-start justify-between gap-x-6 gap-y-3">
        <div>
          <div className="text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
            Generation spend · {range}
          </div>
          <div className="mt-1 font-mono text-[34px] font-semibold leading-none tabular-nums tracking-tight">
            {totalDisplay}
          </div>
        </div>
        <dl className="flex flex-wrap gap-x-6 gap-y-2 text-right">
          {secondary.map((item) => (
            <div key={item.label}>
              <dt className="text-[10px] font-medium uppercase tracking-[0.12em] text-muted-foreground">
                {item.label}
              </dt>
              <dd className="mt-0.5 font-mono text-sm font-semibold tabular-nums">{item.value}</dd>
            </div>
          ))}
        </dl>
      </figcaption>
      <svg
        viewBox="0 0 100 36"
        className="mt-3 h-32 w-full"
        role="img"
        aria-label={`Generation spend over the selected ${range} range; peak bucket ${peakDisplay}`}
        preserveAspectRatio="none"
      >
        {[6, 15.33, 24.67].map((y) => (
          <line key={y} x1="0" x2="100" y1={y} y2={y} className="stroke-border/50" strokeWidth="0.5" vectorEffect="non-scaling-stroke" strokeDasharray="1 3" />
        ))}
        {points && (
          <>
            <polygon points={`0,34 ${points} 100,34`} fill={ACCENT.spend} opacity="0.14" />
            <polyline
              points={points}
              fill="none"
              stroke={ACCENT.spend}
              strokeWidth="1.8"
              strokeLinejoin="round"
              strokeLinecap="round"
              vectorEffect="non-scaling-stroke"
            />
          </>
        )}
        <line x1="0" x2="100" y1="34" y2="34" className="stroke-border" strokeWidth="1" vectorEffect="non-scaling-stroke" />
        {series.map((_, index) => {
          const x = series.length < 2 ? 50 : (index / (series.length - 1)) * 100;
          return <line key={index} x1={x} x2={x} y1="34" y2="35.4" className="stroke-border" strokeWidth="0.75" vectorEffect="non-scaling-stroke" />;
        })}
      </svg>
      <div className="mt-1 flex justify-between font-mono text-[10px] tabular-nums text-muted-foreground" aria-hidden>
        {axis.map((label, index) => (
          <span key={`${label}-${index}`}>{label}</span>
        ))}
      </div>
      <div className="mt-1 text-right font-mono text-[10px] tabular-nums text-muted-foreground">peak {peakDisplay}</div>
      <AccessibleSeriesTable series={series} />
    </figure>
  );
}

function StatCard({
  label,
  value,
  series,
  accent,
  hint,
}: {
  label: string;
  value: string;
  series: Series[];
  accent: string;
  hint: string;
}) {
  const points = linePoints(series, 100, 3, 21);
  return (
    <figure className="min-w-0 rounded-lg border border-border/50 bg-background/35 p-3">
      <figcaption>
        <div className="text-[10px] font-semibold uppercase tracking-[0.13em] text-muted-foreground">{label}</div>
        <div className="mt-1 flex items-baseline justify-between gap-2">
          <span className="font-mono text-lg font-semibold tabular-nums">{value}</span>
          <span className="text-[10px] text-muted-foreground">{hint}</span>
        </div>
      </figcaption>
      <svg viewBox="0 0 100 24" className="mt-2 h-8 w-full" role="img" aria-label={`${label} over time`} preserveAspectRatio="none">
        <line x1="0" x2="100" y1="21" y2="21" className="stroke-border/70" strokeWidth="0.75" vectorEffect="non-scaling-stroke" />
        {points && (
          <polyline points={points} fill="none" stroke={accent} strokeWidth="1.5" strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
        )}
      </svg>
      <AccessibleSeriesTable series={series} />
    </figure>
  );
}

function ReliabilityRow({ label, failures, total, unit }: { label: string; failures: bigint; total: bigint; unit: string }) {
  const rate = total === 0n ? 0 : Number(failures) / Number(total);
  const clean = failures === 0n;
  return (
    <li className="flex items-center gap-3">
      <span className="w-24 shrink-0 text-[11px] font-medium">{label}</span>
      <span
        className="h-1.5 min-w-0 flex-1 overflow-hidden rounded-full bg-muted"
        role="img"
        aria-label={`${label}: ${formatCount(failures)} of ${formatCount(total)} ${unit} failed`}
      >
        <span
          className="block h-full rounded-full"
          style={{ width: `${Math.min(100, Math.max(rate * 100, clean ? 0 : 1.5))}%`, background: ACCENT.failure }}
        />
      </span>
      <span className={cn("w-14 shrink-0 text-right font-mono text-[11px] font-semibold tabular-nums", clean && "text-muted-foreground")}>
        {total === 0n ? "—" : percent.format(rate)}
      </span>
      <span className="w-28 shrink-0 text-right font-mono text-[10px] tabular-nums text-muted-foreground">
        {formatCount(failures)}/{formatCount(total)} {unit}
      </span>
    </li>
  );
}

type LeaderboardColumn = {
  header: string;
  value: (row: ObservabilityBreakdown) => string;
};

// Bars scale by the same metric the first column displays, so the loudest bar
// always belongs to the biggest printed number.
function Leaderboard({
  title,
  rows,
  metric,
  accent,
  columns,
}: {
  title: string;
  rows: ObservabilityBreakdown[];
  metric: (row: ObservabilityBreakdown) => number;
  accent: string;
  columns: LeaderboardColumn[];
}) {
  const visible = rows.slice(0, 6);
  const max = visible.reduce((current, row) => Math.max(current, metric(row)), 0);
  return (
    <div className="min-w-0 rounded-lg border border-border/50 bg-background/35 p-3">
      <h3 className="text-[10px] font-semibold uppercase tracking-[0.13em] text-muted-foreground">{title}</h3>
      {visible.length === 0 ? (
        <p className="mt-3 text-xs text-muted-foreground">Nothing recorded in this range.</p>
      ) : (
        <table className="mt-2 w-full border-separate border-spacing-y-1">
          <thead>
            <tr className="text-[9.5px] font-medium uppercase tracking-wider text-muted-foreground">
              <th className="w-full text-left font-medium">{title}</th>
              {columns.map((column) => (
                <th key={column.header} className="pl-3 text-right font-medium">
                  {column.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {visible.map((row) => (
              <tr key={row.name} className="align-baseline text-[11px]">
                <td className="max-w-0 pr-2">
                  <div className="truncate">{row.name || "Unknown"}</div>
                  <div className="mt-1 h-1 overflow-hidden rounded-full bg-muted">
                    <div
                      className="h-full rounded-full"
                      style={{ width: `${max === 0 ? 0 : (metric(row) / max) * 100}%`, background: accent, opacity: 0.75 }}
                    />
                  </div>
                </td>
                {columns.map((column) => (
                  <td key={column.header} className="whitespace-nowrap pl-3 text-right font-mono tabular-nums">
                    {column.value(row)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function hasRecordedData(totals: ObservabilityTotals): boolean {
  // Runs alone are not the whole story: events from sessions created before
  // the range (long-lived runs) still contribute tool, subagent, and
  // generation activity inside the window.
  return (
    totals.runs > 0n ||
    totals.toolCalls > 0n ||
    totals.subagents > 0n ||
    totals.llmAttempts > 0n ||
    totals.compactions > 0n ||
    totals.generationCostUsd > 0 ||
    totals.generationInputTokens + totals.generationOutputTokens > 0n
  );
}

export function ObservabilityOverview() {
  const [range, setRange] = React.useState<ObservabilityRange>("7d");
  const { data, loading, error, refetch } = useObservabilityOverview(range);
  const totals = data?.totals;
  const empty = !loading && !error && (!totals || !hasRecordedData(totals));

  const buckets = data?.buckets ?? [];
  const spendSeries = toSeries(buckets, range, (t) => t.generationCostUsd, (v) => money.format(v));
  const peakSpend = Math.max(...spendSeries.map((point) => point.value), 0);

  return (
    <section aria-labelledby="observability-heading" className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 id="observability-heading" className="text-sm font-semibold">
            Usage and reliability
          </h2>
          <p className="text-[11px] text-muted-foreground">Historical metrics for visible runs.</p>
        </div>
        <div className="flex items-center gap-2">
          {loading && data && (
            <span className="text-[10.5px] text-muted-foreground" role="status">
              Updating…
            </span>
          )}
          <div className="flex rounded-lg border border-border/60 p-0.5" aria-label="Observability range">
            {RANGES.map((item) => (
              <button
                key={item}
                type="button"
                aria-pressed={range === item}
                onClick={() => setRange(item)}
                className={cn(
                  "rounded-md px-2.5 py-1 font-mono text-[11px] font-medium transition-colors",
                  range === item ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-muted",
                )}
              >
                {item}
              </button>
            ))}
          </div>
        </div>
      </div>

      {loading && !data && (
        <div className="grid gap-2" aria-label="Loading observability">
          <span className="h-48 animate-pulse rounded-xl bg-muted/60" />
          <div className="grid grid-cols-2 gap-2 lg:grid-cols-5">
            {Array.from({ length: 5 }, (_, index) => (
              <span key={index} className="h-24 animate-pulse rounded-lg bg-muted/60" />
            ))}
          </div>
        </div>
      )}
      {error && !data && (
        <div role="alert" className="flex items-center justify-between gap-3 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-xs">
          <span>
            <AlertTriangle className="mr-2 inline size-4" />
            Historical metrics could not be loaded.
          </span>
          <Button variant="outline" size="sm" onClick={() => void refetch()}>
            <RefreshCw className="size-3.5" /> Retry
          </Button>
        </div>
      )}
      {empty && (
        <div className="rounded-xl border border-dashed border-border p-8 text-center text-xs text-muted-foreground">
          No historical metrics were recorded in this range. Runs report usage here as they execute.
        </div>
      )}

      {data && totals && hasRecordedData(totals) && (
        <>
          {error && (
            <p role="alert" className="text-xs text-destructive">
              Refresh failed; showing the last available data.
            </p>
          )}

          <SpendLedger
            series={spendSeries}
            range={range}
            totalDisplay={money.format(totals.generationCostUsd)}
            peakDisplay={money.format(peakSpend)}
            secondary={[
              { label: "Gen tokens in / out", value: `${formatCount(totals.generationInputTokens)} / ${formatCount(totals.generationOutputTokens)}` },
              { label: "Run snapshot cost", value: money.format(totals.costUsd) },
              { label: "Run tokens", value: formatCount(totals.inputTokens + totals.outputTokens) },
            ]}
          />

          <div className="grid grid-cols-2 gap-2 md:grid-cols-3 lg:grid-cols-5">
            <StatCard
              label="Runs"
              value={formatCount(totals.runs)}
              hint="started"
              accent={ACCENT.activity}
              series={toSeries(buckets, range, (t) => Number(t.runs), formatCount)}
            />
            <StatCard
              label="Tool calls"
              value={formatCount(totals.toolCalls)}
              hint={`${formatCount(totals.toolErrors)} errored`}
              accent={ACCENT.activity}
              series={toSeries(buckets, range, (t) => Number(t.toolCalls), formatCount)}
            />
            <StatCard
              label="Subagents"
              value={formatCount(totals.subagents)}
              hint={`${formatCount(totals.subagentFailures)} failed`}
              accent={ACCENT.activity}
              series={toSeries(buckets, range, (t) => Number(t.subagents), formatCount)}
            />
            <StatCard
              label="Gen tokens"
              value={formatCount(totals.generationInputTokens + totals.generationOutputTokens)}
              hint={`${formatCount(totals.llmAttempts)} attempts`}
              accent={ACCENT.tokens}
              series={toSeries(buckets, range, (t) => Number(t.generationInputTokens + t.generationOutputTokens), formatCount)}
            />
            <StatCard
              label="Compactions"
              value={formatCount(totals.compactions)}
              hint={`${formatCount(totals.tokensReclaimed)} tok reclaimed`}
              accent={ACCENT.tokens}
              series={toSeries(buckets, range, (t) => Number(t.compactions), formatCount)}
            />
          </div>

          <div className="rounded-lg border border-border/50 bg-background/35 p-3">
            <h3 className="text-[10px] font-semibold uppercase tracking-[0.13em] text-muted-foreground">Reliability</h3>
            <ul className="mt-3 space-y-2.5" aria-label="Failure rates">
              <ReliabilityRow label="Tool calls" failures={totals.toolErrors} total={totals.toolCalls} unit="calls" />
              <ReliabilityRow label="Model attempts" failures={totals.llmFailures} total={totals.llmAttempts} unit="attempts" />
              <ReliabilityRow label="Subagents" failures={totals.subagentFailures} total={totals.subagents} unit="tasks" />
            </ul>
          </div>

          <div className="grid gap-2 lg:grid-cols-3">
            <Leaderboard
              title="Tools"
              rows={data.tools}
              metric={(row) => Number(row.count)}
              accent={ACCENT.activity}
              columns={[
                { header: "Calls", value: (row) => formatCount(row.count) },
                { header: "Err", value: (row) => formatCount(row.errors) },
                { header: "p95", value: (row) => formatDuration(row.p95DurationMs) },
              ]}
            />
            <Leaderboard
              title="Subagents"
              rows={data.subagents}
              metric={(row) => Number(row.count)}
              accent={ACCENT.activity}
              columns={[
                { header: "Tasks", value: (row) => formatCount(row.count) },
                { header: "Err", value: (row) => formatCount(row.errors) },
                { header: "Cost", value: (row) => money.format(row.costUsd) },
              ]}
            />
            <Leaderboard
              title="Models"
              rows={data.models}
              metric={(row) => row.costUsd}
              accent={ACCENT.spend}
              columns={[
                { header: "Cost", value: (row) => money.format(row.costUsd) },
                { header: "In / out", value: (row) => `${formatCount(row.inputTokens)} / ${formatCount(row.outputTokens)}` },
              ]}
            />
          </div>

          {(!data.dataCompleteness?.metricsComplete || !data.dataCompleteness?.activityComplete || data.coverageWarnings.length > 0) && (
            <details className="rounded-lg border border-border bg-muted/20 p-3 text-[11px]">
              <summary className="cursor-pointer font-medium">Partial historical coverage</summary>
              <ul className="mt-2 list-disc space-y-1 pl-4 text-muted-foreground">
                {data.coverageWarnings.map((warning) => (
                  <li key={warning}>{warning}</li>
                ))}
                {data.coverageWarnings.length === 0 && <li>Some sessions do not include complete metrics or activity.</li>}
              </ul>
            </details>
          )}
        </>
      )}
    </section>
  );
}
