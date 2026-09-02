import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { WorkCard, WorkRowView } from "./WorkRows";
import type { WorkItem } from "./types";
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

  it("renders rows without a detail body as non-interactive, unfocusable text", () => {
    render(<WorkRowView use={entry({ tool: "read_file" })} />);

    expect(screen.queryByRole("button")).toBeNull();
    const row = screen.getByText(/read_file/).closest("[title]");
    expect(row?.tagName).toBe("DIV");
    expect(row?.hasAttribute("aria-expanded")).toBe(false);
    expect(row?.hasAttribute("tabindex")).toBe(false);
  });

  it("exposes a real disclosure only when there is something to expand", () => {
    render(<WorkRowView use={entry({ tool: "grep", inputRaw: JSON.stringify({ pattern: "x" }) })} />);

    const button = screen.getByRole("button");
    expect(button.getAttribute("aria-expanded")).toBe("false");
    const panelId = button.getAttribute("aria-controls");
    expect(panelId).toBeTruthy();
    expect(document.getElementById(panelId!)?.hasAttribute("inert")).toBe(true);

    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(document.getElementById(panelId!)?.hasAttribute("inert")).toBe(false);
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

describe("WorkCard (live)", () => {
  function item(entries: ActivityEntry[]): WorkItem {
    const units: WorkItem["units"] = [];
    for (const e of entries) {
      if (e.type === "tool_use") {
        const result = entries.find((r) => r.type === "tool_result" && r.toolUseId === e.toolUseId);
        units.push({ kind: "row", use: e, result });
      } else if (e.type === "llm_attempt") {
        units.push({ kind: "system", entries: [e] });
      }
    }
    return { kind: "work", units, entries };
  }

  it("titles the in-flight call and counts only finished calls in the summary", () => {
    const entries = [
      entry({ tool: "finalize_work_item", toolUseId: "a", inputRaw: '{"id":58}' }),
      entry({ type: "tool_result", toolUseId: "a", output: "ok" }),
      entry({ tool: "submit_maintainer_report", toolUseId: "b", inputRaw: '{"summary":"Merged PR #101"}' }),
    ];
    render(<WorkCard item={item(entries)} live />);
    expect(screen.getByText("Running submit_maintainer_report…")).toBeTruthy();
    expect(screen.getByText("1× finalize_work_item")).toBeTruthy();
    expect(screen.queryByText(/submit_maintainer_report$/)).toBeNull();
  });

  it("marks the last call as finished once its result is in", () => {
    const entries = [
      entry({ tool: "wait_for_repo_events", toolUseId: "a", inputRaw: '{"since":"latest"}' }),
      entry({ type: "tool_result", toolUseId: "a", output: "ok", toolDurationMs: 364n }),
    ];
    render(<WorkCard item={item(entries)} live />);
    expect(screen.getByText("Working…")).toBeTruthy();
    expect(screen.getByText(/^Finished wait_for_repo_events/)).toBeTruthy();
  });

  it("folds system events into a single trailing row when expanded", () => {
    const entries = [
      entry({ tool: "request_merge", toolUseId: "a" , inputRaw: "{}" }),
      entry({ type: "llm_attempt" }),
      entry({ type: "llm_attempt" }),
      entry({ type: "tool_result", toolUseId: "a", output: "ok" }),
      entry({ tool: "wait_for_repo_events", toolUseId: "b", inputRaw: "{}" }),
      entry({ type: "llm_attempt" }),
      entry({ type: "tool_result", toolUseId: "b", output: "ok" }),
    ];
    render(<WorkCard item={item(entries)} live={false} />);
    fireEvent.click(screen.getByRole("button", { expanded: false }));
    expect(screen.getAllByText(/system events/)).toHaveLength(1);
    expect(screen.getByText(/3 system events/)).toBeTruthy();
  });
});
