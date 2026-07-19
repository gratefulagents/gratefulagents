import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ShareDialog } from "@/components/ShareDialog";
import { client } from "@/lib/client";

vi.mock("@bufbuild/protobuf", () => ({
  create: (_schema: unknown, value: unknown) => value,
}));

vi.mock("@/lib/client", () => ({
  client: {
    listShares: vi.fn().mockResolvedValue({ shares: [] }),
    shareResource: vi.fn().mockResolvedValue({}),
    revokeShare: vi.fn(),
    updateSharePermission: vi.fn(),
  },
}));

vi.mock("@/lib/auth-client", () => ({
  getAuthClient: () => null,
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function renderDialog() {
  render(
    <ShareDialog
      resourceType="agent_run"
      resourceId="run-1"
      resourceNamespace="default"
      open
      onOpenChange={vi.fn()}
    />,
  );
}

describe("ShareDialog", () => {
  it("renders permission choices", async () => {
    renderDialog();

    expect(screen.getByText("Share Run")).toBeTruthy();
    fireEvent.click(screen.getByRole("combobox", { name: "Role" }));

    expect(await screen.findByText("Collaborator")).toBeTruthy();
  });

  it("shares a resource", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText("Email address to share with"), {
      target: { value: "teammate@example.com" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Share" }));

    await waitFor(() => {
      expect(client.shareResource).toHaveBeenCalledWith(
        expect.objectContaining({
          resourceType: "agent_run",
          resourceId: "run-1",
          resourceNamespace: "default",
          sharedWithEmail: "teammate@example.com",
          permission: "viewer",
        }),
      );
    });
  });
});
