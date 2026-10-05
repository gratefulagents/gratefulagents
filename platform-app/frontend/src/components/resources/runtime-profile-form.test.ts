import { describe, expect, it } from "vitest";
import {
  draftFromProfile,
  emptyRuntimeProfileDraft,
  parseStringList,
  pathListError,
  profileFromDraft,
  runtimeProfileDraftErrors,
} from "./runtime-profile-form";

describe("runtime profile form", () => {
  it("hydrates every structured runtime profile field for editing", () => {
    const draft = draftFromProfile({
      name: "build",
      permissionMode: "read-only",
      gitRemoteWrites: "disabled",
      egressMode: "disabled",
      defaultTimeout: "1h0m0s",
      persistWorkspace: true,
      workspaceSize: "20Gi",
      enablePrivateProcfs: true,
      sandboxTemplateRef: "tpl",
      runtimeClassName: "gvisor",
      warmPoolRef: "pool",
      commandPath: ["/usr/bin", "/opt/bin"],
      commandPathPrepend: ["/opt/prepend"],
      commandPathAppend: ["/opt/append"],
      extraReadOnlyPaths: ["/opt/ro"],
      extraWritablePaths: ["/cache/go", "/cache/cargo"],
      commandEnv: { LANG: "C.UTF-8", GOFLAGS: "-mod=mod" },
      resourceRequests: { cpu: "500m" },
      resourceLimits: { memory: "2Gi" },
      maxConcurrentRuns: 4,
      perNamespaceMaxConcurrentRuns: 2,
      staleRunTimeout: "30m0s",
    });
    expect(draft).toMatchObject({
      name: "build",
      permissionMode: "read-only",
      gitRemoteWrites: "disabled",
      egressMode: "disabled",
      defaultTimeout: "1h0m0s",
      persistWorkspace: true,
      workspaceSize: "20Gi",
      enablePrivateProcfs: true,
      sandboxTemplateRef: "tpl",
      runtimeClassName: "gvisor",
      warmPoolRef: "pool",
      commandPath: "/usr/bin\n/opt/bin",
      commandPathPrepend: "/opt/prepend",
      commandPathAppend: "/opt/append",
      extraReadOnlyPaths: "/opt/ro",
      extraWritablePaths: "/cache/go\n/cache/cargo",
      maxConcurrentRuns: "4",
      perNamespaceMaxConcurrentRuns: "2",
      staleRunTimeout: "30m0s",
    });
    // Map rows are sorted by key so the editor is deterministic.
    expect(draft.commandEnv).toEqual([
      { key: "GOFLAGS", value: "-mod=mod" },
      { key: "LANG", value: "C.UTF-8" },
    ]);
    expect(draft.resourceRequests).toEqual([{ key: "cpu", value: "500m" }]);
    expect(draft.resourceLimits).toEqual([{ key: "memory", value: "2Gi" }]);
    expect(runtimeProfileDraftErrors(draft, false)).toEqual({});
  });

  it("falls back to safe defaults for a sparse row", () => {
    const draft = draftFromProfile({ name: "sparse" });
    expect(draft.permissionMode).toBe("workspace-write");
    expect(draft.gitRemoteWrites).toBe("enabled");
    expect(draft.egressMode).toBe("restricted");
    expect(draft.commandEnv).toEqual([]);
    expect(draft.maxConcurrentRuns).toBe("0");
  });

  it("round-trips a draft into the replace-spec payload", () => {
    const draft = {
      ...emptyRuntimeProfileDraft(),
      name: " build ",
      gitRemoteWrites: "disabled",
      defaultTimeout: "2h",
      persistWorkspace: true,
      workspaceSize: "20Gi",
      commandPath: " /usr/bin\n/opt/bin\n\n ",
      extraWritablePaths: "/cache/with,comma",
      commandEnv: [{ key: "LANG", value: "C.UTF-8" }, { key: "", value: "" }],
      resourceRequests: [{ key: "cpu", value: "500m" }],
      resourceLimits: [{ key: "memory", value: "2Gi" }],
      maxConcurrentRuns: "4",
      staleRunTimeout: "30m",
    };
    expect(profileFromDraft(draft)).toEqual({
      name: "build",
      permissionMode: "workspace-write",
      gitRemoteWrites: "disabled",
      egressMode: "restricted",
      defaultTimeout: "2h",
      persistWorkspace: true,
      workspaceSize: "20Gi",
      enablePrivateProcfs: false,
      sandboxTemplateRef: "",
      runtimeClassName: "",
      warmPoolRef: "",
      commandPath: ["/usr/bin", "/opt/bin"],
      commandPathPrepend: [],
      commandPathAppend: [],
      extraReadOnlyPaths: [],
      extraWritablePaths: ["/cache/with,comma"],
      commandEnv: { LANG: "C.UTF-8" },
      resourceRequests: { cpu: "500m" },
      resourceLimits: { memory: "2Gi" },
      maxConcurrentRuns: 4,
      perNamespaceMaxConcurrentRuns: 0,
      staleRunTimeout: "30m",
      replaceSpec: true,
    });
  });

  it("clears the workspace size when persistence is off", () => {
    const draft = { ...emptyRuntimeProfileDraft(), name: "x", workspaceSize: "not-a-size" };
    expect(runtimeProfileDraftErrors(draft)).toEqual({});
    expect(profileFromDraft(draft).workspaceSize).toBe("");
  });

  it("rejects malformed fields", () => {
    const base = { ...emptyRuntimeProfileDraft(), name: "ok" };
    expect(runtimeProfileDraftErrors({ ...base, defaultTimeout: "soon" }).defaultTimeout).toMatch(/Go duration/);
    expect(runtimeProfileDraftErrors({ ...base, staleRunTimeout: "10 minutes" }).staleRunTimeout).toMatch(/Go duration/);
    expect(runtimeProfileDraftErrors({ ...base, persistWorkspace: true, workspaceSize: "10 gigs" }).workspaceSize).toMatch(/quantity/);
    expect(runtimeProfileDraftErrors({ ...base, commandPath: "/usr/bin\nrelative/bin" }).commandPath).toMatch(/absolute path/);
    expect(runtimeProfileDraftErrors({ ...base, commandEnv: [{ key: "A", value: "1" }, { key: "A", value: "2" }] }).commandEnv).toMatch(/Duplicate/);
    expect(runtimeProfileDraftErrors({ ...base, commandEnv: [{ key: "", value: "1" }] }).commandEnv).toMatch(/cannot be empty/);
    expect(runtimeProfileDraftErrors({ ...base, resourceRequests: [{ key: "cpu", value: "lots" }] }).resourceRequests).toMatch(/quantity/);
    expect(runtimeProfileDraftErrors({ ...base, resourceLimits: [{ key: "memory", value: "" }] }).resourceLimits).toMatch(/needs a quantity/);
    expect(runtimeProfileDraftErrors({ ...base, maxConcurrentRuns: "-1" }).maxConcurrentRuns).toMatch(/whole number/);
    expect(runtimeProfileDraftErrors({ ...base, runtimeClassName: "Not Valid" }).runtimeClassName).toMatch(/resource name/);
    expect(runtimeProfileDraftErrors({ ...base, name: "Bad Name" }).name).toMatch(/lowercase/);
    expect(runtimeProfileDraftErrors({ ...base, name: "Bad Name" }, false)).toEqual({});
    expect(() => profileFromDraft({ ...base, defaultTimeout: "soon" })).toThrow(/Go duration/);
  });

  it("parses path lists and flags relative entries", () => {
    expect(parseStringList(" /cache/go\n/cache/cargo\n\n ")).toEqual(["/cache/go", "/cache/cargo"]);
    expect(pathListError("/a\n/b")).toBeNull();
    expect(pathListError("/a\nb")).toMatch(/"b" must be an absolute path/);
  });
});
