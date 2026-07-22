import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { RunSessionLogsPane } from "./RunSessionLogsPane";

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
    expect(screen.getByText(/Older output was omitted/)).toBeTruthy();

    const wrap = screen.getByRole("button", { name: "Wrap" });
    expect(wrap.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(wrap);
    expect(wrap.getAttribute("aria-pressed")).toBe("true");
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
});
