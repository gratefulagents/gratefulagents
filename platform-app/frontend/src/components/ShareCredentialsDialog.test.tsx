import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";

import { ShareCredentialsDialog } from "@/components/ShareCredentialsDialog";
import type { UserSummary } from "@/rpc/auth/service_pb";

vi.mock("@bufbuild/protobuf", () => ({
  create: (_schema: unknown, value: unknown) => value,
}));

vi.mock("@/lib/client", () => ({
  client: {
    shareMyCredentials: vi.fn().mockResolvedValue({ shared: [] }),
  },
}));

const { searchUsers } = vi.hoisted(() => ({ searchUsers: vi.fn() }));
vi.mock("@/lib/auth-client", () => ({
  getAuthClient: () => ({ searchUsers }),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.useRealTimers();
});

function user(id: string, name: string, email: string): UserSummary {
  return { id, name, email, picture: "" } as unknown as UserSummary;
}

function renderDialog() {
  render(
    <ShareCredentialsDialog
      open
      onOpenChange={vi.fn()}
      credentials={[{ id: "openai", label: "OpenAI / ChatGPT", detail: "API key" }]}
    />,
  );
}

describe("ShareCredentialsDialog", () => {
  it("ignores stale search results from a superseded query", async () => {
    vi.useFakeTimers();
    let resolveFirst!: (v: { users: UserSummary[] }) => void;
    let resolveSecond!: (v: { users: UserSummary[] }) => void;
    searchUsers
      .mockImplementationOnce(() => new Promise((resolve) => (resolveFirst = resolve)))
      .mockImplementationOnce(() => new Promise((resolve) => (resolveSecond = resolve)));

    renderDialog();
    const input = screen.getByLabelText("Email address to share credentials with");

    // First query fires a request that stays in flight…
    fireEvent.change(input, { target: { value: "alice" } });
    await act(() => vi.advanceTimersByTimeAsync(300));
    expect(searchUsers).toHaveBeenCalledTimes(1);

    // …then the user retypes the recipient before it resolves.
    fireEvent.change(input, { target: { value: "bob" } });
    await act(() => vi.advanceTimersByTimeAsync(300));
    expect(searchUsers).toHaveBeenCalledTimes(2);

    // The stale response lands late: it must not repopulate suggestions,
    // otherwise Enter would select a recipient from the previous query.
    await act(async () => {
      resolveFirst({ users: [user("u-alice", "Alice", "alice@example.com")] });
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.queryByText("alice@example.com")).toBeNull();

    // The current query's response still applies.
    await act(async () => {
      resolveSecond({ users: [user("u-bob", "Bob", "bob@example.com")] });
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText("bob@example.com")).toBeTruthy();
  });
});
