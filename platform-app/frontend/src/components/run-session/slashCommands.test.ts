import { describe, expect, it } from "vitest";

import { buildSlashCommands, filterSlashCommands } from "./slashCommands";
import type { AgentRun, ModeTemplate } from "@/rpc/platform/service_pb";

function run(partial: Partial<Pick<AgentRun, "modeName">>) {
  return { modeName: "", ...partial } as Pick<AgentRun, "modeName">;
}

function mode(name: string, category = ""): ModeTemplate {
  return { k8sName: name, name, category } as ModeTemplate;
}

describe("buildSlashCommands", () => {
  it("offers plan without autonomy toggles", () => {
    const commands = buildSlashCommands(run({ modeName: "autopilot" }), []);
    const triggers = commands.map((c) => c.trigger);
    expect(triggers).toContain("/plan");
    expect(triggers).not.toContain("/autopilot");
    expect(triggers).not.toContain("/stop");
  });

  it("offers exit when in plan mode", () => {
    const commands = buildSlashCommands(run({ modeName: "plan" }), []);
    const triggers = commands.map((c) => c.trigger);
    expect(triggers).toContain("/chat");
    expect(triggers).not.toContain("/plan");
  });

  it("routes plan exit back to autonomous execution", () => {
    const planCommand = buildSlashCommands(run({ modeName: "autopilot" }), []).find(
      (c) => c.trigger === "/plan",
    );
    expect(planCommand?.action).toEqual({ kind: "mode", target: "plan" });
    const exitCommand = buildSlashCommands(run({ modeName: "plan" }), []).find(
      (c) => c.trigger === "/chat",
    );
    expect(exitCommand?.action).toEqual({ kind: "mode", target: "autopilot" });
  });

  it("lists /mode entries for non-dedicated, non-current templates", () => {
    const commands = buildSlashCommands(run({ modeName: "autopilot" }), [
      mode("deep"),
      mode("autopilot"),
      mode("chat"),
      mode("plan"),
    ]);
    const triggers = commands.map((c) => c.trigger);
    expect(triggers).toContain("/mode deep");
    expect(triggers).not.toContain("/mode autopilot");
    expect(triggers).not.toContain("/mode chat");
    expect(triggers).not.toContain("/mode plan");
  });
});

describe("filterSlashCommands", () => {
  const commands = buildSlashCommands(run({ modeName: "autopilot" }), [mode("deep")]);

  it("returns nothing when the input is not a slash command", () => {
    expect(filterSlashCommands(commands, "hello")).toEqual([]);
  });

  it("returns all commands for a bare slash", () => {
    expect(filterSlashCommands(commands, "/").length).toBe(commands.length);
  });

  it("matches by trigger prefix", () => {
    const result = filterSlashCommands(commands, "/pl");
    expect(result.map((c) => c.trigger)).toEqual(["/plan"]);
  });

  it("matches /mode entries by name token", () => {
    const result = filterSlashCommands(commands, "/mode dee");
    expect(result.map((c) => c.trigger)).toContain("/mode deep");
  });
});
