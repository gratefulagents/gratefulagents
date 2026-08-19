import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";

import { RunDefaultsRows } from "@/components/run-defaults/RunDefaultsRows";
import { emptyDefaults } from "@/components/run-defaults/helpers";
import { client } from "@/lib/client";
import { AgentRunDefaultsSchema, type AgentRunDefaults } from "@/rpc/platform/service_pb";

vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: vi.fn().mockResolvedValue({
      namespace: "user-alice",
      anthropicApiKeyPresent: true,
      openaiApiKeyPresent: true,
      anthropicOauthPresent: true,
      openaiOauthPresent: true,
      copilotOauthPresent: false,
      githubTokenPresent: true,
    }),
    listAvailableModels: vi.fn().mockImplementation(({ provider }: { provider: string }) =>
      Promise.resolve({
        models: provider === "openai" ? ["gpt-5", "gpt-5-mini"] : ["claude-opus", "claude-sonnet"],
      }),
    ),
    listSSHTunnels: vi.fn().mockResolvedValue({
      namespace: "user-alice",
      tunnels: [
        {
          namespace: "user-alice",
          name: "gpu-vllm",
          host: "gpu.example.com",
          port: 22,
          user: "tunnel",
          remoteHost: "127.0.0.1",
          remotePort: 8000,
          phase: "Ready",
          message: "Validated SSH tunnel to tunnel@gpu.example.com",
        },
        {
          namespace: "user-alice",
          name: "legacy-llama",
          host: "llama.example.com",
          user: "tunnel",
          remotePort: 8080,
          phase: "Invalid",
          message: 'secret "llama-ssh" not found',
        },
      ],
    }),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function Harness() {
  const [defaults, setDefaults] = useState<AgentRunDefaults>(() =>
    create(AgentRunDefaultsSchema, {
      ...emptyDefaults(),
      provider: "anthropic",
      authMode: "api-key",
    }),
  );
  return (
    <RunDefaultsRows
      value={defaults}
      onChange={setDefaults}
      useSavedCredentials
      onUseSavedCredentialsChange={() => undefined}
    />
  );
}

describe("RunDefaultsRows model picker", () => {
  it("shows live models and refetches for provider and auth changes", async () => {
    render(<Harness />);

    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "user-alice", provider: "anthropic", authMode: "api-key" },
        expect.anything(),
      );
    });

    fireEvent.click(screen.getByRole("button", { name: /^Model/ }));
    expect(await screen.findByText("2 Anthropic models available")).toBeTruthy();
    const modelPicker = screen.getByLabelText(/^Model/) as HTMLSelectElement;
    expect(Array.from(modelPicker.options).map((option) => option.value)).toContain("claude-sonnet");

    fireEvent.click(screen.getByRole("button", { name: "OpenAI" }));
    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "user-alice", provider: "openai", authMode: "api-key" },
        expect.anything(),
      );
    });
    expect(await screen.findByText("2 OpenAI models available")).toBeTruthy();

    const authGroup = screen.getByRole("group", { name: "Authentication" });
    const oauthButton = Array.from(authGroup.querySelectorAll("button")).find(
      (button) => button.textContent === "OAuth",
    );
    expect(oauthButton).toBeTruthy();
    fireEvent.click(oauthButton as HTMLButtonElement);

    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "user-alice", provider: "openai", authMode: "oauth" },
        expect.anything(),
      );
    });
  });
});

describe("RunDefaultsRows SSH tunnel picker", () => {
  it("lists tunnels with their health, shows the selection's status, and updates the defaults", async () => {
    render(<Harness />);

    fireEvent.click(screen.getByRole("button", { name: /^Advanced/ }));
    await waitFor(() => {
      expect(client.listSSHTunnels).toHaveBeenCalled();
    });

    const picker = (await screen.findByLabelText("SSH tunnel")) as HTMLSelectElement;
    expect(Array.from(picker.options).map((option) => option.value)).toEqual([
      "",
      "gpu-vllm",
      "legacy-llama",
    ]);
    expect(Array.from(picker.options).map((option) => option.textContent)).toEqual([
      "none",
      "gpu-vllm — Ready",
      "legacy-llama — Invalid",
    ]);

    fireEvent.change(picker, { target: { value: "gpu-vllm" } });
    expect((screen.getByLabelText("SSH tunnel") as HTMLSelectElement).value).toBe("gpu-vllm");
    expect(
      screen.getByText(
        "Ready — tunnel@gpu.example.com:22 → 127.0.0.1:8000 · Validated SSH tunnel to tunnel@gpu.example.com",
      ),
    ).toBeTruthy();

    fireEvent.change(screen.getByLabelText("SSH tunnel"), { target: { value: "legacy-llama" } });
    expect(
      screen.getByText('Invalid — tunnel@llama.example.com → 127.0.0.1:8080 · secret "llama-ssh" not found'),
    ).toBeTruthy();
  });
});
