import { describe, expect, it } from "vitest";

import { BACKOFF_CAP_MS, backoffDelayMs } from "./backoff";

describe("backoffDelayMs", () => {
  it("grows exponentially with the attempt number", () => {
    const half = () => 0.5;
    expect(backoffDelayMs(0, half)).toBe(500);
    expect(backoffDelayMs(1, half)).toBe(1000);
    expect(backoffDelayMs(2, half)).toBe(2000);
    expect(backoffDelayMs(3, half)).toBe(4000);
  });

  it("caps the ceiling at 30s", () => {
    const max = () => 0.999999;
    expect(backoffDelayMs(10, max)).toBeLessThanOrEqual(BACKOFF_CAP_MS);
    expect(backoffDelayMs(100, () => 0.5)).toBe(BACKOFF_CAP_MS / 2);
  });

  it("applies full jitter down to zero", () => {
    expect(backoffDelayMs(5, () => 0)).toBe(0);
    for (let attempt = 0; attempt < 8; attempt++) {
      const delay = backoffDelayMs(attempt);
      expect(delay).toBeGreaterThanOrEqual(0);
      expect(delay).toBeLessThanOrEqual(Math.min(BACKOFF_CAP_MS, 1000 * 2 ** attempt));
    }
  });
});
