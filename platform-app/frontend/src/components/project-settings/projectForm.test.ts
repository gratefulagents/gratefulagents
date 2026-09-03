import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { ProjectSchema } from "@/rpc/platform/service_pb";

import {
  deriveProjectName,
  emptyProjectForm,
  modelSummary,
  projectFormFromProject,
  resetSection,
  sectionChanged,
  updateRequestFromForm,
  validateProjectForm,
} from "./projectForm";

describe("deriveProjectName", () => {
  it("slugs the last path segment of common Git URLs", () => {
    expect(deriveProjectName("https://github.com/acme/Payments-API.git")).toBe("payments-api");
    expect(deriveProjectName("git@github.com:acme/Payments_API.git")).toBe("payments-api");
    expect(deriveProjectName("https://gitlab.com/group/sub/repo/")).toBe("repo");
    expect(deriveProjectName("acme/repo")).toBe("repo");
  });

  it("returns an empty suggestion for empty or unusable input", () => {
    expect(deriveProjectName("")).toBe("");
    expect(deriveProjectName("   ")).toBe("");
    expect(deriveProjectName("https://github.com/")).toBe("");
  });

  it("keeps names Kubernetes-safe", () => {
    expect(deriveProjectName("https://x.io/o/--Weird  Name!!.git")).toBe("weird-name");
    expect(deriveProjectName(`https://x.io/o/${"a".repeat(80)}`)).toHaveLength(63);
  });
});

describe("validateProjectForm", () => {
  it("requires a name at create and a display name at edit", () => {
    expect(validateProjectForm(emptyProjectForm(), "create", true)).toBe("Give the project a name.");
    const edit = { ...emptyProjectForm(), displayName: "" };
    expect(validateProjectForm(edit, "edit", true)).toBe("Display name is required.");
  });

  it("blocks saved credentials when none is stored for the provider", () => {
    const form = { ...emptyProjectForm(), name: "p" };
    expect(validateProjectForm(form, "create", false)).toMatch(/No saved Anthropic credential/);
    expect(validateProjectForm(form, "create", true)).toBeNull();
  });
});

describe("section change tracking", () => {
  const project = create(ProjectSchema, {
    namespace: "ns",
    name: "payments",
    displayName: "Payments",
    repoUrl: "https://github.com/acme/payments",
    provider: "openrouter",
    authMode: "api-key",
    model: "z-ai/glm-4.7",
    providerKeys: [{ provider: "openrouter", secretName: "usercred-openrouter", secretKey: "api-key" }],
    mcpServerRefs: ["github"],
  });

  it("attributes edits to their section and resets only that section", () => {
    const initial = projectFormFromProject(project);
    const edited = { ...initial, timeout: "45m", mcpServerRefs: ["github", "fetch"] };
    expect(sectionChanged("runtime", edited, initial)).toBe(true);
    expect(sectionChanged("tools", edited, initial)).toBe(true);
    expect(sectionChanged("model", edited, initial)).toBe(false);

    const afterReset = resetSection("runtime", edited, initial);
    expect(afterReset.timeout).toBe("");
    expect(afterReset.mcpServerRefs).toEqual(["github", "fetch"]);
  });

  it("ignores whitespace-only edits", () => {
    const initial = projectFormFromProject(project);
    expect(sectionChanged("general", { ...initial, displayName: "Payments  " }, initial)).toBe(false);
  });

  it("only sends bug squasher when it changed and omits an unchanged mode", () => {
    const initial = projectFormFromProject(project);
    const unchanged = updateRequestFromForm(initial, project, { isAdmin: false });
    expect(unchanged.bugSquasher).toBeUndefined();
    expect(unchanged.modeRef).toBeUndefined();
    expect(unchanged.kubernetesAdmin).toBeUndefined();

    const flipped = updateRequestFromForm({ ...initial, bugSquasher: true, modeRef: "plan" }, project, {
      isAdmin: true,
    });
    expect(flipped.bugSquasher).toBe(true);
    expect(flipped.modeRef).toBe("plan");
    expect(flipped.kubernetesAdmin).toBe(false);
  });

  it("initialises the saved-credential toggle from usercred refs", () => {
    const form = projectFormFromProject(project);
    expect(form.useSavedCredentials).toBe(true);
    expect(modelSummary(form, true)).toBe("OpenRouter · z-ai/glm-4.7 · saved credentials");
    expect(modelSummary(form, false)).toContain("no saved credential");
  });
});
