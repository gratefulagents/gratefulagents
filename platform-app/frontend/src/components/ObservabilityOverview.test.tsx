import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ObservabilityOverview } from "@/components/ObservabilityOverview";

const mocks = vi.hoisted(() => ({ useOverview: vi.fn() }));

vi.mock("@/hooks/useObservabilityOverview", () => ({
  useObservabilityOverview: mocks.useOverview,
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function totals(overrides: Record<string, bigint | number> = {}) {
  return {
    runs: 0n, costUsd: 0, inputTokens: 0n, outputTokens: 0n, toolCalls: 0n, toolErrors: 0n,
    subagents: 0n, subagentFailures: 0n, llmAttempts: 0n, llmFailures: 0n, compactions: 0n,
    tokensReclaimed: 0n, generationCostUsd: 0, generationInputTokens: 0n, generationOutputTokens: 0n,
    ...overrides,
  };
}

function overview(data: Record<string, unknown>) {
  return {
    buckets: [],
    tools: [],
    subagents: [],
    models: [],
    dataCompleteness: { metricsComplete: true, activityComplete: true },
    coverageWarnings: [],
    ...data,
  };
}

describe("ObservabilityOverview", () => {
  it("shows an empty state and changes historical range", () => {
    mocks.useOverview.mockReturnValue({ data: overview({ totals: totals() }), loading: false, error: null, refetch: vi.fn() });
    render(<ObservabilityOverview />);

    expect(screen.getByText(/No historical metrics were recorded in this range/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "7d" }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(screen.getByRole("button", { name: "30d" }));
    expect(mocks.useOverview).toHaveBeenLastCalledWith("30d");
  });

  it("still shows activity recorded for sessions older than the range", () => {
    const bucket = (seconds: number, generationCostUsd: number) => ({
      start: { seconds: BigInt(seconds) },
      totals: totals({ generationCostUsd }),
    });
    mocks.useOverview.mockReturnValue({
      data: overview({
        totals: totals({ toolCalls: 12n, llmAttempts: 3n, generationCostUsd: 0.42 }),
        buckets: [bucket(1_760_000_000, 0.1), bucket(1_760_086_400, 0.3), bucket(1_760_172_800, 0.02)],
      }),
      loading: false,
      error: null,
      refetch: vi.fn(),
    });
    render(<ObservabilityOverview />);

    expect(screen.queryByText(/No historical metrics were recorded/)).toBeNull();
    expect(screen.getByText("Generation spend · 7d")).toBeTruthy();
    expect(screen.getByText("$0.42")).toBeTruthy();
    // The ledger exposes its buckets as an accessible data table.
    expect(screen.getAllByText("$0.30").length).toBeGreaterThan(0);
  });

  it("offers retry when the overview fails", () => {
    const refetch = vi.fn();
    mocks.useOverview.mockReturnValue({ data: null, loading: false, error: new Error("offline"), refetch });
    render(<ObservabilityOverview />);

    expect(screen.getByRole("alert").textContent).toContain("could not be loaded");
    fireEvent.click(screen.getByRole("button", { name: /Retry/ }));
    expect(refetch).toHaveBeenCalledOnce();
  });

  it("shows per-model cost and tokens in the models leaderboard", () => {
    mocks.useOverview.mockReturnValue({
      data: overview({
        totals: totals({ runs: 2n, costUsd: 3.5, inputTokens: 1200n, outputTokens: 300n, toolCalls: 9n, subagents: 1n, llmAttempts: 4n, generationCostUsd: 1.25 }),
        models: [{ name: "openai/gpt-5.6", count: 4n, errors: 0n, costUsd: 1.25, inputTokens: 41500n, outputTokens: 800n, averageDurationMs: 0, p95DurationMs: 0 }],
      }),
      loading: false,
      error: null,
      refetch: vi.fn(),
    });
    render(<ObservabilityOverview />);

    const row = screen.getByText("openai/gpt-5.6").closest("tr");
    expect(row?.textContent).toContain("$1.25");
    expect(row?.textContent).toContain("41.5K / 800");
  });

  it("shows failure rates for tools, models, and subagents", () => {
    mocks.useOverview.mockReturnValue({
      data: overview({
        totals: totals({ runs: 1n, toolCalls: 10n, toolErrors: 1n, llmAttempts: 8n, llmFailures: 2n, subagents: 4n }),
      }),
      loading: false,
      error: null,
      refetch: vi.fn(),
    });
    render(<ObservabilityOverview />);

    const rates = screen.getByRole("list", { name: "Failure rates" });
    expect(rates.textContent).toContain("10%");
    expect(rates.textContent).toContain("25%");
    expect(rates.textContent).toContain("2/8 attempts");
  });
});
