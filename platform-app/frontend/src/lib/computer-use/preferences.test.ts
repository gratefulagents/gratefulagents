import { beforeEach, describe, expect, it } from "vitest";
import { getDefaultApprovalMode, setDefaultApprovalMode } from "./preferences";

describe("approval mode preference", () => {
  beforeEach(() => localStorage.clear());

  it("defaults to autonomous and drops the legacy key", () => {
    localStorage.setItem("computer-use-desktop-approval-mode", "manual");
    expect(getDefaultApprovalMode()).toBe("auto");
    expect(localStorage.getItem("computer-use-desktop-approval-mode")).toBeNull();
  });

  it("persists ask under the v2 key", () => {
    setDefaultApprovalMode("ask");
    expect(localStorage.getItem("computer-use-approval-mode-v2")).toBe("ask");
    expect(getDefaultApprovalMode()).toBe("ask");
    setDefaultApprovalMode("auto");
    expect(localStorage.getItem("computer-use-approval-mode-v2")).toBeNull();
  });
});
