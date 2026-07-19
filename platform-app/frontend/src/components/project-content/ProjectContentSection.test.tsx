import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, waitFor } from "@testing-library/react";

const clientMock = vi.hoisted(() => ({
  listProjectContent: vi.fn().mockResolvedValue({ items: [] }),
  duplicateProjectContent: vi.fn(),
  listProjectContentVersions: vi.fn(),
  restoreProjectContentVersion: vi.fn(),
  deleteProjectContent: vi.fn(),
}));
const binaryClientMock = vi.hoisted(() => ({
  createProjectContent: vi.fn().mockResolvedValue({}),
  getProjectContent: vi.fn(),
  updateProjectContent: vi.fn(),
}));

vi.mock("@/lib/client", () => ({ client: clientMock, binaryClient: binaryClientMock }));

import { ProjectContentSection } from "./ProjectContentSection";

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  clientMock.listProjectContent.mockResolvedValue({ items: [] });
  binaryClientMock.createProjectContent.mockResolvedValue({});
});

function contentFile(name: string, path: string): File {
  const file = new File([name], name, { type: "text/plain" });
  Object.defineProperty(file, "webkitRelativePath", { value: path });
  Object.defineProperty(file, "arrayBuffer", { value: vi.fn().mockResolvedValue(new TextEncoder().encode(name).buffer) });
  return file;
}

describe("ProjectContentSection", () => {
  it("uploads selected files sequentially using their relative folder paths", async () => {
    const { container } = render(<ProjectContentSection namespace="team" projectName="briefs" canEdit />);
    await waitFor(() => expect(clientMock.listProjectContent).toHaveBeenCalledWith({
      namespace: "team",
      projectName: "briefs",
      includeDeleted: false,
    }));

    const input = container.querySelector("input[type=file]");
    expect(input).toBeTruthy();
    fireEvent.change(input!, {
      target: { files: [contentFile("one.txt", "research/one.txt"), contentFile("two.txt", "research/two.txt")] },
    });

    await waitFor(() => expect(binaryClientMock.createProjectContent).toHaveBeenCalledTimes(2));
    expect(binaryClientMock.createProjectContent).toHaveBeenNthCalledWith(1, expect.objectContaining({
      namespace: "team",
      projectName: "briefs",
      kind: "file",
      path: "research/one.txt",
    }));
    expect(binaryClientMock.createProjectContent).toHaveBeenNthCalledWith(2, expect.objectContaining({
      path: "research/two.txt",
    }));
  });
});
