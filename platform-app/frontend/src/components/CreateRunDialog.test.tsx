import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { CreateRunDialog } from "./CreateRunDialog";
import { client } from "@/lib/client";

const navigate = vi.fn();
vi.mock("react-router-dom", async (load) => {
  const actual = await load<typeof import("react-router-dom")>();
  return { ...actual, useNavigate: () => navigate };
});
vi.mock("@/hooks/useWatchedList", () => ({ useProjects: () => ({ projects: [{
  namespace: "team", name: "project", repoUrl: "https://github.com/acme/repo", additionalRepoUrls: [], baseBranch: "main", model: "claude-sonnet", provider: "anthropic", image: "", claudeApiKeySecret: "", githubTokenSecret: "",
}] }) }));
vi.mock("@/lib/client", () => ({ client: {
  listAvailableModels: vi.fn().mockResolvedValue({ models: ["claude-sonnet"] }),
  listMyCredentials: vi.fn().mockResolvedValue({ namespace: "team" }),
  createAgentRun: vi.fn().mockResolvedValue({ namespace: "team", name: "created" }),
} }));

afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe("CreateRunDialog overseer", () => {
  it("submits every overseer field, preserving an explicit zero cap", async () => {
    render(<MemoryRouter><CreateRunDialog defaultSource="project" defaultNamespace="team" /></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: /New Run/i }));
    fireEvent.click(await screen.findByRole("button", { name: /Overseer/ }));
    fireEvent.click(screen.getByLabelText("Enable overseer", { selector: "input" }));
    fireEvent.change(screen.getByLabelText("Mode name"), { target: { value: "review" } });
    fireEvent.change(screen.getByLabelText("Mode version"), { target: { value: "v3" } });
    fireEvent.change(screen.getByLabelText("Mode channel"), { target: { value: "stable" } });
    fireEvent.change(screen.getByLabelText("Model", { selector: "input#create-run-overseer-model" }), { target: { value: "opus" } });
    fireEvent.click(screen.getByRole("combobox", { name: "Authority" }));
    const enforceOption = await screen.findByRole("option", { name: "Enforce" });
    fireEvent.pointerDown(enforceOption, { pointerType: "mouse" });
    fireEvent.click(enforceOption);
    fireEvent.change(screen.getByLabelText("Interval (minutes)"), { target: { value: "30" } });
    fireEvent.change(screen.getByLabelText("Max interventions"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Start run" }));
    await waitFor(() => expect(client.createAgentRun).toHaveBeenCalledWith(expect.objectContaining({
      overseer: { modeRefName: "review", modeRefVersion: "v3", modeRefChannel: "stable", model: "opus", authority: "enforce", intervalMinutes: 30, maxInterventions: 0 },
    })));
  });
});
