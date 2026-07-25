import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { useScrollEdgeFade } from "./useScrollEdgeFade";

/**
 * jsdom reports 0 for every layout box, so these tests cover the contract the
 * hook must honor rather than pixel behavior: it never crashes without a
 * ResizeObserver, and it applies no mask while the content fits (so wide
 * viewports look exactly as they did before).
 */
function Strip() {
  const [ref, style] = useScrollEdgeFade<HTMLDivElement>();
  return (
    <div ref={ref} style={style} data-testid="strip">
      tabs
    </div>
  );
}

describe("useScrollEdgeFade", () => {
  afterEach(cleanup);

  it("applies no mask when the strip is not scrollable", () => {
    render(<Strip />);
    const strip = screen.getByTestId("strip");
    expect(strip.getAttribute("style") ?? "").not.toContain("mask-image");
  });

  it("renders without a ResizeObserver implementation", () => {
    const original = globalThis.ResizeObserver;
    // @ts-expect-error -- deliberately emulating an environment without it.
    delete globalThis.ResizeObserver;
    try {
      expect(() => render(<Strip />)).not.toThrow();
    } finally {
      globalThis.ResizeObserver = original;
    }
  });

  it("fades only the trailing edge once content overflows", () => {
    render(<Strip />);
    const strip = screen.getByTestId("strip");
    Object.defineProperty(strip, "scrollWidth", { value: 600, configurable: true });
    Object.defineProperty(strip, "clientWidth", { value: 300, configurable: true });
    act(() => { strip.dispatchEvent(new Event("scroll")); });
    expect(strip.getAttribute("style") ?? "").toContain("transparent 100%");
    expect(strip.getAttribute("style") ?? "").toContain("#000 0,");
  });

  it("fades both edges when scrolled to the middle", () => {
    render(<Strip />);
    const strip = screen.getByTestId("strip");
    Object.defineProperty(strip, "scrollWidth", { value: 600, configurable: true });
    Object.defineProperty(strip, "clientWidth", { value: 300, configurable: true });
    Object.defineProperty(strip, "scrollLeft", { value: 100, configurable: true });
    act(() => { strip.dispatchEvent(new Event("scroll")); });
    expect(strip.getAttribute("style") ?? "").toContain("transparent 0");
    expect(strip.getAttribute("style") ?? "").toContain("transparent 100%");
  });
});
