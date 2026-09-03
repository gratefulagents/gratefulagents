import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import {
  SecurityFindingDetail,
  cweLinkUrl,
  githubBlobUrl,
  isHttpUrl,
  parseRawFinding,
} from "@/components/SecurityFindingDetail";
import {
  SecurityFindingEventSchema,
  SecurityFindingSchema,
  SecurityScanSchema,
} from "@/rpc/platform/service_pb";

const {
  getSecurityScan,
  getSecurityFinding,
  listSecurityFindings,
  listSecurityFindingEvents,
  addSecurityFindingComment,
  updateSecurityFindingStatus,
  toastSuccess,
  toastError,
} = vi.hoisted(() => ({
  getSecurityScan: vi.fn(),
  getSecurityFinding: vi.fn(),
  listSecurityFindings: vi.fn(),
  listSecurityFindingEvents: vi.fn(),
  addSecurityFindingComment: vi.fn(),
  updateSecurityFindingStatus: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: {
    getSecurityScan,
    getSecurityFinding,
    listSecurityFindings,
    listSecurityFindingEvents,
    addSecurityFindingComment,
    updateSecurityFindingStatus,
  },
}));

vi.mock("@/components/ui/toaster", () => ({
  toast: { success: toastSuccess, error: toastError },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

const FINDING_ID = "22222222-2222-2222-2222-222222222222";
const PREV_ID = "11111111-1111-1111-1111-111111111111";
const NEXT_ID = "33333333-3333-3333-3333-333333333333";
const LAST_ID = "55555555-5555-5555-5555-555555555555";

function scanFixture() {
  return create(SecurityScanSchema, {
    id: "44444444-4444-4444-4444-444444444444",
    namespace: "user-alice",
    scanName: "nightly",
    runName: "nightly-1",
    repository: "github.com/acme/payments",
    revision: "abc123def456",
    status: "completed",
  });
}

function findingFixture(overrides: Record<string, unknown> = {}) {
  return create(SecurityFindingSchema, {
    id: FINDING_ID,
    namespace: "user-alice",
    scanName: "nightly",
    runName: "nightly-1",
    fingerprint: "fp-abc123",
    title: "SQL injection in payment lookup",
    category: "injection",
    severity: "critical",
    confidence: "high",
    repository: "github.com/acme/payments",
    revision: "abc123def456",
    filePath: "internal/db/query.go",
    startLine: 42,
    endLine: 48,
    symbol: "LookupPayment",
    cwe: ["CWE-89"],
    description: "User input is concatenated into a SQL string.",
    impact: "Full database read access.",
    attackVector: "Crafted payment ID in the lookup endpoint.",
    remediation: "Use parameterized queries.",
    references: ["https://example.com/advisory", "javascript:alert(1)"],
    sourceAgent: "scanner-agent",
    scanStep: "injection-sweep",
    score: 9.5,
    status: "open",
    occurrences: 2,
    raw: JSON.stringify({
      title: "SQL injection in payment lookup",
      tags: ["sqli", "payments"],
      evidence: [
        {
          file_path: "internal/db/query.go",
          start_line: 42,
          end_line: 48,
          snippet: "**not markdown** SELECT * FROM payments WHERE id = '\" + id + \"'",
          note: "PoC query",
        },
      ],
    }),
    firstSeenAt: timestampFromDate(new Date("2026-02-01T00:00:00Z")),
    lastSeenAt: timestampFromDate(new Date("2026-02-02T00:00:00Z")),
    ...overrides,
  });
}

function minimalFindingFixture() {
  return create(SecurityFindingSchema, {
    id: FINDING_ID,
    namespace: "user-alice",
    scanName: "nightly",
    runName: "nightly-1",
    title: "Weak TLS configuration",
    severity: "low",
    status: "open",
    score: 2,
    occurrences: 1,
  });
}

function eventsFixture() {
  return [
    create(SecurityFindingEventSchema, {
      id: 4n,
      eventType: "policy_disposition",
      actor: "secscan-nightly-1-ps-poc-validator",
      note: "repository does not build: missing toolchain",
      detail: '{"execution_id":"exec-1","policy_check":"reproduction","policy_disposition":"unreproducible_env"}',
      createdAt: timestampFromDate(new Date("2026-02-05T10:00:00Z")),
    }),
    create(SecurityFindingEventSchema, {
      id: 3n,
      eventType: "status_reviewed",
      actor: "validator",
      note: "still reproducible",
      detail: '{"from":"confirmed","to":"confirmed"}',
      createdAt: timestampFromDate(new Date("2026-02-04T10:00:00Z")),
    }),
    create(SecurityFindingEventSchema, {
      id: 2n,
      eventType: "status_changed",
      actor: "alice",
      note: "confirmed exploitable",
      detail: '{"from":"open","to":"confirmed"}',
      createdAt: timestampFromDate(new Date("2026-02-03T10:00:00Z")),
    }),
    create(SecurityFindingEventSchema, {
      id: 1n,
      eventType: "comment",
      actor: "bob",
      note: "needs a second look",
      createdAt: timestampFromDate(new Date("2026-02-02T10:00:00Z")),
    }),
  ];
}

function mockHappyPath({
  finding = findingFixture(),
  siblings = [findingFixture()],
  events = eventsFixture(),
} = {}) {
  getSecurityScan.mockResolvedValue(scanFixture());
  getSecurityFinding.mockResolvedValue({ finding, events });
  listSecurityFindings.mockResolvedValue({ findings: siblings });
  listSecurityFindingEvents.mockResolvedValue({ events });
}

function renderDetail(search = "") {
  render(
    <MemoryRouter
      initialEntries={[`/security/user-alice/nightly-1/findings/${FINDING_ID}${search}`]}
    >
      <Routes>
        <Route
          path="/security/:namespace/:runName/findings/:findingId"
          element={<SecurityFindingDetail />}
        />
      </Routes>
    </MemoryRouter>,
  );
}

/** Every dead end offers the filtered scan view plus the two security hubs. */
function expectRecoveryLinks(scanHref: string) {
  expect(screen.getByRole("link", { name: "Back to scan" }).getAttribute("href")).toBe(scanHref);
  expect(screen.getByRole("link", { name: "Scan runs" }).getAttribute("href")).toBe(
    "/security/runs",
  );
  expect(screen.getByRole("link", { name: "Security overview" }).getAttribute("href")).toBe(
    "/security",
  );
  expect(screen.getByRole("button", { name: "Try again" })).toBeTruthy();
}

describe("SecurityFindingDetail", () => {
  it("renders a full finding with facts, sections, and history", async () => {
    mockHappyPath();
    renderDetail();

    expect(
      await screen.findByRole("heading", { name: "SQL injection in payment lookup" }),
    ).toBeTruthy();

    // The finding lookup asserts scan ownership server-side.
    expect(getSecurityFinding).toHaveBeenCalledWith({
      id: FINDING_ID,
      namespace: "user-alice",
      scanName: "nightly",
    });

    // Section headings (a11y structure).
    for (const name of ["Overview", "Details", "Evidence & PoC", "Triage", "Comments & History"]) {
      expect(screen.getByRole("heading", { name })).toBeTruthy();
    }

    // Overview facts.
    expect(screen.getByText("fp-abc123")).toBeTruthy();
    expect(screen.getByText("LookupPayment")).toBeTruthy();
    expect(screen.getByText("injection-sweep")).toBeTruthy();
    expect(screen.getByText("sqli")).toBeTruthy();
    expect(screen.getByRole("link", { name: "CWE-89" }).getAttribute("href")).toBe(
      "https://cwe.mitre.org/data/definitions/89.html",
    );
    // GitHub source link only because the repository is clearly github.com.
    expect(
      screen.getByRole("link", { name: "internal/db/query.go:42-48" }).getAttribute("href"),
    ).toBe(
      "https://github.com/acme/payments/blob/abc123def456/internal/db/query.go#L42-L48",
    );

    // Markdown sections rendered.
    expect(screen.getByText("User input is concatenated into a SQL string.")).toBeTruthy();
    expect(screen.getByText("Use parameterized queries.")).toBeTruthy();

    // Untrusted-content warning present.
    expect(screen.getByText(/Never execute it without careful review/)).toBeTruthy();

    // History entries with from→to provenance.
    expect(screen.getByText("confirmed exploitable")).toBeTruthy();
    expect(screen.getByText("Open → Confirmed")).toBeTruthy();
    expect(screen.getByText("Status reviewed")).toBeTruthy();
    expect(screen.getByText("still reproducible")).toBeTruthy();
    expect(screen.getByText("Policy disposition")).toBeTruthy();
    expect(screen.getByText("reproduction: unreproducible env")).toBeTruthy();
    expect(screen.getByText("repository does not build: missing toolchain")).toBeTruthy();
    expect(screen.getByText("needs a second look")).toBeTruthy();

    // Source agent run link.
    expect(screen.getByRole("button", { name: /Agent run/ }).getAttribute("href")).toBe(
      "/runs/user-alice/nightly-1",
    );

    // Header identity: severity, score, short repo label, and a copy action
    // for the file:line the reviewer needs in their editor.
    expect(screen.getByText("critical")).toBeTruthy();
    expect(screen.getByText("9.5")).toBeTruthy();
    expect(screen.getByText("acme/payments")).toBeTruthy();
    const clipboard = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText: clipboard } });
    fireEvent.click(screen.getByRole("button", { name: "Copy file and line" }));
    expect(clipboard).toHaveBeenCalledWith("internal/db/query.go:42");
  });

  it("renders untrusted references and evidence safely", async () => {
    mockHappyPath();
    renderDetail();
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    // http(s) reference is a link; javascript: reference is plain text.
    expect(
      screen.getByRole("link", { name: "https://example.com/advisory" }).getAttribute("href"),
    ).toBe("https://example.com/advisory");
    const jsRef = screen.getByText("javascript:alert(1)");
    expect(jsRef.closest("a")).toBeNull();

    // Evidence snippet stays literal text (no markdown interpretation). It
    // appears in the parsed evidence block and inside the raw JSON dump.
    const literals = screen.getAllByText(/\*\*not markdown\*\* SELECT \* FROM payments/);
    expect(literals.length).toBeGreaterThan(0);
    for (const el of literals) {
      expect(el.closest("pre")).not.toBeNull();
    }
  });

  it("renders a minimal finding with 'not provided' placeholders", async () => {
    mockHappyPath({ finding: minimalFindingFixture(), siblings: [minimalFindingFixture()], events: [] });
    renderDetail();

    expect(await screen.findByRole("heading", { name: "Weak TLS configuration" })).toBeTruthy();
    // Description/impact/attack vector/remediation all missing.
    expect(screen.getAllByText("Not provided.").length).toBeGreaterThanOrEqual(5);
    expect(screen.getByText("No history recorded.")).toBeTruthy();
    expect(screen.queryByRole("link", { name: /CWE-/ })).toBeNull();
  });

  it("names each missing security fact instead of a generic 'Not set'", async () => {
    mockHappyPath({ finding: minimalFindingFixture(), siblings: [minimalFindingFixture()], events: [] });
    renderDetail();
    await screen.findByRole("heading", { name: "Weak TLS configuration" });

    expect(screen.getByText("No CWE assigned")).toBeTruthy();
    expect(screen.getByText("No category")).toBeTruthy();
    expect(screen.getByText("Unassigned")).toBeTruthy();
    expect(screen.getByText("No code location")).toBeTruthy();
    expect(screen.getByText("No ticket linked")).toBeTruthy();
    // Repository, revision, source agent and tool all read "Not recorded".
    expect(screen.getAllByText("Not recorded")).toHaveLength(4);
  });

  it("shows a clear not-found state with a way back", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFinding.mockRejectedValue(
      new ConnectError("security finding not found", Code.NotFound),
    );
    listSecurityFindings.mockResolvedValue({ findings: [] });
    renderDetail("?severity=high");

    expect(await screen.findByText("Finding not found")).toBeTruthy();
    expectRecoveryLinks(`/security/user-alice/nightly-1?severity=high&selected=${FINDING_ID}`);

    // Retry re-runs the whole load instead of stranding the user.
    getSecurityFinding.mockResolvedValue({ finding: findingFixture(), events: [] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { name: "SQL injection in payment lookup" })).toBeTruthy();
  });

  it("shows a forbidden state when the namespace is not accessible", async () => {
    getSecurityScan.mockRejectedValue(
      new ConnectError("namespace access denied", Code.PermissionDenied),
    );
    renderDetail();

    expect(await screen.findByText("You don't have access to this finding")).toBeTruthy();
    expectRecoveryLinks(`/security/user-alice/nightly-1?selected=${FINDING_ID}`);
  });

  it("shows an unsupported-store state", async () => {
    getSecurityScan.mockRejectedValue(
      new ConnectError("security findings are not supported", Code.FailedPrecondition),
    );
    renderDetail();

    expect(await screen.findByText("Security findings are unavailable")).toBeTruthy();
    expectRecoveryLinks(`/security/user-alice/nightly-1?selected=${FINDING_ID}`);
  });

  it("shows a generic error state with the server detail and a retry", async () => {
    getSecurityScan.mockRejectedValue(new ConnectError("boom", Code.Internal));
    renderDetail();

    expect(await screen.findByText("Failed to load finding")).toBeTruthy();
    expect(screen.getByText(/boom/)).toBeTruthy();
    expectRecoveryLinks(`/security/user-alice/nightly-1?selected=${FINDING_ID}`);

    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFinding.mockResolvedValue({ finding: findingFixture(), events: [] });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(
      await screen.findByRole("heading", { name: "SQL injection in payment lookup" }),
    ).toBeTruthy();
  });

  it("submits a comment and refreshes history", async () => {
    mockHappyPath();
    addSecurityFindingComment.mockResolvedValue(
      create(SecurityFindingEventSchema, { id: 3n, eventType: "comment", actor: "alice" }),
    );
    renderDetail();
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    const box = screen.getByLabelText("Add a comment");
    fireEvent.change(box, { target: { value: "  validated with a local PoC  " } });
    fireEvent.click(screen.getByRole("button", { name: "Comment" }));

    await waitFor(() => {
      expect(addSecurityFindingComment).toHaveBeenCalledWith({
        id: FINDING_ID,
        namespace: "user-alice",
        scanName: "nightly",
        body: "validated with a local PoC",
      });
    });
    await waitFor(() => {
      expect((box as HTMLTextAreaElement).value).toBe("");
    });
    expect(listSecurityFindingEvents).toHaveBeenCalledWith({
      id: FINDING_ID,
      namespace: "user-alice",
      scanName: "nightly",
    });
    expect(toastSuccess).toHaveBeenCalled();
  });

  it("guards in-page navigation while a comment draft is unsaved", async () => {
    const confirmMock = vi.fn().mockReturnValue(false);
    vi.stubGlobal("confirm", confirmMock);
    mockHappyPath({
      siblings: [findingFixture({ id: PREV_ID }), findingFixture(), findingFixture({ id: NEXT_ID })],
    });
    renderDetail();
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });
    const findingFetches = getSecurityFinding.mock.calls.length;

    fireEvent.change(screen.getByLabelText("Add a comment"), {
      target: { value: "draft comment" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Next finding" }));

    expect(confirmMock).toHaveBeenCalledWith(
      "You have an unsaved comment. Leave without posting it?",
    );
    // Declined: still on the same finding with the draft intact.
    expect(getSecurityFinding.mock.calls.length).toBe(findingFetches);
    expect((screen.getByLabelText("Add a comment") as HTMLTextAreaElement).value).toBe(
      "draft comment",
    );

    // Accepted: the navigation proceeds to the next finding.
    confirmMock.mockReturnValue(true);
    fireEvent.click(screen.getByRole("button", { name: "Next finding" }));
    await waitFor(() => {
      expect(getSecurityFinding).toHaveBeenCalledWith(
        expect.objectContaining({ id: NEXT_ID }),
      );
    });
  });

  it("computes prev/next from the filtered sibling list and keeps filter params", async () => {
    mockHappyPath({
      siblings: [findingFixture({ id: PREV_ID }), findingFixture(), findingFixture({ id: NEXT_ID })],
    });
    renderDetail("?severity=critical&q=sql");
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    // The sibling query reuses the scan table's filters.
    expect(listSecurityFindings).toHaveBeenCalledWith({
      namespace: "user-alice",
      runName: "nightly-1",
      severity: "critical",
      status: "actionable",
      category: "",
      search: "sql",
      baselineState: "",
      assignee: "",
      suppressed: "exclude",
      includeDuplicates: false,
    });
    expect(screen.getByTestId("finding-position").textContent).toBe("2 of 3");
    expect(screen.getByRole("button", { name: "Previous finding" }).getAttribute("href")).toBe(
      `/security/user-alice/nightly-1/findings/${PREV_ID}?q=sql&severity=critical&selected=${PREV_ID}`,
    );
    expect(screen.getByRole("button", { name: "Next finding" }).getAttribute("href")).toBe(
      `/security/user-alice/nightly-1/findings/${NEXT_ID}?q=sql&severity=critical&selected=${NEXT_ID}`,
    );
  });

  it("sends the whole canonical filter set to the sibling query", async () => {
    mockHappyPath();
    renderDetail(
      "?q=sql&severity=high&status=confirmed&category=injection&baseline=new"
        + "&assignee=alice&suppressed=only&dupes=include",
    );
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    expect(listSecurityFindings).toHaveBeenCalledWith({
      namespace: "user-alice",
      runName: "nightly-1",
      severity: "high",
      status: "confirmed",
      category: "injection",
      search: "sql",
      baselineState: "new",
      assignee: "alice",
      suppressed: "only",
      includeDuplicates: true,
    });
  });

  it("narrows the sibling walk by the client-side tool and file filters", async () => {
    mockHappyPath({
      siblings: [
        findingFixture({ id: PREV_ID, sourceAgent: "other-agent" }),
        findingFixture(),
        findingFixture({ id: NEXT_ID, filePath: "internal/http/handler.go" }),
        findingFixture({ id: LAST_ID }),
      ],
    });
    renderDetail("?tool=scanner-agent&file=query.go");
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    // `tool` and `file` have no server-side equivalent, so they are applied to
    // the returned page — the walk matches the table the user filtered.
    expect(listSecurityFindings).toHaveBeenCalledWith(
      expect.objectContaining({ severity: "", status: "actionable", category: "" }),
    );
    expect(screen.getByTestId("finding-position").textContent).toBe("1 of 2");
    expect(screen.getByRole("button", { name: "Previous finding" }).hasAttribute("href")).toBe(
      false,
    );
    expect(screen.getByRole("button", { name: "Next finding" }).getAttribute("href")).toBe(
      `/security/user-alice/nightly-1/findings/${LAST_ID}?tool=scanner-agent&file=query.go&selected=${LAST_ID}`,
    );
  });

  it("says so when the finding is outside the filtered list", async () => {
    mockHappyPath({ siblings: [findingFixture({ id: PREV_ID })] });
    renderDetail("?severity=low");
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    expect(screen.getByTestId("finding-position").textContent).toBe("Not in the filtered list");
    expect(screen.getByRole("button", { name: "Previous finding" }).hasAttribute("href")).toBe(
      false,
    );
    expect(screen.getByRole("button", { name: "Next finding" }).hasAttribute("href")).toBe(false);
  });

  it("carries the filters and the selection back to the scan list", async () => {
    mockHappyPath();
    renderDetail("?severity=critical&q=sql&suppressed=only&selected=other-id");
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    // `selected` is rewritten to this finding so the list restores the row.
    expect(screen.getByRole("link", { name: /Scan nightly-1/ }).getAttribute("href")).toBe(
      `/security/user-alice/nightly-1?q=sql&severity=critical&suppressed=only&selected=${FINDING_ID}`,
    );
  });

  it("walks prev/next from the keyboard with a visible hint", async () => {
    mockHappyPath({
      siblings: [findingFixture({ id: PREV_ID }), findingFixture(), findingFixture({ id: NEXT_ID })],
    });
    renderDetail();
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });
    expect(screen.getByText(/to move\s+between findings/)).toBeTruthy();

    fireEvent.keyDown(window, { key: "j" });
    await waitFor(() => {
      expect(getSecurityFinding).toHaveBeenCalledWith(expect.objectContaining({ id: NEXT_ID }));
    });

    fireEvent.keyDown(window, { key: "ArrowLeft" });
    await waitFor(() => {
      expect(getSecurityFinding).toHaveBeenCalledWith(expect.objectContaining({ id: PREV_ID }));
    });
  });

  it("ignores navigation keys typed into a field", async () => {
    mockHappyPath({
      siblings: [findingFixture(), findingFixture({ id: NEXT_ID })],
    });
    renderDetail();
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    fireEvent.keyDown(screen.getByLabelText("Add a comment"), { key: "j" });
    expect(getSecurityFinding).not.toHaveBeenCalledWith(expect.objectContaining({ id: NEXT_ID }));
  });

  it("translates the all-statuses view into an unfiltered sibling query", async () => {
    mockHappyPath();
    renderDetail("?status=all");

    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });
    expect(listSecurityFindings).toHaveBeenCalledWith(
      expect.objectContaining({ status: "" }),
    );
  });

  it("updates the status with a note and recovers from stale updates", async () => {
    mockHappyPath();
    updateSecurityFindingStatus.mockResolvedValue(findingFixture({ status: "confirmed" }));
    renderDetail();
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "confirmed" } });
    fireEvent.change(screen.getByLabelText("Note (optional)"), {
      target: { value: "verified in staging" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Update status" }));

    await waitFor(() => {
      expect(updateSecurityFindingStatus).toHaveBeenCalledWith({
        id: FINDING_ID,
        status: "confirmed",
        note: "verified in staging",
        namespace: "user-alice",
      });
    });
    expect(toastSuccess).toHaveBeenCalled();

    // A conflicting/stale update refetches the authoritative state.
    updateSecurityFindingStatus.mockRejectedValue(
      new ConnectError("security finding not found", Code.NotFound),
    );
    const scanCallsBefore = getSecurityScan.mock.calls.length;
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "fixed" } });
    fireEvent.click(screen.getByRole("button", { name: "Update status" }));

    await waitFor(() => {
      expect(toastError).toHaveBeenCalled();
    });
    await waitFor(() => {
      expect(getSecurityScan.mock.calls.length).toBeGreaterThan(scanCallsBefore);
    });
  });
});

