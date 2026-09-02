import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { VirtuosoMockContext } from "react-virtuoso";
import { afterEach, describe, expect, it, vi } from "vitest";

import { TraceWaterfallView } from "@/components/TraceWaterfallView";
import { GetAgentTraceResponseSchema, TraceSpanSchema, type TraceSpan } from "@/rpc/platform/service_pb";

function mkSpan(opts: {
  id: string;
  parent?: string;
  kind?: string;
  name?: string;
  start?: number;
  dur?: number;
  isError?: boolean;
}): TraceSpan {
  return create(TraceSpanSchema, {
    spanId: opts.id,
    parentSpanId: opts.parent ?? "",
    operationName: opts.name ?? opts.id,
    kind: opts.kind ?? "tool",
    startTimeUnixUs: BigInt(opts.start ?? 0),
    durationUs: BigInt(opts.dur ?? 1000),
    isError: opts.isError ?? false,
  });
}

function renderTrace(spans: TraceSpan[], isComplete = true) {
  const trace = create(GetAgentTraceResponseSchema, { traceId: "t1", spans, isComplete });
  return render(
    <VirtuosoMockContext.Provider value={{ viewportHeight: 2400, itemHeight: 24 }}>
      <TraceWaterfallView trace={trace} />
    </VirtuosoMockContext.Provider>,
  );
}

// jsdom has no 2D canvas; the minimap bails out when getContext returns null.
HTMLCanvasElement.prototype.getContext = (() => null) as unknown as HTMLCanvasElement["getContext"];

afterEach(cleanup);

describe("TraceWaterfallView", () => {
  it("moves focus between rows with ArrowDown and clears selection with Escape", () => {
    renderTrace([
      mkSpan({ id: "a", name: "first", start: 0, dur: 1000 }),
      mkSpan({ id: "b", name: "second", start: 1000, dur: 1000 }),
      mkSpan({ id: "c", name: "third", start: 2000, dur: 1000, isError: true }),
    ]);

    const items = screen.getAllByRole("treeitem");
    expect(items).toHaveLength(3);
    expect(items[0].getAttribute("tabindex")).toBe("0");
    expect(items[1].getAttribute("tabindex")).toBe("-1");
    expect(items[0].getAttribute("aria-level")).toBe("1");

    act(() => items[0].focus());
    fireEvent.keyDown(items[0], { key: "ArrowDown" });
    expect(document.activeElement).toBe(items[1]);
    expect(items[1].getAttribute("tabindex")).toBe("0");
    expect(items[0].getAttribute("tabindex")).toBe("-1");

    fireEvent.keyDown(items[1], { key: "Enter" });
    expect(items[1].getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("button", { name: "Close details" })).toBeTruthy();

    fireEvent.keyDown(screen.getByRole("tree"), { key: "Escape" });
    expect(screen.queryByRole("button", { name: "Close details" })).toBeNull();
    expect(items[1].getAttribute("aria-selected")).toBe("false");

    // `n` jumps to the next error span and selects it.
    fireEvent.keyDown(items[0], { key: "n" });
    expect(document.activeElement).toBe(items[2]);
    expect(items[2].getAttribute("aria-selected")).toBe("true");
  });

  it("labels expand/collapse controls with the span name", () => {
    renderTrace([mkSpan({ id: "p", name: "parent" }), mkSpan({ id: "c", parent: "p", name: "child", start: 10 })]);
    expect(screen.getByRole("button", { name: "Collapse parent" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Expand all spans" })).toBeTruthy();
    const row = screen.getAllByRole("treeitem")[0];
    expect(row.getAttribute("aria-expanded")).toBe("true");
    fireEvent.keyDown(row, { key: "ArrowLeft" });
    expect(row.getAttribute("aria-expanded")).toBe("false");
    expect(screen.getAllByRole("treeitem")).toHaveLength(1);
  });

  it("keeps the newest rows while the trace is live and the first rows once complete", () => {
    const spans = Array.from({ length: 1600 }, (_, i) =>
      mkSpan({ id: `s${i}`, name: `span-${i}`, start: i * 10, dur: 5 }),
    );
    renderTrace(spans, false);
    expect(screen.getByText(/Live · showing the newest 1,500 of 1,600 rows/)).toBeTruthy();
    const liveFirst = screen.getAllByRole("treeitem")[0];
    expect(liveFirst.textContent).toContain("span-100");
    cleanup();

    renderTrace(spans, true);
    expect(screen.getByText(/Showing the first 1,500 of 1,600 rows/)).toBeTruthy();
    expect(screen.getAllByRole("treeitem")[0].textContent).toContain("span-0");
  });

  it("offers a copy-span-id action in the detail panel", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    renderTrace([mkSpan({ id: "abc123", name: "only" })]);
    fireEvent.click(screen.getAllByRole("treeitem")[0]);
    fireEvent.click(screen.getByRole("button", { name: /Copy span id/ }));
    expect(writeText).toHaveBeenCalledWith("abc123");
  });
});
