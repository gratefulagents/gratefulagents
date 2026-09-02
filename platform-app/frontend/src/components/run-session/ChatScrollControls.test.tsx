import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { ChatScrollControls } from "./ChatScrollControls";

afterEach(() => {
  cleanup();
});

describe("ChatScrollControls", () => {
  it("labels the scroll-to-bottom button with the number of new messages", () => {
    const onScrollTo = vi.fn();
    render(
      <ChatScrollControls
        show
        isPinnedToTop
        isPinnedToBottom={false}
        onScrollTo={onScrollTo}
        newCount={3}
      />,
    );

    const button = screen.getByRole("button", { name: "Scroll to bottom, 3 new messages" });
    expect(button.textContent).toContain("3 new");
    fireEvent.click(button);
    expect(onScrollTo).toHaveBeenCalledWith("bottom");
  });

  it("stays a plain icon button without new messages or when pinned to the bottom", () => {
    const { rerender } = render(
      <ChatScrollControls show isPinnedToTop isPinnedToBottom={false} onScrollTo={() => {}} />,
    );
    expect(screen.getByRole("button", { name: "Scroll to bottom" }).textContent).toBe("");

    rerender(
      <ChatScrollControls
        show
        isPinnedToTop={false}
        isPinnedToBottom
        onScrollTo={() => {}}
        newCount={5}
      />,
    );
    expect(screen.queryByText(/new/)).toBeNull();
    expect(screen.getByRole("button", { name: "Scroll to top" })).toBeTruthy();
  });
});
