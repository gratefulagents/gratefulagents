import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";

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

const MINUTE = 60 * 1000;
const DAY = 24 * 60 * MINUTE;

function scanFixture(overrides: MessageInitShape<typeof SecurityScanSchema> = {}) {
  return create(SecurityScanSchema, {
    id: "11111111-1111-1111-1111-111111111111",
    namespace: "user-alice",
    scanName: "nightly",
    runName: "nightly-1",
    repository: "github.com/acme/payments",
    revision: "abc123",
    status: "completed",
    startedAt: timestampFromDate(new Date(Date.now() - 60 * MINUTE)),
    completedAt: timestampFromDate(new Date(Date.now() - 30 * MINUTE)),
    counts: {
      critical: 2,
      high: 1,
      actionable_critical: 1,
      actionable_high: 0,
      total: 3,
      actionable: 1,
    },
    ...overrides,
  });
}

/** Three rows that differ on every filterable dimension. */
function scanSet() {
  return [
    scanFixture(),
    scanFixture({
      id: "22222222-2222-2222-2222-222222222222",
      scanName: "weekly",
      runName: "weekly-7",
      repository: "github.com/acme/ledger",
      status: "running",
      startedAt: timestampFromDate(new Date(Date.now() - 5 * MINUTE)),
      completedAt: undefined,
      counts: { low: 4, actionable_low: 4 },
    }),
    scanFixture({
      id: "33333333-3333-3333-3333-333333333333",
      scanName: "weekly",
      runName: "weekly-6",
      repository: "github.com/globex/api",
      status: "failed",
      startedAt: timestampFromDate(new Date(Date.now() - 10 * DAY)),
      completedAt: timestampFromDate(new Date(Date.now() - 10 * DAY)),
      counts: { high: 2, actionable_high: 2 },
    }),
  ];
}

function LocationProbe() {
  return <span data-testid="search">{useLocation().search}</span>;
}

/** Current query string, as the router sees it. */
function search(): string {
  return screen.getByTestId("search").textContent ?? "";
}

function setup(initialEntry = "/security/runs") {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <SecurityScanList />
      <LocationProbe />
    </MemoryRouter>,
  );
}

/** Run names of the rendered rows, in render order. */
function rowNames(): string[] {
  return screen
    .getAllByRole("row")
    .slice(1) // header
    .map((row) => within(row).getAllByRole("link")[0].textContent ?? "");
}

