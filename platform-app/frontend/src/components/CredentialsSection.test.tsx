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
// stub them so this test exercises the provider list itself.
vi.mock("@/components/AnthropicOAuthConnect", () => ({
  AnthropicOAuthConnect: () => <div>anthropic-oauth-flow</div>,
}));
vi.mock("@/components/OpenAIOAuthConnect", () => ({
  OpenAIOAuthConnect: () => <div>openai-oauth-flow</div>,
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
});
