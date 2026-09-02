import { Stat, StatBar } from "@/components/detail-page";
import type { UsageTotals } from "@/rpc/platform/service_pb";

const UNKNOWN_TITLE = "Token counts were not reported for this run";

function fmtCount(value: bigint | number | undefined, known: boolean): { text: string; title?: string } {
  if (!known) return { text: "—", title: UNKNOWN_TITLE };
  const n = typeof value === "bigint" ? Number(value) : (value ?? 0);
  return { text: n.toLocaleString() };
}

export function RunUsageSummary({ totals }: { totals?: UsageTotals }) {
  if (!totals) return null;
  const known = totals.tokensKnown;
  const items = [
    { label: "Input", ...fmtCount(totals.inputTokens, known) },
    { label: "Output", ...fmtCount(totals.outputTokens, known) },
    { label: "Cache read", ...fmtCount(totals.cacheReadInputTokens, known) },
    { label: "Cache write", ...fmtCount(totals.cacheCreationInputTokens, known) },
    { label: "Total", ...fmtCount(totals.totalTokens, known) },
  ];
  return (
    <div className="@container">
      {/* Narrow inspector: a compact two-column list. Wide: the shared stat bar. */}
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-xs @[480px]:hidden">
        {items.map((item) => (
          <div key={item.label} className="contents">
            <dt className="text-muted-foreground">{item.label}</dt>
            <dd className="text-right font-mono text-foreground tabular-nums" title={item.title}>
              {item.text}
            </dd>
          </div>
        ))}
      </dl>
      <div className="hidden @[480px]:block">
        <StatBar>
          {items.map((item) => (
            <Stat key={item.label} label={item.label} value={<span title={item.title}>{item.text}</span>} />
          ))}
        </StatBar>
      </div>
    </div>
  );
}
