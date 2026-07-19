import { describe, expect, it } from "vitest";

import { getSubagentColor } from "@/lib/subagentColors";

describe("getSubagentColor", () => {
  it("returns the curated color for well-known agents", () => {
    expect(getSubagentColor("Explore").text).toBe("text-blue-400");
    expect(getSubagentColor("Plan").text).toBe("text-cyan-400");
    expect(getSubagentColor("general-purpose").text).toBe("text-purple-400");
  });

  it("is deterministic regardless of call/encounter order", () => {
    const first = getSubagentColor("custom-analyst");
    // Touch several other types to prove there is no order-dependent state.
    getSubagentColor("zzz-other");
    getSubagentColor("another-one");
    const again = getSubagentColor("custom-analyst");
    expect(again).toEqual(first);
  });

  it("maps the same unknown type to the same color across reloads (pure hash)", () => {
    const a = getSubagentColor("data-migrator");
    const b = getSubagentColor("data-migrator");
    expect(a).toBe(b);
  });

  it("falls back to a neutral color when the type is missing", () => {
    expect(getSubagentColor(undefined).text).toBe("text-gray-400");
    expect(getSubagentColor("").text).toBe("text-gray-400");
  });

  it("distributes distinct types across the palette", () => {
    const types = ["a", "b", "c", "d", "e", "f", "g", "h", "i", "j"];
    const colors = new Set(types.map((t) => getSubagentColor(t).dot));
    // Not all identical — the hash spreads them across the 8-color palette.
    expect(colors.size).toBeGreaterThan(1);
  });
});
