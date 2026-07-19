import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { client } from "@/lib/client";
import { AgentRunSchema } from "@/rpc/platform/service_pb";
import { RunContextSheet } from "./RunContextSheet";

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

describe("RunContextSheet", () => {
  it("keeps run context out of the document while closed", () => {
    render(
      <RunContextSheet
        open={false}
        onOpenChange={vi.fn()}
        namespace="demo"
        name="run-ui-polish"
        run={run}
        showRepositories
        canClone
        sandboxReady
        startupMessage="Preparing sandbox…"
      />,
    );

    expect(screen.queryByText("Run context")).toBeNull();
    expect(screen.queryByText(run.modeInstructions)).toBeNull();
    expect(listRepositories).not.toHaveBeenCalled();
  });

  it("shows mode instructions and repositories together on demand", async () => {
    listRepositories.mockResolvedValue({
      repositories: [
        {
          name: "operator-app",
          path: "/workspace/repo",
          remoteUrl: "https://github.com/acme/operator-app.git",
          branch: "chat-polish-run-header-3k2v",
          isPrimary: true,
        },
      ],
    });

    render(
      <RunContextSheet
        open
        onOpenChange={vi.fn()}
        namespace="demo"
        name="run-ui-polish"
        run={run}
        showRepositories
        canClone
        sandboxReady
        startupMessage="Preparing sandbox…"
      />,
    );

    expect(screen.getByRole("heading", { name: "Run context" })).toBeTruthy();
    expect(screen.getByText(run.modeInstructions)).toBeTruthy();
    expect(await screen.findByText("operator-app")).toBeTruthy();
    expect(screen.getByText("primary")).toBeTruthy();
  });
});
