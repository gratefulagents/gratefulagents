import { describe, expect, it } from "vitest";

import { formatRepoShort, formatSuccessRate } from "@/lib/format";

describe("formatSuccessRate", () => {
  it("returns an em dash until at least one run has finished", () => {
    expect(formatSuccessRate(0, 0)).toBe("—");
  });

  it("computes the rate over finished runs only", () => {
    expect(formatSuccessRate(3, 1)).toBe("75%");
    expect(formatSuccessRate(0, 2)).toBe("0%");
    expect(formatSuccessRate(2, 0)).toBe("100%");
  });
});

describe("formatRepoShort", () => {
  it("strips protocol and host from https URLs", () => {
    expect(formatRepoShort("https://github.com/gratefulagents/gratefulagents")).toBe(
      "gratefulagents/gratefulagents",
    );
  });

  it("strips trailing .git and slashes", () => {
    expect(formatRepoShort("https://github.com/acme/widgets.git")).toBe("acme/widgets");
    expect(formatRepoShort("https://gitlab.com/acme/widgets/")).toBe("acme/widgets");
  });

  it("handles ssh remotes", () => {
    expect(formatRepoShort("git@github.com:acme/widgets.git")).toBe("acme/widgets");
  });

  it("returns empty string for empty input", () => {
    expect(formatRepoShort("")).toBe("");
  });
});
