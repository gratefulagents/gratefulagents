import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { create, type MessageInitShape } from "@bufbuild/protobuf";

import { RunPullRequestPanel } from "./RunPullRequestPanel";
import { buildReviewFixMessage } from "@/lib/pullRequests";
import {
  GetAgentRunPullRequestsResponseSchema,
  PullRequestDetailsSchema,
} from "@/rpc/platform/service_pb";
import { client } from "@/lib/client";

vi.mock("@/lib/client", () => ({
  client: {
    getAgentRunPullRequests: vi.fn(),
    sendAgentRunMessage: vi.fn(),
  },
}));

const getPullRequestsMock = client.getAgentRunPullRequests as unknown as ReturnType<typeof vi.fn>;
const sendMessageMock = client.sendAgentRunMessage as unknown as ReturnType<typeof vi.fn>;

function makePR(overrides: MessageInitShape<typeof PullRequestDetailsSchema> = {}) {
  return create(PullRequestDetailsSchema, {
    url: "https://github.com/acme/widgets/pull/7",
    repository: "acme/widgets",
    number: 7,
    title: "Add widget",
    state: "OPEN",
    reviewThreads: [
      {
        id: "PRRT_1",
        resolved: false,
        path: "src/widget.ts",
        line: 12,
        comments: [{ author: "alice", body: "Rename this variable." }],
      },
      {
        id: "PRRT_2",
        resolved: false,
        outdated: true,
        path: "src/other.ts",
        line: 3,
        comments: [{ author: "bob", body: "Missing error handling." }],
      },
      {
        id: "PRRT_3",
        resolved: true,
        path: "src/done.ts",
        line: 1,
        comments: [{ author: "carol", body: "Already fixed." }],
      },
    ],
    ...overrides,
  });
}

function mockResponse(prs = [makePR()]) {
  getPullRequestsMock.mockResolvedValue(
    create(GetAgentRunPullRequestsResponseSchema, { pullRequests: prs }),
  );
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("RunPullRequestPanel", () => {
  it("sends selected review threads to the agent and clears the selection", async () => {
    mockResponse();
    sendMessageMock.mockResolvedValue({});
    render(<RunPullRequestPanel namespace="ns" name="run" canSend />);

    const checkbox = await screen.findByLabelText("Select review thread src/widget.ts:12");
    fireEvent.click(checkbox);
    expect(screen.getByText("1 review comment selected")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /Send to agent/ }));

    await waitFor(() => expect(sendMessageMock).toHaveBeenCalledTimes(1));
    const { namespace, name, message } = sendMessageMock.mock.calls[0][0];
    expect(namespace).toBe("ns");
    expect(name).toBe("run");
    expect(message).toContain("acme/widgets#7");
    expect(message).toContain("src/widget.ts:12");
    expect(message).toContain("PRRT_1");
    expect(message).toContain("@alice: Rename this variable.");
    expect(message).not.toContain("PRRT_2");

    await waitFor(() => expect(screen.queryByText("1 review comment selected")).toBeNull());
  });

  it("offers no checkbox for resolved threads", async () => {
    mockResponse();
    render(<RunPullRequestPanel namespace="ns" name="run" canSend />);

    await screen.findByLabelText("Select review thread src/widget.ts:12");
    expect(screen.queryByLabelText("Select review thread src/done.ts:1")).toBeNull();
  });

  it("hides selection controls when sending is not allowed", async () => {
    mockResponse();
    render(<RunPullRequestPanel namespace="ns" name="run" canSend={false} />);

    await screen.findByText("src/widget.ts:12");
    expect(screen.queryByLabelText("Select review thread src/widget.ts:12")).toBeNull();
    expect(screen.queryByRole("button", { name: /Send to agent/ })).toBeNull();
  });

  it("select all toggles every unresolved thread in a PR", async () => {
    mockResponse();
    render(<RunPullRequestPanel namespace="ns" name="run" canSend />);

    fireEvent.click(await screen.findByRole("button", { name: "Select all" }));
    expect(screen.getByText("2 review comments selected")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Deselect all" }));
    expect(screen.queryByText(/review comments selected/)).toBeNull();
  });
});

describe("buildReviewFixMessage", () => {
  it("numbers threads across PRs and flags outdated ones", () => {
    const prA = makePR();
    const prB = makePR({
      url: "https://github.com/acme/gadgets/pull/9",
      repository: "acme/gadgets",
      number: 9,
      reviewThreads: [
        { id: "PRRT_9", resolved: false, path: "lib/g.go", line: 0, comments: [{ author: "", body: "Fix this." }] },
      ],
    });
    const message = buildReviewFixMessage(
      [prA, prB],
      new Set([
        "https://github.com/acme/widgets/pull/7::PRRT_2",
        "https://github.com/acme/gadgets/pull/9::PRRT_9",
      ]),
    );
    expect(message).toContain("Please address the following PR review comments:");
    expect(message).toContain("1. `src/other.ts:3` — review thread PRRT_2 (marked outdated — re-check against the current code)");
    expect(message).toContain("@bob: Missing error handling.");
    expect(message).toContain("acme/gadgets#9 (https://github.com/acme/gadgets/pull/9):");
    expect(message).toContain("2. `lib/g.go` — review thread PRRT_9");
    expect(message).toContain("reviewer: Fix this.");
    expect(message).toContain("reply to and resolve each addressed review thread");
  });
});
