import { describe, expect, it } from "vitest";

import { REASONING_LEVELS } from "./reasoning";

describe("reasoning levels", () => {
  it("exposes max after xhigh", () => {
    expect(REASONING_LEVELS.slice(-2)).toEqual(["xhigh", "max"]);
  });
});
