import { describe, expect, it } from "vitest";
import { runKey } from "../scenario";
import { scenarios } from "./index";

describe("scenario fixtures", () => {
  it.each(Object.values(scenarios).map((s) => [s.name, s] as const))(
    "%s: route params and per-run maps reference existing fixtures",
    (_name, scenario) => {
      const runKeys = new Set(scenario.runs.map((r) => runKey(r.namespace, r.name)));

      const resourceKeys = {
        runs: runKeys,
        projects: new Set(scenario.projects.map((item) => runKey(item.namespace, item.name))),
        linear: new Set(scenario.linearProjects.map((item) => runKey(item.namespace, item.name))),
        github: new Set(scenario.githubRepositories.map((item) => runKey(item.namespace, item.name))),
        cron: new Set(scenario.crons.map((item) => runKey(item.namespace, item.name))),
        slack: new Set(scenario.slackAgents.map((item) => runKey(item.namespace, item.name))),
      };

      for (const route of scenario.routes) {
        const match = /^\/(runs|projects|linear|github|cron|slack)\/([^/?]+)\/([^/?]+)(?:\?.*)?$/.exec(route.path);
        if (!match) continue;
        const [, kind, namespace, name] = match;
        expect(resourceKeys[kind as keyof typeof resourceKeys], `route ${route.path}`).toContain(
          runKey(namespace, name),
        );
      }
      for (const mapName of ["activityLogs", "usage", "pullRequests", "diffs", "traces"] as const) {
        for (const key of Object.keys(scenario[mapName])) {
          expect(runKeys, `${mapName} key ${key}`).toContain(key);
        }
      }
    },
  );

  it("default scenario covers the phases the home screen groups by", () => {
    const phases = new Set(scenarios.default.runs.map((r) => r.phase));
    for (const phase of ["Running", "Succeeded", "Failed", "Pending", "Cancelled"]) {
      // Cancelled lives in the error scenario; the rest must be in default.
      if (phase === "Cancelled") continue;
      expect(phases).toContain(phase);
    }
  });

  it.each(Object.values(scenarios).map((s) => [s.name, s] as const))(
    "%s: security deep-link routes reference existing fixtures",
    (_name, scenario) => {
      const configs = new Set(
        scenario.securityScanConfigs.map((c) => runKey(c.namespace, c.name)),
      );
      const scanRuns = new Set(scenario.securityScans.map((x) => runKey(x.namespace, x.runName)));
      const findingIds = new Set(scenario.securityFindings.map((f) => f.id));

      for (const route of scenario.routes) {
        const config = /^\/security\/configs\/([^/?]+)\/([^/?]+)$/.exec(route.path);
        if (config) {
          expect(configs, `route ${route.path}`).toContain(runKey(config[1], config[2]));
          continue;
        }
        const finding = /^\/security\/([^/?]+)\/([^/?]+)\/findings\/([^/?]+)$/.exec(route.path);
        if (finding) {
          expect(scanRuns, `route ${route.path}`).toContain(runKey(finding[1], finding[2]));
          expect(findingIds, `route ${route.path}`).toContain(finding[3]);
          continue;
        }
        const scan = /^\/security\/(?!runs|configs|library)([^/?]+)\/([^/?]+)$/.exec(route.path);
        if (scan) {
          expect(scanRuns, `route ${route.path}`).toContain(runKey(scan[1], scan[2]));
        }
      }
    },
  );

  it("default security fixtures are rich enough to exercise every filter", () => {
    const { securityScans, securityFindings, securityScanConfigs } = scenarios.default;

    expect(securityScans.length).toBeGreaterThanOrEqual(8);
    for (const status of ["running", "completed", "failed", "pending"]) {
      expect(securityScans.map((x) => x.status)).toContain(status);
    }
    expect(new Set(securityScans.map((x) => x.repository)).size).toBeGreaterThanOrEqual(3);
    // Completion times must be spread across hours, days, and weeks so the
    // time-range filters have something to separate.
    const completions = securityScans
      .filter((x) => x.completedAt)
      .map((x) => Number(x.completedAt!.seconds));
    expect(Math.max(...completions) - Math.min(...completions)).toBeGreaterThan(14 * 24 * 3600);

    expect(securityFindings.length).toBeGreaterThanOrEqual(12);
    for (const severity of ["critical", "high", "medium", "low", "info"]) {
      expect(securityFindings.map((f) => f.severity)).toContain(severity);
    }
    for (const status of ["open", "triaged", "confirmed", "false_positive", "fixed", "accepted_risk"]) {
      expect(securityFindings.map((f) => f.status)).toContain(status);
    }
    for (const state of ["new", "recurring", "regressed", "resolved", "reopened"]) {
      expect(securityFindings.map((f) => f.baselineState)).toContain(state);
    }
    expect(securityFindings.some((f) => f.duplicateOf !== "")).toBe(true);
    expect(securityFindings.some((f) => f.suppressedBy !== "")).toBe(true);
    expect(securityFindings.some((f) => f.sourceKind === "scanner")).toBe(true);
    expect(new Set(securityFindings.map((f) => f.category)).size).toBeGreaterThanOrEqual(5);
    expect(new Set(securityFindings.map((f) => f.filePath)).size).toBeGreaterThanOrEqual(8);

    expect(securityScanConfigs.length).toBeGreaterThanOrEqual(5);
    expect(securityScanConfigs.some((c) => c.spec?.suspend)).toBe(true);
    expect(securityScanConfigs.some((c) => c.conditionReady !== "True" && c.lastError !== "")).toBe(true);
    expect(securityScanConfigs.some((c) => c.lastRunName === "")).toBe(true);
    expect(securityScanConfigs.some((c) => c.spec?.schedule === "")).toBe(true);
    expect(securityScanConfigs.some((c) => c.spec?.schedule !== "")).toBe(true);
    expect(securityScanConfigs.some((c) => c.spec?.securityProgramRef !== "")).toBe(true);
  });

  it("every scan run and finding points at a fixture that exists", () => {
    const { securityScans, securityFindings, securityScanConfigs } = scenarios.default;
    const configNames = new Set(securityScanConfigs.map((c) => c.name));
    const scanIds = new Map(securityScans.map((x) => [x.id, x] as const));

    for (const scan of securityScans) {
      expect(configNames, `scan ${scan.runName}`).toContain(scan.scanName);
    }
    for (const finding of securityFindings) {
      const scan = scanIds.get(finding.scanId);
      expect(scan, `finding ${finding.id}`).toBeDefined();
      expect(finding.runName).toBe(scan!.runName);
      expect(finding.scanName).toBe(scan!.scanName);
      expect(finding.repository).toBe(scan!.repository);
    }
  });

  it("library resources are populated and referenced by the scan configs", () => {
    const scenario = scenarios.default;
    expect(scenario.securityWorkflows.length).toBeGreaterThanOrEqual(2);
    expect(scenario.securityRankers.length).toBeGreaterThanOrEqual(2);
    expect(scenario.securityPostScripts.length).toBeGreaterThanOrEqual(2);
    expect(scenario.securityPolicyPacks.length).toBeGreaterThanOrEqual(2);
    expect(scenario.securityPrograms.length).toBeGreaterThanOrEqual(2);

    const names = {
      workflow: new Set(scenario.securityWorkflows.map((x) => x.name)),
      ranker: new Set(scenario.securityRankers.map((x) => x.name)),
      postScript: new Set(scenario.securityPostScripts.map((x) => x.name)),
      policyPack: new Set(scenario.securityPolicyPacks.map((x) => x.name)),
      program: new Set(scenario.securityPrograms.map((x) => x.name)),
    };
    for (const config of scenario.securityScanConfigs) {
      const spec = config.spec!;
      if (spec.workflowRef) expect(names.workflow).toContain(spec.workflowRef);
      for (const ref of spec.rankerRefs) expect(names.ranker).toContain(ref);
      for (const ref of spec.postScriptRefs) expect(names.postScript).toContain(ref);
      if (spec.policyPackRef) expect(names.policyPack).toContain(spec.policyPackRef);
      if (spec.securityProgramRef) expect(names.program).toContain(spec.securityProgramRef);
    }
  });
});
