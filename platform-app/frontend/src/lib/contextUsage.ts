import { generationInputIncludesCache } from "@/lib/traceUsage";
import type { TraceSpan } from "@/rpc/platform/service_pb";

/**
 * Derives the run's *current* context size from the streamed trace: the most
 * recent main-agent `llm.generation` span's prompt-side tokens, accounting for
 * whether provider input usage includes cached tokens. Sub-agent
 * generations are excluded — they run in their own context windows and would
 * otherwise make the bar jump around while delegated work streams in.
 */

function numericTag(span: TraceSpan, ...keys: string[]): number {
  for (const key of keys) {
    const raw = span.tags.find((tag) => tag.key === key)?.value;
    if (raw !== undefined && raw !== "") {
      const parsed = Number(raw);
      if (Number.isFinite(parsed)) return parsed;
    }
  }
  return 0;
}

/** True when the span or any ancestor is a subagent span. */
function isSubagentScoped(span: TraceSpan, byId: Map<string, TraceSpan>): boolean {
  let current: TraceSpan | undefined = span;
  const seen = new Set<string>();
  while (current) {
    if (current.kind.startsWith("subagent")) return true;
    if (!current.parentSpanId || seen.has(current.spanId)) return false;
    seen.add(current.spanId);
    current = byId.get(current.parentSpanId);
  }
  return false;
}

/**
 * Returns the latest main-agent generation's context tokens, or null when the
 * trace has no usable generation yet.
 */
export function currentContextTokens(spans: TraceSpan[] | undefined): number | null {
  if (!spans?.length) return null;
  const byId = new Map(spans.map((span) => [span.spanId, span]));

  let latest: TraceSpan | null = null;
  for (const span of spans) {
    if (span.kind !== "llm.generation") continue;
    if (isSubagentScoped(span, byId)) continue;
    if (!latest || span.startTimeUnixUs > latest.startTimeUnixUs) {
      latest = span;
    }
  }
  if (!latest) return null;

  const inputTokens = numericTag(latest, "gen.input_tokens", "gen.prompt_tokens");
  const context = generationInputIncludesCache(latest)
    ? inputTokens
    : inputTokens +
      numericTag(latest, "gen.cache_read_tokens") +
      numericTag(latest, "gen.cache_creation_tokens");
  return context > 0 ? context : null;
}
