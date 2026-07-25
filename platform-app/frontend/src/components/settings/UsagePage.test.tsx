import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import UsagePage from "@/components/settings/UsagePage";
import { client } from "@/lib/client";
import {
  AnthropicUsageLimitSchema,
  CopilotUsageQuotaSchema,
  MyAnthropicUsageSchema,
  MyCopilotUsageSchema,
  MyOpenAIUsageSchema,
  type MyOpenAIUsage,
  OpenAIUsageLimitSchema,
} from "@/rpc/platform/service_pb";

vi.mock("@/lib/client", () => ({
  client: { getMyOpenAIUsage: vi.fn(), getMyCopilotUsage: vi.fn(), getMyAnthropicUsage: vi.fn() },
}));

function renderPage() {
  return render(
    <MemoryRouter>
      <UsagePage />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.mocked(client.getMyCopilotUsage).mockResolvedValue(
    create(MyCopilotUsageSchema, { copilotOauthPresent: false }),
  );
  vi.mocked(client.getMyAnthropicUsage).mockResolvedValue(
    create(MyAnthropicUsageSchema, { anthropicOauthPresent: false }),
  );
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("UsagePage", () => {
  it("renders ChatGPT account data when Copilot usage fails", async () => {
    vi.mocked(client.getMyCopilotUsage).mockRejectedValue(new Error("Copilot unavailable"));
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
    fireEvent.click(screen.getByRole("tab", { name: "OpenAI" }));

    expect(await screen.findByText("ChatGPT Pro")).toBeTruthy();
    expect(screen.getByText("oauth@example.com")).toBeTruthy();
    expect(screen.getByText("58% left")).toBeTruthy();
    expect(screen.getByText("10,000")).toBeTruthy();
    expect(screen.getByText("700")).toBeTruthy();
    expect(screen.queryByText(/Observed model usage/)).toBeNull();
    expect(screen.queryByText(/Est. cost/)).toBeNull();
  });

  it("renders GitHub Copilot quota data when OpenAI usage fails", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockRejectedValue(new Error("OpenAI unavailable"));
    vi.mocked(client.getMyCopilotUsage).mockResolvedValue(
      create(MyCopilotUsageSchema, {
        copilotOauthPresent: true,
        accountLogin: "octocat",
        plan: "individual_pro",
        usageAvailable: true,
        quotaResetDate: "2026-08-01",
        quotas: [
          create(CopilotUsageQuotaSchema, {
            name: "premium_interactions",
            entitlement: 300n,
            remaining: 225n,
          }),
        ],
      }),
    );

    renderPage();
    fireEvent.click(screen.getByRole("tab", { name: "Copilot" }));

    expect(await screen.findByText("GitHub Copilot Individual Pro")).toBeTruthy();
    expect(screen.getByText("@octocat")).toBeTruthy();
    expect(screen.getByText("Premium requests")).toBeTruthy();
    expect(screen.getByText("225 left")).toBeTruthy();
    expect(screen.getByRole("progressbar", { name: "Premium requests used" }).getAttribute("aria-valuenow")).toBe("25");
  });

  it("renders Copilot for users without OpenAI OAuth", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockResolvedValue(
      create(MyOpenAIUsageSchema, { openaiOauthPresent: false }),
    );
    vi.mocked(client.getMyCopilotUsage).mockResolvedValue(
      create(MyCopilotUsageSchema, {
        copilotOauthPresent: true,
        plan: "individual_pro",
        usageAvailable: true,
      }),
    );

    renderPage();
    fireEvent.click(screen.getByRole("tab", { name: "Copilot" }));

    expect(await screen.findByText("GitHub Copilot Individual Pro")).toBeTruthy();
    expect(client.getMyOpenAIUsage).not.toHaveBeenCalled();
  });

  it("renders every available Claude OAuth allowance and credential stat", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockResolvedValue(
      create(MyOpenAIUsageSchema, { openaiOauthPresent: false, lookbackDays: 30 }),
    );
    vi.mocked(client.getMyAnthropicUsage).mockResolvedValue(
      create(MyAnthropicUsageSchema, {
        anthropicOauthPresent: true,
        accountEmail: "claude@example.com",
        accountUuid: "account-123",
        credentialExpiresAtUnix: 1893456000n,
        credentialLastRefreshedAtUnix: 1893427200n,
        usageAvailable: true,
        limits: [
          create(AnthropicUsageLimitSchema, {
            label: "5 hour",
            usedPercent: 42,
            resetAtUnix: 1893456000n,
          }),
          create(AnthropicUsageLimitSchema, {
            label: "Weekly Opus",
            usedPercent: 11,
            resetAtUnix: 1893888000n,
          }),
        ],
        extraUsageAvailable: true,
        extraUsageEnabled: true,
        extraUsageMonthlyLimitUsdCents: 5000,
        extraUsageUsedCreditsUsdCents: 1250,
        extraUsageUtilization: 25,
      }),
    );

    renderPage();

    expect(await screen.findByText("Claude account")).toBeTruthy();
    expect(screen.getByText("claude@example.com")).toBeTruthy();
    expect(screen.getByText("Account account-123")).toBeTruthy();
    expect(screen.getByText("Weekly Opus")).toBeTruthy();
    expect(screen.getByText("89% left")).toBeTruthy();
    expect(screen.getByText("$50.00")).toBeTruthy();
    expect(screen.getByText("$12.50")).toBeTruthy();
    expect(screen.getByText("25%")).toBeTruthy();
    expect(screen.getByText("Token expires")).toBeTruthy();
    expect(screen.getByText("Last refreshed")).toBeTruthy();
  });

  it("requests only the open provider and caches tabs after they load", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockResolvedValue(
      create(MyOpenAIUsageSchema, { openaiOauthPresent: true, planType: "pro" }),
    );

    renderPage();

    await waitFor(() => expect(client.getMyAnthropicUsage).toHaveBeenCalledTimes(1));
    expect(client.getMyOpenAIUsage).not.toHaveBeenCalled();
    expect(client.getMyCopilotUsage).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("tab", { name: "OpenAI" }));
    expect(await screen.findByText("ChatGPT Pro")).toBeTruthy();
    expect(client.getMyOpenAIUsage).toHaveBeenCalledTimes(1);
    expect(client.getMyCopilotUsage).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("tab", { name: "Anthropic" }));
    await waitFor(() => expect(client.getMyAnthropicUsage).toHaveBeenCalledTimes(1));
  });

  it("refreshes only the open provider", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockResolvedValueOnce(
      create(MyOpenAIUsageSchema, { openaiOauthPresent: true, planType: "pro" }),
    );
    renderPage();
    await waitFor(() => expect(client.getMyAnthropicUsage).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("tab", { name: "OpenAI" }));
    expect(await screen.findByText("ChatGPT Pro")).toBeTruthy();

    vi.mocked(client.getMyOpenAIUsage).mockResolvedValueOnce(
      create(MyOpenAIUsageSchema, { openaiOauthPresent: true, planType: "business" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Refresh usage" }));

    expect(await screen.findByText("ChatGPT Business")).toBeTruthy();
    expect(screen.queryByText("ChatGPT Pro")).toBeNull();
    expect(client.getMyOpenAIUsage).toHaveBeenCalledTimes(2);
    expect(client.getMyAnthropicUsage).toHaveBeenCalledTimes(1);
    expect(client.getMyCopilotUsage).not.toHaveBeenCalled();
  });

  it("prompts users to reconnect a rejected Anthropic OAuth credential", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockResolvedValue(
      create(MyOpenAIUsageSchema, { openaiOauthPresent: false }),
    );
    vi.mocked(client.getMyAnthropicUsage).mockResolvedValue(
      create(MyAnthropicUsageSchema, { anthropicOauthPresent: true, reconnectRequired: true }),
    );

    renderPage();

    expect(await screen.findByText("Reconnect Anthropic to see usage")).toBeTruthy();
    expect(screen.getAllByRole("link", { name: "Open Credentials" })[0]?.getAttribute("href")).toBe("/settings/credentials");
  });

  it("points disconnected users to Credentials", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockResolvedValue(
      create(MyOpenAIUsageSchema, { openaiOauthPresent: false, lookbackDays: 30 }),
    );

    renderPage();

    expect(await screen.findByText("Connect Anthropic to see usage")).toBeTruthy();
    fireEvent.click(screen.getByRole("tab", { name: "OpenAI" }));
    expect(await screen.findByText("Connect OpenAI to see usage")).toBeTruthy();
    fireEvent.click(screen.getByRole("tab", { name: "Copilot" }));
    expect(await screen.findByText("Connect Copilot to see usage")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Open Credentials" }).getAttribute("href")).toBe(
      "/settings/credentials",
    );
  });

  it("keeps retry feedback visible while recovering from an initial failure", async () => {
    vi.mocked(client.getMyOpenAIUsage).mockRejectedValueOnce(new Error("backend unavailable"));
    renderPage();
    fireEvent.click(screen.getByRole("tab", { name: "OpenAI" }));

    expect(await screen.findByText("ChatGPT usage unavailable")).toBeTruthy();
    expect(screen.getAllByText("backend unavailable").length).toBeGreaterThan(0);

    let resolveRetry!: (value: MyOpenAIUsage) => void;
    const retry = new Promise<MyOpenAIUsage>((resolve) => {
      resolveRetry = resolve;
    });
    vi.mocked(client.getMyOpenAIUsage).mockReturnValueOnce(retry);
    fireEvent.click(screen.getAllByRole("button", { name: "Try again" })[0]!);

    expect(screen.getAllByRole("button", { name: "Trying again…" }).length).toBeGreaterThan(0);
    expect(screen.getByText("ChatGPT usage unavailable")).toBeTruthy();

    resolveRetry(create(MyOpenAIUsageSchema, { openaiOauthPresent: false, lookbackDays: 30 }));
    expect(await screen.findByText("Connect OpenAI to see usage")).toBeTruthy();
    await waitFor(() => expect(client.getMyOpenAIUsage).toHaveBeenCalledTimes(2));
  });
});
