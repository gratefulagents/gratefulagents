import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ModelDefaultsSection } from "@/components/ModelDefaultsSection";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    getMyModelDefaults: vi.fn(),
    listMyCredentials: vi.fn(),
    listAvailableModels: vi.fn(),
    updateMyModelDefaults: vi.fn(),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const getDefaults = vi.mocked(client.getMyModelDefaults);
const listCredentials = vi.mocked(client.listMyCredentials);
const listModels = vi.mocked(client.listAvailableModels);
const updateDefaults = vi.mocked(client.updateMyModelDefaults);

beforeEach(() => {
  listCredentials.mockResolvedValue({
    namespace: "user-alice",
    anthropicApiKeyPresent: true,
    openaiApiKeyPresent: true,
    openrouterApiKeyPresent: false,
    xaiApiKeyPresent: false,
    anthropicOauthPresent: false,
    openaiOauthPresent: false,
    copilotOauthPresent: false,
    githubTokenPresent: false,
  } as never);
  listModels.mockResolvedValue({ models: [] } as never);
});

describe("ModelDefaultsSection", () => {
  it("loads saved defaults into the fields", async () => {
    getDefaults.mockResolvedValue({
      provider: "openai",
      authMode: "api-key",
      model: "gpt-5",
      reasoningLevel: "high",
      disabled: false,
      updatedAt: { seconds: BigInt(1_700_000_000), nanos: 0 },
    } as never);

    render(<ModelDefaultsSection />);

    await waitFor(() => {
      expect((screen.getByLabelText("Provider") as HTMLSelectElement).value).toBe("openai");
    });
    expect((screen.getByLabelText("Authentication mode") as HTMLSelectElement).value).toBe(
      "api-key",
    );
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("gpt-5");
    expect((screen.getByLabelText("Reasoning level") as HTMLSelectElement).value).toBe("high");
    expect(screen.getByText(/Last saved/)).toBeTruthy();
  });

  it("shows the never-saved hint when updatedAt is unset", async () => {
    getDefaults.mockResolvedValue({
      provider: "",
      authMode: "",
      model: "",
      reasoningLevel: "",
      disabled: false,
    } as never);

    render(<ModelDefaultsSection />);

    await screen.findByText(/No default model saved/);
    expect((screen.getByLabelText("Provider") as HTMLSelectElement).value).toBe("anthropic");
  });

  it("saves edited values and reflects the server response", async () => {
    getDefaults.mockResolvedValue({
      provider: "",
      authMode: "",
      model: "",
      reasoningLevel: "",
      disabled: false,
    } as never);
    updateDefaults.mockResolvedValue({
      provider: "openai",
      authMode: "api-key",
      model: "gpt-5-mini",
      reasoningLevel: "low",
      disabled: true,
      updatedAt: { seconds: BigInt(1_700_000_000), nanos: 0 },
    } as never);

    render(<ModelDefaultsSection />);
    await screen.findByText(/No default model saved/);

    fireEvent.change(screen.getByLabelText("Provider"), { target: { value: "openai" } });
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "  gpt-5-mini  " } });
    fireEvent.change(screen.getByLabelText("Reasoning level"), { target: { value: "low" } });
    fireEvent.click(screen.getByRole("switch"));
    fireEvent.click(screen.getByRole("button", { name: "Save default model" }));

    await waitFor(() => {
      expect(updateDefaults).toHaveBeenCalledWith({
        provider: "openai",
        authMode: "api-key",
        model: "gpt-5-mini",
        reasoningLevel: "low",
        disabled: true,
      });
    });
    await screen.findByText(/Last saved/);
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("gpt-5-mini");
  });

  it("clears defaults with empty values", async () => {
    getDefaults.mockResolvedValue({
      provider: "openai",
      authMode: "api-key",
      model: "gpt-5",
      reasoningLevel: "high",
      disabled: false,
      updatedAt: { seconds: BigInt(1_700_000_000), nanos: 0 },
    } as never);
    updateDefaults.mockResolvedValue({
      provider: "",
      authMode: "",
      model: "",
      reasoningLevel: "",
      disabled: false,
    } as never);

    render(<ModelDefaultsSection />);
    await screen.findByText(/Last saved/);

    fireEvent.click(screen.getByRole("button", { name: "Clear" }));

    await waitFor(() => {
      expect(updateDefaults).toHaveBeenCalledWith({
        provider: "",
        authMode: "",
        model: "",
        reasoningLevel: "",
        disabled: false,
      });
    });
    await screen.findByText(/No default model saved/);
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("");
  });

  it("surfaces load errors", async () => {
    getDefaults.mockRejectedValue(new Error("defaults unavailable"));

    render(<ModelDefaultsSection />);

    await screen.findByText("defaults unavailable");
  });

  it("offers only providers and authentication modes backed by saved credentials", async () => {
    getDefaults.mockResolvedValue({
      provider: "anthropic",
      authMode: "api-key",
      model: "claude-sonnet-4-6",
      reasoningLevel: "",
      disabled: false,
    } as never);
    listCredentials.mockResolvedValue({
      namespace: "user-alice",
      anthropicApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiApiKeyPresent: false,
      openaiOauthPresent: true,
      copilotOauthPresent: false,
      openrouterApiKeyPresent: true,
      xaiApiKeyPresent: false,
    } as never);
    updateDefaults.mockResolvedValue({
      provider: "anthropic",
      authMode: "api-key",
      model: "claude-sonnet-4-6",
      reasoningLevel: "",
      disabled: true,
    } as never);

    render(<ModelDefaultsSection />);

    const providerSelect = (await screen.findByLabelText("Provider")) as HTMLSelectElement;
    await waitFor(() => expect(providerSelect.value).toBe("anthropic"));
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe(
      "claude-sonnet-4-6",
    );
    expect(Array.from(providerSelect.options).map((option) => option.text)).toEqual([
      "Anthropic (credential unavailable)",
      "OpenAI",
      "OpenRouter",
    ]);
    const authSelect = screen.getByLabelText("Authentication mode") as HTMLSelectElement;
    expect(Array.from(authSelect.options).map((option) => option.text)).toEqual([
      "API key (credential unavailable)",
    ]);
    expect(authSelect.value).toBe("api-key");

    fireEvent.click(screen.getByRole("switch"));
    fireEvent.click(screen.getByRole("button", { name: "Save default model" }));
    await waitFor(() => {
      expect(updateDefaults).toHaveBeenCalledWith({
        provider: "anthropic",
        authMode: "api-key",
        model: "claude-sonnet-4-6",
        reasoningLevel: "",
        disabled: true,
      });
    });

    fireEvent.change(providerSelect, { target: { value: "openai" } });
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("");
    expect((screen.getByLabelText("Authentication mode") as HTMLSelectElement).value).toBe(
      "oauth",
    );
  });

  it("offers xAI when an xAI API key is saved", async () => {
    getDefaults.mockResolvedValue({
      provider: "",
      authMode: "",
      model: "",
      reasoningLevel: "",
      disabled: false,
    } as never);
    listCredentials.mockResolvedValue({
      namespace: "user-alice",
      anthropicApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiApiKeyPresent: false,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      openrouterApiKeyPresent: false,
      xaiApiKeyPresent: true,
    } as never);
    listModels.mockResolvedValue({ models: ["grok-4"] } as never);

    render(<ModelDefaultsSection />);

    const providerSelect = (await screen.findByLabelText("Provider")) as HTMLSelectElement;
    await waitFor(() => expect(providerSelect.value).toBe("xai"));
    expect(Array.from(providerSelect.options).map((option) => option.text)).toEqual(["xAI"]);
    expect((screen.getByLabelText("Authentication mode") as HTMLSelectElement).value).toBe(
      "api-key",
    );
    await waitFor(() => {
      expect(listModels).toHaveBeenCalledWith(
        { namespace: "user-alice", provider: "xai", authMode: "api-key" },
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      );
    });
    expect(await screen.findByText("1 xAI models available")).toBeTruthy();
  });

  it("loads model suggestions for the selected credential and auth mode", async () => {
    getDefaults.mockResolvedValue({
      provider: "openai",
      authMode: "api-key",
      model: "gpt-5",
      reasoningLevel: "",
      disabled: false,
    } as never);
    listCredentials.mockResolvedValue({
      namespace: "user-alice",
      anthropicApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiApiKeyPresent: true,
      openaiOauthPresent: true,
      copilotOauthPresent: false,
      openrouterApiKeyPresent: false,
      xaiApiKeyPresent: false,
    } as never);
    listModels.mockResolvedValue({ models: ["gpt-5", "gpt-5-mini"] } as never);

    render(<ModelDefaultsSection />);

    await waitFor(() => {
      expect(listModels).toHaveBeenCalledWith(
        { namespace: "user-alice", provider: "openai", authMode: "api-key" },
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      );
    });
    expect(await screen.findByText("2 OpenAI models available")).toBeTruthy();
    expect((screen.getByLabelText("Model") as HTMLInputElement).getAttribute("list")).toBe(
      "model-defaults-model-options",
    );
    expect(document.querySelector('#model-defaults-model-options option[value="gpt-5-mini"]'))
      .toBeTruthy();

    fireEvent.change(screen.getByLabelText("Authentication mode"), {
      target: { value: "oauth" },
    });
    await waitFor(() => {
      expect(listModels).toHaveBeenLastCalledWith(
        { namespace: "user-alice", provider: "openai", authMode: "oauth" },
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      );
    });
  });
});
