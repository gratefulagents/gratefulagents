import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { OptionRow, OptionRows } from "@/components/create-flow/create-flow";

afterEach(() => {
  cleanup();
});

describe("OptionRow", () => {
  it("keeps content unmounted while collapsed and shows the summary", () => {
    render(
      <OptionRows label="Options">
        <OptionRow title="Model" summary="Anthropic · saved credentials">
          <input aria-label="Model name" required />
        </OptionRow>
      </OptionRows>,
    );

    // Collapsed: summary visible, content (with its native `required`) absent
    // from the DOM so it cannot block form submission.
    expect(screen.getByText("Anthropic · saved credentials")).toBeTruthy();
    expect(screen.queryByLabelText("Model name")).toBeNull();
  });

  it("expands on click and hides the summary", () => {
    render(
      <OptionRow title="Model" summary="Anthropic">
        <input aria-label="Model name" />
      </OptionRow>,
    );

    fireEvent.click(screen.getByRole("button", { name: /Model/ }));

    expect(screen.getByLabelText("Model name")).toBeTruthy();
    expect(screen.queryByText("Anthropic")).toBeNull();
  });

  it("respects defaultOpen", () => {
    render(
      <OptionRow title="Model" summary="Anthropic" defaultOpen>
        <input aria-label="Model name" />
      </OptionRow>,
    );

    expect(screen.getByLabelText("Model name")).toBeTruthy();
  });
});

describe("modified indicator", () => {
  it("marks customized rows", () => {
    const { container } = render(
      <OptionRow title="Runtime" summary="node:22" modified>
        <div />
      </OptionRow>,
    );

    expect(container.querySelector(".bg-\\[color\\:var\\(--color-primary\\)\\]\\/70")).toBeTruthy();
  });
});
