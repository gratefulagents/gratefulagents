import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { SkillPicker } from "@/components/SkillPicker";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: { listSkills: vi.fn() },
}));

const listSkills = vi.mocked(client.listSkills);

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("SkillPicker", () => {
  it("toggles skills and flags selected skills that no longer exist", async () => {
    listSkills.mockResolvedValue({
      skills: [
        { name: "pdf", version: "1.0.0", description: "Work with PDFs", mcpServerRefs: ["fetch"], phase: "Ready" },
      ],
    } as never);
    const onChange = vi.fn();
    render(<SkillPicker selected={["retired"]} onChange={onChange} />);

    const attach = await screen.findByRole("switch", { name: "Attach pdf" });
    expect(screen.getByText("brings: fetch")).toBeTruthy();
    expect(screen.queryByText("Ready")).toBeNull();
    fireEvent.click(attach);
    expect(onChange).toHaveBeenCalledWith(["retired", "pdf"]);

    expect(screen.getByText("not found in your namespace")).toBeTruthy();
    fireEvent.click(screen.getByRole("switch", { name: "Detach retired" }));
    expect(onChange).toHaveBeenLastCalledWith([]);
  });

  it("shows the phase and error for skills that failed to resolve", async () => {
    listSkills.mockResolvedValue({
      skills: [
        {
          name: "flaky", version: "", description: "Upstream moved", mcpServerRefs: [],
          phase: "Error", statusMessage: "fetching SKILL.md: 404 Not Found",
        },
        { name: "draft", version: "", description: "", resolvedDescription: "From frontmatter", mcpServerRefs: [], phase: "Invalid", statusMessage: "" },
      ],
    } as never);
    render(<SkillPicker selected={[]} onChange={vi.fn()} />);

    expect(await screen.findByText("Error")).toBeTruthy();
    expect(screen.getByText("fetching SKILL.md: 404 Not Found")).toBeTruthy();
    expect(screen.getByText("Invalid")).toBeTruthy();
    expect(screen.getByText("From frontmatter")).toBeTruthy();
  });

  it("explains an empty namespace", async () => {
    listSkills.mockResolvedValue({ skills: [] } as never);
    render(<SkillPicker selected={[]} onChange={vi.fn()} />);
    expect(await screen.findByText(/No skills in your namespace/)).toBeTruthy();
  });
});
