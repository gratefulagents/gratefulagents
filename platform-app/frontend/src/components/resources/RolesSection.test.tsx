import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { RolesSection } from "@/components/resources/RolesSection";
import { client } from "@/lib/client";

let role = "member";

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u1", role } }),
}));

vi.mock("@/lib/client", () => ({
  client: {
    listRoleInstructions: vi.fn().mockResolvedValue({
      instructions: [{
        name: "executor",
        description: "Ships approved plans",
        instructions: "<identity>You implement the plan.</identity>",
        toolAccess: "execution",
        model: "",
        modelsByProvider: { anthropic: "claude-opus-5-5" },
        reasoningLevel: "high",
      }, {
        name: "general",
        description: "Team general agent",
        instructions: "Follow the team runbook.",
        toolAccess: "full",
        model: "",
        modelsByProvider: {},
        reasoningLevel: "",
      }],
    }),
    createRoleInstruction: vi.fn().mockResolvedValue({}),
    updateRoleInstruction: vi.fn().mockResolvedValue({}),
    deleteRoleInstruction: vi.fn().mockResolvedValue({}),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  role = "member";
});

describe("RolesSection", () => {
  it("lets a member read a role without editing it", async () => {
    render(<RolesSection />);

    expect(await screen.findByText("Ships approved plans")).toBeTruthy();
    expect(screen.getByText("execution tools")).toBeTruthy();
    expect(screen.getByText("high reasoning")).toBeTruthy();
    expect(screen.getByText("anthropic=claude-opus-5-5")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "New role" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete executor" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit executor" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "View executor" }));
    const instructions = screen.getByLabelText(/^Instructions/) as HTMLTextAreaElement;
    expect(instructions.value).toBe("<identity>You implement the plan.</identity>");
    expect((instructions.closest("fieldset") as HTMLFieldSetElement).disabled).toBe(true);
    expect(screen.getAllByRole("button", { name: "Close" }).length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: /Save changes|Create role/ })).toBeNull();
  });

  it("lets an admin create a role with provider models", async () => {
    role = "admin";
    render(<RolesSection />);
    expect(await screen.findByText("Ships approved plans")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Edit executor" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Delete executor" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "New role" }));
    const create = screen.getByRole("button", { name: "Create role" });
    expect((create as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "reviewer" } });
    fireEvent.change(screen.getByLabelText(/^Instructions/), { target: { value: "Review every diff." } });

    fireEvent.click(screen.getByRole("button", { name: /Model routing/ }));
    fireEvent.click(screen.getByRole("button", { name: "+ anthropic" }));
    fireEvent.change(screen.getByLabelText("Model 1"), { target: { value: "claude-opus-5-5" } });

    expect((create as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(create);

    await waitFor(() => {
      expect(client.createRoleInstruction).toHaveBeenCalledWith({
        instruction: expect.objectContaining({
          name: "reviewer",
          instructions: "Review every diff.",
          toolAccess: "full",
          modelsByProvider: { anthropic: "claude-opus-5-5" },
          reasoningLevel: "",
        }),
      });
    });
  });

  it("explains what deleting a role does to runs", async () => {
    role = "admin";
    render(<RolesSection />);
    expect(await screen.findByText("Ships approved plans")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Delete executor" }));
    expect(screen.getByText("Runs stop offering this sub-agent. This cannot be undone.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    fireEvent.click(screen.getByRole("button", { name: "Delete general" }));
    expect(screen.getByText("Runs go back to the built-in general sub-agent. This cannot be undone.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Delete role" }));
    await waitFor(() => {
      expect(client.deleteRoleInstruction).toHaveBeenCalledWith({ name: "general" });
    });
  });

  it("blocks saving while a provider row has no name", async () => {
    role = "admin";
    render(<RolesSection />);
    expect(await screen.findByText("Ships approved plans")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "New role" }));
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "reviewer" } });
    fireEvent.change(screen.getByLabelText(/^Instructions/), { target: { value: "Review every diff." } });
    fireEvent.click(screen.getByRole("button", { name: /Model routing/ }));
    fireEvent.click(screen.getByRole("button", { name: "Add provider" }));
    fireEvent.change(screen.getByLabelText("Model 1"), { target: { value: "gpt-5.6" } });

    expect(screen.getByText(/Provider names cannot be empty/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Create role" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
