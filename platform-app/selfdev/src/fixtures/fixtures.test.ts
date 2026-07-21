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
});
