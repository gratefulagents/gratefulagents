import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { SecurityScanList } from "@/components/SecurityScanList";
import { SecurityScanSchema } from "@/rpc/platform/service_pb";

const { listSecurityScans } = vi.hoisted(() => ({
  listSecurityScans: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { listSecurityScans },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function scanFixture() {
  return create(SecurityScanSchema, {
    id: "11111111-1111-1111-1111-111111111111",
    namespace: "user-alice",
    scanName: "nightly",
    runName: "nightly-1",
    repository: "github.com/acme/payments",
    revision: "abc123",
    status: "completed",
    startedAt: timestampFromDate(new Date(Date.now() - 60 * 60 * 1000)),
    completedAt: timestampFromDate(new Date(Date.now() - 30 * 60 * 1000)),
    counts: {
      critical: 2,
      high: 1,
      actionable_critical: 1,
      actionable_high: 0,
      total: 3,
      actionable: 1,
    },
  });
}

describe("SecurityScanList", () => {
  it("renders scans with repository, status, severity badges, and detail link", async () => {
    listSecurityScans.mockResolvedValue({ scans: [scanFixture()] });

    render(
      <MemoryRouter>
        <SecurityScanList />
      </MemoryRouter>,
    );

    expect(await screen.findByText("github.com/acme/payments")).toBeTruthy();
    expect(screen.getByText("completed")).toBeTruthy();
    expect(screen.getByText("critical")).toBeTruthy();
    expect(screen.getByText("1")).toBeTruthy();
    expect(screen.queryByText("high")).toBeNull();
    expect(screen.queryByText("medium")).toBeNull();
    expect(
      screen.getByRole("link", { name: "nightly-1" }).getAttribute("href"),
    ).toBe("/security/user-alice/nightly-1");
    expect(
      screen.getByRole("link", { name: "View agent run nightly-1" }).getAttribute("href"),
    ).toBe("/runs/user-alice/nightly-1");
    expect(listSecurityScans).toHaveBeenCalledWith({ namespace: "" });
  });

  it("shows the empty state when there are no scans", async () => {
    listSecurityScans.mockResolvedValue({ scans: [] });

    render(
      <MemoryRouter>
        <SecurityScanList />
      </MemoryRouter>,
    );

    expect(await screen.findByText("No security scans found")).toBeTruthy();
  });

  it("shows the error state when loading fails", async () => {
    listSecurityScans.mockRejectedValue(new Error("store offline"));

    render(
      <MemoryRouter>
        <SecurityScanList />
      </MemoryRouter>,
    );

    expect(await screen.findByText("store offline")).toBeTruthy();
  });
});
