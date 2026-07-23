import { create } from "@bufbuild/protobuf";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { PlanCard } from "@/components/activity-log/InteractionCards";
import { ActivityEntrySchema } from "@/rpc/platform/service_pb";

afterEach(() => {
  cleanup();
});

describe("PlanCard", () => {
  it("opens the current plan in the shared plan dialog", () => {
    const use = create(ActivityEntrySchema, {
      type: "tool_use",
      tool: "present_plan",
      inputRaw: JSON.stringify({
        summary: "A concise plan summary",
        actions: [{ id: "approve", label: "Approve" }],
      }),
    });

    render(
      <PlanCard
        use={use}
        planContent={"## Current plan\n\n- Implement the popup action"}
      />,
    );

    expect(screen.getByText("A concise plan summary")).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Current plan" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "View plan" }));

    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Plan" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Current plan" })).toBeTruthy();
  });
});
