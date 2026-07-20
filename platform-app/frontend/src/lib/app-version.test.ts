import { describe, expect, it } from "vitest";

import { findVersionMismatch, normalizeAppVersion } from "@/lib/app-version";

describe("native app version compatibility", () => {
  it("normalizes whitespace and a release-tag v prefix", () => {
    expect(normalizeAppVersion(" v1.2.3 ")).toBe("1.2.3");
  });

  it("accepts the same app and server version", () => {
    expect(findVersionMismatch("1.2.3", "v1.2.3")).toBeNull();
  });

  it("reports when the server is newer than the app", () => {
    expect(findVersionMismatch("1.2.2", "1.2.3")).toEqual({
      appVersion: "1.2.2",
      serverVersion: "1.2.3",
    });
  });

  it("does not prompt when the server is older than the app", () => {
    expect(findVersionMismatch("0.3.0", "0.1.3")).toBeNull();
  });

  it("compares numeric version parts rather than sorting them as text", () => {
    expect(findVersionMismatch("0.9.0", "0.10.0")).toEqual({
      appVersion: "0.9.0",
      serverVersion: "0.10.0",
    });
    expect(findVersionMismatch("0.10.0", "0.9.0")).toBeNull();
  });

  it("handles prerelease precedence", () => {
    expect(findVersionMismatch("1.2.3-beta.1", "1.2.3")).toEqual({
      appVersion: "1.2.3-beta.1",
      serverVersion: "1.2.3",
    });
    expect(findVersionMismatch("1.2.3", "1.2.3-beta.1")).toBeNull();
  });

  it("ignores development and malformed versions", () => {
    expect(findVersionMismatch("1.2.3", "dev")).toBeNull();
    expect(findVersionMismatch("1.2.3", "  ")).toBeNull();
    expect(findVersionMismatch("1.2.3", "next")).toBeNull();
    expect(findVersionMismatch("unknown", "1.2.3")).toBeNull();
  });
});
