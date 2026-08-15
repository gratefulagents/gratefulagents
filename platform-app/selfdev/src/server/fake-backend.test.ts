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

  it("installs the security skill bundle only after the mutation", async () => {
    const before = await platform.getSecuritySkillsStatus({});
    expect(before.state).toBe("not_installed");
    expect(before.installedCount).toBe(0);
    expect(before.availableCount).toBeGreaterThan(0);

    const installed = await platform.installSecuritySkills({});
    expect(installed.state).toBe("installed");
    expect(installed.installedCount).toBe(installed.availableCount);

    const after = await platform.getSecuritySkillsStatus({});
    expect(after.state).toBe("installed");
    // The backend clones fixtures; interactive mutation must not leak back.
    expect(defaultScenario.securitySkillsInstalled).toBe(false);
  });

  it("lists bug report fixtures and applies status/category filters", async () => {
    const all = await platform.listBugReports({ namespace: "" });
    expect(all.reports.length).toBe(defaultScenario.bugReports.length);

    const open = await platform.listBugReports({ namespace: "demo", status: "open" });
    expect(open.reports.length).toBeGreaterThan(0);
    expect(open.reports.every((r) => r.status === "open")).toBe(true);

    const bugs = await platform.listBugReports({ namespace: "demo", category: "bug" });
    expect(bugs.reports.every((r) => r.category === "bug")).toBe(true);
  });

  it("updates bug report status without leaking into the shared scenario object", async () => {
    const target = defaultScenario.bugReports.find((r) => r.status === "open")!;
    const updated = await platform.updateBugReportStatus({
      namespace: "demo",
      id: target.id,
      status: "acknowledged",
      note: "triaged in test",
    });
    expect(updated.status).toBe("acknowledged");
    expect(updated.statusNote).toBe("triaged in test");
    expect(updated.statusActor).toBe(defaultScenario.user.username);

    const after = await platform.listBugReports({ namespace: "demo", status: "acknowledged" });
    expect(after.reports.some((r) => r.id === target.id)).toBe(true);
    // The imported fixture itself must stay pristine (structuredClone per server).
    expect(target.status).toBe("open");
  });

  it("filters security findings by severity, status, baseline, and suppression", async () => {
    const all = await platform.listSecurityFindings({ namespace: "demo" });
    // Duplicates and governed-suppressed findings are excluded by default.
    expect(all.findings.every((f) => f.duplicateOf === "" && f.suppressedBy === "")).toBe(true);

    const critical = await platform.listSecurityFindings({ namespace: "demo", severity: "critical" });
    expect(critical.findings.length).toBeGreaterThan(0);
    expect(critical.findings.every((f) => f.severity === "critical")).toBe(true);

    const triaged = await platform.listSecurityFindings({ namespace: "demo", status: "triaged" });
    expect(triaged.findings.every((f) => f.status === "triaged")).toBe(true);

    // "actionable" groups the statuses that still require remediation.
    const actionable = await platform.listSecurityFindings({ namespace: "demo", status: "actionable" });
    expect(actionable.findings.length).toBeGreaterThan(triaged.findings.length);
    expect(
      actionable.findings.every((f) => ["open", "triaged", "confirmed"].includes(f.status)),
    ).toBe(true);

    const recurring = await platform.listSecurityFindings({ namespace: "demo", baselineState: "recurring" });
    expect(recurring.findings.every((f) => f.baselineState === "recurring")).toBe(true);

    const suppressed = await platform.listSecurityFindings({ namespace: "demo", suppressed: "only" });
    expect(suppressed.findings.length).toBeGreaterThan(0);
    expect(suppressed.findings.every((f) => f.suppressedBy !== "")).toBe(true);

    const dupes = await platform.listSecurityFindings({ namespace: "demo", includeDuplicates: true });
    expect(dupes.findings.some((f) => f.duplicateOf !== "")).toBe(true);

    const oneRun = await platform.listSecurityFindings({ namespace: "demo", runName: "nightly-webapp-3" });
    expect(oneRun.findings.length).toBeGreaterThan(0);
    expect(oneRun.findings.every((f) => f.runName === "nightly-webapp-3")).toBe(true);
  });

  it("summarizes findings with severity, open, and baseline counters", async () => {
    const summary = await platform.getSecurityFindingSummary({ namespace: "demo" });
    const visible = defaultScenario.securityFindings.filter((f) => f.suppressedBy === "");
    expect(summary.counts.total).toBe(visible.length);
    expect(summary.counts.critical).toBeGreaterThan(0);
    expect(summary.counts.open_critical).toBeGreaterThan(0);
    // "open" is the persisted status; "actionable" also folds in triaged and
    // confirmed, exactly like the Postgres summary.
    expect(summary.counts.actionable).toBeGreaterThan(summary.counts.open);
    expect(summary.counts.actionable_high).toBeGreaterThan(0);
    expect(summary.counts.source_scanner).toBeGreaterThan(0);
    expect(summary.counts.baseline_new).toBeGreaterThan(0);
    expect(summary.counts.suppressed).toBeGreaterThan(0);
    expect(summary.trends?.triagedCount).toBeGreaterThan(0);

    const scoped = await platform.getSecurityFindingSummary({
      namespace: "demo",
      runName: "nightly-webapp-4",
    });
    expect(scoped.counts.total).toBeLessThan(summary.counts.total);
  });

  it("derives the security overview from the scenario scans and configs", async () => {
    const overview = await platform.getSecurityOverview({ namespace: "demo" });
    expect(overview.storeSupported).toBe(true);
    expect(overview.activeScans.every((x) => x.completedAt === undefined)).toBe(true);
    expect(overview.activeScans.length).toBeGreaterThan(0);
    expect(overview.recentScans.length).toBeGreaterThan(0);
    expect(overview.recentScans.every((x) => x.completedAt !== undefined)).toBe(true);
    expect(overview.configCount).toBe(defaultScenario.securityScanConfigs.length);
    expect(overview.configIssues.length).toBeGreaterThan(0);
    expect(overview.configIssues.some((issue) => issue.suspended)).toBe(true);
    expect(overview.baselineAvailable).toBe(true);
    expect(overview.newFindings).toBeGreaterThan(0);
  });

  it("derives config postures with per-config activity", async () => {
    const res = await platform.getSecurityConfigPostures({ namespace: "demo" });
    expect(res.storeSupported).toBe(true);
    expect(res.postures.length).toBeGreaterThan(0);
    const names = res.postures.map((p) => p.scanName);
    expect([...names].sort()).toEqual(names);
    const nightly = res.postures.find((p) => p.scanName === "nightly-webapp");
    expect(nightly?.lastRunName).toBe("nightly-webapp-5");
    expect(nightly?.activity.length).toBeGreaterThan(0);
  });

  it("serves one finding with its synthesized audit trail", async () => {
    const id = "5d0a6f1e-2222-4bde-9a51-aa10c3b4d902";
    const res = await platform.getSecurityFinding({ id, namespace: "demo", scanName: "nightly-webapp" });
    expect(res.finding?.id).toBe(id);
    expect(res.events.length).toBeGreaterThan(1);
    // Newest first, and the oldest entry is always the detection.
    expect(res.events[res.events.length - 1]?.eventType).toBe("detected");

    const events = await platform.listSecurityFindingEvents({ id, namespace: "demo" });
    expect(events.events.map((e) => e.eventType)).toEqual(res.events.map((e) => e.eventType));
  });

  it("returns NotFound for unknown security lookups", async () => {
    const lookups = [
      platform.getSecurityScan({ namespace: "demo", runName: "nope" }),
      platform.getSecurityScanConfig({ namespace: "demo", name: "nope" }),
      platform.getSecurityWorkflow({ namespace: "demo", name: "nope" }),
      platform.getSecurityRanker({ namespace: "demo", name: "nope" }),
      platform.getSecurityPostScript({ namespace: "demo", name: "nope" }),
      platform.getSecurityPolicyPack({ namespace: "demo", name: "nope" }),
      platform.getSecurityProgram({ namespace: "demo", name: "nope" }),
      platform.getSecurityFinding({ id: "nope", namespace: "demo" }),
      platform.listSecurityFindingEvents({ id: "nope", namespace: "demo" }),
      platform.getSecurityScanReport({ namespace: "demo", runName: "nope" }),
      // A finding read through another scan's route must not resolve.
      platform.getSecurityFinding({
        id: "5d0a6f1e-1111-4bde-9a51-aa10c3b4d901",
        namespace: "demo",
        scanName: "billing-api-weekly",
      }),
    ];
    for (const lookup of lookups) {
      const err = await lookup.catch((e) => e);
      expect(err).toBeInstanceOf(ConnectError);
      expect((err as ConnectError).code).toBe(Code.NotFound);
    }
  });

  it("serves the security library resources scans reference", async () => {
    const [workflows, rankers, postScripts, packs, programs, filters] = await Promise.all([
      platform.listSecurityWorkflows({ namespace: "demo" }),
      platform.listSecurityRankers({ namespace: "demo" }),
      platform.listSecurityPostScripts({ namespace: "demo" }),
      platform.listSecurityPolicyPacks({ namespace: "demo" }),
      platform.listSecurityPrograms({ namespace: "demo" }),
      platform.listSecuritySavedFilters({ namespace: "demo" }),
    ]);
    expect(workflows.workflows.length).toBe(defaultScenario.securityWorkflows.length);
    expect(rankers.rankers.length).toBe(defaultScenario.securityRankers.length);
    expect(postScripts.postScripts.length).toBe(defaultScenario.securityPostScripts.length);
    expect(packs.policyPacks.length).toBe(defaultScenario.securityPolicyPacks.length);
    expect(programs.programs.length).toBe(defaultScenario.securityPrograms.length);
    expect(filters.filters.length).toBe(defaultScenario.securitySavedFilters.length);

    const program = await platform.getSecurityProgram({ namespace: "demo", name: "acme-public-bounty" });
    expect(program.severitySystem).toBe("immunefi-v2.3");
  });

  it("renders scan reports in markdown and SARIF", async () => {
    const markdown = await platform.getSecurityScanReport({
      namespace: "demo",
      runName: "nightly-webapp-4",
    });
    expect(markdown.format).toBe("markdown");
    expect(markdown.filename).toBe("nightly-webapp-4.md");
    expect(markdown.content).toContain("SQL injection in payment lookup endpoint");

    const sarif = await platform.getSecurityScanReport({
      namespace: "demo",
      runName: "nightly-webapp-4",
      format: "sarif",
    });
    expect(sarif.format).toBe("sarif");
    const parsed = JSON.parse(sarif.content) as { runs: { results: unknown[] }[] };
    expect(parsed.runs[0].results.length).toBeGreaterThan(0);
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
});
