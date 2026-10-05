import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { SkillsSection } from "@/components/resources/SkillsSection";
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
const listSkillCatalog = vi.mocked(client.listSkillCatalog);
const installSkillFromCatalog = vi.mocked(client.installSkillFromCatalog);

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
    expect(await screen.findByText("No skills yet")).toBeTruthy();

    fireEvent.click(screen.getAllByRole("button", { name: "New skill" })[0]);
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
          gitUrl: "",
        }),
      ),
    );
    expect(await screen.findByRole("button", { name: "test-skill" })).toBeTruthy();
    expect(screen.getByText("Checks results")).toBeTruthy();
    expect(listSkills).toHaveBeenCalledTimes(1);
  });

  it("creates a git-sourced skill with blank instructions", async () => {
    listSkills.mockResolvedValue({ skills: [] } as never);
    listMCPServers.mockResolvedValue({ servers: [] } as never);
    upsertSkill.mockResolvedValue({ name: "pdf", gitUrl: "https://github.com/anthropics/skills/tree/main/document-skills/pdf", phase: "Pending" } as never);

    render(<SkillsSection />);
    fireEvent.click(await screen.findByRole("button", { name: "New skill" }));
    fireEvent.change(screen.getByPlaceholderText("my-skill"), { target: { value: "pdf" } });
    fireEvent.click(screen.getByRole("button", { name: "From a Git repository" }));
    expect(screen.queryByPlaceholderText("Query discipline, safety rules, runbooks…")).toBeNull();
    fireEvent.change(screen.getByLabelText(/^Git link/), {
      target: { value: "https://github.com/anthropics/skills/tree/main/document-skills/pdf" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create skill" }));

    await waitFor(() =>
      expect(upsertSkill).toHaveBeenCalledWith(
        expect.objectContaining({
          name: "pdf",
          instructions: "",
          gitUrl: "https://github.com/anthropics/skills/tree/main/document-skills/pdf",
        }),
      ),
    );
    expect(await screen.findByText("Git")).toBeTruthy();
  });

  it("renders installed skills with provenance and hides delete for nothing", async () => {
    listSkills.mockResolvedValue({
      skills: [
        {
          name: "astro",
          description: "Skill for building with Astro.",
          gitUrl: "https://github.com/astrolicious/agent-skills",
          phase: "Ready",
          catalogSource: "astrolicious/agent-skills",
          catalogSkillId: "astro",
          catalogUrl: "https://skills.sh/astrolicious/agent-skills/astro",
          mcpServerRefs: ["grafana"],
        },
      ],
    } as never);
    listMCPServers.mockResolvedValue({ servers: [] } as never);

    render(<SkillsSection />);
    expect(await screen.findByRole("button", { name: "astro" })).toBeTruthy();
    expect(screen.getByText("Ready")).toBeTruthy();
    expect(screen.getByText("skills.sh")).toBeTruthy();
    expect(screen.getByText("needs: grafana")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Edit astro" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Delete astro" })).toBeTruthy();
  });

  it("lists the skills.sh catalog and installs an entry", async () => {
    listSkills.mockResolvedValue({ skills: [] } as never);
    listMCPServers.mockResolvedValue({ servers: [] } as never);
    listSkillCatalog.mockResolvedValue({
      skills: [
        { source: "anthropics/skills", skillId: "pdf", name: "pdf", installs: 1200n, isOfficial: true, catalogUrl: "https://skills.sh/anthropics/skills/pdf" },
        { source: "acme/skills", skillId: "deploy", name: "deploy", installs: 3n, isOfficial: false, catalogUrl: "https://skills.sh/acme/skills/deploy" },
      ],
      total: 2n,
      hasMore: false,
      page: 0,
    } as never);
    installSkillFromCatalog.mockResolvedValue({} as never);

    render(<SkillsSection />);
    fireEvent.click((await screen.findAllByRole("button", { name: "Browse skills.sh" }))[0]);

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Install from skills.sh")).toBeTruthy();
    expect(await within(dialog).findByText("pdf")).toBeTruthy();
    expect(within(dialog).getByText("Official")).toBeTruthy();
    expect(within(dialog).getByText("1,200 installs")).toBeTruthy();
    expect(within(dialog).getByRole("link", { name: "Open pdf on skills.sh" })).toBeTruthy();

    listSkills.mockResolvedValue({
      skills: [{ name: "pdf", catalogSource: "anthropics/skills", catalogSkillId: "pdf", phase: "Ready" }],
    } as never);
    fireEvent.click(within(dialog).getAllByRole("button", { name: "Install" })[0]);

    await waitFor(() => expect(installSkillFromCatalog).toHaveBeenCalledWith({ source: "anthropics/skills", skillId: "pdf" }));
    await waitFor(() => expect(listSkills).toHaveBeenCalledTimes(2));
    // The dialog stays open and marks the entry as installed.
    expect(await within(dialog).findByRole("button", { name: "Installed" })).toBeTruthy();
  });
});
