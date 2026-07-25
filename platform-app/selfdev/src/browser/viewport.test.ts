import { describe, expect, it } from "vitest";
import { parseViewports, VIEWPORT_PRESETS } from "./viewport";

describe("parseViewports", () => {
  it.each(Object.entries(VIEWPORT_PRESETS))("resolves the %s preset", (name, size) => {
    expect(parseViewports(name)).toEqual([{ name, size }]);
  });

  it("expands all responsive presets", () => {
    expect(parseViewports("all")).toEqual([
      { name: "desktop", size: VIEWPORT_PRESETS.desktop },
      { name: "mobile", size: VIEWPORT_PRESETS.mobile },
      { name: "ipad-portrait", size: VIEWPORT_PRESETS["ipad-portrait"] },
      { name: "ipad-landscape", size: VIEWPORT_PRESETS["ipad-landscape"] },
    ]);
  });

  it("accepts a comma-separated mix of presets and custom sizes", () => {
    expect(parseViewports("mobile,1024x768")).toEqual([
      { name: "mobile", size: VIEWPORT_PRESETS.mobile },
      { name: "1024x768", size: { width: 1024, height: 768 } },
    ]);
  });

  it.each(["tablet", "390X844", "0x844", "390x0", "mobile,"])(
    "rejects invalid value %s",
    (value) => expect(() => parseViewports(value)).toThrow(),
  );
});
