import { describe, expect, it } from "vitest";

import {
  diffWords,
  pairChangedRows,
  parseUnifiedDiff,
  summarizeDiff,
  type DiffHunk,
} from "./unifiedDiff";

const modified = [
  "diff --git a/src/app.ts b/src/app.ts",
  "index 1111111..2222222 100644",
  "--- a/src/app.ts",
  "+++ b/src/app.ts",
  "@@ -1,4 +1,5 @@",
  " import a from \"a\";",
  "-const x = 1;",
  "+const x = 2;",
  "+const y = 3;",
  " export { x };",
  " ",
  "@@ -20,2 +21,2 @@ function tail() {",
  "-  return 1;",
  "+  return 2;",
  "",
].join("\n");

describe("parseUnifiedDiff", () => {
  it("returns no files for empty input", () => {
    expect(parseUnifiedDiff("")).toEqual([]);
    expect(parseUnifiedDiff("   \n")).toEqual([]);
  });

  it("parses files, hunks and rows with old/new line numbers", () => {
    const files = parseUnifiedDiff(modified);
    expect(files).toHaveLength(1);
    const file = files[0];
    expect(file.oldPath).toBe("src/app.ts");
    expect(file.newPath).toBe("src/app.ts");
    expect(file.displayPath).toBe("src/app.ts");
    expect(file.status).toBe("modified");
    expect(file.additions).toBe(3);
    expect(file.deletions).toBe(2);
    expect(file.id).toBe("0:src/app.ts");
    expect(file.headerLines).toEqual([
      "diff --git a/src/app.ts b/src/app.ts",
      "index 1111111..2222222 100644",
      "--- a/src/app.ts",
      "+++ b/src/app.ts",
    ]);

    expect(file.hunks).toHaveLength(2);
    const [first, second] = file.hunks;
    expect(first).toMatchObject({ header: "@@ -1,4 +1,5 @@", oldStart: 1, oldLines: 4, newStart: 1, newLines: 5 });
    expect(first.lines).toEqual([
      { kind: "context", oldNo: 1, newNo: 1, text: "import a from \"a\";" },
      { kind: "delete", oldNo: 2, text: "const x = 1;" },
      { kind: "add", newNo: 2, text: "const x = 2;" },
      { kind: "add", newNo: 3, text: "const y = 3;" },
      { kind: "context", oldNo: 3, newNo: 4, text: "export { x };" },
      { kind: "context", oldNo: 4, newNo: 5, text: "" },
    ]);
    expect(second.header).toBe("@@ -20,2 +21,2 @@ function tail() {");
    expect(second.lines[0]).toEqual({ kind: "delete", oldNo: 20, text: "  return 1;" });
    expect(second.lines[1]).toEqual({ kind: "add", newNo: 21, text: "  return 2;" });
  });

  it("normalises CRLF line endings", () => {
    const files = parseUnifiedDiff(modified.replace(/\n/g, "\r\n"));
    expect(files[0].hunks[0].lines[1]).toEqual({ kind: "delete", oldNo: 2, text: "const x = 1;" });
    expect(files[0].additions).toBe(3);
  });

  it("treats empty context lines (stripped trailing whitespace) as context", () => {
    const diff = ["--- a/f", "+++ b/f", "@@ -1,3 +1,3 @@", " a", "", "-b", "+c"].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.hunks[0].lines[1]).toEqual({ kind: "context", oldNo: 2, newNo: 2, text: "" });
    expect(file.hunks[0].lines[2]).toEqual({ kind: "delete", oldNo: 3, text: "b" });
  });

  it("defaults omitted hunk counts to 1", () => {
    const diff = ["--- a/f", "+++ b/f", "@@ -1 +1 @@", "-a", "+b"].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.hunks[0]).toMatchObject({ oldStart: 1, oldLines: 1, newStart: 1, newLines: 1 });
    expect(file.hunks[0].lines).toHaveLength(2);
  });

  it("captures `\\ No newline at end of file` as a meta row", () => {
    const diff = [
      "diff --git a/f b/f",
      "--- a/f",
      "+++ b/f",
      "@@ -1 +1 @@",
      "-old",
      "\\ No newline at end of file",
      "+new",
      "\\ No newline at end of file",
    ].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.hunks[0].lines).toEqual([
      { kind: "delete", oldNo: 1, text: "old" },
      { kind: "meta", text: "No newline at end of file" },
      { kind: "add", newNo: 1, text: "new" },
      { kind: "meta", text: "No newline at end of file" },
    ]);
    expect(file.additions).toBe(1);
    expect(file.deletions).toBe(1);
  });

  it("detects added files", () => {
    const diff = [
      "diff --git a/new.txt b/new.txt",
      "new file mode 100644",
      "index 0000000..e69de29",
      "--- /dev/null",
      "+++ b/new.txt",
      "@@ -0,0 +1,2 @@",
      "+hello",
      "+world",
    ].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.status).toBe("added");
    expect(file.oldPath).toBe("");
    expect(file.newPath).toBe("new.txt");
    expect(file.displayPath).toBe("new.txt");
    expect(file.additions).toBe(2);
    expect(file.hunks[0].lines[1]).toEqual({ kind: "add", newNo: 2, text: "world" });
  });

  it("detects deleted files", () => {
    const diff = [
      "diff --git a/old.txt b/old.txt",
      "deleted file mode 100644",
      "index e69de29..0000000",
      "--- a/old.txt",
      "+++ /dev/null",
      "@@ -1,2 +0,0 @@",
      "-hello",
      "-world",
    ].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.status).toBe("deleted");
    expect(file.displayPath).toBe("old.txt");
    expect(file.deletions).toBe(2);
  });

  it("detects renames with similarity headers and no hunks", () => {
    const diff = [
      "diff --git a/src/a.ts b/src/b.ts",
      "similarity index 100%",
      "rename from src/a.ts",
      "rename to src/b.ts",
    ].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.status).toBe("renamed");
    expect(file.oldPath).toBe("src/a.ts");
    expect(file.newPath).toBe("src/b.ts");
    expect(file.displayPath).toBe("src/a.ts → src/b.ts");
    expect(file.hunks).toEqual([]);
    expect(file.headerLines).toHaveLength(4);
  });

  it("detects renames with content changes", () => {
    const diff = [
      "diff --git a/a.ts b/b.ts",
      "similarity index 90%",
      "rename from a.ts",
      "rename to b.ts",
      "index 1..2 100644",
      "--- a/a.ts",
      "+++ b/b.ts",
      "@@ -1 +1 @@",
      "-x",
      "+y",
    ].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.status).toBe("renamed");
    expect(file.additions).toBe(1);
    expect(file.deletions).toBe(1);
  });

  it("detects binary files", () => {
    const diff = [
      "diff --git a/img.png b/img.png",
      "index 1..2 100644",
      "Binary files a/img.png and b/img.png differ",
    ].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.status).toBe("binary");
    expect(file.displayPath).toBe("img.png");
    expect(file.hunks).toEqual([]);
  });

  it("detects git binary patches", () => {
    const diff = [
      "diff --git a/img.png b/img.png",
      "new file mode 100644",
      "index 0000000..1234567",
      "GIT binary patch",
      "literal 10",
      "zcmZQzU|?",
      "",
    ].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.status).toBe("binary");
  });

  it("keeps mode-only changes as modified files without hunks", () => {
    const diff = [
      "diff --git a/run.sh b/run.sh",
      "old mode 100644",
      "new mode 100755",
    ].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.status).toBe("modified");
    expect(file.displayPath).toBe("run.sh");
    expect(file.headerLines).toEqual(["diff --git a/run.sh b/run.sh", "old mode 100644", "new mode 100755"]);
  });

  it("parses multiple files and assigns stable ids", () => {
    const diff = [
      "diff --git a/a b/a",
      "--- a/a",
      "+++ b/a",
      "@@ -1 +1 @@",
      "-1",
      "+2",
      "diff --git a/b b/b",
      "--- a/b",
      "+++ b/b",
      "@@ -1 +1 @@",
      "-3",
      "+4",
    ].join("\n");
    const files = parseUnifiedDiff(diff);
    expect(files.map((f) => f.id)).toEqual(["0:a", "1:b"]);
    expect(files[1].hunks[0].lines[0]).toEqual({ kind: "delete", oldNo: 1, text: "3" });
  });

  it("parses diffs without `diff --git` headers (plain `---`/`+++`)", () => {
    const diff = [
      "--- a/one.txt\t2024-01-01",
      "+++ b/one.txt\t2024-01-02",
      "@@ -1 +1 @@",
      "-a",
      "+b",
      "--- two.txt",
      "+++ two.txt",
      "@@ -1 +1 @@",
      "-c",
      "+d",
    ].join("\n");
    const files = parseUnifiedDiff(diff);
    expect(files).toHaveLength(2);
    expect(files[0].displayPath).toBe("one.txt");
    expect(files[1].displayPath).toBe("two.txt");
    expect(files[1].hunks[0].lines[1]).toEqual({ kind: "add", newNo: 1, text: "d" });
  });

  it("does not confuse `---`/`+++` content rows inside a hunk with headers", () => {
    const diff = [
      "--- a/f",
      "+++ b/f",
      "@@ -1,2 +1,2 @@",
      "--- dashes",
      "+++ pluses",
      " keep",
    ].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.hunks[0].lines).toEqual([
      { kind: "delete", oldNo: 1, text: "-- dashes" },
      { kind: "add", newNo: 1, text: "++ pluses" },
      { kind: "context", oldNo: 2, newNo: 2, text: "keep" },
    ]);
    expect(parseUnifiedDiff(diff)).toHaveLength(1);
  });

  it("ignores preamble before the first file", () => {
    const diff = ["commit abc", "Author: x", "", ...modified.split("\n")].join("\n");
    const files = parseUnifiedDiff(diff);
    expect(files).toHaveLength(1);
    expect(files[0].displayPath).toBe("src/app.ts");
  });

  it("creates a placeholder file for headerless hunks", () => {
    const [file] = parseUnifiedDiff("@@ -1 +1 @@\n-a\n+b\n");
    expect(file.displayPath).toBe("(unknown file)");
    expect(file.hunks[0].lines).toHaveLength(2);
  });

  it("handles paths containing spaces via ---/+++ headers", () => {
    const diff = [
      "diff --git a/my file.txt b/my file.txt",
      "--- a/my file.txt",
      "+++ b/my file.txt",
      "@@ -1 +1 @@",
      "-a",
      "+b",
    ].join("\n");
    const [file] = parseUnifiedDiff(diff);
    expect(file.displayPath).toBe("my file.txt");
  });
});

