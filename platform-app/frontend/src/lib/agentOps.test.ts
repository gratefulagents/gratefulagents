import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  canRunAction,
  getRunAttention,
  getRunBucket,
  runComparisonKey,
  runSourcePath,
} from "@/lib/agentOps";
import { AgentRunSchema, type AgentRun } from "@/rpc/platform/service_pb";

function run(fields: MessageInitShape<typeof AgentRunSchema> = {}): AgentRun {
  return create(AgentRunSchema, {
    namespace: "demo",
    name: "run-1",
    phase: "Running",
    myPermission: "owner",
    createdAtUnix: 100n,
    ...fields,
  });
}

describe("agent ops classification", () => {
  it("prioritizes direct user input over other attention signals", () => {
    const item = run({
      phase: "Failed",
      lastError: "build failed",
      userInputRequest: { type: "plan_review", message: "Approve the plan", actions: [] },
    });

    expect(getRunAttention(item)).toMatchObject({ kind: "input", label: "Approval" });
    expect(getRunBucket(item)).toBe("attention");
  });

  it("recognizes failed, blocked review, and runtime expiration", () => {
    expect(getRunAttention(run({ phase: "Failed", lastError: "boom" }))).toMatchObject({ kind: "failed", detail: "boom" });
    expect(getRunAttention(run({ prLoop: { state: "blocked" } }))).toMatchObject({ kind: "review" });
    expect(getRunAttention(run({ blockedReason: "runtime expired" }))).toMatchObject({ kind: "runtime" });
  });

  it("keeps a high-cost running run active instead of marking it as needing attention", () => {
    const expensive = run({ phase: "Running", costUsd: "72.17" });

    expect(getRunAttention(expensive)).toMatchObject({ kind: "none" });
    expect(getRunBucket(expensive)).toBe("active");
  });

  it("does not treat a timeout-paused run as needing attention", () => {
    const paused = run({
      phase: "Paused",
      blockedReason: "paused after 1h0m0s timeout — extend maxRuntime to resume",
    });

    expect(getRunAttention(paused)).toMatchObject({ kind: "none" });
    expect(getRunBucket(paused)).toBe("active");
  });

  it("keeps an actionable request visible even when a run later times out", () => {
    const paused = run({
      phase: "Paused",
      blockedReason: "runtime expired",
      userInputRequest: { type: "plan_review", message: "Approve the plan", actions: [] },
    });

    expect(getRunAttention(paused)).toMatchObject({ kind: "input", label: "Approval" });
  });

  it("surfaces a circuit breaker as an operator blocker", () => {
    const item = run({
      userInputRequest: { type: "circuit_breaker", message: "Repeated tool failures", actions: [] },
    });

    expect(getRunAttention(item)).toMatchObject({ kind: "blocked", label: "Agent needs help", tone: "danger" });
    expect(getRunBucket(item)).toBe("attention");
  });

  it("does not treat non-actionable idle or stopped state as needing attention", () => {
    for (const type of ["idle", "stopped"]) {
      const item = run({
        phase: "Running",
        userInputRequest: { type, message: "The agent can accept another message.", actions: [] },
      });

      expect(getRunAttention(item)).toMatchObject({ kind: "none" });
      expect(getRunBucket(item)).toBe("active");
    }
  });

  it("does not turn the legacy idle queue mirror into a blocked alert", () => {
    const item = run({
      phase: "Running",
      queueState: "Running",
      blockedReason: "idle",
      userInputRequest: { type: "idle", actions: [] },
    });

    expect(getRunAttention(item)).toMatchObject({ kind: "none" });
    expect(getRunBucket(item)).toBe("active");
  });

  it("does not turn the stopped queue mirror into a blocked alert", () => {
    const item = run({
      phase: "Running",
      queueState: "Running",
      blockedReason: "stopped",
      userInputRequest: { type: "stopped", actions: [] },
    });

    expect(getRunAttention(item)).toMatchObject({ kind: "none" });
    expect(getRunBucket(item)).toBe("active");
  });

  it("treats legacy untyped pending actions or prompts as actionable input", () => {
    const withActions = run({
      phase: "Running",
      pendingActions: [{ id: "approve", label: "Approve" }],
    });
    expect(getRunAttention(withActions)).toMatchObject({ kind: "input", label: "Input needed" });

    const withPrompt = run({
      phase: "Running",
      userInputRequest: { type: "", message: "Which branch?", actions: [] },
    });
    expect(getRunAttention(withPrompt)).toMatchObject({ kind: "input", detail: "Which branch?" });
  });

  it("separates queued, active, and completed runs", () => {
    expect(getRunBucket(run({ phase: "Pending", queueState: "Queued" }))).toBe("queued");
    expect(getRunBucket(run({ phase: "Running" }))).toBe("active");
    expect(getRunBucket(run({ phase: "Succeeded" }))).toBe("completed");
  });

  it("does not flag stale interaction state after a successful terminal outcome", () => {
    const succeeded = run({
      phase: "Succeeded",
      userInputRequest: { type: "idle", message: "The agent is waiting for a response.", actions: [] },
      pendingActions: [{ id: "continue", label: "Continue" }],
      blockedReason: "runtime expired",
      prLoop: { state: "blocked", reviewVerdict: "changes_requested" },
    });

    expect(getRunAttention(succeeded)).toMatchObject({ kind: "none" });
    expect(getRunBucket(succeeded)).toBe("completed");
  });
});