describe("SecurityScanList", () => {
  it("renders scans with repository, status, severity badges, and detail link", async () => {
    listSecurityScans.mockResolvedValue({ scans: [scanFixture()] });
    setup();

    expect(await screen.findByText("acme/payments")).toBeTruthy();
    expect(screen.getByText("nightly")).toBeTruthy();
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
    setup();

    expect(await screen.findByText("No security scans found")).toBeTruthy();
    expect(screen.queryByRole("group", { name: "Scan run filters" })).toBeNull();
  });

  it("shows the error state when loading fails", async () => {
    listSecurityScans.mockRejectedValue(new Error("store offline"));
    setup();

    expect(await screen.findByText("store offline")).toBeTruthy();
  });

  it("applies the search term from the URL and keeps it on reload", async () => {
    listSecurityScans.mockResolvedValue({ scans: scanSet() });
    setup("/security/runs?q=ledger");

    await screen.findByText("weekly-7");
    expect(rowNames()).toEqual(["weekly-7"]);
    // A reload lands on the same URL: the search box is re-hydrated from it.
    expect(
      (screen.getByRole("searchbox", { name: "Search security scans…" }) as HTMLInputElement).value,
    ).toBe("ledger");
    expect(screen.getByText("1 of 3 runs")).toBeTruthy();
  });

  it("round-trips typed search into the query string", async () => {
    listSecurityScans.mockResolvedValue({ scans: scanSet() });
    setup("/security/runs?selected=finding-1");

    await screen.findByText("nightly-1");
    fireEvent.change(screen.getByRole("searchbox", { name: "Search security scans…" }), {
      target: { value: "globex" },
    });

    await waitFor(() => expect(search()).toContain("q=globex"));
    expect(search()).toContain("selected=finding-1");
    await waitFor(() => expect(rowNames()).toEqual(["weekly-6"]));
  });

  it("filters by severity floor, status, repository, configuration, and time range", async () => {
    listSecurityScans.mockResolvedValue({ scans: scanSet() });

    setup("/security/runs?severity=high");
    await screen.findByText("nightly-1");
    // critical counts as "high or worse"; the low-only run drops out.
    expect(rowNames()).toEqual(["nightly-1", "weekly-6"]);
    cleanup();

    setup("/security/runs?status=running");
    await screen.findByText("weekly-7");
    expect(rowNames()).toEqual(["weekly-7"]);
    cleanup();

    setup("/security/runs?repo=acme%2Fledger");
    await screen.findByText("weekly-7");
    expect(rowNames()).toEqual(["weekly-7"]);
    cleanup();

    setup("/security/runs?config=weekly");
    await screen.findByText("weekly-7");
    expect(rowNames()).toEqual(["weekly-7", "weekly-6"]);
    cleanup();

    setup("/security/runs?range=24h");
    await screen.findByText("weekly-7");
    expect(rowNames()).toEqual(["weekly-7", "nightly-1"]);
  });

  it("combines filters and reports the active count", async () => {
    listSecurityScans.mockResolvedValue({ scans: scanSet() });
    setup("/security/runs?status=completed&severity=critical&repo=acme%2Fpayments");

    await screen.findByText("nightly-1");
    expect(rowNames()).toEqual(["nightly-1"]);
    expect(screen.getByLabelText("3 active filters")).toBeTruthy();
    expect(screen.getByText("1 of 3 runs")).toBeTruthy();
  });

  it("sorts by last scan and by severity through the column headers", async () => {
    listSecurityScans.mockResolvedValue({ scans: scanSet() });
    setup();

    await screen.findByText("nightly-1");
    expect(rowNames()).toEqual(["weekly-7", "nightly-1", "weekly-6"]);
    expect(screen.getByRole("columnheader", { name: /Last scan/ }).getAttribute("aria-sort"))
      .toBe("descending");

    fireEvent.click(screen.getByRole("button", { name: /Last scan/ }));
    await waitFor(() => expect(search()).toContain("sort=oldest"));
    expect(rowNames()).toEqual(["weekly-6", "nightly-1", "weekly-7"]);
    expect(screen.getByRole("columnheader", { name: /Last scan/ }).getAttribute("aria-sort"))
      .toBe("ascending");

    fireEvent.click(screen.getByRole("button", { name: /Findings/ }));
    await waitFor(() => expect(search()).toContain("sort=severity"));
    // critical > high > low, regardless of recency.
    expect(rowNames()).toEqual(["nightly-1", "weekly-6", "weekly-7"]);
    expect(screen.getByRole("columnheader", { name: /Findings/ }).getAttribute("aria-sort"))
      .toBe("descending");
    expect(screen.getByRole("columnheader", { name: /Last scan/ }).getAttribute("aria-sort"))
      .toBe("none");

    fireEvent.click(screen.getByRole("button", { name: /Findings/ }));
    await waitFor(() => expect(search()).toContain("sort=least-severe"));
    expect(rowNames()).toEqual(["weekly-7", "weekly-6", "nightly-1"]);
  });

  it("drops the default sort back out of the URL", async () => {
    listSecurityScans.mockResolvedValue({ scans: scanSet() });
    setup("/security/runs?sort=oldest");

    await screen.findByText("nightly-1");
    fireEvent.click(screen.getByRole("button", { name: /Last scan/ }));
    await waitFor(() => expect(search()).toBe(""));
  });

  it("offers a distinct filtered-empty state that clears the filters", async () => {
    listSecurityScans.mockResolvedValue({ scans: scanSet() });
    setup("/security/runs?q=nothing-matches&severity=critical");

    expect(await screen.findByText("No scan runs match these filters")).toBeTruthy();
    expect(screen.queryByText("No security scans found")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /Clear filters/ }));
    await waitFor(() => expect(search()).toBe(""));
    expect(rowNames()).toEqual(["weekly-7", "nightly-1", "weekly-6"]);
  });

  it("keeps polling in the background without clearing the rendered rows", async () => {
    vi.useFakeTimers();
    listSecurityScans.mockResolvedValue({ scans: scanSet() });
    setup();

    await vi.waitFor(() => expect(screen.getByText("nightly-1")).toBeTruthy());

    listSecurityScans.mockRejectedValueOnce(new Error("transient"));
    await vi.advanceTimersByTimeAsync(5_000);

    // A failed background refresh must not surface an error or empty the table.
    expect(screen.getByText("nightly-1")).toBeTruthy();
    expect(screen.queryByText("transient")).toBeNull();
    expect(listSecurityScans).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });
});
