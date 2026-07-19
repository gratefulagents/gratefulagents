import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { WorkRowView } from "./WorkRows";
import { ActivityDetailProvider } from "./detailContext";
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
    timestampUnix: 1n,
    toolDurationMs: 0n,
    ...overrides,
  } as ActivityEntry;
}

describe("WorkRowView", () => {
  it("renders representative tool event rows", () => {
    render(
      <div>
        <WorkRowView use={entry({ tool: "bash", inputRaw: JSON.stringify({ command: "pnpm test" }) })} />
        <WorkRowView use={entry({ tool: "read_file", inputRaw: JSON.stringify({ path: "src/components/App.tsx" }) })} />
        <WorkRowView use={entry({ tool: "grep", inputRaw: JSON.stringify({ pattern: "shareResource" }) })} />
      </div>,
    );

    expect(screen.getByText("pnpm test")).toBeTruthy();
    expect(screen.getByText("components/App.tsx")).toBeTruthy();
    expect(screen.getByText("shareResource")).toBeTruthy();
  });

  it("shows the actual tool name as the row verb", () => {
    render(
      <div>
        <WorkRowView use={entry({ tool: "read_file", inputRaw: JSON.stringify({ path: "src/App.tsx" }) })} />
        <WorkRowView use={entry({ tool: "Terminal", inputRaw: JSON.stringify({ op: "start" }) })} />
      </div>,
    );

    expect(screen.getByText(/read_file/)).toBeTruthy();
    expect(screen.getByText(/Terminal/)).toBeTruthy();
  });

  it("summarizes unknown tool inputs from well-known argument keys", () => {
    render(
      <WorkRowView
        use={entry({
          tool: "task_create",
          inputRaw: JSON.stringify({ title: "Fix flaky test", priority: 2 }),
        })}
      />,
    );

    expect(screen.getByText("Fix flaky test")).toBeTruthy();
  });

  it("skips shell boilerplate when summarizing a multi-line Bash command", () => {
    render(
      <WorkRowView
        use={entry({
          tool: "Bash",
          inputRaw: JSON.stringify({
            command:
              "set -euo pipefail\n# write the doc page\ncat > docs/guide.md <<'EOF'\n# Guide\nEOF",
          }),
        })}
      />,
    );

    expect(screen.getByText("cat > docs/guide.md <<'EOF'")).toBeTruthy();
  });

  describe("truncated payload expand", () => {
    const truncatedUse = () =>
      entry({
        tool: "mystery_tool",
        inputRaw: "truncated-preview",
        inputTruncated: true,
        eventId: 9n,
        toolUseId: "tu-9",
      });

    it("fetches and renders the full payload when a truncated row is expanded", async () => {
      const fetchDetail = vi.fn().mockResolvedValue({ inputRaw: "FULL_INPUT_PAYLOAD", output: "" });
      render(
        <ActivityDetailProvider value={fetchDetail}>
          <WorkRowView use={truncatedUse()} />
        </ActivityDetailProvider>,
      );

      fireEvent.click(screen.getByRole("button"));

      expect(await screen.findByText(/FULL_INPUT_PAYLOAD/)).toBeTruthy();
      expect(fetchDetail).toHaveBeenCalledTimes(1);
      expect(fetchDetail).toHaveBeenCalledWith(expect.objectContaining({ eventId: 9n, toolUseId: "tu-9" }));
    });

    it("falls back to the truncated preview with a note when the fetch fails", async () => {
      const fetchDetail = vi.fn().mockRejectedValue(new Error("boom"));
      render(
        <ActivityDetailProvider value={fetchDetail}>
          <WorkRowView use={truncatedUse()} />
        </ActivityDetailProvider>,
      );

      fireEvent.click(screen.getByRole("button"));

      expect(await screen.findByText(/Couldn't load the full payload/)).toBeTruthy();
      expect(screen.getByText(/truncated-preview/)).toBeTruthy();
    });

    it("does not fetch details for non-truncated rows", async () => {
      const fetchDetail = vi.fn();
      render(
        <ActivityDetailProvider value={fetchDetail}>
          <WorkRowView use={entry({ tool: "mystery_tool", inputRaw: "small-input", eventId: 9n })} />
        </ActivityDetailProvider>,
      );

      fireEvent.click(screen.getByRole("button"));

      expect(await screen.findByText(/small-input/)).toBeTruthy();
      expect(fetchDetail).not.toHaveBeenCalled();
    });
  });
});
