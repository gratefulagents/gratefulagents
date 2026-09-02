import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { VirtuosoMockContext } from "react-virtuoso";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { UnifiedDiffViewer } from "./UnifiedDiffViewer";

vi.mock("@/components/ui/toaster", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const diff = [
  "diff --git a/src/app.ts b/src/app.ts",
  "index 1111111..2222222 100644",
  "--- a/src/app.ts",
  "+++ b/src/app.ts",
  "@@ -1,3 +1,3 @@",
  " import a from \"a\";",
  "-const value = 1;",
  "+const value = 2;",
  " export { value };",
  "diff --git a/README.md b/README.md",
  "new file mode 100644",
  "--- /dev/null",
  "+++ b/README.md",
  "@@ -0,0 +1,2 @@",
  "+# Title",
  "+Body",
  "",
].join("\n");

function renderViewer(ui: ReactNode) {
  return render(
    <VirtuosoMockContext.Provider value={{ viewportHeight: 2000, itemHeight: 20 }}>
      {ui}
    </VirtuosoMockContext.Provider>,
  );
}

describe("UnifiedDiffViewer", () => {
  beforeEach(() => {
    globalThis.ResizeObserver ??= class {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
  });

  afterEach(cleanup);

  it("renders file headers, hunks and rows with old/new line numbers", () => {
    renderViewer(<UnifiedDiffViewer diff={diff} isComplete />);

    expect(screen.getByText("app.ts")).toBeTruthy();
    expect(screen.getByText("src/")).toBeTruthy();
    expect(screen.getByText("README.md")).toBeTruthy();
    expect(screen.getByText("@@ -1,3 +1,3 @@")).toBeTruthy();
    expect(screen.getByText("@@ -0,0 +1,2 @@")).toBeTruthy();

    const body = screen.getByTestId("diff-body");
    const deleted = body.querySelector('[data-kind="delete"]');
    const added = body.querySelector('[data-kind="add"]');
    expect(deleted).toBeTruthy();
    expect(added).toBeTruthy();
    const deletedGutters = deleted!.querySelectorAll("span[aria-hidden]");
    expect(deletedGutters[0].textContent).toBe("2");
    expect(deletedGutters[1].textContent).toBe("");
    expect(deletedGutters[2].textContent).toBe("−");
    const addedGutters = added!.querySelectorAll("span[aria-hidden]");
    expect(addedGutters[0].textContent).toBe("");
    expect(addedGutters[1].textContent).toBe("2");
    expect(deleted!.textContent).toContain("const value = 1;");
    expect(added!.textContent).toContain("const value = 2;");

    // Word diff marks the changed token only.
    const marks = body.querySelectorAll("mark");
    expect(marks.length).toBe(2);
    expect([...marks].map((mark) => mark.textContent)).toEqual(["1", "2"]);
    // Syntax highlighting produced hljs tokens.
    expect(body.querySelector(".hljs-keyword")).toBeTruthy();

    expect(screen.getByLabelText("Diff summary").textContent).toBe("2 files·+3 −1");
    expect(screen.getByText("Final")).toBeTruthy();
  });

  it("collapses and expands a file", () => {
    renderViewer(<UnifiedDiffViewer diff={diff} isComplete />);
    const body = screen.getByTestId("diff-body");
    expect(body.querySelectorAll('[data-kind="add"]').length).toBe(3);

    fireEvent.click(screen.getByRole("button", { name: "Collapse README.md" }));
    expect(body.querySelectorAll('[data-kind="add"]').length).toBe(1);
    expect(screen.queryByText("@@ -0,0 +1,2 @@")).toBeNull();
    expect(screen.getByRole("button", { name: "Expand README.md" }).getAttribute("aria-expanded")).toBe("false");

    fireEvent.click(screen.getByRole("button", { name: "Expand README.md" }));
    expect(body.querySelectorAll('[data-kind="add"]').length).toBe(3);
  });

  it("collapses and expands all files from the toolbar", () => {
    renderViewer(<UnifiedDiffViewer diff={diff} isComplete />);
    const body = screen.getByTestId("diff-body");

    fireEvent.click(screen.getByRole("button", { name: "Collapse all files" }));
    expect(body.querySelectorAll("[data-kind]").length).toBe(0);
    expect(screen.getByText("app.ts")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Expand all files" }));
    expect(body.querySelectorAll('[data-kind="add"]').length).toBe(3);
  });

  it("toggles line wrapping", () => {
    renderViewer(<UnifiedDiffViewer diff={diff} />);
    const toggle = screen.getByRole("button", { name: "Wrap lines" });
    const body = screen.getByTestId("diff-body");
    // Wrapped by default: the pane is narrow.
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    expect(body.querySelector('[data-kind="add"] .whitespace-pre-wrap')).toBeTruthy();
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    expect(body.querySelector('[data-kind="add"] .whitespace-pre-wrap')).toBeNull();
  });

  it("copies the patch to the clipboard", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    renderViewer(<UnifiedDiffViewer diff={diff} />);

    fireEvent.click(screen.getByRole("button", { name: "Copy patch" }));
    expect(writeText).toHaveBeenCalledWith(diff);

    fireEvent.click(screen.getByRole("button", { name: "Copy path src/app.ts" }));
    expect(writeText).toHaveBeenCalledWith("src/app.ts");
  });

  it("shows the live badge while incomplete and the source as a title", () => {
    renderViewer(<UnifiedDiffViewer diff={diff} source="workspace" />);
    const badge = screen.getByText("Live");
    expect(badge.getAttribute("title")).toBe("Source: workspace");
  });

  it("renders the loading skeleton when there is no diff yet", () => {
    renderViewer(<UnifiedDiffViewer diff="" loading />);
    expect(screen.getByRole("status", { name: "Loading diff" })).toBeTruthy();
    expect(screen.getByText("Loading")).toBeTruthy();
  });

  it("renders empty states that depend on completion", () => {
    const { unmount } = renderViewer(<UnifiedDiffViewer diff="" />);
    expect(screen.getByText("No changes yet")).toBeTruthy();
    expect(screen.getByText("Changes appear here as the agent edits files.")).toBeTruthy();
    unmount();

    renderViewer(<UnifiedDiffViewer diff="" isComplete />);
    expect(screen.getByText("The run finished without modifying tracked files.")).toBeTruthy();
  });

  it("renders error and unavailable states", () => {
    const { unmount } = renderViewer(<UnifiedDiffViewer diff="" error={new Error("boom")} />);
    expect(screen.getByRole("alert").textContent).toContain("boom");
    expect(screen.getByText("Error")).toBeTruthy();
    unmount();

    renderViewer(<UnifiedDiffViewer diff="" source="unavailable" />);
    expect(screen.getByText("Diff source unavailable")).toBeTruthy();
  });

  it("shows truncation and warning banners alongside a diff", () => {
    renderViewer(<UnifiedDiffViewer diff={diff} truncated error="partial" />);
    expect(screen.getByText(/Diff truncated/)).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("partial");
  });

  it("renders the toolbar slot and wraps the body", () => {
    renderViewer(
      <UnifiedDiffViewer
        diff={diff}
        toolbar={<span>repo picker</span>}
        bodyWrapper={(body) => (
          <div data-testid="wrapper">
            <span>wrapped</span>
            {body}
          </div>
        )}
      />,
    );
    expect(screen.getByText("repo picker")).toBeTruthy();
    const wrapper = screen.getByTestId("wrapper");
    expect(within(wrapper).getByText("wrapped")).toBeTruthy();
    expect(within(wrapper).getByTestId("diff-body")).toBeTruthy();
  });

  it("marks newly arrived hunks for the entrance animation", () => {
    const { rerender } = renderViewer(<UnifiedDiffViewer diff={diff} />);
    expect(document.querySelector(".animate-row-in")).toBeNull();

    const grown = `${diff}diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-a\n+b\n`;
    rerender(
      <VirtuosoMockContext.Provider value={{ viewportHeight: 2000, itemHeight: 20 }}>
        <UnifiedDiffViewer diff={grown} />
      </VirtuosoMockContext.Provider>,
    );
    const fresh = document.querySelectorAll(".animate-row-in");
    expect(fresh.length).toBe(1);
    expect(fresh[0].textContent).toBe("@@ -1 +1 @@");
  });
});
