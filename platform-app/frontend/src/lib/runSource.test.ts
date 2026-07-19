import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { AgentRunSchema, type AgentRun } from "@/rpc/platform/service_pb";
import { projectRunKey, runSourceLabel } from "@/lib/runSource";

function run(namespace: string, project?: { kind: string; name: string }): AgentRun {
  return create(AgentRunSchema, {
    namespace,
    name: "run-1",
    project,
  });
}

describe("projectRunKey", () => {
  it("keys Project runs by run namespace + project name", () => {
    expect(projectRunKey(run("team-a", { kind: "Project", name: "test" }))).toBe("team-a/test");
  });

  it("accepts refs without an explicit kind", () => {
    expect(projectRunKey(run("team-a", { kind: "", name: "test" }))).toBe("team-a/test");
  });

  it("excludes trigger-owned runs even when names collide with a project", () => {
    expect(projectRunKey(run("team-a", { kind: "SlackAgent", name: "test" }))).toBeNull();
    expect(projectRunKey(run("team-a", { kind: "Cron", name: "test" }))).toBeNull();
    expect(projectRunKey(run("team-a", { kind: "GitHubRepository", name: "test" }))).toBeNull();
    expect(projectRunKey(run("team-a", { kind: "LinearProject", name: "test" }))).toBeNull();
  });

  it("excludes runs without a project ref", () => {
    expect(projectRunKey(run("team-a"))).toBeNull();
  });
});

describe("runSourceLabel", () => {
  it("uses the bare name for Project runs", () => {
    expect(runSourceLabel(run("team-a", { kind: "Project", name: "test" }))).toBe("test");
  });

  it("prefixes trigger runs with a friendly kind", () => {
    expect(runSourceLabel(run("team-a", { kind: "SlackAgent", name: "test" }))).toBe("Slack · test");
    expect(runSourceLabel(run("team-a", { kind: "GitHubRepository", name: "api" }))).toBe("GitHub · api");
  });

  it("falls back to the raw kind for unknown triggers", () => {
    expect(runSourceLabel(run("team-a", { kind: "Webhook", name: "hook" }))).toBe("Webhook · hook");
  });

  it("returns empty for runs without a source", () => {
    expect(runSourceLabel(run("team-a"))).toBe("");
  });
});
