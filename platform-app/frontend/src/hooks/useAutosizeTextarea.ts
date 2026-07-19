import { useCallback, useEffect, type RefObject } from "react";

/**
 * Auto-grows a textarea to fit its content up to `maxPx`, after which it
 * scrolls internally. Shared by the message composers (e.g. RunSessionFooter)
 * so multi-line input behaves consistently.
 */
export function useAutosizeTextarea(
  ref: RefObject<HTMLTextAreaElement | null>,
  value: string,
  maxPx = 320,
): void {
  const autosize = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, maxPx)}px`;
  }, [ref, maxPx]);

  useEffect(() => {
    autosize();
  }, [value, autosize]);
}