describe("agent ops action safety", () => {
  it("matches server-side lifecycle eligibility", () => {
    const active = run();
    expect(canRunAction(active, "stop")).toBe(true);
    expect(canRunAction(active, "promote")).toBe(true);
    expect(canRunAction(active, "extend")).toBe(true);
    expect(canRunAction(active, "retry")).toBe(false);

    const failed = run({ phase: "Failed" });
    expect(canRunAction(failed, "retry")).toBe(true);
    expect(canRunAction(failed, "stop")).toBe(false);
    expect(canRunAction(failed, "extend")).toBe(false);

    const stopped = run({ phase: "Cancelled" });
    expect(canRunAction(stopped, "retry")).toBe(true);
    expect(canRunAction(stopped, "stop")).toBe(false);

    const viewer = run({ myPermission: "viewer" });
    expect(canRunAction(viewer, "stop")).toBe(false);
    expect(canRunAction(viewer, "extend")).toBe(false);
    expect(canRunAction(run({ phase: "Cancelled", myPermission: "viewer" }), "retry")).toBe(false);
  });
});

describe("agent ops links and comparisons", () => {
  it("maps existing source kinds back to dashboard pages", () => {
    expect(runSourcePath(run({ trigger: { kind: "Cron", name: "nightly" } }))).toBe("/cron/demo/nightly");
    expect(runSourcePath(run({ project: { kind: "Project", name: "console" } }))).toBe("/projects/demo/console");
  });

  it("uses only stable external task identity to associate attempts", () => {
    const first = run({ name: "one", trigger: { kind: "GitHubRepository", externalId: "issue-42" } });
    const second = run({ name: "two", trigger: { kind: "GitHubRepository", externalId: "issue-42" } });
    expect(runComparisonKey(first)).toBe(runComparisonKey(second));
    const repoA = run({ trigger: { kind: "GitHubRepository", name: "repo-a", externalIdentifier: "#42" } });
    const repoB = run({ trigger: { kind: "GitHubRepository", name: "repo-b", externalIdentifier: "#42" } });
    expect(runComparisonKey(repoA)).not.toBe(runComparisonKey(repoB));
    const urlA = run({ trigger: { kind: "LinearProject", externalUrl: " HTTPS://LINEAR.APP/ISSUE/OPS-42 " } });
    const urlB = run({ trigger: { kind: "LinearProject", externalUrl: "https://linear.app/issue/ops-42" } });
    const urlOther = run({ trigger: { kind: "LinearProject", externalUrl: "https://linear.app/issue/OPS-43" } });
    expect(runComparisonKey(urlA)).toBe(runComparisonKey(urlB));
    expect(runComparisonKey(urlA)).not.toBe(runComparisonKey(urlOther));
    expect(runComparisonKey(run({ repoUrl: "https://github.com/acme/app", displayName: "Generic task" }))).toBe("");
  });
});
