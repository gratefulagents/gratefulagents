import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { RepositoriesPanel } from "./RepositoriesPanel";
import { client } from "@/lib/client";

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
const cloneRepository = client.cloneRepository as unknown as ReturnType<typeof vi.fn>;

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("RepositoriesPanel", () => {
  it("lists repositories with the primary repo marked", async () => {
    listRepositories.mockResolvedValue({
      repositories: [
        { name: "repo", path: "/workspace/repo", remoteUrl: "https://github.com/o/primary.git", branch: "run-1", isPrimary: true },
        { name: "widgets", path: "/workspace/widgets", remoteUrl: "https://github.com/o/widgets.git", branch: "main", isPrimary: false },
      ],
    });

    render(<RepositoriesPanel namespace="ns" name="run" canClone />);

    expect(await screen.findByText("repo")).toBeTruthy();
    expect(screen.getByText("widgets")).toBeTruthy();
    expect(screen.getByText("primary")).toBeTruthy();
  });

  it("hides the clone button for viewers", async () => {
    listRepositories.mockResolvedValue({ repositories: [] });
    render(<RepositoriesPanel namespace="ns" name="run" canClone={false} />);
    await waitFor(() => expect(listRepositories).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: /clone repo/i })).toBeNull();
  });

  it("shows startup copy without listing while the sandbox is not ready", () => {
    render(
      <RepositoriesPanel
        namespace="ns"
        name="run"
        canClone
        sandboxReady={false}
        startupMessage="Sandbox pod is starting… repositories will appear once it is ready."
      />,
    );

    expect(screen.getByRole("status").textContent).toContain("Sandbox pod is starting");
    expect((screen.getByRole("button", { name: /refresh repositories/i }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: /clone repo/i }) as HTMLButtonElement).disabled).toBe(true);
    expect(listRepositories).not.toHaveBeenCalled();
  });

  it("clones a repository and refreshes the list", async () => {
    listRepositories.mockResolvedValue({ repositories: [] });
    cloneRepository.mockResolvedValue({ repository: { name: "widgets", path: "/workspace/widgets" } });

    render(<RepositoriesPanel namespace="ns" name="run" canClone />);
    await waitFor(() => expect(listRepositories).toHaveBeenCalledTimes(1));

    fireEvent.click(screen.getByRole("button", { name: /clone repo/i }));

    const input = await screen.findByLabelText("Repository URL");
    fireEvent.change(input, { target: { value: "https://github.com/o/widgets.git" } });
    fireEvent.click(screen.getByRole("button", { name: /^clone$/i }));

    await waitFor(() =>
      expect(cloneRepository).toHaveBeenCalledWith({
        namespace: "ns",
        name: "run",
        resourceType: "AgentRun",
        repoUrl: "https://github.com/o/widgets.git",
        baseBranch: "",
      }),
    );
    // The list refreshes after a successful clone.
    await waitFor(() => expect(listRepositories).toHaveBeenCalledTimes(2));
  });
});