describe("SecurityFindingDetail helpers", () => {
  it("only links canonical CWE ids", () => {
    expect(cweLinkUrl("CWE-89")).toBe("https://cwe.mitre.org/data/definitions/89.html");
    expect(cweLinkUrl("cwe-79")).toBe("https://cwe.mitre.org/data/definitions/79.html");
    expect(cweLinkUrl("CWE-89; DROP")).toBeNull();
    expect(cweLinkUrl("javascript:alert(1)")).toBeNull();
    expect(cweLinkUrl("89")).toBeNull();
  });

  it("only treats http(s) URLs as links", () => {
    expect(isHttpUrl("https://example.com/x")).toBe(true);
    expect(isHttpUrl("http://example.com")).toBe(true);
    expect(isHttpUrl("javascript:alert(1)")).toBe(false);
    expect(isHttpUrl("ftp://example.com")).toBe(false);
    expect(isHttpUrl("not a url")).toBe(false);
  });

  it("only fabricates source links for clear github.com repositories", () => {
    expect(githubBlobUrl("github.com/acme/payments", "abc", "a/b.go", 1, 3)).toBe(
      "https://github.com/acme/payments/blob/abc/a/b.go#L1-L3",
    );
    expect(githubBlobUrl("https://github.com/acme/payments.git", "abc", "a.go", 0, 0)).toBe(
      "https://github.com/acme/payments/blob/abc/a.go",
    );
    expect(githubBlobUrl("gitlab.com/acme/payments", "abc", "a.go", 1, 1)).toBeNull();
    expect(githubBlobUrl("github.com/acme/payments", "", "a.go", 1, 1)).toBeNull();
    expect(githubBlobUrl("github.com/acme/payments", "abc", "", 1, 1)).toBeNull();
    expect(githubBlobUrl("evil.com/github.com/acme/payments", "abc", "a.go", 1, 1)).toBeNull();
  });

  it("parses raw finding JSON defensively", () => {
    expect(parseRawFinding("")).toEqual({ evidence: [], tags: [] });
    expect(parseRawFinding("not json")).toEqual({ evidence: [], tags: [] });
    expect(parseRawFinding('{"tags":["a",7],"evidence":[{"snippet":"s"},"junk"]}')).toEqual({
      evidence: [{ filePath: "", startLine: 0, endLine: 0, snippet: "s", note: "" }],
      tags: ["a"],
    });
  });
});

