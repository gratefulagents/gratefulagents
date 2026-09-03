import { describe, expect, it } from "vitest";

import { monogramHue, monogramInitials, monogramStyle } from "@/lib/monogram";

describe("monogram", () => {
  it("derives stable initials from names", () => {
    expect(monogramInitials("Grateful Agents")).toBe("GA");
    expect(monogramInitials("gateway")).toBe("GA");
    expect(monogramInitials("gf-all-openai")).toBe("GA");
    expect(monogramInitials("project_quickfrence")).toBe("PQ");
    expect(monogramInitials("localhost:5199")).toBe("LO");
    expect(monogramInitials("")).toBe("?");
    expect(monogramInitials("---")).toBe("?");
  });

  it("maps a name to the same hue regardless of case", () => {
    expect(monogramHue("Platform")).toBe(monogramHue("platform"));
    expect(monogramHue("platform")).toBeGreaterThanOrEqual(0);
    expect(monogramHue("platform")).toBeLessThan(360);
  });

  it("spreads distinct names across hues", () => {
    const hues = new Set(["gateway", "lottiefiles", "Grateful Agents", "quickfrence", "gf-all-openai"].map(monogramHue));
    expect(hues.size).toBeGreaterThan(3);
  });

  it("exposes theme-aware CSS variables", () => {
    const style = monogramStyle("platform") as Record<string, string>;
    expect(style["--mono"]).toMatch(/^oklch\(var\(--mono-l, [\d.]+\) var\(--mono-c, [\d.]+\) \d+\)$/);
    expect(style["--mono-bg"]).toContain("var(--mono-bg-alpha");
  });
});
