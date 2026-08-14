import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { BugReportList } from "@/components/BugReportList";
import { BugReportSchema } from "@/rpc/platform/service_pb";

const { listBugReports, updateBugReportStatus } = vi.hoisted(() => ({
  listBugReports: vi.fn(),
  updateBugReportStatus: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { listBugReports, updateBugReportStatus },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function reportFixture() {
  return create(BugReportSchema, {
    id: "11111111-1111-1111-1111-111111111111",
    namespace: "user-alice",
    runName: "run-42",
    category: "bug",
    toolName: "task_create",
    title: "task_create times out on large descriptions",
    body: "Calling task_create with a 100KB description hangs for 30s and fails.",
    occurrences: 7,
    status: "open",
    firstSeenAt: timestampFromDate(new Date(Date.now() - 24 * 60 * 60 * 1000)),
    lastSeenAt: timestampFromDate(new Date(Date.now() - 10 * 60 * 1000)),
  });
}

describe("BugReportList", () => {
  it("renders reports with category, tool, occurrences, run link, and status", async () => {
    listBugReports.mockResolvedValue({ reports: [reportFixture()] });

    render(
      <MemoryRouter>
        <BugReportList />
      </MemoryRouter>,
    );

    expect(await screen.findByText("task_create times out on large descriptions")).toBeTruthy();
    // "bug" and "open" also appear as <option>s in the filter/status selects.
    expect(screen.getAllByText("bug").some((el) => el.tagName !== "OPTION")).toBe(true);
    expect(screen.getAllByText("open").some((el) => el.tagName !== "OPTION")).toBe(true);
    expect(screen.getByText("task_create")).toBeTruthy();
    expect(screen.getByText("7")).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "View agent run run-42" }).getAttribute("href"),
    ).toBe("/runs/user-alice/run-42");
    expect(listBugReports).toHaveBeenCalledWith({ namespace: "" });
  });

  it("expands a row to show the full body", async () => {
    listBugReports.mockResolvedValue({ reports: [reportFixture()] });

    render(
      <MemoryRouter>
        <BugReportList />
      </MemoryRouter>,
    );

    await screen.findByText("task_create times out on large descriptions");
    fireEvent.click(
      screen.getByRole("button", {
        name: "Toggle details for task_create times out on large descriptions",
      }),
    );
    expect(
      screen.getByText("Calling task_create with a 100KB description hangs for 30s and fails."),
    ).toBeTruthy();
  });

  it("updates a report status via the RPC and reflects the returned report", async () => {
    const report = reportFixture();
    listBugReports.mockResolvedValue({ reports: [report] });
    updateBugReportStatus.mockResolvedValue(
      create(BugReportSchema, { ...report, status: "resolved", statusActor: "alice" }),
    );

    render(
      <MemoryRouter>
        <BugReportList />
      </MemoryRouter>,
    );

    const select = await screen.findByLabelText(
      "Set status for task_create times out on large descriptions",
    );
    fireEvent.change(select, { target: { value: "resolved" } });

    await waitFor(() =>
      expect(updateBugReportStatus).toHaveBeenCalledWith({
        namespace: "user-alice",
        id: "11111111-1111-1111-1111-111111111111",
        status: "resolved",
        note: "",
      }),
    );
    await waitFor(() =>
      expect(screen.getAllByText("resolved").some((el) => el.tagName !== "OPTION")).toBe(true),
    );
    expect((select as HTMLSelectElement).value).toBe("resolved");
  });

  it("shows the empty state when there are no reports", async () => {
    listBugReports.mockResolvedValue({ reports: [] });

    render(
      <MemoryRouter>
        <BugReportList />
      </MemoryRouter>,
    );

    expect(await screen.findByText("No bug reports")).toBeTruthy();
  });

  it("shows a friendly message when the Postgres store is not configured", async () => {
    listBugReports.mockRejectedValue(
      new ConnectError("bug reports require postgres", Code.FailedPrecondition),
    );

    render(
      <MemoryRouter>
        <BugReportList />
      </MemoryRouter>,
    );

    expect(
      await screen.findByText(/require the Postgres state store/),
    ).toBeTruthy();
  });

  it("shows the error state when loading fails", async () => {
    listBugReports.mockRejectedValue(new Error("store offline"));

    render(
      <MemoryRouter>
        <BugReportList />
      </MemoryRouter>,
    );

    expect(await screen.findByText("store offline")).toBeTruthy();
  });
});
