import { describe, expect, it } from "vitest";
import {
  classifySubagentStatus,
  isLiveSubagentStatus,
  isTerminalSubagentStatus,
  isWaitingSubagentStatus,
  subagentStatusLabel,
} from "./subagentStatus";

describe("classifySubagentStatus", () => {
  it("buckets the SDK vocabulary", () => {
    expect(classifySubagentStatus("")).toBe("live");
    expect(classifySubagentStatus("running")).toBe("live");
    expect(classifySubagentStatus("started")).toBe("live");
    expect(classifySubagentStatus("waiting")).toBe("waiting");
    expect(classifySubagentStatus("pending")).toBe("waiting");
    expect(classifySubagentStatus("reconciling")).toBe("waiting");
    expect(classifySubagentStatus("completed")).toBe("succeeded");
    expect(classifySubagentStatus("succeeded")).toBe("succeeded");
    expect(classifySubagentStatus("failed")).toBe("failed");
    expect(classifySubagentStatus("stopped")).toBe("stopped");
    expect(classifySubagentStatus("cancelled")).toBe("stopped");
    expect(classifySubagentStatus("canceled")).toBe("stopped");
  });

  it("treats timeout and error spellings as failures, not success", () => {
    for (const status of ["timeout", "timed_out", "error", "errored", "killed"]) {
      expect(classifySubagentStatus(status)).toBe("failed");
      expect(isTerminalSubagentStatus(status)).toBe(true);
    }
  });

  it("is case- and whitespace-insensitive", () => {
    expect(classifySubagentStatus(" Completed ")).toBe("succeeded");
    expect(classifySubagentStatus("FAILED")).toBe("failed");
  });

  it("fails closed on unknown strings", () => {
    expect(classifySubagentStatus("something_new")).toBe("unknown");
    expect(classifySubagentStatus("constructor")).toBe("unknown");
    expect(isTerminalSubagentStatus("something_new")).toBe(false);
    expect(isLiveSubagentStatus("something_new")).toBe(false);
    expect(isWaitingSubagentStatus("something_new")).toBe(false);
    expect(classifySubagentStatus(undefined)).toBe("live");
    expect(classifySubagentStatus(null)).toBe("live");
  });

  it("labels non-live statuses for badges", () => {
    expect(subagentStatusLabel("failed")).toBe("failed");
    expect(subagentStatusLabel("timeout")).toBe("failed (timeout)");
    expect(subagentStatusLabel("cancelled")).toBe("stopped");
    expect(subagentStatusLabel("waiting")).toBe("waiting");
    expect(subagentStatusLabel("something_new")).toBe("unknown status: something_new");
  });
});
