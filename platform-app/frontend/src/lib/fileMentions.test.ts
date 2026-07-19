import { describe, expect, it } from "vitest";

import { getMentionQuery, matchWorkspaceFiles } from "./fileMentions";

describe("getMentionQuery", () => {
  it("detects a mention at the caret", () => {
    const text = "look at @src/ma";
    const m = getMentionQuery(text, text.length);
    expect(m).toEqual({ query: "src/ma", start: 8, end: text.length });
  });

  it("returns an empty query right after @", () => {
    const text = "open @";
    const m = getMentionQuery(text, text.length);
    expect(m?.query).toBe("");
    expect(m?.start).toBe(5);
  });

  it("ignores @ that is not at a word boundary (emails)", () => {
    const text = "ping user@host";
    expect(getMentionQuery(text, text.length)).toBeNull();
  });

  it("returns null when whitespace separates the caret from @", () => {
    const text = "@foo bar";
    expect(getMentionQuery(text, text.length)).toBeNull();
  });

  it("matches a mention at the start of input", () => {
    const text = "@README";
    const m = getMentionQuery(text, text.length);
    expect(m).toEqual({ query: "README", start: 0, end: 7 });
  });

  it("resolves the token under the caret, not the end of input", () => {
    const text = "@foo and @bar";
    // caret placed just after "@fo"
    const m = getMentionQuery(text, 3);
    expect(m?.query).toBe("fo");
    expect(m?.start).toBe(0);
  });
});

describe("matchWorkspaceFiles", () => {
  const files = [
    "README.md",
    "src/main.go",
    "src/components/RunSessionFooter.tsx",
    "src/lib/fileMentions.ts",
    "internal/dashboard/server.go",
  ];

  it("ranks filename matches above directory matches", () => {
    const matches = matchWorkspaceFiles(files, "fileMentions");
    expect(matches[0]?.path).toBe("src/lib/fileMentions.ts");
  });

  it("returns no matches when nothing is a subsequence", () => {
    expect(matchWorkspaceFiles(files, "zzzzz")).toHaveLength(0);
  });

  it("fuzzy matches across path separators", () => {
    const matches = matchWorkspaceFiles(files, "srvgo");
    expect(matches.map((m) => m.path)).toContain("internal/dashboard/server.go");
  });

  it("returns shallow paths first for an empty query", () => {
    const matches = matchWorkspaceFiles(files, "");
    expect(matches[0]?.path).toBe("README.md");
  });

  it("respects the limit", () => {
    expect(matchWorkspaceFiles(files, "", 2)).toHaveLength(2);
  });

  it("reports matched positions for highlighting", () => {
    const matches = matchWorkspaceFiles(["src/main.go"], "main");
    expect(matches[0]?.positions.length).toBe(4);
  });
});
