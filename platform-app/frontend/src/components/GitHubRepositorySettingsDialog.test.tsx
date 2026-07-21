import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { GitHubRepositorySettingsDialog } from "@/components/GitHubRepositorySettingsDialog";
import { client } from "@/lib/client";
import {
  AgentRunDefaultsSchema,
  GitHubRepositorySchema,
  GitHubRepositoryTriggerSettingsSchema,
} from "@/rpc/platform/service_pb";

vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: vi.fn().mockResolvedValue({
      namespace: "user-alice",
      anthropicApiKeyPresent: true,
      openaiApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      githubTokenPresent: true,
      secrets: [{ name: "github-webhook-secret", keys: ["secret"] }],
    }),
    listAvailableModels: vi.fn().mockResolvedValue({ models: [] }),
    updateGitHubRepository: vi.fn().mockResolvedValue({}),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function repo(namespace = "user-alice", webhookSecret = "") {
  return create(GitHubRepositorySchema, {
    namespace,
    name: "acme-payments",
    owner: "acme",
    repo: "payments",
    triggerKeyword: "@agent",
    defaults: create(AgentRunDefaultsSchema, {
      repoUrl: "https://github.com/acme/payments.git",
      model: "anthropic/claude-sonnet-4-6",
      provider: "anthropic",
      authMode: "api-key",
    }),
    reviewerDefaults: create(AgentRunDefaultsSchema, {
      model: "anthropic/claude-sonnet-4-6",
      provider: "anthropic",
      authMode: "api-key",
      workflowMode: "chat",
      executionMode: "team",
      modeRef: "implementer-mode",
    }),
    triggerSettings: create(GitHubRepositoryTriggerSettingsSchema, {
      triggerKeyword: "@agent",
      pollInterval: "60s",
      webhookSecret,
      cancelRunsOnIssueClose: true,
      reviewLoopDisabled: true,
    }),
  });
}

function submitForm() {
  const form = document.querySelector("form");
  expect(form).toBeTruthy();
  fireEvent.submit(form as HTMLFormElement);
}

