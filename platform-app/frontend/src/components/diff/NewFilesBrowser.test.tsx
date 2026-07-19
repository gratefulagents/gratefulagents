import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { client } from "@/lib/client";
import { NewFilesBrowser } from "./NewFilesBrowser";

vi.mock("@/lib/client", () => ({
  client: {
    readFile: vi.fn(),
  },
}));

const readFile = vi.mocked(client.readFile);

describe("NewFilesBrowser", () => {
  beforeEach(() => {
    readFile.mockReset();
  });

  afterEach(cleanup);

  it("shows paths without fetching contents", () => {
    render(
      <NewFilesBrowser namespace="demo" name="run-1" files={["src/new.ts", "docs/new.md"]}>
        <div>tracked diff</div>
      </NewFilesBrowser>,
    );

    expect(screen.getByText("src/new.ts")).toBeTruthy();
    expect(screen.getByText("docs/new.md")).toBeTruthy();
    expect(screen.getByText("tracked diff")).toBeTruthy();
    expect(readFile).not.toHaveBeenCalled();
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

    fireEvent.click(screen.getByRole("button", { name: /src\/new.ts/ }));

    await waitFor(() => expect(screen.getByText("export const value = 1;")).toBeTruthy());
    expect(readFile).toHaveBeenCalledTimes(1);
    expect(readFile).toHaveBeenCalledWith({
      namespace: "demo",
      name: "run-1",
      resourceType: "AgentRun",
      repoPath: "/workspace/repo/repos/sdk",
      path: "src/new.ts",
      maxLines: 1000,
    });

    fireEvent.click(screen.getByRole("button", { name: "Back to diff" }));
    fireEvent.click(screen.getByRole("button", { name: /src\/new.ts/ }));
    expect(readFile).toHaveBeenCalledTimes(1);
  });
});