describe("SecurityFindingDetail provenance and suppression", () => {
  it("renders scanner provenance facts as plain text", async () => {
    mockHappyPath({
      finding: findingFixture({
        sourceKind: "scanner",
        tool: "semgrep <img src=x>",
        toolVersion: "1.50.0",
        ruleId: "go.lang.security.sqli",
        correlatedFingerprints: ["fp-agent-9"],
      }),
    });
    renderDetail();

    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });
    expect(screen.getByText("deterministic scanner")).toBeTruthy();
    // Untrusted tool strings render verbatim as text, never as HTML.
    expect(screen.getByText("semgrep <img src=x> 1.50.0")).toBeTruthy();
    expect(document.querySelector("img[src='x']")).toBeNull();
    expect(screen.getByText("go.lang.security.sqli")).toBeTruthy();
    expect(screen.getByText("fp-agent-9")).toBeTruthy();
  });

  it("defaults the source to agent when no provenance is recorded", async () => {
    mockHappyPath();
    renderDetail();

    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });
    expect(screen.getByText("agent")).toBeTruthy();
    expect(screen.queryByText("deterministic scanner")).toBeNull();
  });

  it("explains a governed suppression with rule, reason, owner, and expiry", async () => {
    mockHappyPath({
      finding: findingFixture({
        suppressedBy: "prod-policy/vendored",
        suppressedReason: "third-party code, tracked upstream",
        suppressedOwner: "sec-team",
        suppressedAt: timestampFromDate(new Date("2026-02-01T00:00:00Z")),
        suppressionExpiresAt: timestampFromDate(new Date("2026-06-01T00:00:00Z")),
      }),
    });
    renderDetail();

    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });
    expect(screen.getByText("suppressed")).toBeTruthy();
    const details = screen.getByTestId("suppression-details");
    expect(details.textContent).toContain("prod-policy/vendored");
    expect(details.textContent).toContain("Reason: third-party code, tracked upstream");
    expect(details.textContent).toContain("Owner: sec-team");
    expect(details.textContent).toContain("Unsuppresses automatically on");
    expect(details.textContent).toContain("never deleted");
  });

  it("shows no suppression section for unsuppressed findings", async () => {
    mockHappyPath();
    renderDetail();

    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });
    expect(screen.queryByTestId("suppression-details")).toBeNull();
    expect(screen.queryByText("suppressed")).toBeNull();
  });
});

