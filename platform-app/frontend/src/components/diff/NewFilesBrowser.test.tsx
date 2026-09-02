import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { client } from "@/lib/client";
import { NewFilesBrowser } from "./NewFilesBrowser";

vi.mock("@/lib/client", () => ({
  client: {
    readFile: vi.fn(),
  },
}));

vi.mock("@/components/ui/toaster", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const readFile = vi.mocked(client.readFile);

describe("NewFilesBrowser", () => {
  beforeEach(() => {
    readFile.mockReset();
  });

  afterEach(cleanup);

  it("renders the tracked pane by default and passes children through without new files", () => {
    const { unmount } = render(
      <NewFilesBrowser namespace="demo" name="run-1" files={[]}>
        <div>tracked diff</div>
      </NewFilesBrowser>,
    );
    expect(screen.getByText("tracked diff")).toBeTruthy();
    expect(screen.queryByRole("tablist")).toBeNull();
    unmount();

    render(
      <NewFilesBrowser namespace="demo" name="run-1" files={["src/new.ts"]}>
        <div>tracked diff</div>
      </NewFilesBrowser>,
    );
    expect(screen.getByText("tracked diff")).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Tracked changes" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("tab", { name: /New files/ }).textContent).toContain("1");
  });

  it("shows paths without fetching contents", () => {
    render(
      <NewFilesBrowser namespace="demo" name="run-1" files={["src/new.ts", "docs/new.md"]} filesTruncated>
        <div>tracked diff</div>
      </NewFilesBrowser>,
    );

    fireEvent.click(screen.getByRole("tab", { name: /New files/ }));

    expect(screen.getByText("src/new.ts")).toBeTruthy();
    expect(screen.getByText("docs/new.md")).toBeTruthy();
    expect(screen.getByText("File list truncated.")).toBeTruthy();
    expect(screen.getByRole("tab", { name: /New files/ }).textContent).toContain("2+");
    // The tracked pane stays mounted (hidden) so its scroll state survives.
    expect(screen.getByText("tracked diff")).toBeTruthy();
    expect(readFile).not.toHaveBeenCalled();
  });

  it("switches modes with arrow keys on the tablist", () => {
    render(
      <NewFilesBrowser namespace="demo" name="run-1" files={["src/new.ts"]}>
        <div>tracked diff</div>
      </NewFilesBrowser>,
    );
    const tracked = screen.getByRole("tab", { name: "Tracked changes" });
    fireEvent.keyDown(tracked, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: /New files/ }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("list", { name: "New files" })).toBeTruthy();

    fireEvent.keyDown(screen.getByRole("tab", { name: /New files/ }), { key: "ArrowLeft" });
    expect(tracked.getAttribute("aria-selected")).toBe("true");
    expect(screen.queryByRole("list", { name: "New files" })).toBeNull();
  });

  it("loads only the selected file and caches its content", async () => {
    readFile.mockResolvedValue({ content: "export const value = 1;", truncated: false, $typeName: "platform.v1.ReadFileResponse" });
    render(
      <NewFilesBrowser
        namespace="demo"
        name="run-1"
        repoPath="/workspace/repo/repos/sdk"
        files={["src/new.ts", "docs/new.md"]}
      >
        <div>tracked diff</div>
      </NewFilesBrowser>,
    );

    fireEvent.click(screen.getByRole("tab", { name: /New files/ }));
    fireEvent.click(screen.getByRole("button", { name: /src\/new.ts/ }));

    await waitFor(() => expect(document.querySelector("pre")?.textContent).toContain("export const value = 1;"));
    expect(document.querySelector(".hljs-keyword")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy file contents" })).toBeTruthy();
    expect(readFile).toHaveBeenCalledTimes(1);
    expect(readFile).toHaveBeenCalledWith({
      namespace: "demo",
      name: "run-1",
      resourceType: "AgentRun",
      repoPath: "/workspace/repo/repos/sdk",
      path: "src/new.ts",
      maxLines: 1000,
    });

    fireEvent.click(screen.getByRole("button", { name: "Back" }));
    expect(screen.getByRole("tab", { name: "Tracked changes" }).getAttribute("aria-selected")).toBe("true");
    fireEvent.click(screen.getByRole("tab", { name: /New files/ }));
    fireEvent.click(screen.getByRole("button", { name: /src\/new.ts/ }));
    expect(readFile).toHaveBeenCalledTimes(1);
  });

  it("reports load errors for the selected file", async () => {
    readFile.mockRejectedValue(new Error("nope"));
    render(
      <NewFilesBrowser namespace="demo" name="run-1" files={["src/new.ts"]}>
        <div>tracked diff</div>
      </NewFilesBrowser>,
    );
    fireEvent.click(screen.getByRole("tab", { name: /New files/ }));
    fireEvent.click(screen.getByRole("button", { name: /src\/new.ts/ }));
    await waitFor(() => expect(screen.getByRole("alert").textContent).toContain("nope"));
  });
});
