import { describe, expect, it } from "vitest";

import { disambiguateSubagentTitles } from "./subagentTitles";

describe("disambiguateSubagentTitles", () => {
  it("keeps unique titles unchanged", () => {
    const titles = disambiguateSubagentTitles([
      { title: "Review the API surface" },
      { title: "Harden the auth flow" },
    ]);
    expect(titles).toEqual(["Review the API surface", "Harden the auth flow"]);
  });

  it("surfaces the distinguishing suffix for identical truncated titles", () => {
    const shared =
      "You are auditing mobile/tablet screenshots of the dashboard web UI for UI/UX defects.";
    const titles = disambiguateSubagentTitles([
      {
        title: `${shared.slice(0, 60)}…`,
        detail: `${shared} Analyze the run list screenshots in /workspace/repo/pl/one.`,
      },
      {
        title: `${shared.slice(0, 60)}…`,
        detail: `${shared} Analyze the settings page screenshots in /workspace/repo/pl/two.`,
      },
    ]);

    expect(titles[0]).not.toEqual(titles[1]);
    expect(titles[0]).toMatch(/^…/);
    expect(titles[0]).toContain("run list");
    expect(titles[1]).toContain("settings page");
  });

  it("numbers entries whose details are also identical", () => {
    const titles = disambiguateSubagentTitles([
      { title: "Same task", detail: "Same task same prompt" },
      { title: "Same task", detail: "Same task same prompt" },
      { title: "Same task", detail: "Same task same prompt" },
    ]);
    expect(new Set(titles).size).toBe(3);
    expect(titles[1]).toContain("(2)");
    expect(titles[2]).toContain("(3)");
  });

  it("treats a trailing ellipsis as part of the same title group", () => {
    const titles = disambiguateSubagentTitles([
      { title: "Audit screenshots…", detail: "Audit screenshots for the run page" },
      { title: "Audit screenshots…", detail: "Audit screenshots for the graph tab" },
    ]);
    expect(titles[0]).toContain("run page");
    expect(titles[1]).toContain("graph tab");
  });

  it("clips very long distinguishing suffixes at a word boundary", () => {
    const shared = "Shared prompt prefix that is long enough to trigger disambiguation";
    const longTail = `alpha ${"word ".repeat(40)}end`;
    const titles = disambiguateSubagentTitles([
      { title: "Shared", detail: `${shared} ${longTail}` },
      { title: "Shared", detail: `${shared} beta short tail` },
    ]);
    expect(titles[0].length).toBeLessThanOrEqual(90);
    expect(titles[0].endsWith("…")).toBe(true);
    expect(titles[1]).toContain("beta short tail");
  });

  it("ignores empty titles and handles missing details", () => {
    const titles = disambiguateSubagentTitles([
      { title: "" },
      { title: "Task" },
      { title: "Task" },
    ]);
    expect(titles[0]).toBe("");
    expect(new Set(titles.slice(1)).size).toBe(2);
  });
});
