import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  aggregateTraceUsage,
  providerInputIncludesCache,
  traceTagValue,
} from "@/lib/traceUsage";
import { TraceSpanSchema } from "@/rpc/platform/service_pb";

describe("aggregateTraceUsage", () => {
  it("aggregates live usage and cost from llm generation spans", () => {
    const usage = aggregateTraceUsage([
      create(TraceSpanSchema, {
        spanId: "gen-1",
        kind: "llm.generation",
        tags: [
          { key: "gen.model_provider", value: "openai" },
          { key: "gen.input_tokens", value: "120" },
          { key: "gen.output_tokens", value: "80" },
          { key: "gen.cache_read_tokens", value: "40" },
          { key: "gen.cache_creation_tokens", value: "10" },
          { key: "gen.cost_usd", value: "0.0123" },
          { key: "gen.cost_known", value: "true" },
        ],
      }),
      create(TraceSpanSchema, {
        spanId: "gen-2",
        kind: "llm.generation",
        tags: [
          { key: "gen.input_tokens", value: "30" },
          { key: "gen.output_tokens", value: "20" },
          { key: "gen.cost_usd", value: "0.0045" },
          { key: "gen.cost_known", value: "true" },
        ],
      }),
    ]);

    expect(usage.inputTokens).toBe(150);
    expect(usage.outputTokens).toBe(100);
    expect(usage.cacheReadTokens).toBe(40);
    expect(usage.cacheCreationTokens).toBe(10);
    expect(usage.totalTokens).toBe(250);
    expect(usage.costUsd).toBeCloseTo(0.0168, 6);
    expect(usage.hasUsage).toBe(true);
    expect(usage.hasCost).toBe(true);
  });

  it("keeps cache additive for Anthropic and unknown providers", () => {
    const usage = aggregateTraceUsage([
      create(TraceSpanSchema, {
        spanId: "anthropic",
        kind: "llm.generation",
        tags: [
          { key: "gen.model_provider", value: "anthropic" },
          { key: "gen.input_tokens", value: "100" },
          { key: "gen.output_tokens", value: "20" },
          { key: "gen.cache_read_tokens", value: "30" },
        ],
      }),
      create(TraceSpanSchema, {
        spanId: "unknown",
        kind: "llm.generation",
        tags: [
          { key: "gen.input_tokens", value: "10" },
          { key: "gen.output_tokens", value: "5" },
          { key: "gen.cache_creation_tokens", value: "2" },
        ],
      }),
    ]);

    expect(usage.totalTokens).toBe(167);
  });

  it("prefers explicit cache semantics and falls back to model identity", () => {
    const usage = aggregateTraceUsage([
      create(TraceSpanSchema, {
        spanId: "model-fallback",
        kind: "llm.generation",
        tags: [
          { key: "gen.resolved_model", value: "openai/gpt-5.6" },
          { key: "gen.input_tokens", value: "100" },
          { key: "gen.output_tokens", value: "20" },
          { key: "gen.cache_read_tokens", value: "30" },
        ],
      }),
      create(TraceSpanSchema, {
        spanId: "explicit-additive",
        kind: "llm.generation",
        tags: [
          { key: "gen.model_provider", value: "openai" },
          { key: "gen.input_tokens_include_cache_known", value: "true" },
          { key: "gen.input_tokens_include_cache", value: "false" },
          { key: "gen.input_tokens", value: "10" },
          { key: "gen.output_tokens", value: "5" },
          { key: "gen.cache_creation_tokens", value: "2" },
        ],
      }),
      create(TraceSpanSchema, {
        spanId: "explicit-inclusive",
        kind: "llm.generation",
        tags: [
          { key: "gen.model_provider", value: "custom" },
          { key: "gen.input_tokens_include_cache_known", value: "true" },
          { key: "gen.input_tokens_include_cache", value: "true" },
          { key: "gen.input_tokens", value: "50" },
          { key: "gen.output_tokens", value: "10" },
          { key: "gen.cache_read_tokens", value: "20" },
        ],
      }),
    ]);

    expect(usage.totalTokens).toBe(197);
  });

  it("falls back to session cost when llm spans lack live pricing", () => {
    const usage = aggregateTraceUsage([
      create(TraceSpanSchema, {
        spanId: "gen-1",
        kind: "llm.generation",
        tags: [
          { key: "gen.input_tokens", value: "10" },
          { key: "gen.output_tokens", value: "5" },
        ],
      }),
      create(TraceSpanSchema, {
        spanId: "session-1",
        operationName: "session",
        kind: "session",
        tags: [{ key: "session.cost_usd", value: "0.0222" }],
      }),
    ]);

    expect(usage.costUsd).toBeCloseTo(0.0222, 6);
    expect(usage.hasCost).toBe(true);
  });
});

describe("providerInputIncludesCache", () => {
  it.each(["openai", "copilot", "azure", "openrouter", "x.ai", "OpenAI-compatible"])(
    "recognizes inclusive provider label %s",
    (provider) => {
      expect(providerInputIncludesCache(provider)).toBe(true);
    },
  );

  it("uses model identity when provider is missing", () => {
    expect(providerInputIncludesCache(undefined, "openai/gpt-5.6")).toBe(true);
  });

  it("keeps unknown providers additive", () => {
    expect(providerInputIncludesCache("custom-provider", "custom/model")).toBe(false);
  });
});

describe("traceTagValue", () => {
  it("returns the first matching key in order", () => {
    const span = create(TraceSpanSchema, {
      spanId: "gen-1",
      kind: "llm.generation",
      tags: [
        { key: "gen.requested_model", value: "sonnet" },
        { key: "gen.resolved_model", value: "claude-sonnet-4-5" },
      ],
    });

    expect(
      traceTagValue(span, "gen.model", "gen.resolved_model", "gen.requested_model"),
    ).toBe("claude-sonnet-4-5");
    expect(traceTagValue(span, "gen.model", "gen.requested_model")).toBe("sonnet");
    expect(traceTagValue(span, "gen.model")).toBeUndefined();
  });
});
