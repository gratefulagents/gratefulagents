import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { create, type MessageInitShape } from "@bufbuild/protobuf";

import { RunModelSwitcher } from "./RunModelSwitcher";
import { AgentRunSchema } from "@/rpc/platform/service_pb";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listAvailableModels: vi.fn().mockResolvedValue({ models: ["claude-opus-4-5", "claude-sonnet-4-5"] }),
  },
}));

const listAvailableModelsMock = client.listAvailableModels as unknown as ReturnType<typeof vi.fn>;

afterEach(() => {
  cleanup();
  listAvailableModelsMock.mockClear();
});

function makeRun(overrides: MessageInitShape<typeof AgentRunSchema> = {}) {
  return create(AgentRunSchema, {
    namespace: "ns",
    name: "run",
    phase: "Running",
    model: "anthropic/claude-opus-4-5",
    authMode: "api-key",
    claudeApiKeySecret: "claude-secret",
    ...overrides,
  });
}

describe("RunModelSwitcher", () => {
  it("fetches models once per open, not on every streaming run update", async () => {
    const onUpdate = vi.fn();
    const { rerender } = render(
      <RunModelSwitcher run={makeRun()} canUpdate updating={false} onUpdate={onUpdate} />,
    );

    fireEvent.click(screen.getByRole("button", { name: "Switch provider or model" }));
    expect(await screen.findByText("2 models available")).toBeTruthy();
    expect(listAvailableModelsMock).toHaveBeenCalledTimes(1);

    // Watch events replace the run object (fresh references, same content);
    // the open editor must not abort/reissue the models request.
    rerender(<RunModelSwitcher run={makeRun()} canUpdate updating={false} onUpdate={onUpdate} />);
    rerender(<RunModelSwitcher run={makeRun()} canUpdate updating={false} onUpdate={onUpdate} />);

    expect(listAvailableModelsMock).toHaveBeenCalledTimes(1);
  });

  it("refetches when the provider selection changes", async () => {
    const run = makeRun({
      providerKeys: [{ provider: "openai", secretName: "openai-key", secretKey: "api-key" }],
    });
    render(<RunModelSwitcher run={run} canUpdate updating={false} onUpdate={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: "Switch provider or model" }));
    await screen.findByText("2 models available");
    expect(listAvailableModelsMock).toHaveBeenCalledTimes(1);
    expect(listAvailableModelsMock.mock.calls[0][0]).toMatchObject({
      namespace: "ns",
      provider: "anthropic",
      claudeApiKeySecret: "claude-secret",
    });

    fireEvent.change(screen.getByLabelText("Provider"), { target: { value: "openai" } });
    await screen.findByText("2 models available");
    expect(listAvailableModelsMock).toHaveBeenCalledTimes(2);
    // Foreign provider: list via saved caller credentials, not run refs.
    expect(listAvailableModelsMock.mock.calls[1][0]).toEqual({ namespace: "ns", provider: "openai" });
  });

  it("submits the selected provider, model, and reasoning level", async () => {
    const onUpdate = vi.fn().mockResolvedValue(undefined);
    render(<RunModelSwitcher run={makeRun()} canUpdate updating={false} onUpdate={onUpdate} />);

    fireEvent.click(screen.getByRole("button", { name: "Switch provider or model" }));
    await screen.findByText("2 models available");

    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "claude-sonnet-4-5" } });
    fireEvent.change(screen.getByLabelText("Reasoning"), { target: { value: "high" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onUpdate).toHaveBeenCalledWith({
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      reasoningLevel: "high",
    });
  });

  it("warns that an OAuth-run provider switch restarts the pod, but allows it", async () => {
    const onUpdate = vi.fn().mockResolvedValue(undefined);
    const run = makeRun({ authMode: "oauth", claudeApiKeySecret: "", openaiOauthSecret: "anthropic-oauth" });
    render(<RunModelSwitcher run={run} canUpdate updating={false} onUpdate={onUpdate} />);

    fireEvent.click(screen.getByRole("button", { name: "Switch provider or model" }));
    await screen.findByText("2 models available");

    const providerSelect = screen.getByLabelText("Provider") as HTMLSelectElement;
    expect(providerSelect.disabled).toBe(false);
    expect(screen.queryByText(/restarts the run's pod/i)).toBeNull();

    fireEvent.change(providerSelect, { target: { value: "openai" } });
    await screen.findByText("2 models available");
    expect(screen.getByText(/restarts the run's pod/i)).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "claude-sonnet-4-5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save & restart" }));
    expect(onUpdate).toHaveBeenCalledWith({
      provider: "openai",
      model: "claude-sonnet-4-5",
      reasoningLevel: "",
    });
  });

  it("switches live between OAuth providers whose material is mounted on the run", async () => {
    const run = makeRun({
      authMode: "oauth",
      claudeApiKeySecret: "",
      openaiOauthSecret: "usercred-anthropic",
      providerOauthSecrets: [
        { provider: "openai", secretName: "usercred-openai" },
        { provider: "anthropic", secretName: "usercred-anthropic" },
      ],
    });
    render(<RunModelSwitcher run={run} canUpdate updating={false} onUpdate={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: "Switch provider or model" }));
    await screen.findByText("2 models available");

    const providerSelect = screen.getByLabelText("Provider") as HTMLSelectElement;
    // The target's OAuth material is mounted on the pod: live switch.
    fireEvent.change(providerSelect, { target: { value: "openai" } });
    await screen.findByText("2 models available");
    expect(screen.queryByText(/restarts the run's pod/i)).toBeNull();
    expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();

    // No copilot OAuth material mounted: the switch restarts compute.
    fireEvent.change(providerSelect, { target: { value: "copilot" } });
    await screen.findByText("2 models available");
    expect(screen.getByText(/restarts the run's pod/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save & restart" })).toBeTruthy();
  });

  it("warns only for provider switches without an API key mounted on the run", async () => {
    const run = makeRun({
      providerKeys: [{ provider: "openai", secretName: "openai-key", secretKey: "api-key" }],
    });
    render(<RunModelSwitcher run={run} canUpdate updating={false} onUpdate={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: "Switch provider or model" }));
    await screen.findByText("2 models available");

    const providerSelect = screen.getByLabelText("Provider") as HTMLSelectElement;
    fireEvent.change(providerSelect, { target: { value: "openai" } });
    await screen.findByText("2 models available");
    // openai key is mounted on the run: the switch applies live, no restart.
    expect(screen.queryByText(/restarts the run's pod/i)).toBeNull();
    expect(screen.getByRole("button", { name: "Save" })).toBeTruthy();

    fireEvent.change(providerSelect, { target: { value: "gemini" } });
    await screen.findByText("2 models available");
    expect(screen.getByText(/restarts the run's pod/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Save & restart" })).toBeTruthy();
  });
});
