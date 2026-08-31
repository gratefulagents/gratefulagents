import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { AgentRunSchema, ChatMessageSchema, type AgentRun } from "@/rpc/platform/service_pb";
import {
  deriveStartupStages,
  formatStartupElapsed,
  hasFirstAgentOutput,
  StartupProgress,
} from "./StartupProgress";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function run(overrides: Partial<Pick<AgentRun, "phase" | "currentStep" | "createdAtUnix">>): AgentRun {
  return create(AgentRunSchema, { namespace: "demo", name: "run-x", ...overrides });
}

function statuses(stages: ReturnType<typeof deriveStartupStages>): string[] {
  return (stages ?? []).map((stage) => `${stage.id}:${stage.status}`);
}

describe("deriveStartupStages", () => {
  it("marks the queued stage active while the run is Pending", () => {
    expect(statuses(deriveStartupStages(run({ phase: "Pending" })))).toEqual([
      "queued:active",
      "sandbox:upcoming",
      "workspace:upcoming",
      "working:upcoming",
    ]);
  });

  it("ignores a pre-seeded currentStep before the run reaches Running", () => {
    const stages = deriveStartupStages(run({ phase: "Pending", currentStep: "starting" }));
    expect(stages?.[0].status).toBe("active");
    expect(stages?.[2].status).toBe("upcoming");
  });

  it("marks sandbox startup active for Admitted and Provisioning", () => {
    for (const phase of ["Admitted", "Provisioning"]) {
      expect(statuses(deriveStartupStages(run({ phase })))).toEqual([
        "queued:complete",
        "sandbox:active",
        "workspace:upcoming",
        "working:upcoming",
      ]);
    }
  });

  it("marks workspace prep active while Running on a setup step", () => {
    for (const step of ["cloning-repository", "setting-up-workspace", "setup", "starting"]) {
      expect(statuses(deriveStartupStages(run({ phase: "Running", currentStep: step })))).toEqual([
        "queued:complete",
        "sandbox:complete",
        "workspace:active",
        "working:upcoming",
      ]);
    }
  });

  it("marks the agent as working once Running leaves the setup steps", () => {
    for (const step of ["", "exploring", "implementing"]) {
      expect(statuses(deriveStartupStages(run({ phase: "Running", currentStep: step })))).toEqual([
        "queued:complete",
        "sandbox:complete",
        "workspace:complete",
        "working:active",
      ]);
    }
  });

  it("returns null for unknown or non-startup phases", () => {
    for (const phase of ["", "Paused", "Failed", "Succeeded", "SomethingNew"]) {
      expect(deriveStartupStages(run({ phase }))).toBeNull();
    }
  });
});

describe("hasFirstAgentOutput", () => {
  it("is false with no activity and only user messages", () => {
    expect(hasFirstAgentOutput(0, [])).toBe(false);
    expect(hasFirstAgentOutput(0, [create(ChatMessageSchema, { role: "user", content: "hi" })])).toBe(false);
  });

  it("is true once any activity entry exists", () => {
    expect(hasFirstAgentOutput(1, [])).toBe(true);
  });

  it("is true once a non-user message is delivered", () => {
    expect(
      hasFirstAgentOutput(0, [
        create(ChatMessageSchema, { role: "user", content: "hi" }),
        create(ChatMessageSchema, { role: "assistant", content: "hello" }),
      ]),
    ).toBe(true);
  });
});

describe("formatStartupElapsed", () => {
  it("formats seconds, minutes, and hours", () => {
    expect(formatStartupElapsed(9)).toBe("9s");
    expect(formatStartupElapsed(83)).toBe("1m 23s");
    expect(formatStartupElapsed(3720)).toBe("1h 2m");
  });
});

describe("StartupProgress", () => {
  it("renders the stepper with the current stage highlighted and elapsed time", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2024-01-01T00:01:30Z"));
    const createdAtUnix = BigInt(Math.floor(Date.parse("2024-01-01T00:00:00Z") / 1000));
    render(<StartupProgress run={run({ phase: "Provisioning", createdAtUnix })} />);

    expect(screen.getByText("Starting up")).toBeTruthy();
    expect(screen.getByText("Run queued")).toBeTruthy();
    expect(screen.getByText("Preparing workspace")).toBeTruthy();
    expect(screen.getByText("Agent is working")).toBeTruthy();
    const active = screen.getByText("Starting sandbox").closest("li");
    expect(active?.getAttribute("aria-current")).toBe("step");
    expect(screen.getByText("1m 30s")).toBeTruthy();
  });

  it("omits the elapsed time when the creation timestamp is missing", () => {
    render(<StartupProgress run={run({ phase: "Pending" })} />);
    expect(screen.getByText("Starting up")).toBeTruthy();
    expect(screen.queryByText(/^\d+s$/)).toBeNull();
  });

  it("renders nothing for unknown phases (generic copy fallback)", () => {
    const { container } = render(<StartupProgress run={run({ phase: "SomethingNew" })} />);
    expect(container.innerHTML).toBe("");
  });
});
