import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";

import { RunSessionErrorsPane } from "./RunSessionErrorsPane";
import { AgentRunErrorSchema } from "@/rpc/platform/service_pb";

afterEach(cleanup);

describe("RunSessionErrorsPane", () => {
  it("renders error-only context and preserves recovered failures", () => {
    render(
      <RunSessionErrorsPane
        errors={[
          create(AgentRunErrorSchema, {
            timestampUnix: 1_700_000_000n,
            message: "rate limit exceeded; retrying",
            source: "activity",
            kind: "llm_attempt",
          }),
        ]}
        loading={false}
        error={null}
        truncated={false}
      />,
    );

    expect(screen.getByText("rate limit exceeded; retrying")).toBeTruthy();
    expect(screen.getByText(/Errors stay visible after retries/)).toBeTruthy();
    expect(screen.queryByText(/trace export completed/)).toBeNull();
  });

  it("shows a clear empty state", () => {
    render(<RunSessionErrorsPane errors={[]} loading={false} error={null} truncated={false} />);
    expect(screen.getByText("No errors recorded")).toBeTruthy();
  });

  it("collapses identical source+message repeats into one row with a count and offers copy", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    const mk = (ts: bigint, message: string, source = "pod") =>
      create(AgentRunErrorSchema, { timestampUnix: ts, message, source });
    render(
      <RunSessionErrorsPane
        errors={[mk(1_700_000_000n, "connection reset"), mk(1_700_000_005n, "connection reset"), mk(1_700_000_010n, "other failure")]}
        loading={false}
        error={null}
        truncated={false}
      />,
    );

    expect(screen.getAllByText("connection reset")).toHaveLength(1);
    expect(screen.getByText("×2")).toBeTruthy();
    expect(screen.getByText("other failure")).toBeTruthy();
    expect(screen.getByText(/2 distinct/)).toBeTruthy();

    fireEvent.click(screen.getAllByRole("button", { name: "Copy error" })[0]);
    expect(writeText).toHaveBeenCalledWith("connection reset");
  });
});
