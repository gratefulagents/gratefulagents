import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { SkillsSection } from "@/components/SkillsSection";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    listSkills: vi.fn(),
    listMCPServers: vi.fn(),
    upsertSkill: vi.fn(),
    deleteSkill: vi.fn(),
    listSkillCatalog: vi.fn(),
    installSkillFromCatalog: vi.fn(),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const listSkills = vi.mocked(client.listSkills);
const listMCPServers = vi.mocked(client.listMCPServers);
const upsertSkill = vi.mocked(client.upsertSkill);

describe("SkillsSection", () => {
  it("shows a newly created inline skill without waiting for a second list request", async () => {
    listSkills.mockResolvedValue({ skills: [] } as never);
    listMCPServers.mockResolvedValue({ servers: [] } as never);
    upsertSkill.mockResolvedValue({
      name: "test-skill",
      description: "Checks results",
      instructions: "Always verify the result.",
      mcpServerRefs: [],
    } as never);

    render(<SkillsSection />);

    expect(screen.getByText(/Installing adds a skill to your library, not to every project/)).toBeTruthy();

    fireEvent.click(await screen.findByRole("button", { name: "New inline skill" }));
    fireEvent.change(screen.getByPlaceholderText("my-skill"), { target: { value: "test-skill" } });
    fireEvent.change(screen.getByPlaceholderText("What the skill teaches the agent — shown in pickers"), {
      target: { value: "Checks results" },
    });
    fireEvent.change(screen.getByPlaceholderText("Query discipline, safety rules, runbooks…"), {
      target: { value: "Always verify the result." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create skill" }));

    await waitFor(() =>
      expect(upsertSkill).toHaveBeenCalledWith(
        expect.objectContaining({
          name: "test-skill",
          description: "Checks results",
          instructions: "Always verify the result.",
        }),
      ),
    );
    expect(await screen.findByText("test-skill")).toBeTruthy();
    expect(screen.getByText("Checks results")).toBeTruthy();
    expect(listSkills).toHaveBeenCalledTimes(1);
  });
});
