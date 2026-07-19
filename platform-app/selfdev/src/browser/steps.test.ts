import { describe, expect, it } from "vitest";
import { parseSteps } from "./steps";

describe("parseSteps", () => {
  it("parses a valid step list", () => {
    const steps = parseSteps(
      JSON.stringify([
        { action: "click", selector: "text=Settings" },
        { action: "fill", selector: "#username", value: "dana" },
        { action: "press", key: "Meta+k" },
        { action: "waitFor", selector: "#main-content", state: "visible" },
        { action: "wait", ms: 250 },
        { action: "goto", route: "/settings" },
      ]),
    );
    expect(steps).toHaveLength(6);
    expect(steps[0]).toEqual({ action: "click", selector: "text=Settings" });
  });

  it("rejects non-array JSON", () => {
    expect(() => parseSteps("{}")).toThrow(/expected a JSON array/);
  });

  it("rejects unknown actions", () => {
    expect(() => parseSteps('[{"action":"teleport"}]')).toThrow(/unknown action "teleport"/);
  });

  it("rejects steps missing required fields", () => {
    expect(() => parseSteps('[{"action":"click"}]')).toThrow(/missing string field "selector"/);
    expect(() => parseSteps('[{"action":"fill","selector":"#x"}]')).toThrow(/missing string field "value"/);
    expect(() => parseSteps('[{"action":"wait"}]')).toThrow(/missing numeric field "ms"/);
  });

  it("rejects invalid JSON with a helpful error", () => {
    expect(() => parseSteps("[")).toThrow(/invalid JSON/);
  });
});
