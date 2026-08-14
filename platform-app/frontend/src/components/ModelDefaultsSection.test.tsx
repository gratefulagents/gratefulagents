import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ModelDefaultsSection } from "@/components/ModelDefaultsSection";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    getMyModelDefaults: vi.fn(),
    updateMyModelDefaults: vi.fn(),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const getDefaults = vi.mocked(client.getMyModelDefaults);
const updateDefaults = vi.mocked(client.updateMyModelDefaults);

describe("ModelDefaultsSection", () => {
  it("loads saved defaults into the fields", async () => {
    getDefaults.mockResolvedValue({
      provider: "openai",
      model: "gpt-5",
      reasoningLevel: "high",
      disabled: false,
      updatedAt: { seconds: BigInt(1_700_000_000), nanos: 0 },
    } as never);

    render(<ModelDefaultsSection />);

    await waitFor(() => {
      expect((screen.getByLabelText("Provider") as HTMLSelectElement).value).toBe("openai");
    });
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("gpt-5");
    expect((screen.getByLabelText("Reasoning level") as HTMLSelectElement).value).toBe("high");
    expect(screen.getByText(/Last saved/)).toBeTruthy();
  });

  it("shows the never-saved hint when updatedAt is unset", async () => {
    getDefaults.mockResolvedValue({
      provider: "",
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
      model: "",
      reasoningLevel: "",
      disabled: false,
    } as never);
    updateDefaults.mockResolvedValue({
      provider: "openai",
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
      model: "gpt-5",
      reasoningLevel: "high",
      disabled: false,
      updatedAt: { seconds: BigInt(1_700_000_000), nanos: 0 },
    } as never);
    updateDefaults.mockResolvedValue({
      provider: "",
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
});
