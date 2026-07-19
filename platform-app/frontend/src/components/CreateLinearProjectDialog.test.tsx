import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { CreateLinearProjectDialog } from "@/components/CreateLinearProjectDialog";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: vi.fn().mockResolvedValue({ namespace: "user-alice" }),
    listAvailableModels: vi.fn().mockImplementation(
      ({ provider, authMode }: { provider: string; authMode: string }) =>
        Promise.resolve({ models: [`${provider}-${authMode}-large`, `${provider}-${authMode}-small`] }),
    ),
    createLinearProject: vi.fn(),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("CreateLinearProjectDialog model picker", () => {
  it("loads models from saved credentials and refreshes for provider and auth changes", async () => {
    render(
      <MemoryRouter>
        <CreateLinearProjectDialog />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Create project" }));

    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "user-alice", provider: "anthropic", authMode: "api-key" },
        expect.anything(),
      );
    });
    expect(await screen.findByText("2 models available")).toBeTruthy();

    const modelPicker = screen.getByLabelText("Model *") as HTMLSelectElement;
    expect(Array.from(modelPicker.options).map((option) => option.value)).toContain(
      "anthropic-api-key-large",
    );

    fireEvent.change(screen.getByLabelText("Provider"), { target: { value: "openai" } });
    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "user-alice", provider: "openai", authMode: "api-key" },
        expect.anything(),
      );
    });

    fireEvent.change(screen.getByLabelText("Authentication mode"), { target: { value: "oauth" } });
    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "user-alice", provider: "openai", authMode: "oauth" },
        expect.anything(),
      );
    });
    expect(await screen.findByText("2 models available")).toBeTruthy();
    expect(Array.from((screen.getByLabelText("Model *") as HTMLSelectElement).options).map((option) => option.value)).toContain(
      "openai-oauth-large",
    );
  });
});