describe("GitHubRepositorySettingsDialog", () => {
  it("submits GitHub trigger settings with run defaults", async () => {
    render(<GitHubRepositorySettingsDialog repo={repo()} />);

    fireEvent.click(screen.getByRole("button", { name: /Settings/ }));
    fireEvent.click(await screen.findByRole("button", { name: /Trigger settings/ }));

    fireEvent.change(await screen.findByLabelText(/Trigger keyword/), {
      target: { value: "@agents" },
    });
    fireEvent.change(screen.getByLabelText(/Poll interval/), {
      target: { value: "2m" },
    });
    fireEvent.change(screen.getByLabelText(/Webhook secret/), {
      target: { value: "github-webhook-secret" },
    });
    fireEvent.change(screen.getByLabelText(/Allowed GitHub users/), {
      target: { value: "alice, bob" },
    });
    fireEvent.change(screen.getByLabelText(/Denied GitHub users/), {
      target: { value: "mallory" },
    });
    fireEvent.click(screen.getByRole("button", { name: /PR review loop/ }));
    expect(
      screen.getByRole("switch", { name: /Disable autonomous PR review loop/ }).getAttribute("aria-checked"),
    ).toBe("true");
    fireEvent.change(screen.getByLabelText(/Max review rounds/), {
      target: { value: "5" },
    });
    fireEvent.change(screen.getByLabelText(/^Reviewer mode$/), {
      target: { value: "security-review" },
    });
    fireEvent.change(screen.getByLabelText(/Reviewer mode version/), {
      target: { value: "v2" },
    });
    fireEvent.change(screen.getByLabelText(/Reviewer mode channel/), {
      target: { value: "stable" },
    });
    fireEvent.click(screen.getAllByRole("button", { name: /^Model/ })[0]);
    const reviewerModel = document.querySelector<HTMLInputElement>("#github-settings-reviewer-model");
    expect(reviewerModel).toBeTruthy();
    fireEvent.change(reviewerModel as HTMLInputElement, {
      target: { value: "anthropic/claude-opus-4-6" },
    });

    submitForm();

    await waitFor(() => {
      expect(client.updateGitHubRepository).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.updateGitHubRepository).mock.calls[0][0];
    expect(request.namespace).toBe("user-alice");
    expect(request.name).toBe("acme-payments");
    expect(request.defaults?.model).toBe("anthropic/claude-sonnet-4-6");
    expect(request.triggerSettings?.triggerKeyword).toBe("@agents");
    expect(request.triggerSettings?.pollInterval).toBe("2m");
    expect(request.triggerSettings?.webhookSecret).toBe("github-webhook-secret");
    expect(request.triggerSettings?.cancelRunsOnIssueClose).toBe(true);
    expect(request.triggerSettings?.authAllowedUsers).toEqual(["alice", "bob"]);
    expect(request.triggerSettings?.authDenyUsers).toEqual(["mallory"]);
    expect(request.triggerSettings?.reviewLoopDisabled).toBe(true);
    expect(request.triggerSettings?.reviewLoopMaxRounds).toBe(5);
    expect(request.triggerSettings?.reviewerModeRef).toBe("security-review");
    expect(request.triggerSettings?.reviewerModeVersion).toBe("v2");
    expect(request.triggerSettings?.reviewerModeChannel).toBe("stable");
    expect(request.useReviewerDefaults).toBe(true);
    expect(request.reviewerDefaults?.model).toBe("anthropic/claude-opus-4-6");
    expect(request.reviewerDefaults?.workflowMode).toBe("");
    expect(request.reviewerDefaults?.executionMode).toBe("");
    expect(request.reviewerDefaults?.modeRef).toBe("");
    expect(request.useSavedReviewerCredentials).toBe(true);
  });

  it("keeps the maintainer off by default and saves its settings when enabled", async () => {
    render(<GitHubRepositorySettingsDialog repo={repo()} />);

    fireEvent.click(screen.getByRole("button", { name: /Settings/ }));
    fireEvent.click(await screen.findByRole("button", { name: /^Maintainer/ }));

    const enabled = screen.getByRole("switch", { name: /Enable maintainer/ });
    expect(enabled.getAttribute("aria-checked")).toBe("false");
    expect(screen.queryByLabelText(/Max concurrent dispatches/)).toBeNull();

    fireEvent.click(enabled);
    expect(await screen.findByLabelText(/Max concurrent dispatches/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/Max concurrent dispatches/), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText(/Max dispatches per day/), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText(/Standup interval/), { target: { value: "6h" } });
    fireEvent.change(screen.getByLabelText(/^Maintainer mode$/), { target: { value: "repository-maintainer" } });
    fireEvent.change(screen.getByLabelText(/Maintainer model/), { target: { value: "claude-opus-4-6" } });
    fireEvent.click(screen.getByRole("switch", { name: /Allow the maintainer to merge approved pull requests/ }));

    submitForm();

    await waitFor(() => {
      expect(client.updateGitHubRepository).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.updateGitHubRepository).mock.calls[0][0];
    expect(request.triggerSettings?.maintainerEnabled).toBe(true);
    expect(request.triggerSettings?.maintainerMaxConcurrentDispatches).toBe(4);
    expect(request.triggerSettings?.maintainerMaxDispatchesPerDay).toBe(12);
    expect(request.triggerSettings?.maintainerStandupInterval).toBe("6h");
    expect(request.triggerSettings?.maintainerModeRef).toBe("repository-maintainer");
    expect(request.triggerSettings?.maintainerModel).toBe("claude-opus-4-6");
    expect(request.triggerSettings?.maintainerAllowPrMerge).toBe(true);
  });

  it("does not offer caller secrets when editing a shared repository namespace", async () => {
    render(<GitHubRepositorySettingsDialog repo={repo("user-bob", "shared-webhook")} />);

    fireEvent.click(screen.getByRole("button", { name: /Settings/ }));
    fireEvent.click(await screen.findByRole("button", { name: /Trigger settings/ }));

    const picker = await screen.findByLabelText(/Webhook secret/);
    expect(screen.queryByRole("option", { name: "github-webhook-secret" })).toBeNull();
    expect(screen.getByRole("option", { name: "shared-webhook (not found)" })).toBeTruthy();
    expect((picker as HTMLSelectElement).value).toBe("shared-webhook");
  });
});
