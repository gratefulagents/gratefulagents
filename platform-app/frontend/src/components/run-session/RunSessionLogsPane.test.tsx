import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { RunSessionLogsPane } from "./RunSessionLogsPane";

afterEach(cleanup);

describe("RunSessionLogsPane", () => {
  it("renders worker output and controls", () => {
    render(
      <RunSessionLogsPane
        content={"2026-06-18T10:14:01Z INFO worker started\n"}
        podName="demo-worker"
        available
        loading={false}
        error={null}
        truncated
        lastUpdated={new Date("2026-06-18T10:15:00Z")}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByText("Worker logs")).toBeTruthy();
    expect(screen.getByText("demo-worker")).toBeTruthy();
    expect(screen.getByText(/INFO worker started/)).toBeTruthy();
    expect(screen.getByText(/Most recent 2,000 lines/)).toBeTruthy();

    const wrap = screen.getByRole("button", { name: "Wrap" });
    expect(wrap.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(wrap);
    expect(wrap.getAttribute("aria-pressed")).toBe("false");
  });

  it("explains when the worker pod is unavailable", () => {
    render(
      <RunSessionLogsPane
        content=""
        podName=""
        available={false}
        loading={false}
        error={null}
        truncated={false}
        lastUpdated={null}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByText("Worker logs are unavailable")).toBeTruthy();
    expect(screen.getByText(/may still be starting/)).toBeTruthy();
  });

  it("filters lines with highlighting and shows a line-number gutter", () => {
    render(
      <RunSessionLogsPane
        content={"alpha one\nbeta two\ngamma three\n"}
        podName=""
        available
        loading={false}
        error={null}
        truncated={false}
        lastUpdated={null}
        onRefresh={vi.fn()}
      />,
    );

    expect(screen.getByText("3")).toBeTruthy(); // gutter for the third line
    fireEvent.change(screen.getByLabelText("Filter log lines"), { target: { value: "beta" } });
    expect(screen.queryByText(/alpha one/)).toBeNull();
    const mark = screen.getByText("beta");
    expect(mark.tagName).toBe("MARK");
    expect(screen.getByText("1/3")).toBeTruthy();
  });

  it("shows a jump-to-latest pill when scrolled away from the bottom and follows again on click", () => {
    render(
      <RunSessionLogsPane
        content={Array.from({ length: 50 }, (_, i) => `line ${i}`).join("\n")}
        podName=""
        available
        loading={false}
        error={null}
        truncated={false}
        lastUpdated={null}
        onRefresh={vi.fn()}
      />,
    );

    const scroller = screen.getByTestId("log-scroller");
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 1000 });
    Object.defineProperty(scroller, "clientHeight", { configurable: true, value: 200 });
    expect(screen.queryByRole("button", { name: /Jump to latest/ })).toBeNull();

    scroller.scrollTop = 100;
    act(() => {
      fireEvent.scroll(scroller);
    });
    const pill = screen.getByRole("button", { name: /Jump to latest/ });

    fireEvent.click(pill);
    expect(scroller.scrollTop).toBe(1000);
    expect(screen.queryByRole("button", { name: /Jump to latest/ })).toBeNull();
  });
});
