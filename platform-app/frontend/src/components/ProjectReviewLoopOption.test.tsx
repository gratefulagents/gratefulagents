import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ProjectReviewLoopOption } from "./ProjectReviewLoopOption";

afterEach(cleanup);

describe("ProjectReviewLoopOption", () => {
  it("shows the project policy and reports changes", () => {
    const onDisabledChange = vi.fn();
    render(
      <ProjectReviewLoopOption
        id="review-loop"
        disabled
        modified={false}
        onDisabledChange={onDisabledChange}
      />,
    );

    expect(screen.getByRole("button", { name: /PR review loop/ }).textContent).toContain("Disabled");
    fireEvent.click(screen.getByRole("button", { name: /PR review loop/ }));

    expect(
      screen
        .getByRole("switch", { name: "Disable autonomous PR review loop" })
        .getAttribute("aria-checked"),
    ).toBe("true");
    expect(onDisabledChange).not.toHaveBeenCalled();
    expect(screen.getByText(/additional repositories/)).toBeTruthy();
  });
});
