import { describe, expect, it } from "vitest";

import { pullRequestLabel, runPullRequestUrls } from "./pullRequests";

describe("pullRequestLabel", () => {
  it("formats GitHub PR URLs as owner/repo#number", () => {
    expect(pullRequestLabel("https://github.com/acme/widgets/pull/123")).toBe("acme/widgets#123");
  });

  it("formats GitLab merge request URLs", () => {
    expect(pullRequestLabel("https://gitlab.com/acme/widgets/merge_requests/9")).toBe("acme/widgets#9");
  });

  it("falls back to the path for non-PR URLs", () => {
    expect(pullRequestLabel("https://github.com/acme/widgets")).toBe("acme/widgets");
  });

  it("returns the raw string for unparseable URLs", () => {
    expect(pullRequestLabel("not a url")).toBe("not a url");
  });
});

describe("runPullRequestUrls", () => {
  it("returns the multi-PR list when present, deduplicated", () => {
    expect(
      runPullRequestUrls({
        pullRequestUrls: [
          "https://github.com/a/b/pull/1",
          "https://github.com/a/b/pull/1",
          "https://github.com/c/d/pull/2",
        ],
        pullRequestUrl: "https://github.com/a/b/pull/1",
      }),
    ).toEqual(["https://github.com/a/b/pull/1", "https://github.com/c/d/pull/2"]);
  });

  it("falls back to the legacy single URL", () => {
    expect(runPullRequestUrls({ pullRequestUrl: "https://github.com/a/b/pull/7" })).toEqual([
      "https://github.com/a/b/pull/7",
    ]);
  });

  it("ignores blank entries", () => {
    expect(runPullRequestUrls({ pullRequestUrls: ["", "  "], pullRequestUrl: "" })).toEqual([]);
  });
});
