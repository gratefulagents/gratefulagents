import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createClient, Code, ConnectError } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import {
  PlatformService,
} from "../../../frontend/src/rpc/platform/service_pb";
import { AuthService } from "../../../frontend/src/rpc/auth/service_pb";
import { defaultScenario } from "../fixtures/default";
import { startFakeBackend, type FakeBackend } from "./fake-backend";

describe("fake backend", () => {
  let backend: FakeBackend;
  let platform: ReturnType<typeof createClient<typeof PlatformService>>;
  let auth: ReturnType<typeof createClient<typeof AuthService>>;

  beforeAll(async () => {
    backend = await startFakeBackend(defaultScenario, { port: 0 });
    const transport = createConnectTransport({ baseUrl: backend.url, httpVersion: "1.1" });
    platform = createClient(PlatformService, transport);
    auth = createClient(AuthService, transport);
  });

  afterAll(async () => {
    await backend.close();
  });

  it("serves runtime metadata", async () => {
    const config = await fetch(`${backend.url}/api/config`);
    expect(config.status).toBe(200);
    expect(await config.json()).toEqual({ authEnabled: true, googleClientId: "" });

    const version = await fetch(`${backend.url}/api/version`);
    expect(version.status).toBe(200);
    expect(await version.json()).toEqual({ version: "0.1.0" });
  });

  it("accepts any login and returns the scenario user", async () => {
    const res = await auth.login({ username: "whoever", password: "whatever" });
    expect(res.accessToken).not.toBe("");
    expect(res.refreshToken).not.toBe("");
    expect(res.user?.email).toBe(defaultScenario.user.email);
  });

  it("login also works over plain JSON POST (how AuthContext calls it)", async () => {
    const res = await fetch(`${backend.url}/auth.v1.AuthService/Login`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ username: "u", password: "p" }),
    });
    expect(res.status).toBe(200);
    const body = (await res.json()) as { accessToken?: string; user?: { email?: string } };
    expect(body.accessToken).toBeTruthy();
    expect(body.user?.email).toBe(defaultScenario.user.email);
  });

  it("lists fixture agent runs", async () => {
    const res = await platform.listAgentRuns({ namespace: "" });
    expect(res.runs.length).toBe(defaultScenario.runs.length);
    const phases = new Set(res.runs.map((r) => r.phase));
    for (const phase of ["Running", "Succeeded", "Failed", "Pending"]) {
      expect(phases).toContain(phase);
    }
  });

  it("serves branch suggestions over the generated RPC contract", async () => {
    const response = await platform.listGitHubBranches({ repoUrl: "https://github.com/acme/repo" });
    expect(response.branches).toEqual(["main", "develop", "release/next"]);
    expect(response.nextPage).toBe(0);
  });

  it("returns NotFound for unknown runs (useAgentRun startup grace expects it)", async () => {
    const err = await platform.getAgentRun({ namespace: "demo", name: "nope" }).catch((e) => e);
    expect(err).toBeInstanceOf(ConnectError);
    expect((err as ConnectError).code).toBe(Code.NotFound);
  });

  it("streams the snapshot on watchAgentRuns and stays open", async () => {
    const controller = new AbortController();
    const events: string[] = [];
    const expected = defaultScenario.runs.length;
    try {
      for await (const ev of platform.watchAgentRuns({ namespace: "" }, { signal: controller.signal })) {
        events.push(`${ev.type}:${ev.run?.name}`);
        if (events.length === expected) controller.abort();
      }
    } catch (err) {
      // Aborting the still-open stream surfaces as a cancellation — expected.
      expect(ConnectError.from(err).code).toBe(Code.Canceled);
    }
    expect(events.length).toBe(expected);
    expect(events[0]).toMatch(/^ADDED:/);
  });

  it("serves activity log fixtures", async () => {
    const res = await platform.getActivityLog({ namespace: "demo", name: "run-ui-polish" });
    expect(res.entries.length).toBeGreaterThan(5);
    expect(res.subagentGraph?.hasSubagents).toBe(true);
  });

  it("defaults unimplemented unary methods to an empty response", async () => {
    const res = await platform.getTeamApprovalStatus({ parent: { namespace: "demo", name: "run-team-refactor" } });
    expect(res.state).toBe("");
  });

  it("applies mutations without leaking into the shared scenario object", async () => {
    await platform.sendAgentRunMessage({ namespace: "demo", name: "run-ui-polish", message: "hi from test" });
    const after = await platform.getAgentRun({ namespace: "demo", name: "run-ui-polish" });
    const original = defaultScenario.runs.find((r) => r.name === "run-ui-polish");
    expect(after.conversation.length).toBe(original!.conversation.length + 1);
    // The imported fixture itself must stay pristine (structuredClone per server).
    expect(original!.conversation.some((m) => m.content === "hi from test")).toBe(false);
  });

  it("creates, updates, and deletes inline skills", async () => {
    const name = "selfdev-inline-skill";

    const created = await platform.upsertSkill({
      name,
      description: "Initial description",
      instructions: "Always verify the result.",
    });
    expect(created.name).toBe(name);
    expect(created.instructions).toBe("Always verify the result.");
    expect((await platform.listSkills({})).skills.some((skill) => skill.name === name)).toBe(true);

    await platform.upsertSkill({
      name,
      description: "Updated description",
      instructions: "Verify twice.",
    });
    const updated = (await platform.listSkills({})).skills.find((skill) => skill.name === name);
    expect(updated?.description).toBe("Updated description");
    expect(updated?.instructions).toBe("Verify twice.");

    await platform.deleteSkill({ name });
    expect((await platform.listSkills({})).skills.some((skill) => skill.name === name)).toBe(false);
  });

  it("serves the resource catalogs and applies name-keyed CRUD", async () => {
    expect((await platform.listMCPServers({})).servers.map((server) => server.name)).toEqual([
      "duckduckgo", "grafana", "postgres-readonly",
    ]);
    expect((await platform.listRuntimeProfiles({})).profiles.length).toBe(3);
    expect((await platform.listGuardrailPolicies({})).policies[0].rules.length).toBe(3);
    expect((await platform.listModeTemplates({})).templates.some((mode) => mode.name === "autopilot")).toBe(true);
    expect((await platform.listRoleInstructions({})).instructions.map((role) => role.name)).toEqual(["explore", "general", "reviewer"]);

    await platform.createGuardrailPolicy({ policy: { name: "zzz-test", rules: [] } });
    const names = (await platform.listGuardrailPolicies({})).policies.map((policy) => policy.name);
    expect(names[names.length - 1]).toBe("zzz-test");
    await expect(platform.createGuardrailPolicy({ policy: { name: "zzz-test", rules: [] } })).rejects.toThrow(/already exists/);
    await expect(platform.updateRuntimeProfile({ profile: { name: "missing" } })).rejects.toThrow(/not found/);
    await platform.deleteGuardrailPolicy({ name: "zzz-test" });
    expect((await platform.listGuardrailPolicies({})).policies.some((policy) => policy.name === "zzz-test")).toBe(false);
  });

  it("pages and searches the skills.sh catalog and installs from it", async () => {
    const first = await platform.listSkillCatalog({ query: "", page: 0 });
    expect(first.skills.length).toBe(5);
    expect(first.hasMore).toBe(true);
    const search = await platform.listSkillCatalog({ query: "grafana", page: 0 });
    expect(search.skills.map((entry) => entry.skillId)).toEqual(["grafana-dashboards"]);

    const installed = await platform.installSkillFromCatalog({ source: "grafana/skills", skillId: "grafana-dashboards" });
    expect(installed.catalogSource).toBe("grafana/skills");
    expect(installed.gitUrl).toBe("");
    expect(installed.instructions).toContain("grafana-dashboards");
    expect((await platform.listSkills({})).skills.some((skill) => skill.name === "grafana-dashboards")).toBe(true);
    await expect(platform.installSkillFromCatalog({ source: "grafana/skills", skillId: "grafana-dashboards" })).rejects.toThrow(/already installed/);
  });
});
