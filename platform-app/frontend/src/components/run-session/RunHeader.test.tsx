import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { RunUsageSummary } from "./RunHeader";

afterEach(cleanup);

describe("RunUsageSummary", () => {
  it("always identifies cost, input tokens, and output tokens", () => {
    render(<RunUsageSummary costUsd={0.12345} inputTokens={12_345} outputTokens={678} />);

    const usage = screen.getByLabelText("Run usage");
    expect(within(usage).getByTitle("Cost").textContent).toBe("Cost$0.1235");
    expect(within(usage).getByTitle("Input tokens").textContent).toBe("In12.3k");
    expect(within(usage).getByTitle("Output tokens").textContent).toBe("Out678");
  });

  it("keeps all metrics visible while usage data is unavailable", () => {
    render(<RunUsageSummary costUsd={null} inputTokens={Number.NaN} outputTokens={Number.NaN} />);

    const usage = screen.getByLabelText("Run usage");
    expect(within(usage).getByTitle("Cost").textContent).toBe("Cost$—");
    expect(within(usage).getByTitle("Input tokens").textContent).toBe("In—");
    expect(within(usage).getByTitle("Output tokens").textContent).toBe("Out—");
  });
});
