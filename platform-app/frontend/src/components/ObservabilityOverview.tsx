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
const integer = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });
const money = new Intl.NumberFormat(undefined, { style: "currency", currency: "USD", maximumFractionDigits: 2 });

function formatInteger(value: bigint): string {
  return integer.format(value);
}

function bucketLabel(bucket: ObservabilityBucket): string {
  if (!bucket.start) return "Unknown time";
  return new Date(Number(bucket.start.seconds) * 1_000).toLocaleString(undefined, {
    month: "short", day: "numeric", hour: "numeric",
  });
}

type Series = { label: string; value: number; display: string };

function HistoricalChart({ title, description, series }: { title: string; description: string; series: Series[] }) {
  const max = Math.max(...series.map((point) => point.value), 0);
  const points = series.map((point, index) => {
    const x = series.length < 2 ? 50 : (index / (series.length - 1)) * 100;
    const y = max === 0 ? 48 : 48 - (point.value / max) * 44;
    return `${x},${y}`;
  }).join(" ");

  return (
    <figure className="min-w-0 rounded-lg border border-border/60 bg-background/35 p-3">
      <figcaption>
        <div className="text-xs font-semibold">{title}</div>
        <div className="text-[10.5px] text-muted-foreground">{description}</div>
      </figcaption>
      <svg viewBox="0 0 100 52" className="mt-3 h-24 w-full overflow-visible" role="img" aria-label={`${title} over time`} preserveAspectRatio="none">
        <line x1="0" x2="100" y1="48" y2="48" className="stroke-border" strokeWidth="0.6" />
        {points && <polyline points={points} fill="none" vectorEffect="non-scaling-stroke" className="stroke-primary" strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" />}
        {series.map((point, index) => {
          const [x, y] = points.split(" ")[index].split(",");
          return <circle key={`${point.label}-${index}`} cx={x} cy={y} r="1.7" className="fill-primary"><title>{point.label}: {point.display}</title></circle>;
        })}
      </svg>
      <details className="mt-1 text-[10.5px] text-muted-foreground">
        <summary className="cursor-pointer select-none">View accessible data</summary>
        <table className="mt-2 w-full text-left">
          <thead><tr><th className="font-medium">Time</th><th className="text-right font-medium">Value</th></tr></thead>
          <tbody>{series.map((point, index) => <tr key={index}><td>{point.label}</td><td className="text-right tabular-nums">{point.display}</td></tr>)}</tbody>
        </table>
      </details>
    </figure>
  );
}

function Breakdown({ title, rows, value, detail }: { title: string; rows: ObservabilityBreakdown[]; value: (row: ObservabilityBreakdown) => string; detail?: (row: ObservabilityBreakdown) => string }) {
  const visible = rows.slice(0, 5);
  const max = visible.reduce((current, row) => row.count > current ? row.count : current, 0n);
  return (
    <div className="rounded-lg border border-border/60 bg-background/35 p-3">
      <h3 className="text-xs font-semibold">{title}</h3>
      {visible.length === 0 ? <p className="mt-3 text-xs text-muted-foreground">No data recorded.</p> : (
        <ul className="mt-3 space-y-2" aria-label={title}>
          {visible.map((row) => (
            <li key={row.name}>
              <div className="flex items-center justify-between gap-3 text-[11px]"><span className="truncate">{row.name || "Unknown"}</span><span className="shrink-0 font-mono tabular-nums">{value(row)}</span></div>
              {detail && <div className="mt-0.5 text-right font-mono text-[10px] tabular-nums text-muted-foreground">{detail(row)}</div>}
              <div className="mt-1 h-1.5 overflow-hidden rounded-full bg-muted"><div className="h-full rounded-full bg-primary/70" style={{ width: `${max === 0n ? 0 : Number((row.count * 100n) / max)}%` }} /></div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function series(buckets: ObservabilityBucket[], select: (totals: ObservabilityTotals) => number, display: (value: number) => string): Series[] {
  return buckets.map((bucket) => {
    const value = bucket.totals ? select(bucket.totals) : 0;
    return { label: bucketLabel(bucket), value, display: display(value) };
  });
}

export function ObservabilityOverview() {
  const [range, setRange] = React.useState<ObservabilityRange>("7d");
  const { data, loading, error, refetch } = useObservabilityOverview(range);
  const totals = data?.totals;
  const empty = !loading && !error && (!totals || totals.runs === 0n);

  return (
    <section aria-labelledby="observability-heading" className="rounded-xl border border-border/60 bg-card/30 p-4 shadow-[var(--elevation-low)]">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div><h2 id="observability-heading" className="text-sm font-semibold">Usage and reliability</h2><p className="text-[11px] text-muted-foreground">Historical metrics for visible runs.</p></div>
        <div className="flex items-center gap-2">
          {loading && data && <span className="text-[10.5px] text-muted-foreground" role="status">Updating…</span>}
          <div className="flex rounded-lg border border-border/60 p-0.5" aria-label="Observability range">
            {RANGES.map((item) => <button key={item} type="button" aria-pressed={range === item} onClick={() => setRange(item)} className={cn("rounded-md px-2.5 py-1 text-[11px] font-medium", range === item ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-muted")}>{item}</button>)}
          </div>
        </div>
      </div>

      {loading && !data && <div className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-4" aria-label="Loading observability"><span className="h-16 animate-pulse rounded-lg bg-muted/60" /><span className="h-16 animate-pulse rounded-lg bg-muted/60" /><span className="h-16 animate-pulse rounded-lg bg-muted/60" /><span className="h-16 animate-pulse rounded-lg bg-muted/60" /></div>}
      {error && !data && <div role="alert" className="mt-4 flex items-center justify-between gap-3 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-xs"><span><AlertTriangle className="mr-2 inline size-4" />Historical metrics could not be loaded.</span><Button variant="outline" size="sm" onClick={() => void refetch()}><RefreshCw className="size-3.5" /> Retry</Button></div>}
      {empty && <div className="mt-4 rounded-lg border border-dashed border-border p-6 text-center text-xs text-muted-foreground">No historical metrics were recorded in this range.</div>}

      {data && totals && totals.runs > 0n && <>
        {error && <p role="alert" className="mt-3 text-xs text-destructive">Refresh failed; showing the last available data.</p>}
        <div className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
          {[
            ["Runs", formatInteger(totals.runs)], ["Run cost", money.format(totals.costUsd)],
            ["Run tokens", formatInteger(totals.inputTokens + totals.outputTokens)], ["Tool calls", formatInteger(totals.toolCalls)],
            ["Subagents", formatInteger(totals.subagents)], ["Errors", formatInteger(totals.toolErrors + totals.subagentFailures + totals.llmFailures)],
          ].map(([label, value]) => <div key={label} className="rounded-lg border border-border/50 bg-background/35 p-3"><div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">{label}</div><div className="mt-1 font-mono text-lg font-semibold tabular-nums">{value}</div></div>)}
        </div>
        <div className="mt-3 grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          <HistoricalChart title="Cost" description="Generation-attributed USD" series={series(data.buckets, (t) => t.generationCostUsd, (v) => money.format(v))} />
          <HistoricalChart title="Tokens" description="Generation-attributed input and output" series={series(data.buckets, (t) => Number(t.generationInputTokens + t.generationOutputTokens), (v) => integer.format(v))} />
          <HistoricalChart title="Tools" description="Completed calls" series={series(data.buckets, (t) => Number(t.toolCalls), (v) => integer.format(v))} />
          <HistoricalChart title="Subagents" description="Terminal tasks" series={series(data.buckets, (t) => Number(t.subagents), (v) => integer.format(v))} />
          <HistoricalChart title="Compactions" description="Compaction events" series={series(data.buckets, (t) => Number(t.compactions), (v) => integer.format(v))} />
          <HistoricalChart title="Errors" description="Tool, subagent, and model failures" series={series(data.buckets, (t) => Number(t.toolErrors + t.subagentFailures + t.llmFailures), (v) => integer.format(v))} />
        </div>
        <div className="mt-3 grid gap-3 md:grid-cols-3">
          <Breakdown title="Top tools" rows={data.tools} value={(row) => `${formatInteger(row.count)} calls`} />
          <Breakdown title="Subagent roles" rows={data.subagents} value={(row) => `${formatInteger(row.count)} tasks`} />
          <Breakdown title="Models" rows={data.models} value={(row) => money.format(row.costUsd)} detail={(row) => `${formatInteger(row.inputTokens)} in · ${formatInteger(row.outputTokens)} out tok`} />
        </div>
        {(!data.dataCompleteness?.metricsComplete || !data.dataCompleteness?.activityComplete || data.coverageWarnings.length > 0) && <details className="mt-3 rounded-lg border border-border bg-muted/20 p-3 text-[11px]"><summary className="cursor-pointer font-medium">Partial historical coverage</summary><ul className="mt-2 list-disc space-y-1 pl-4 text-muted-foreground">{data.coverageWarnings.map((warning) => <li key={warning}>{warning}</li>)}{data.coverageWarnings.length === 0 && <li>Some sessions do not include complete metrics or activity.</li>}</ul></details>}
      </>}
    </section>
  );
}
