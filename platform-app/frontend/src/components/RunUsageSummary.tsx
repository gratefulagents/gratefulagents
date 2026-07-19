import { Stat, StatBar } from "@/components/detail-page";
import type { UsageTotals } from "@/rpc/platform/service_pb";

function fmtCount(value: bigint | number | undefined, known: boolean) {
  if (!known) return "unknown";
  const n = typeof value === "bigint" ? Number(value) : value ?? 0;
  return n.toLocaleString();
}

export function RunUsageSummary({ totals }: { totals?: UsageTotals }) {
  if (!totals) return null;
  return (
    <StatBar>
      <Stat label="Input" value={fmtCount(totals.inputTokens, totals.tokensKnown)} />
      <Stat label="Output" value={fmtCount(totals.outputTokens, totals.tokensKnown)} />
      <Stat label="Cache read" value={fmtCount(totals.cacheReadInputTokens, totals.tokensKnown)} />
      <Stat label="Cache write" value={fmtCount(totals.cacheCreationInputTokens, totals.tokensKnown)} />
      <Stat label="Total" value={fmtCount(totals.totalTokens, totals.tokensKnown)} />
    </StatBar>
  );
}
