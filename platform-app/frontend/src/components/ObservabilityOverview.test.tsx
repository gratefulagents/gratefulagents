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

describe("ObservabilityOverview", () => {
  it("shows an empty state and changes historical range", () => {
    mocks.useOverview.mockReturnValue({ data: { totals: { runs: 0n } }, loading: false, error: null, refetch: vi.fn() });
    render(<ObservabilityOverview />);

    expect(screen.getByText("No historical metrics were recorded in this range.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "7d" }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(screen.getByRole("button", { name: "30d" }));
    expect(mocks.useOverview).toHaveBeenLastCalledWith("30d");
  });

  it("offers retry when the overview fails", () => {
    const refetch = vi.fn();
    mocks.useOverview.mockReturnValue({ data: null, loading: false, error: new Error("offline"), refetch });
    render(<ObservabilityOverview />);

    expect(screen.getByRole("alert").textContent).toContain("could not be loaded");
    fireEvent.click(screen.getByRole("button", { name: /Retry/ }));
    expect(refetch).toHaveBeenCalledOnce();
  });

  it("shows per-model cost and tokens in the models breakdown", () => {
    mocks.useOverview.mockReturnValue({
      data: {
        totals: {
          runs: 2n, costUsd: 3.5, inputTokens: 1200n, outputTokens: 300n, toolCalls: 9n,
          subagents: 1n, toolErrors: 0n, subagentFailures: 0n, llmFailures: 0n,
        },
        buckets: [],
        tools: [],
        subagents: [],
        models: [{ name: "openai/gpt-5.6", count: 4n, errors: 0n, costUsd: 1.25, inputTokens: 41500n, outputTokens: 800n }],
        dataCompleteness: { metricsComplete: true, activityComplete: true },
        coverageWarnings: [],
      },
      loading: false,
      error: null,
      refetch: vi.fn(),
    });
    render(<ObservabilityOverview />);

    const models = screen.getByRole("list", { name: "Models" });
    expect(models.textContent).toContain("openai/gpt-5.6");
    expect(models.textContent).toContain("$1.25");
    expect(models.textContent).toContain("41.5K in · 800 out tok");
  });
});