describe("summarizeDiff", () => {
  it("totals files, additions and deletions", () => {
    const files = parseUnifiedDiff([
      modified,
      "diff --git a/x b/x",
      "--- a/x",
      "+++ b/x",
      "@@ -1 +1 @@",
      "-1",
      "+2",
    ].join("\n"));
    expect(summarizeDiff(files)).toEqual({ files: 2, additions: 4, deletions: 3 });
    expect(summarizeDiff([])).toEqual({ files: 0, additions: 0, deletions: 0 });
  });
});

describe("diffWords", () => {
  it("returns the changed token ranges on both sides", () => {
    const result = diffWords("const x = 1;", "const x = 2;");
    expect(result.old).toEqual([{ start: 10, end: 11 }]);
    expect(result.new).toEqual([{ start: 10, end: 11 }]);
  });

  it("merges adjacent changed tokens", () => {
    const result = diffWords("return foo(a);", "return bar(a, b);");
    expect(result.old).toEqual([{ start: 7, end: 10 }]);
    expect(result.new).toEqual([
      { start: 7, end: 10 },
      { start: 12, end: 15 },
    ]);
  });

  it("returns nothing for identical lines", () => {
    expect(diffWords("same", "same")).toEqual({ old: [], new: [] });
  });

  it("returns nothing when lines share no meaningful token", () => {
    expect(diffWords("alpha beta", "gamma delta")).toEqual({ old: [], new: [] });
    expect(diffWords("  alpha", "  beta")).toEqual({ old: [], new: [] });
  });

  it("skips lines longer than 400 characters", () => {
    const long = "x ".repeat(250);
    expect(diffWords(long, `${long}y`)).toEqual({ old: [], new: [] });
  });
});

