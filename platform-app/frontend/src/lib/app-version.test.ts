import { describe, expect, it } from "vitest";

import { findVersionMismatch, normalizeAppVersion } from "@/lib/app-version";

describe("native app version compatibility", () => {
  it("normalizes whitespace and a release-tag v prefix", () => {
    expect(normalizeAppVersion(" v1.2.3 ")).toBe("1.2.3");
  });

  it("accepts the same app and server version", () => {
    expect(findVersionMismatch("1.2.3", "v1.2.3")).toBeNull();
  });

  it("reports any release mismatch", () => {
    expect(findVersionMismatch("1.2.2", "1.2.3")).toEqual({
      appVersion: "1.2.2",
      serverVersion: "1.2.3",
    });
  });

  it("ignores development and malformed server versions", () => {
    expect(findVersionMismatch("1.2.3", "dev")).toBeNull();
    expect(findVersionMismatch("1.2.3", "  ")).toBeNull();
  });
});
