import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { BranchPicker } from "./BranchPicker";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({ client: { listGitHubBranches: vi.fn() } }));

afterEach(() => {
  cleanup();
  vi.resetAllMocks();
});

function Picker({ repoUrl = "https://github.com/acme/repo", namespace = "team" }) {
  const [value, setValue] = useState("main");
  return <><label htmlFor="branch">Base branch</label><BranchPicker id="branch" repoUrl={repoUrl} namespace={namespace} value={value} onChange={setValue} /></>;
}

function options() {
  const input = screen.getByLabelText("Base branch") as HTMLInputElement;
  return Array.from(input.list?.options ?? []).map((option) => option.value);
}

describe("BranchPicker", () => {
  it("loads all pages on focus and allows suggested, arbitrary and empty values", async () => {
    vi.mocked(client.listGitHubBranches)
      .mockResolvedValueOnce({ branches: ["main", "develop"], nextPage: 2 } as never)
      .mockResolvedValueOnce({ branches: ["develop", "release/next"], nextPage: 0 } as never);
    render(<Picker />);
    const input = screen.getByLabelText("Base branch") as HTMLInputElement;
    expect(client.listGitHubBranches).not.toHaveBeenCalled();
    fireEvent.focus(input);
    expect(screen.getByRole("status").textContent).toContain("Loading branches");
    await waitFor(() => expect(options()).toEqual(["main", "develop", "release/next"]));
    expect(client.listGitHubBranches).toHaveBeenNthCalledWith(2,
      { repoUrl: "https://github.com/acme/repo", namespace: "team", page: 2 },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    for (const value of ["release/next", "my/new-branch", ""]) {
      fireEvent.change(input, { target: { value } });
      expect(input.value).toBe(value);
    }
    expect(client.listGitHubBranches).toHaveBeenCalledTimes(2);
  });

  it("clears old suggestions on repository and namespace changes without changing the value", async () => {
    vi.mocked(client.listGitHubBranches).mockResolvedValue({ branches: ["old"], nextPage: 0 } as never);
    const { rerender } = render(<Picker />);
    fireEvent.focus(screen.getByLabelText("Base branch"));
    await waitFor(() => expect(options()).toEqual(["old"]));
    vi.mocked(client.listGitHubBranches).mockResolvedValue({ branches: ["new"], nextPage: 0 } as never);
    rerender(<Picker repoUrl="https://github.com/acme/other" />);
    expect(options()).toEqual([]);
    expect((screen.getByLabelText("Base branch") as HTMLInputElement).value).toBe("main");
    await waitFor(() => expect(options()).toEqual(["new"]));
    rerender(<Picker repoUrl="https://github.com/acme/other" namespace="other-team" />);
    expect(options()).toEqual([]);
    await waitFor(() => expect(client.listGitHubBranches).toHaveBeenLastCalledWith(
      { repoUrl: "https://github.com/acme/other", namespace: "other-team", page: 0 }, expect.anything(),
    ));
  });

  it("aborts and ignores a stale response after the repository changes", async () => {
    let resolveOld!: (value: never) => void;
    vi.mocked(client.listGitHubBranches)
      .mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve; }))
      .mockResolvedValueOnce({ branches: ["new"], nextPage: 0 } as never);
    const { rerender, unmount } = render(<Picker />);
    fireEvent.focus(screen.getByLabelText("Base branch"));
    await waitFor(() => expect(client.listGitHubBranches).toHaveBeenCalledTimes(1));
    const signal = vi.mocked(client.listGitHubBranches).mock.calls[0][1]?.signal;
    rerender(<Picker repoUrl="https://github.com/acme/other" />);
    expect(signal?.aborted).toBe(true);
    await waitFor(() => expect(options()).toEqual(["new"]));
    await act(async () => resolveOld({ branches: ["stale"], nextPage: 2 } as never));
    expect(options()).toEqual(["new"]);
    expect(client.listGitHubBranches).toHaveBeenCalledTimes(2);
    const newSignal = vi.mocked(client.listGitHubBranches).mock.calls[1][1]?.signal;
    unmount();
    expect(newSignal?.aborted).toBe(true);
  });

  it("keeps free-form entry usable after errors and recovers on repository change", async () => {
    vi.mocked(client.listGitHubBranches).mockRejectedValueOnce(new Error("private or unavailable"));
    const { rerender } = render(<Picker />);
    const input = screen.getByLabelText("Base branch") as HTMLInputElement;
    fireEvent.focus(input);
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("Could not load branches"));
    fireEvent.change(input, { target: { value: "free/form" } });
    expect(input.value).toBe("free/form");
    vi.mocked(client.listGitHubBranches).mockResolvedValueOnce({ branches: [], nextPage: 0 } as never);
    rerender(<Picker repoUrl="https://github.com/acme/empty" />);
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("Choose a GitHub branch or type any branch name."));
    expect(input.value).toBe("free/form");
    expect(options()).toEqual([]);
  });

  it("does not fetch empty or non-GitHub repositories and cancels pending loads", async () => {
    const { rerender } = render(<Picker />);
    fireEvent.focus(screen.getByLabelText("Base branch"));
    rerender(<Picker repoUrl="" />);
    rerender(<Picker repoUrl="https://gitlab.com/acme/repo" />);
    fireEvent.change(screen.getByLabelText("Base branch"), { target: { value: "custom" } });
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 300)); });
    expect(client.listGitHubBranches).not.toHaveBeenCalled();
    expect((screen.getByLabelText("Base branch") as HTMLInputElement).value).toBe("custom");
  });
});
