import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import type React from "react";

import { PlanApprovalPanel } from "@/components/PlanApprovalPanel";

afterEach(() => {
  cleanup();
});

function renderPanel(props: Partial<React.ComponentProps<typeof PlanApprovalPanel>> = {}) {
  const onSendMessage = vi.fn();

  render(
    <PlanApprovalPanel
      planContent={"## Proposed plan\n\n- Build the approval panel\n- **Render markdown**"}
      onSendMessage={onSendMessage}
      {...props}
    />,
  );

  return { onSendMessage };
}

describe("PlanApprovalPanel", () => {
  it("renders a compact plan-ready bar with a view-plan action", () => {
    renderPanel();

    expect(screen.getByText("Plan ready")).toBeTruthy();
    expect(screen.getByText("View plan")).toBeTruthy();
    // The plan body is not embedded inline; it opens in the shared Plan dialog.
    expect(screen.queryByRole("heading", { name: "Proposed plan" })).toBeNull();
  });

  it("omits the view-plan action when there is no plan content", () => {
    renderPanel({ planContent: "" });

    expect(screen.queryByText("View plan")).toBeNull();
  });

  it("renders one in-place plan approval action", () => {
    renderPanel();

    expect(screen.getByRole("button", { name: "Approve & continue" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Build on auto mode" })).toBeNull();
  });

  it("sends the accept-plan action when approving the plan", () => {
    const { onSendMessage } = renderPanel();

    fireEvent.click(screen.getByRole("button", { name: "Approve & continue" }));

    expect(onSendMessage).toHaveBeenCalledWith("__action:accept_plan");
  });

  it("disables the approval action when disabled", () => {
    renderPanel({ disabled: true });

    expect((screen.getByRole("button", { name: "Approve & continue" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("focuses the approve button on mount only when nothing else is focused", () => {
    renderPanel();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Approve & continue" }));
    cleanup();

    const input = document.createElement("input");
    document.body.appendChild(input);
    input.focus();
    renderPanel();
    expect(document.activeElement).toBe(input);
    input.remove();
  });

  it("renders a request-changes action only when a handler is provided", () => {
    const onRequestChanges = vi.fn();
    renderPanel({ onRequestChanges });
    fireEvent.click(screen.getByRole("button", { name: "Request changes" }));
    expect(onRequestChanges).toHaveBeenCalledTimes(1);
    cleanup();

    renderPanel();
    expect(screen.queryByRole("button", { name: "Request changes" })).toBeNull();
  });
});
