import { describe, expect, it } from "vitest";
import { runKey } from "../scenario";
import { scenarios } from "./index";

describe("scenario fixtures", () => {
  it.each(Object.values(scenarios).map((s) => [s.name, s] as const))(
    "%s: route params and per-run maps reference existing fixtures",
    (_name, scenario) => {
      const runKeys = new Set(scenario.runs.map((r) => runKey(r.namespace, r.name)));

      for (const route of scenario.routes) {
        const m = /^\/runs\/([^/]+)\/([^/]+)$/.exec(route.path);
        if (m) expect(runKeys, `route ${route.path}`).toContain(runKey(m[1], m[2]));
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
