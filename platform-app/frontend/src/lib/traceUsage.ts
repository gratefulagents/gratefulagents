import type { TraceSpan } from "@/rpc/platform/service_pb";

export type TraceUsageSummary = {
  inputTokens: number;
  outputTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  totalTokens: number;
  costUsd: number;
  hasUsage: boolean;
  hasCost: boolean;
};

function tagValue(span: TraceSpan, ...keys: string[]): string | undefined {
  for (const key of keys) {
    const value = span.tags.find((tag) => tag.key === key)?.value;
    if (value !== undefined && value !== "") {
      return value;
    }
  }
  return undefined;
}

function numericTag(span: TraceSpan, ...keys: string[]): number {
  const raw = tagValue(span, ...keys);
  if (!raw) return 0;
  const parsed = Number(raw);
  return Number.isFinite(parsed) ? parsed : 0;
}

function hasAnyTag(span: TraceSpan, keys: string[]): boolean {
  return keys.some((key) => span.tags.some((tag) => tag.key === key));
}

export function providerInputIncludesCache(provider: string | undefined, model?: string): boolean {
  const label = provider?.trim().toLowerCase().replace(/[-_. ]/g, "");
  if (
    [
      "openai",
      "copilot",
      "githubcopilot",
      "azure",
      "azureopenai",
      "openrouter",
      "xai",
      "openaicompatible",
    ].includes(label ?? "")
  ) {
    return true;
  }
  const normalizedModel = model?.trim().toLowerCase();
  return ["openai/", "copilot/", "github-copilot/", "azure/", "azure-openai/", "openrouter/", "xai/"].some(
    (prefix) => normalizedModel?.startsWith(prefix) ?? false,
  );
}

export function generationInputIncludesCache(span: TraceSpan): boolean {
  if (tagValue(span, "gen.input_tokens_include_cache_known")?.toLowerCase() === "true") {
    return tagValue(span, "gen.input_tokens_include_cache")?.toLowerCase() === "true";
  }
  return providerInputIncludesCache(
    tagValue(span, "gen.model_provider"),
    tagValue(span, "gen.resolved_model", "gen.requested_model", "gen.model_name"),
  );
}

function generationTotalTokens(span: TraceSpan): number {
  const inputTokens = numericTag(span, "gen.input_tokens", "gen.prompt_tokens");
  const outputTokens = numericTag(span, "gen.output_tokens", "gen.completion_tokens");
  if (generationInputIncludesCache(span)) {
    return inputTokens + outputTokens;
  }
  return (
    inputTokens +
    outputTokens +
    numericTag(span, "gen.cache_read_tokens") +
    numericTag(span, "gen.cache_creation_tokens")
  );
}

export function traceTagValue(span: TraceSpan, ...keys: string[]): string | undefined {
  return tagValue(span, ...keys);
}

function zeroSummary(): TraceUsageSummary {
  return {
    inputTokens: 0,
    outputTokens: 0,
    cacheReadTokens: 0,
    cacheCreationTokens: 0,
    totalTokens: 0,
    costUsd: 0,
    hasUsage: false,
    hasCost: false,
  };
}

export function aggregateTraceUsage(spans: TraceSpan[]): TraceUsageSummary {
  const llmSpans = spans.filter((span) => span.kind === "llm.generation");
  const sessionSpan = spans.find(
    (span) =>
      span.operationName.startsWith("session") || span.tags.some((tag) => tag.key === "session.cost_usd"),
  );

  if (llmSpans.length > 0) {
    const inputTokens = llmSpans.reduce(
      (sum, span) => sum + numericTag(span, "gen.input_tokens", "gen.prompt_tokens"),
      0,
    );
    const outputTokens = llmSpans.reduce(
      (sum, span) => sum + numericTag(span, "gen.output_tokens", "gen.completion_tokens"),
      0,
    );
    const cacheReadTokens = llmSpans.reduce(
      (sum, span) => sum + numericTag(span, "gen.cache_read_tokens"),
      0,
    );
    const cacheCreationTokens = llmSpans.reduce(
      (sum, span) => sum + numericTag(span, "gen.cache_creation_tokens"),
      0,
    );
    const generationCost = llmSpans.reduce(
      (sum, span) => sum + numericTag(span, "gen.cost_usd"),
      0,
    );
    const hasGenerationCost = llmSpans.some((span) => {
      const explicitKnown = tagValue(span, "gen.cost_known");
      if (explicitKnown === "true") return true;
      return tagValue(span, "gen.cost_usd") !== undefined && numericTag(span, "gen.cost_usd") > 0;
    });
    const sessionCost = sessionSpan ? numericTag(sessionSpan, "session.cost_usd") : 0;
    const hasSessionCost = sessionSpan ? tagValue(sessionSpan, "session.cost_usd") !== undefined && sessionCost > 0 : false;
    return {
      inputTokens,
      outputTokens,
      cacheReadTokens,
      cacheCreationTokens,
      totalTokens: llmSpans.reduce((sum, span) => sum + generationTotalTokens(span), 0),
      costUsd: hasGenerationCost ? generationCost : sessionCost,
      hasUsage: llmSpans.some((span) =>
        hasAnyTag(span, [
          "gen.input_tokens",
          "gen.prompt_tokens",
          "gen.output_tokens",
          "gen.completion_tokens",
          "gen.total_tokens",
          "gen.cache_read_tokens",
          "gen.cache_creation_tokens",
        ]),
      ),
      hasCost: hasGenerationCost || (!hasGenerationCost && hasSessionCost),
    };
  }

  if (!sessionSpan) {
    return zeroSummary();
  }

  const inputTokens = numericTag(sessionSpan, "session.input_tokens");
  const outputTokens = numericTag(sessionSpan, "session.output_tokens");
  const cacheReadTokens = numericTag(sessionSpan, "session.cache_read_tokens");
  const cacheCreationTokens = numericTag(sessionSpan, "session.cache_creation_tokens");
  return {
    inputTokens,
    outputTokens,
    cacheReadTokens,
    cacheCreationTokens,
    totalTokens: inputTokens + outputTokens + cacheReadTokens + cacheCreationTokens,
    costUsd: numericTag(sessionSpan, "session.cost_usd"),
    hasUsage: hasAnyTag(sessionSpan, [
      "session.input_tokens",
      "session.output_tokens",
      "session.cache_read_tokens",
      "session.cache_creation_tokens",
    ]),
    hasCost: tagValue(sessionSpan, "session.cost_usd") !== undefined && numericTag(sessionSpan, "session.cost_usd") > 0,
  };
}
