import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { client } from "@/lib/client";
import { AgentRunSchema } from "@/rpc/platform/service_pb";
import { RunContextContent } from "./RunContextSheet";

vi.mock("@/lib/client", () => ({
  client: {
    listRepositories: vi.fn(),
    cloneRepository: vi.fn(),
  },
}));

vi.mock("@/components/ui/toaster", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const listRepositories = client.listRepositories as unknown as ReturnType<typeof vi.fn>;

const run = create(AgentRunSchema, {
  namespace: "demo",
  name: "run-ui-polish",
  phase: "Running",
  modeInstructions: "Review the motion checklist before changing the interface.",
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("RunContextContent", () => {
  it("renders mode instructions inline for the inspector's Context tab", () => {
    listRepositories.mockResolvedValue({ repositories: [] });
    render(
      <RunContextContent
        namespace="demo"
        name="run-ui-polish"
        run={run}
        showRepositories={false}
        canClone={false}
        sandboxReady
        startupMessage=""
      />,
    );

    expect(screen.queryByText("Run context")).toBeNull();
    expect(screen.getByText(run.modeInstructions)).toBeTruthy();
    expect(screen.queryByRole("region", { name: "Skills" })).toBeNull();
    expect(listRepositories).not.toHaveBeenCalled();
  });

  it("lists the run's skills and marks the ones loaded into context", () => {
    listRepositories.mockResolvedValue({ repositories: [] });
    const withSkills = create(AgentRunSchema, {
      ...run,
      modeInstructions: "",
      skillRefs: ["pdf", "astro"],
      resolvedSkills: ["astro"],
    });
    render(
      <RunContextContent
        namespace="demo"
        name="run-ui-polish"
        run={withSkills}
        showRepositories={false}
        canClone={false}
        sandboxReady
        startupMessage=""
      />,
    );

    const skills = screen.getByRole("region", { name: "Skills" });
    expect(skills.textContent).toContain("1 of 2 loaded");
    expect(screen.getByText("pdf")).toBeTruthy();
    expect(screen.getByText("astro").parentElement?.textContent).toContain("loaded");
    expect(screen.getByText("pdf").parentElement?.textContent).not.toContain("loaded");
    expect(screen.queryByText("No additional run context is available.")).toBeNull();
  });
});