describe("pairChangedRows", () => {
  it("pairs the i-th delete with the i-th add in a change block", () => {
    const hunk: DiffHunk = {
      header: "@@ -1,4 +1,4 @@",
      oldStart: 1,
      oldLines: 4,
      newStart: 1,
      newLines: 4,
      lines: [
        { kind: "context", oldNo: 1, newNo: 1, text: "ctx" },
        { kind: "delete", oldNo: 2, text: "let a = 1;" },
        { kind: "delete", oldNo: 3, text: "let b = 2;" },
        { kind: "add", newNo: 2, text: "let a = 10;" },
        { kind: "add", newNo: 3, text: "let b = 20;" },
        { kind: "add", newNo: 4, text: "let c = 30;" },
      ],
    };
    const highlights = pairChangedRows(hunk);
    expect([...highlights.keys()].sort()).toEqual([1, 2, 3, 4]);
    expect(highlights.get(1)).toEqual([{ start: 8, end: 9 }]);
    expect(highlights.get(3)).toEqual([{ start: 8, end: 10 }]);
    expect(highlights.has(5)).toBe(false);
  });

  it("returns an empty map when a hunk only adds or only deletes", () => {
    const hunk: DiffHunk = {
      header: "@@ -0,0 +1,2 @@",
      oldStart: 0,
      oldLines: 0,
      newStart: 1,
      newLines: 2,
      lines: [
        { kind: "add", newNo: 1, text: "a" },
        { kind: "add", newNo: 2, text: "b" },
      ],
    };
    expect(pairChangedRows(hunk).size).toBe(0);
  });
});
