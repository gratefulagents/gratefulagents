import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createClient, Code, ConnectError } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-node";
import { PlatformService } from "../../../frontend/src/rpc/platform/service_pb";
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

  it("serves /api/config", async () => {
    const res = await fetch(`${backend.url}/api/config`);
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({ authEnabled: true, googleClientId: "" });
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
});
