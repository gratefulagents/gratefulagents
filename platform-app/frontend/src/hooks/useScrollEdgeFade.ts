import { useCallback, useEffect, useRef, useState, type CSSProperties } from "react";

/** Width of the fade applied to a clipped edge. */
const FADE_PX = 24;

function maskFor(start: boolean, end: boolean): string | undefined {
  if (!start && !end) return undefined;
  const stops = [
    start ? `transparent 0, #000 ${FADE_PX}px` : "#000 0",
    end ? `#000 calc(100% - ${FADE_PX}px), transparent 100%` : "#000 100%",
  ];
  return `linear-gradient(to right, ${stops.join(", ")})`;
}

/**
 * Fades the clipped edges of a horizontally scrollable strip.
 *
 * Tab bars, settings navs, and table cards scroll sideways once they run out
 * of room, which happens on every phone and on iPad portrait. Their
 * scrollbars are hidden (or overlay-styled), so the overflowing items were
 * simply invisible: a tab could render as "Qu…" or vanish entirely with
 * nothing to suggest the strip scrolls. The fade is a passive affordance —
 * it never blocks pointer or keyboard interaction — and disappears entirely
 * when the content fits, so wide viewports look unchanged.
 *
 * Returns `[ref, style]` to attach to the scroll container itself; no extra
 * wrapper element is needed.
 */
export function useScrollEdgeFade<T extends HTMLElement>() {
  const ref = useRef<T | null>(null);
  const [edges, setEdges] = useState({ start: false, end: false });

  const measure = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    const max = el.scrollWidth - el.clientWidth;
    // `scrollLeft` is negative in RTL, so compare against the travelled
    // distance rather than the raw value.
    const offset = Math.abs(el.scrollLeft);
    const next = { start: offset > 1, end: max > 1 && offset < max - 1 };
    setEdges((prev) => (prev.start === next.start && prev.end === next.end ? prev : next));
  }, []);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    measure();
    el.addEventListener("scroll", measure, { passive: true });
    // Track the container and its children: tab labels stream in
    // asynchronously (counts, badges), which changes scrollWidth after mount.
    // ResizeObserver is absent in jsdom and old WebViews; the fade is a
    // progressive enhancement, so fall back to the mutation observer alone.
    const observer =
      typeof ResizeObserver === "undefined" ? null : new ResizeObserver(measure);
    observer?.observe(el);
    for (const child of Array.from(el.children)) observer?.observe(child);
    const mutations = new MutationObserver(measure);
    mutations.observe(el, { childList: true, subtree: true, characterData: true });
    return () => {
      el.removeEventListener("scroll", measure);
      observer?.disconnect();
      mutations.disconnect();
    };
  }, [measure]);

  const mask = maskFor(edges.start, edges.end);
  const style: CSSProperties | undefined = mask
    ? { maskImage: mask, WebkitMaskImage: mask }
    : undefined;

  return [ref, style] as const;
}
