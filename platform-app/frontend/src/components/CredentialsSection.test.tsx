import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { CredentialsSection } from "@/components/CredentialsSection";

const listMyCredentials = vi.fn();
const updateMyCredentials = vi.fn();

vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: (...args: unknown[]) => listMyCredentials(...args),
    updateMyCredentials: (...args: unknown[]) => updateMyCredentials(...args),
  },
}));

// The OAuth device flows and CLI import have their own components/tests;
// stub them so this test exercises the provider list itself. The stubs echo
// the slot prop so slot targeting is observable.
vi.mock("@/components/AnthropicOAuthConnect", () => ({
  AnthropicOAuthConnect: ({ slot }: { slot?: number }) => (
    <div>anthropic-oauth-flow{slot !== undefined ? ` slot:${slot}` : ""}</div>
  ),
}));
vi.mock("@/components/OpenAIOAuthConnect", () => ({
  OpenAIOAuthConnect: ({ slot }: { slot?: number }) => (
    <div>openai-oauth-flow{slot !== undefined ? ` slot:${slot}` : ""}</div>
  ),
}));
vi.mock("@/components/CopilotOAuthConnect", () => ({
  CopilotOAuthConnect: () => <div>copilot-oauth-flow</div>,
}));
vi.mock("@/components/ImportLocalCredentials", () => ({
  ImportLocalCredentials: () => null,
}));
vi.mock("@/components/ShareCredentialsDialog", () => ({
  ShareCredentialsDialog: () => null,
}));

const baseCredentials = {
  namespace: "user-alice",
  anthropicApiKeyPresent: true,
  openaiApiKeyPresent: false,
  openrouterApiKeyPresent: false,
  xaiApiKeyPresent: false,
  anthropicOauthPresent: true,
  openaiOauthPresent: false,
  copilotOauthPresent: false,
  githubTokenPresent: false,
  integrations: [],
};

