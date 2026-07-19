import { describe, expect, it } from "vitest";
import {
  parseExtraWritablePaths,
  parseResourceClaims,
  parseStringMap,
  runtimeProfileFormFromRow,
} from "./runtime-profile-form";

describe("runtime profile form", () => {
  it("hydrates every structured runtime profile field for editing", () => {
    const form = runtimeProfileFormFromRow({
      name: "build",
      enablePrivateProcfs: true,
      commandPath: ["/usr/bin", "/opt/bin"],
      extraWritablePaths: ["/cache/go", "/cache/cargo"],
      commandEnv: { LANG: "C.UTF-8" },
      resourceRequests: { cpu: "500m" },
      resourceLimits: { memory: "2Gi" },
      resourceClaims: [{ name: "gpu", request: "inference" }],
      maxConcurrentRuns: 4,
      perNamespaceMaxConcurrentRuns: 2,
      staleRunTimeout: "30m0s",
    });
    expect(form.enablePrivateProcfs).toBe(true);
    expect(form.commandPath).toBe("/usr/bin\n/opt/bin");
    expect(form.extraWritablePaths).toBe("/cache/go\n/cache/cargo");
    expect(JSON.parse(form.commandEnv)).toEqual({ LANG: "C.UTF-8" });
    expect(JSON.parse(form.resourceRequests)).toEqual({ cpu: "500m" });
    expect(JSON.parse(form.resourceLimits)).toEqual({ memory: "2Gi" });
    expect(JSON.parse(form.resourceClaims)).toEqual([{ name: "gpu", request: "inference" }]);
    expect(form.maxConcurrentRuns).toBe("4");
    expect(form.perNamespaceMaxConcurrentRuns).toBe("2");
    expect(form.staleRunTimeout).toBe("30m0s");
  });

  it("parses list and map fields into RPC values", () => {
    expect(parseExtraWritablePaths(" /cache/go\n/cache/cargo\n\n ")).toEqual(["/cache/go", "/cache/cargo"]);
    expect(parseExtraWritablePaths("/cache/with,comma")).toEqual(["/cache/with,comma"]);
    expect(parseStringMap('{"cpu":"500m","memory":"1Gi"}', "Resources")).toEqual({ cpu: "500m", memory: "1Gi" });
    expect(parseResourceClaims('[{"name":"gpu","request":"training"}]')).toEqual([{ name: "gpu", request: "training" }]);
  });

  it("rejects malformed structured fields", () => {
    expect(() => parseStringMap("[]", "Resources")).toThrow("Resources must be a JSON object");
    expect(() => parseStringMap('{"cpu":1}', "Resources")).toThrow("values must be strings");
    expect(() => parseResourceClaims('[{"request":"training"}]')).toThrow("fields must be strings");
    expect(() => parseResourceClaims('[{"name":12}]')).toThrow("fields must be strings");
  });
});
