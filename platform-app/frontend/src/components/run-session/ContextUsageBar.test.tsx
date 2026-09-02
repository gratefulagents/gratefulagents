import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { ContextUsageBar } from "./ContextUsageBar";

afterEach(cleanup);

function fill() {
  return screen.getByRole("meter").querySelector("[data-slot=context-usage-fill]") as HTMLElement;
}

describe("ContextUsageBar", () => {
  it("hides itself until usage and the compaction trigger are known", () => {
    render(<ContextUsageBar usedTokens={null} triggerTokens={0} targetTokens={0} />);
    expect(screen.queryByRole("meter")).toBeNull();
  });

  it("colours the fill by pressure level using tone utilities", () => {
    const { rerender } = render(<ContextUsageBar usedTokens={30_000} triggerTokens={100_000} targetTokens={0} />);
    expect(fill().className).toContain("bg-tone-running");
    expect(fill().className).toContain("duration-[var(--dur-slow)]");
    expect(fill().className).not.toContain("duration-500");

    rerender(<ContextUsageBar usedTokens={75_000} triggerTokens={100_000} targetTokens={0} />);
    expect(fill().className).toContain("bg-tone-warning");

    rerender(<ContextUsageBar usedTokens={95_000} triggerTokens={100_000} targetTokens={0} />);
    expect(fill().className).toContain("bg-tone-danger");
    expect(screen.getByRole("meter").getAttribute("aria-valuenow")).toBe("95");
  });
});