beforeEach(() => {
  listMyCredentials.mockResolvedValue(baseCredentials);
  updateMyCredentials.mockResolvedValue(baseCredentials);
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("CredentialsSection", () => {
  it("lists every provider with its saved state", async () => {
    render(<CredentialsSection />);
    await waitFor(() => expect(screen.getByText("user-alice")).toBeTruthy());

    const anthropic = screen.getByRole("button", { name: /Anthropic \/ Claude/ });
    expect(anthropic.textContent).toContain("API key · Claude account");
    expect(anthropic.textContent).toContain("Connected");

    const openai = screen.getByRole("button", { name: /OpenAI \/ ChatGPT/ });
    expect(openai.textContent).toContain("Not connected");
    expect(openai.textContent).not.toContain("Connected");

    for (const name of [/GitHub Copilot/, /OpenRouter/, /xAI \/ Grok/, /^GitHub Not connected/]) {
      expect(screen.getByRole("button", { name })).toBeTruthy();
    }
  });

  it("expands one provider at a time", async () => {
    render(<CredentialsSection />);
    await waitFor(() => expect(screen.getByText("user-alice")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /Anthropic \/ Claude/ }));
    expect(screen.getByText("anthropic-oauth-flow")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /OpenAI \/ ChatGPT/ }));
    expect(screen.getByText("openai-oauth-flow")).toBeTruthy();
    expect(screen.queryByText("anthropic-oauth-flow")).toBeNull();
  });

  it("saves a single credential from its provider panel", async () => {
    render(<CredentialsSection />);
    await waitFor(() => expect(screen.getByText("user-alice")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /OpenRouter/ }));
    fireEvent.change(screen.getByPlaceholderText("sk-or-v1-..."), {
      target: { value: "sk-or-v1-secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(updateMyCredentials).toHaveBeenCalledWith({ openrouterApiKey: "sk-or-v1-secret" }),
    );
  });

  it("removes a saved credential", async () => {
    render(<CredentialsSection />);
    await waitFor(() => expect(screen.getByText("user-alice")).toBeTruthy());

    fireEvent.click(screen.getByRole("button", { name: /Anthropic \/ Claude/ }));
    const removeButtons = screen.getAllByRole("button", { name: "Remove" });
    fireEvent.click(removeButtons[0]);

    await waitFor(() =>
      expect(updateMyCredentials).toHaveBeenCalledWith({ clear: ["anthropic-oauth"] }),
    );
  });

  describe("OAuth subscriptions", () => {
    const sub = (provider: string, slot: number, accountLabel = "") => ({
      provider,
      slot,
      secretName: slot === 1 ? `usercred-${provider}` : `usercred-${provider}-${slot}`,
      accountLabel,
    });

    it("lists connected subscriptions sorted by slot with account labels", async () => {
      listMyCredentials.mockResolvedValue({
        ...baseCredentials,
        oauthSubscriptions: [
          sub("anthropic", 2, "bob@example.com"),
          sub("anthropic", 1, "alice@example.com"),
        ],
      });
      render(<CredentialsSection />);
      await waitFor(() => expect(screen.getByText("user-alice")).toBeTruthy());

      fireEvent.click(screen.getByRole("button", { name: /Anthropic \/ Claude/ }));
      const rows = screen.getAllByText(/^Subscription \d$/);
      expect(rows.map((r) => r.textContent)).toEqual(["Subscription 1", "Subscription 2"]);
      expect(screen.getByText("alice@example.com")).toBeTruthy();
      expect(screen.getByText("bob@example.com")).toBeTruthy();
    });

    it("disconnects the primary with the legacy key and slot N with the numbered key", async () => {
      const withSubs = {
        ...baseCredentials,
        oauthSubscriptions: [sub("anthropic", 1), sub("anthropic", 3)],
      };
      listMyCredentials.mockResolvedValue(withSubs);
      updateMyCredentials.mockResolvedValue(withSubs);
      render(<CredentialsSection />);
      await waitFor(() => expect(screen.getByText("user-alice")).toBeTruthy());
      fireEvent.click(screen.getByRole("button", { name: /Anthropic \/ Claude/ }));

      fireEvent.click(screen.getByRole("button", { name: "Disconnect anthropic subscription 3" }));
      await waitFor(() =>
        expect(updateMyCredentials).toHaveBeenCalledWith({ clear: ["anthropic-oauth-3"] }),
      );

      fireEvent.click(screen.getByRole("button", { name: "Disconnect anthropic subscription 1" }));
      await waitFor(() =>
        expect(updateMyCredentials).toHaveBeenCalledWith({ clear: ["anthropic-oauth"] }),
      );
    });

    it("connects another subscription at the lowest free slot", async () => {
      listMyCredentials.mockResolvedValue({
        ...baseCredentials,
        openaiOauthPresent: true,
        oauthSubscriptions: [sub("openai", 1), sub("openai", 3)],
      });
      render(<CredentialsSection />);
      await waitFor(() => expect(screen.getByText("user-alice")).toBeTruthy());

      fireEvent.click(screen.getByRole("button", { name: /OpenAI \/ ChatGPT/ }));
      fireEvent.click(screen.getByRole("button", { name: "Connect another subscription" }));
      expect(screen.getByText("openai-oauth-flow slot:2")).toBeTruthy();
    });

    it("hides connect-another when every failover slot is used", async () => {
      listMyCredentials.mockResolvedValue({
        ...baseCredentials,
        oauthSubscriptions: Array.from({ length: 9 }, (_, i) => sub("anthropic", i + 1)),
      });
      render(<CredentialsSection />);
      await waitFor(() => expect(screen.getByText("user-alice")).toBeTruthy());

      fireEvent.click(screen.getByRole("button", { name: /Anthropic \/ Claude/ }));
      expect(screen.getAllByText(/^Subscription \d$/)).toHaveLength(9);
      expect(screen.queryByRole("button", { name: "Connect another subscription" })).toBeNull();
    });

    it("shows copilot subscription rows with per-slot disconnect but no multi-connect", async () => {
      listMyCredentials.mockResolvedValue({
        ...baseCredentials,
        copilotOauthPresent: true,
        oauthSubscriptions: [sub("copilot", 1), sub("copilot", 2, "carol@example.com")],
      });
      render(<CredentialsSection />);
      await waitFor(() => expect(screen.getByText("user-alice")).toBeTruthy());

      fireEvent.click(screen.getByRole("button", { name: /GitHub Copilot/ }));
      expect(screen.getByText("carol@example.com")).toBeTruthy();
      expect(screen.queryByRole("button", { name: "Connect another subscription" })).toBeNull();

      fireEvent.click(screen.getByRole("button", { name: "Disconnect copilot subscription 2" }));
      await waitFor(() =>
        expect(updateMyCredentials).toHaveBeenCalledWith({ clear: ["copilot-oauth-2"] }),
      );
    });
  });
});
