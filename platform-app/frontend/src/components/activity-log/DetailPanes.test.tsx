import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";

import { RowDetail } from "./DetailPanes";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

afterEach(() => {
  cleanup();
});

function entry(overrides: Partial<ActivityEntry>): ActivityEntry {
  return {
    type: "tool_use",
    tool: "",
    input: "",
    inputRaw: "",
    message: "",
    output: "",
    isError: false,
    timestampUnix: 1n,
    toolDurationMs: 0n,
    ...overrides,
  } as ActivityEntry;
}

describe("RowDetail (Edit tool)", () => {
  it("renders the unified diff returned by the Edit tool result", () => {
    const use = entry({
      tool: "Edit",
      inputRaw: JSON.stringify({
        file_path: "src/main.go",
        old_string: 'println("old")',
        new_string: 'println("new")',
      }),
    });
    const result = entry({
      type: "tool_result",
      tool: "Edit",
      output: [
        "Successfully edited /workspace/repo/src/main.go",
        "@@ -1,5 +1,5 @@",
        " func main() {",
        '-\tprintln("old")',
        '+\tprintln("new")',
        " }",
      ].join("\n"),
    });

    const { container } = render(<RowDetail use={use} result={result} />);

    expect(screen.getByText("src/main.go")).toBeTruthy();
    expect(screen.getByText("@@ -1,5 +1,5 @@")).toBeTruthy();
    const removed = container.querySelector('[data-kind="delete"]');
    const added = container.querySelector('[data-kind="add"]');
    expect(removed?.textContent).toBe('-\tprintln("old")');
    expect(added?.textContent).toBe('+\tprintln("new")');
  });

  it("falls back to old_string/new_string blocks when the result has no diff", () => {
    const use = entry({
      tool: "Edit",
      inputRaw: JSON.stringify({
        file_path: "notes.txt",
        old_string: "before text",
        new_string: "after text",
      }),
    });
    const result = entry({
      type: "tool_result",
      tool: "Edit",
      output: "Successfully edited notes.txt",
    });

    render(<RowDetail use={use} result={result} />);

    expect(screen.getByText("before text")).toBeTruthy();
    expect(screen.getByText("after text")).toBeTruthy();
  });

  it("shows the error output when the edit failed", () => {
    const use = entry({
      tool: "Edit",
      inputRaw: JSON.stringify({
        file_path: "notes.txt",
        old_string: "missing",
        new_string: "x",
      }),
    });
    const result = entry({
      type: "tool_result",
      tool: "Edit",
      isError: true,
      output: "old_string not found in file",
    });

    render(<RowDetail use={use} result={result} />);

    expect(screen.getByText("old_string not found in file")).toBeTruthy();
  });
});
