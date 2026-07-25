import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { RunUsageSummary } from "./RunHeader";
import { resolveRunUsageTokens } from "./runUsage";

afterEach(cleanup);

describe("RunUsageSummary", () => {
  it("always identifies cost, input tokens, and output tokens", () => {
    render(<RunUsageSummary costUsd={0.12345} inputTokens={12_345} outputTokens={678} />);

    const usage = screen.getByLabelText("Run usage");
    expect(within(usage).getByTitle("Cost").textContent).toBe("Cost$0.1235");
    expect(within(usage).getByTitle("Input tokens").textContent).toBe("In12.3k");
    expect(within(usage).getByTitle("Output tokens").textContent).toBe("Out678");
  });

  it("preserves unknown usage from default protobuf token values", () => {
    const tokens = resolveRunUsageTokens(0n, 0n, null);
    render(<RunUsageSummary costUsd={null} {...tokens} />);

    const usage = screen.getByLabelText("Run usage");
    expect(within(usage).getByTitle("Cost").textContent).toBe("Cost$—");
    expect(within(usage).getByTitle("Input tokens").textContent).toBe("In—");
    expect(within(usage).getByTitle("Output tokens").textContent).toBe("Out—");
  });

  it("displays explicit zero usage from trace telemetry", () => {
    const tokens = resolveRunUsageTokens(0n, 0n, {
      hasUsage: true,
      inputTokens: 0,
      outputTokens: 0,
    });
    render(<RunUsageSummary costUsd={0} {...tokens} />);

    const usage = screen.getByLabelText("Run usage");
    expect(within(usage).getByTitle("Cost").textContent).toBe("Cost$0.0000");
    expect(within(usage).getByTitle("Input tokens").textContent).toBe("In0");
    expect(within(usage).getByTitle("Output tokens").textContent).toBe("Out0");
  });
});
