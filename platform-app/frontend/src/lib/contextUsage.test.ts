import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { TraceSpanSchema, type TraceSpan } from "@/rpc/platform/service_pb";
import { currentContextTokens } from "@/lib/contextUsage";

function span(
  spanId: string,
  parentSpanId: string,
  kind: string,
  startUs: number,
  tags: Record<string, string> = {},
): TraceSpan {
  return create(TraceSpanSchema, {
    spanId,
    parentSpanId,
    kind,
    operationName: kind,
    startTimeUnixUs: BigInt(startUs),
    tags: Object.entries(tags).map(([key, value]) => ({ key, value })),
  });
}

describe("currentContextTokens", () => {
  it("returns null without generation spans", () => {
    expect(currentContextTokens(undefined)).toBeNull();
    expect(currentContextTokens([])).toBeNull();
    expect(currentContextTokens([span("a", "", "tool.bash", 1)])).toBeNull();
  });

  it("uses the latest main-agent generation's prompt-side tokens", () => {
    const spans = [
      span("root", "", "agent.run", 0),
      span("g1", "root", "llm.generation", 10, { "gen.input_tokens": "1000" }),
      span("g2", "root", "llm.generation", 20, {
        "gen.input_tokens": "2000",
        "gen.cache_read_tokens": "500",
        "gen.cache_creation_tokens": "100",
      }),
    ];
    expect(currentContextTokens(spans)).toBe(2600);
  });

  it("does not add cache tokens to OpenAI inclusive input", () => {
    const spans = [
      span("g1", "", "llm.generation", 10, {
        "gen.model_provider": "openai",
        "gen.input_tokens": "2000",
        "gen.cache_read_tokens": "500",
        "gen.cache_creation_tokens": "100",
      }),
    ];
    expect(currentContextTokens(spans)).toBe(2000);
  });

  it("prefers explicit cache semantics over provider labels", () => {
    const spans = [
      span("g1", "", "llm.generation", 10, {
        "gen.model_provider": "openai",
        "gen.input_tokens_include_cache_known": "true",
        "gen.input_tokens_include_cache": "false",
        "gen.input_tokens": "2000",
        "gen.cache_read_tokens": "500",
      }),
    ];
    expect(currentContextTokens(spans)).toBe(2500);
  });

  it("ignores sub-agent generations even when they are newest", () => {
    const spans = [
      span("root", "", "agent.run", 0),
      span("main", "root", "llm.generation", 10, { "gen.input_tokens": "1200" }),
      span("sub", "root", "subagent.explore", 15),
      span("subgen", "sub", "llm.generation", 30, { "gen.input_tokens": "90000" }),
    ];
    expect(currentContextTokens(spans)).toBe(1200);
  });

  it("falls back to prompt_tokens and skips zero-usage generations", () => {
    const spans = [
      span("g1", "", "llm.generation", 10, { "gen.prompt_tokens": "800" }),
      span("g2", "", "llm.generation", 20),
    ];
    // Latest generation has no usage tags → treated as unusable (null).
    expect(currentContextTokens(spans)).toBeNull();
    expect(currentContextTokens(spans.slice(0, 1))).toBe(800);
  });
});
