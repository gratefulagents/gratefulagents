import { useEffect, useState } from "react";

/**
 * Re-renders on an interval and returns the current epoch millis.
 * Use for relative timestamps ("3m ago") so they stay fresh on
 * quiet screens with no streaming updates.
 */
export function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);
  return now;
}
