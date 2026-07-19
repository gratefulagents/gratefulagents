import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { RoleModelsSection } from "@/components/RoleModelsSection";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listRoleInstructions: vi.fn(),
    getMyRoleModelPreferences: vi.fn(),
    updateMyRoleModelPreferences: vi.fn(),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const listRoles = vi.mocked(client.listRoleInstructions);
const getPreferences = vi.mocked(client.getMyRoleModelPreferences);
const updatePreferences = vi.mocked(client.updateMyRoleModelPreferences);

function mockLoad() {
  listRoles.mockResolvedValue({
    instructions: [
      {
        name: "explore",
        description: "Fast codebase search",
        model: "",
        modelsByProvider: {
          openai: "gpt-5.6-terra",
          anthropic: "claude-haiku-4-5",
          copilot: "gpt-5.6-terra",
        },
      },
    ],
  } as never);
  getPreferences.mockResolvedValue({
    preferences: [
      { roleName: "explore", provider: "openai", model: "gpt-5.4-mini" },
    ],
  } as never);
}

describe("RoleModelsSection", () => {
  it("loads personal values and shows platform defaults", async () => {
    mockLoad();

    render(<RoleModelsSection />);

    const openAI = (await screen.findByLabelText("OpenAI")) as HTMLInputElement;
    expect(openAI.value).toBe("gpt-5.4-mini");
    expect(openAI.placeholder).toBe("gpt-5.6-terra");
    expect(screen.getByText("Platform: claude-haiku-4-5")).toBeTruthy();
  });

  it("ignores the legacy generic model when a provider is not mapped", async () => {
    listRoles.mockResolvedValue({
      instructions: [{
        name: "explore",
        model: "legacy-generic-default",
        modelsByProvider: { openai: "gpt-5.6-terra" },
      }],
    } as never);
    getPreferences.mockResolvedValue({ preferences: [] } as never);

    render(<RoleModelsSection />);

    const anthropic = (await screen.findByLabelText("Anthropic")) as HTMLInputElement;
    expect(anthropic.placeholder).toBe("Parent model");
    expect(screen.getAllByText("Platform: inherits parent model").length).toBeGreaterThan(0);
  });

  it("saves all nonblank overrides and clears blank entries", async () => {
    mockLoad();
    updatePreferences.mockResolvedValue({
      preferences: [
        { roleName: "explore", provider: "anthropic", model: "claude-sonnet-4-6" },
      ],
    } as never);

    render(<RoleModelsSection />);

    fireEvent.change(await screen.findByLabelText("OpenAI"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Anthropic"), {
      target: { value: "  claude-sonnet-4-6  " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save role models" }));

    await waitFor(() => {
      expect(updatePreferences).toHaveBeenCalledWith({
        preferences: [
          { roleName: "explore", provider: "anthropic", model: "claude-sonnet-4-6" },
        ],
      });
    });
  });
});
