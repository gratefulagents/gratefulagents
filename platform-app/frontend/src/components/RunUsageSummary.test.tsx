import { cleanup, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it } from "vitest";

import { RunUsageBreakdownTable } from "@/components/RunUsageBreakdownTable";
import { RunUsageSummary } from "@/components/RunUsageSummary";
import { UsageTaskSchema, UsageTotalsSchema } from "@/rpc/platform/service_pb";

afterEach(cleanup);

describe("RunUsageSummary", () => {
  it("renders an em dash with a title when token counts are unknown", () => {
    render(<RunUsageSummary totals={create(UsageTotalsSchema, { tokensKnown: false })} />);
    const dashes = screen.getAllByText("—");
    expect(dashes.length).toBeGreaterThanOrEqual(5);
    expect(dashes[0].getAttribute("title")).toMatch(/not reported/);
    expect(screen.queryByText("unknown")).toBeNull();
  });

  it("formats known totals", () => {
    render(<RunUsageSummary totals={create(UsageTotalsSchema, { tokensKnown: true, inputTokens: 12345n, totalTokens: 12345n })} />);
    expect(screen.getAllByText("12,345").length).toBeGreaterThan(0);
  });
});

describe("RunUsageBreakdownTable", () => {
  it("sorts tasks by tokens descending and renders share bars", () => {
    render(
      <RunUsageBreakdownTable
        title="Top-level"
        tasks={[
          create(UsageTaskSchema, { taskId: "small", usage: { tokensKnown: true, totalTokens: 100n } }),
          create(UsageTaskSchema, { taskId: "big", usage: { tokensKnown: true, totalTokens: 300n } }),
          create(UsageTaskSchema, { taskId: "unknown", usage: { tokensKnown: false } }),
        ]}
      />,
    );
    const ids = screen.getAllByRole("row").slice(1).map((row) => row.querySelector("td")?.textContent);
    expect(ids).toEqual(["big", "small", "unknown"]);
    const unknownCell = screen.getAllByRole("row")[3].querySelectorAll("td")[3];
    expect(unknownCell.textContent).toBe("—");
    expect(unknownCell.querySelector("span")?.getAttribute("title")).toMatch(/not reported/);
    const bar = screen.getAllByRole("row")[1].querySelector(".bg-tone-running\\/60") as HTMLElement;
    expect(bar.style.width).toBe("75%");
  });
});