describe("SecurityFindingDetail presentation", () => {
  it("groups prev/next into one control that names its order and explains disabled ends", async () => {
    // A single sibling: both ends of the pager are dead, and must say why.
    mockHappyPath();
    renderDetail();
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    const position = screen.getByTestId("finding-position");
    const prev = screen.getByRole("button", { name: "Previous finding" });
    const next = screen.getByRole("button", { name: "Next finding" });
    const group = position.parentElement!;
    expect(group.contains(prev)).toBe(true);
    expect(group.contains(next)).toBe(true);

    expect(screen.getByText("Ordered by score, as filtered")).toBeTruthy();
    expect(prev.closest("span[title]")?.getAttribute("title")).toMatch(/first finding/);
    expect(next.closest("span[title]")?.getAttribute("title")).toMatch(/last finding/);
    // Disabled reads as "nothing to page to", not as a broken control.
    expect(prev.className).toContain("disabled:opacity-100");
  });

  it("normalises status casing, empty values, and timestamps", async () => {
    mockHappyPath({ finding: findingFixture({ confidence: "", category: "" }) });
    renderDetail();
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    // "Open" in the header and in the overview, never "open".
    expect(screen.getAllByText("Open").length).toBeGreaterThanOrEqual(2);
    expect(screen.queryByText("open")).toBeNull();

    // Low-value gaps keep the generic wording; the high-value security facts
    // name what is missing instead.
    expect(screen.getAllByText("Not set").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("Unassigned")).toBeTruthy();
    expect(screen.getByText("No category")).toBeTruthy();
    expect((screen.getByLabelText("Assignee") as HTMLInputElement).placeholder).toBe("Not set");

    // Compact absolute stamp plus relative age, never "2/1/2026, 12:00:00 AM".
    const timeline = screen.getByRole("region", { name: "Timeline" });
    expect(timeline.textContent).toMatch(/Feb 1/);
    expect(timeline.textContent).toMatch(/(ago|in \d)/);
    expect(screen.queryByText(/\d{1,2}\/\d{1,2}\/\d{4}/)).toBeNull();

    // The header copy action carries a visible label, not just an icon.
    expect(screen.getByRole("button", { name: "Copy file and line" }).textContent).toContain(
      "Copy",
    );
  });

  it("keeps 'Update status' disabled until the status changes, then confirms the save", async () => {
    mockHappyPath();
    updateSecurityFindingStatus.mockResolvedValue(findingFixture({ status: "confirmed" }));
    renderDetail();
    await screen.findByRole("heading", { name: "SQL injection in payment lookup" });

    const submit = () => screen.getByRole("button", { name: "Update status" }) as HTMLButtonElement;
    expect(submit().disabled).toBe(true);
    expect(submit().closest("span[title]")?.getAttribute("title")).toMatch(/different status/);

    // A note alone is not a change; the status itself has to move.
    fireEvent.change(screen.getByLabelText("Note (optional)"), { target: { value: "checked" } });
    expect(submit().disabled).toBe(true);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "confirmed" } });
    expect(submit().disabled).toBe(false);
    fireEvent.click(submit());

    // Saved in place, and the button falls back to disabled once applied.
    expect(await screen.findByText("Saved")).toBeTruthy();
    expect(submit().disabled).toBe(true);
  });
});
