import { render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { RunSessionErrorsPane } from "./RunSessionErrorsPane";
import { AgentRunErrorSchema } from "@/rpc/platform/service_pb";

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
});
