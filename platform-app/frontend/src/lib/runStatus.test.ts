import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { isRunComputing, runActivitySummary, runStatusLabel, runStatusTone } from "@/lib/runStatus";
import { AgentRunSchema, type AgentRun } from "@/rpc/platform/service_pb";

function run(fields: MessageInitShape<typeof AgentRunSchema> = {}): AgentRun {
  return create(AgentRunSchema, { phase: "Running", currentStep: "starting", ...fields });
}

describe("run status presentation", () => {
  it("treats idle as ready rather than active computation", () => {
    const item = run({ userInputRequest: { type: "idle", actions: [] } });
    expect(runStatusLabel(item)).toBe("Ready");
    expect(runStatusTone(item)).toBe("neutral");
    expect(isRunComputing(item)).toBe(false);
  });

  it("suppresses stale input state after terminal completion", () => {
    const item = run({ phase: "Succeeded", userInputRequest: { type: "question", actions: [] } });
    expect(runStatusLabel(item)).toBe("Succeeded");
    expect(isRunComputing(item)).toBe(false);
  });

  it("keeps paused authoritative over stale ready input", () => {
    const item = run({ phase: "Paused", userInputRequest: { type: "idle", actions: [] } });
    expect(runStatusLabel(item)).toBe("Paused");
    expect(runStatusTone(item)).toBe("warning");
    expect(isRunComputing(item)).toBe(false);
  });

  it("uses fresh activity but demotes stale activity behind the current step", () => {
    const now = 1_000_000;
    const fresh = run({
      currentStep: "implementing",
      recentActivity: [{ timestampUnix: 999n, summary: "Using Edit" }],
    });
    expect(runActivitySummary(fresh, now).summary).toBe("Using Edit");

    const stale = run({
      currentStep: "implementing",
      recentActivity: [{ timestampUnix: 700n, summary: "Cloning repository" }],
    });
    expect(runActivitySummary(stale, now).summary).toBe("Implementing changes");
  });
});
