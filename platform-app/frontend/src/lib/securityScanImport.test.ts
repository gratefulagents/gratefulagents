import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  buildImportedScanCreateRequest,
  runDefaultsFromModelDefaults,
} from "@/lib/securityScanImport";
import type { ProgramScanTarget } from "@/lib/programTargetCatalog";
import {
  AgentRunDefaultsSchema,
  ModelDefaultsSchema,
  TriggerPoliciesSchema,
} from "@/rpc/platform/service_pb";

const repoTarget: ProgramScanTarget = {
  name: "scan-first",
  displayName: "First target",
  repoUrl: "https://example.com/first",
  targetUrl: "",
  baseBranch: "main",
  workflowRef: "workflow-first",
  policyPackRef: "policy-first",
  securityProgramRef: "program-multi",
  priority: 10,
  parameterValues: { scope: "api" },
};

const websiteTarget: ProgramScanTarget = {
  ...repoTarget,
  name: "scan-site",
  displayName: "Site target",
  repoUrl: "",
  targetUrl: "https://app.example.com",
  baseBranch: "",
};

describe("buildImportedScanCreateRequest", () => {
  it("builds the same payload the prefilled scan form creates", () => {
    const request = buildImportedScanCreateRequest(repoTarget);

    expect(request.name).toBe("scan-first");
    expect(request.useSavedCredentials).toBe(true);
    expect(request.spec).toMatchObject({
      repoUrl: "https://example.com/first",
      targetUrl: "",
      baseBranch: "main",
      workflowRef: "workflow-first",
      policyPackRef: "policy-first",
      securityProgramRef: "program-multi",
      parameterValues: { scope: "api" },
      manualOnly: true,
      concurrencyPolicy: "Forbid",
      parallelism: 4,
    });
    expect(request.spec?.minSeverity).toBe("");
    expect(request.spec?.dedupe?.enabled).toBe(true);
    expect(request.spec?.defaults?.dockerInDocker).toBe(true);
  });

  it("defaults to workspace-write access with unrestricted egress", () => {
    const request = buildImportedScanCreateRequest(repoTarget);
    expect(request.policies).toMatchObject({
      configureRuntimeProfile: true,
      permissionMode: "workspace-write",
      egressMode: "unrestricted",
      configureMcpPolicy: false,
    });
  });

  it("honors explicit policies", () => {
    const request = buildImportedScanCreateRequest(repoTarget, {
      policies: create(TriggerPoliciesSchema, {
        permissionMode: "read-only",
        egressMode: "restricted",
      }),
    });
    expect(request.policies).toMatchObject({ permissionMode: "read-only" });
  });

  it("omits the base branch for website targets", () => {
    const request = buildImportedScanCreateRequest(websiteTarget);
    expect(request.spec?.repoUrl).toBe("");
    expect(request.spec?.baseBranch).toBe("");
    expect(request.spec?.targetUrl).toBe("https://app.example.com");
  });

  it("normalizes the supplied run defaults and drops explicit secrets", () => {
    const request = buildImportedScanCreateRequest(repoTarget, {
      defaults: create(AgentRunDefaultsSchema, {
        provider: "openai",
        authMode: "api-key",
        model: "  gpt-5  ",
        reasoningLevel: "high",
        claudeApiKeySecret: "explicit-secret",
        skillRefs: [" skill ", ""],
      }),
    });
    expect(request.spec?.defaults).toMatchObject({
      provider: "openai",
      authMode: "api-key",
      model: "gpt-5",
      reasoningLevel: "high",
      claudeApiKeySecret: "",
      skillRefs: ["skill"],
    });
  });

  it("uses empty model defaults when none are supplied", () => {
    const request = buildImportedScanCreateRequest(repoTarget);
    expect(request.spec?.defaults).toMatchObject({
      provider: "",
      model: "",
      reasoningLevel: "",
      dockerInDocker: true,
    });
  });

  it("lets non-admin import callers explicitly disable the privileged default", () => {
    const request = buildImportedScanCreateRequest(repoTarget, { dockerInDocker: false });
    expect(request.spec?.defaults?.dockerInDocker).toBe(false);
  });
});

describe("runDefaultsFromModelDefaults", () => {
  it("returns empty defaults when there are no active model defaults", () => {
    expect(runDefaultsFromModelDefaults(null)).toMatchObject({ provider: "", model: "" });
    expect(
      runDefaultsFromModelDefaults(
        create(ModelDefaultsSchema, { provider: "openai", model: "gpt-5", disabled: true }),
      ),
    ).toMatchObject({ provider: "", model: "" });
  });

  it("seeds provider, model, and reasoning level from saved defaults", () => {
    expect(
      runDefaultsFromModelDefaults(
        create(ModelDefaultsSchema, {
          provider: "openai",
          authMode: "api-key",
          model: "gpt-5",
          reasoningLevel: "high",
        }),
      ),
    ).toMatchObject({
      provider: "openai",
      authMode: "api-key",
      model: "gpt-5",
      reasoningLevel: "high",
    });
  });

  it("forces oauth for copilot", () => {
    expect(
      runDefaultsFromModelDefaults(create(ModelDefaultsSchema, { provider: "copilot" })),
    ).toMatchObject({ provider: "copilot", authMode: "oauth" });
  });
});
