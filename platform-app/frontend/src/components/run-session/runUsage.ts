import type { TraceUsageSummary } from "@/lib/traceUsage";

export function resolveRunUsageTokens(
  persistedInputTokens: bigint,
  persistedOutputTokens: bigint,
  traceUsage: Pick<TraceUsageSummary, "hasUsage" | "inputTokens" | "outputTokens"> | null,
): { inputTokens: number; outputTokens: number } {
  if (traceUsage?.hasUsage) {
    return {
      inputTokens: traceUsage.inputTokens,
      outputTokens: traceUsage.outputTokens,
    };
  }

  if (persistedInputTokens > 0n || persistedOutputTokens > 0n) {
    return {
      inputTokens: Number(persistedInputTokens),
      outputTokens: Number(persistedOutputTokens),
    };
  }

  return { inputTokens: Number.NaN, outputTokens: Number.NaN };
}
