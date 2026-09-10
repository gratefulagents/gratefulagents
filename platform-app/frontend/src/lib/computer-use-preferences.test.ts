import { afterEach, describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import {
  getComputerUseApprovalMode,
  modeAutoApproves,
  setComputerUseApprovalMode,
  useComputerUseApprovalMode,
} from "./computer-use-preferences";

afterEach(() => setComputerUseApprovalMode("manual"));

describe("computer use approval mode preference", () => {
  it("defaults to manual and only persists an explicit opt-in", () => {
    expect(getComputerUseApprovalMode()).toBe("manual");
    expect(localStorage.getItem("computer-use-approval-mode")).toBeNull();
    setComputerUseApprovalMode("auto");
    expect(localStorage.getItem("computer-use-approval-mode")).toBe("auto");
    setComputerUseApprovalMode("manual");
    expect(localStorage.getItem("computer-use-approval-mode")).toBeNull();
  });

  it("treats unknown stored values as manual", () => {
    localStorage.setItem("computer-use-approval-mode", "yolo");
    expect(getComputerUseApprovalMode()).toBe("manual");
  });

  it("maps modes to the kinds they approve automatically", () => {
    for (const kind of ["observe", "click", "scroll", "type", "key", "activate"] as const) {
      expect(modeAutoApproves("manual", kind)).toBe(false);
      expect(modeAutoApproves("auto", kind)).toBe(true);
    }
    expect(modeAutoApproves("assisted", "observe")).toBe(true);
    expect(modeAutoApproves("assisted", "scroll")).toBe(true);
    expect(modeAutoApproves("assisted", "activate")).toBe(true);
    expect(modeAutoApproves("assisted", "move")).toBe(true);
    expect(modeAutoApproves("assisted", "click")).toBe(false);
    expect(modeAutoApproves("assisted", "drag")).toBe(false);
    expect(modeAutoApproves("assisted", "type")).toBe(false);
    expect(modeAutoApproves("assisted", "key")).toBe(false);
  });

  it("notifies subscribed hooks in the same page and across tabs", () => {
    const { result } = renderHook(() => useComputerUseApprovalMode());
    expect(result.current).toBe("manual");
    act(() => setComputerUseApprovalMode("assisted"));
    expect(result.current).toBe("assisted");
    act(() => {
      localStorage.removeItem("computer-use-approval-mode");
      window.dispatchEvent(new StorageEvent("storage", { key: "computer-use-approval-mode", newValue: null }));
    });
    expect(result.current).toBe("manual");
  });
});
