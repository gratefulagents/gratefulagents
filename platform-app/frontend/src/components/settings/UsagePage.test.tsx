import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import UsagePage from "@/components/settings/UsagePage";
import { client } from "@/lib/client";
import {
  MyOpenAIUsageSchema,
  type MyOpenAIUsage,
  OpenAIUsageLimitSchema,
} from "@/rpc/platform/service_pb";

vi.mock("@/lib/client", () => ({
  client: { getMyOpenAIUsage: vi.fn() },
}));

function renderPage() {
  return render(
    <MemoryRouter>
      <UsagePage />
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("UsagePage", () => {
  it("renders account data from the current ChatGPT OAuth credential", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockResolvedValue(
      create(MyOpenAIUsageSchema, {
        openaiOauthPresent: true,
        accountEmail: "oauth@example.com",
        planType: "pro",
        accountStatusAvailable: true,
        limits: [
          create(OpenAIUsageLimitSchema, {
            label: "5 hour",
            usedPercent: 42,
            resetAtUnix: 1893456000n,
          }),
          create(OpenAIUsageLimitSchema, {
            label: "Weekly",
            usedPercent: 9,
            resetAtUnix: 1893888000n,
          }),
        ],
        credits: "12.50",
        tokenActivityAvailable: true,
        lifetimeTokens: 10000n,
        peakDailyTokens: 1200n,
        currentStreakDays: 3n,
        longestStreakDays: 8n,
        longestRunningTurnSeconds: 3900n,
        last30DaysTokens: 700n,
        lookbackDays: 30,
      }),
    );

    renderPage();

    expect(await screen.findByText("ChatGPT Pro")).toBeTruthy();
    expect(screen.getByText("oauth@example.com")).toBeTruthy();
    expect(screen.getByText("58% left")).toBeTruthy();
    expect(screen.getByText("10,000")).toBeTruthy();
    expect(screen.getByText("700")).toBeTruthy();
    expect(screen.queryByText(/Observed model usage/)).toBeNull();
    expect(screen.queryByText(/Est. cost/)).toBeNull();
  });

  it("points disconnected users to Credentials", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockResolvedValue(
      create(MyOpenAIUsageSchema, { openaiOauthPresent: false, lookbackDays: 30 }),
    );

    renderPage();

    expect(await screen.findByText("Connect OpenAI to see usage")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Open Credentials" }).getAttribute("href")).toBe(
      "/settings/credentials",
    );
  });

  it("keeps retry feedback visible while recovering from an initial failure", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockRejectedValueOnce(new Error("backend unavailable"));
    renderPage();

    expect(await screen.findByText("Usage unavailable")).toBeTruthy();
    expect(screen.getByText("backend unavailable")).toBeTruthy();

    let resolveRetry!: (value: MyOpenAIUsage) => void;
    const retry = new Promise<MyOpenAIUsage>((resolve) => {
      resolveRetry = resolve;
    });
    vi.mocked(client.getMyOpenAIUsage).mockReturnValueOnce(retry);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(screen.getByRole("button", { name: "Trying again…" })).toBeTruthy();
    expect(screen.getByText("Usage unavailable")).toBeTruthy();

    resolveRetry(create(MyOpenAIUsageSchema, { openaiOauthPresent: false, lookbackDays: 30 }));
    expect(await screen.findByText("Connect OpenAI to see usage")).toBeTruthy();
    await waitFor(() => expect(client.getMyOpenAIUsage).toHaveBeenCalledTimes(2));
  });
});
