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
    expect(listRepositories).not.toHaveBeenCalled();
  });
});
