import { describe, expect, it } from "vitest";

import { extractUnifiedDiff, firstMeaningfulLine, formatWall, wallSeconds } from "./activityLogFormat";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

function entry(timestampUnix: bigint): ActivityEntry {
  return { timestampUnix } as ActivityEntry;
}

describe("wallSeconds", () => {
  it("returns the span between the earliest and latest timestamps", () => {
    expect(wallSeconds([entry(100n), entry(130n), entry(117n)])).toBe(30);
  });

  it("ignores unset (zero) timestamps", () => {
    expect(wallSeconds([entry(0n), entry(100n), entry(160n)])).toBe(60);
  });

  it("ignores negative timestamps from unset year-1 times", () => {
    // time.Time{}.Unix() == -62135596800 must not poison the span.
    expect(wallSeconds([entry(-62135596800n), entry(1775477147n)])).toBe(0);
    expect(
      wallSeconds([entry(-62135596800n), entry(1775477100n), entry(1775477160n)]),
    ).toBe(60);
  });

  it("returns 0 when no valid timestamps are present", () => {
    expect(wallSeconds([entry(0n), entry(-5n)])).toBe(0);
  });
});

describe("formatWall", () => {
  it("formats sub-minute, minute and hour spans", () => {
    expect(formatWall(0)).toBe("");
    expect(formatWall(17)).toBe("17s");
    expect(formatWall(125)).toBe("2m 5s");
    expect(formatWall(3720)).toBe("1h 2m");
  });
});

describe("firstMeaningfulLine", () => {
  it("skips shebangs, comments, blanks, and set boilerplate", () => {
    const cmd =
      "#!/bin/bash\nset -euo pipefail\n\n# create the page\ncat > docs/guide.md <<'EOF'\nbody\nEOF";
    expect(firstMeaningfulLine(cmd)).toBe("cat > docs/guide.md <<'EOF'");
  });

  it("falls back to the first line when everything is boilerplate", () => {
    expect(firstMeaningfulLine("set -euo pipefail")).toBe("set -euo pipefail");
  });
});

describe("extractUnifiedDiff", () => {
  it("returns the hunks from an Edit tool result", () => {
    const output = [
      "Successfully edited /workspace/repo/main.go",
      "@@ -1,5 +1,5 @@",
      " func main() {",
      '-\tprintln("old")',
      '+\tprintln("new")',
      " }",
    ].join("\n");
    expect(extractUnifiedDiff(output)).toBe(
      [
        "@@ -1,5 +1,5 @@",
        " func main() {",
        '-\tprintln("old")',
        '+\tprintln("new")',
        " }",
      ].join("\n"),
    );
  });

  it("keeps multiple hunks and the truncation note", () => {
    const output = [
      "Successfully replaced 2 occurrences in notes.txt",
      "@@ -1,2 +1,2 @@",
      "-a",
      "+b",
      "@@ -10,2 +10,2 @@",
      "-a",
      "+b",
      "... [diff truncated]",
    ].join("\n");
    expect(extractUnifiedDiff(output).split("\n")).toHaveLength(7);
  });

  it("returns empty for outputs without hunks", () => {
    expect(extractUnifiedDiff("Successfully edited main.go")).toBe("");
    expect(extractUnifiedDiff("")).toBe("");
    expect(extractUnifiedDiff("old_string not found in file")).toBe("");
  });

  it("ignores @@-like text that is not a hunk header", () => {
    expect(extractUnifiedDiff("mentions @@ -not a hunk @@ inline")).toBe("");
  });
});
