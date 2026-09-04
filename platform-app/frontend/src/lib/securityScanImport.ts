import { clone, create } from "@bufbuild/protobuf";

import { buildCronRequest, emptyDefaults } from "@/components/run-defaults/helpers";
import { resolvedTriggerPolicies } from "@/components/TriggerDefaultsDialog";
import { applyModelDefaults, hasActiveModelDefaults } from "@/lib/modelDefaults";
import type { ProgramScanTarget } from "@/lib/programTargetCatalog";
import {
  AgentRunDefaultsSchema,
  CreateSecurityScanRequestSchema,
  SecurityScanConfigSpecSchema,
  UpdateSecurityScanRequestSchema,
  type AgentRunDefaults,
  type CreateSecurityScanRequest,
  type ModelDefaults,
  type SecurityScanConfig,
  type TriggerPolicies,
  type UpdateSecurityScanRequest,
} from "@/rpc/platform/service_pb";

export type ImportedScanOptions = {
  defaults?: AgentRunDefaults;
  policies?: TriggerPolicies;
  dockerInDocker?: boolean;
};

/**
 * runDefaultsFromModelDefaults turns the caller's saved model defaults into
 * the AgentRunDefaults a headless import starts from — the same seeding
 * SecurityScanFormDialog applies to a fresh scan form. Inactive (missing,
 * disabled, or effectively empty) defaults leave the run defaults untouched so
 * the server picks its own.
 */
export function runDefaultsFromModelDefaults(
  defaults: ModelDefaults | null | undefined,
): AgentRunDefaults {
  if (!hasActiveModelDefaults(defaults)) return emptyDefaults();
  const seeded = applyModelDefaults(defaults);
  return create(AgentRunDefaultsSchema, {
    provider: seeded.provider,
    authMode: seeded.provider === "copilot" ? "oauth" : seeded.authMode,
    model: seeded.model,
    reasoningLevel: seeded.reasoningLevel,
  });
}

/**
 * buildImportedScanCreateRequest produces the CreateSecurityScan request for a
 * security-program scan target imported without review: the same payload the
 * interactive form builds for a prefilled target — manual-only schedule, the
 * selected policy pack's severity floor, the caller's saved credentials, and
 * workspace-write access with unrestricted egress.
 */
export function buildImportedScanCreateRequest(
  target: ProgramScanTarget,
  options: ImportedScanOptions = {},
): CreateSecurityScanRequest {
  // Reuse the shared trigger-defaults normalization the scan form runs before
  // a save so model/provider/auth defaults land identically.
  const { defaults } = buildCronRequest({
    namespace: "",
    name: "",
    schedule: "",
    timeZone: "",
    suspend: false,
    concurrencyPolicy: "",
    prompt: "-",
    defaults: options.defaults ?? emptyDefaults(),
    useSavedCredentials: true,
  });
  // Security-program imports are expected to run local containerized tooling.
  // Keep DinD enabled by default; non-admin callers explicitly opt out in the
  // dashboard because the backend protects this privileged capability.
  defaults.dockerInDocker = options.dockerInDocker ?? true;
  const spec = create(SecurityScanConfigSpecSchema, {
    repoUrl: target.repoUrl,
    targetUrl: target.targetUrl,
    baseBranch: target.repoUrl ? target.baseBranch : "",
    workflowRef: target.workflowRef,
    policyPackRef: target.policyPackRef,
    securityProgramRef: target.securityProgramRef,
    parameterValues: target.parameterValues,
    manualOnly: true,
    // The interactive form seeds a fresh scan with "Forbid"; spelling it out
    // keeps the payloads identical instead of relying on the server default.
    concurrencyPolicy: "Forbid",
    parallelism: 4,
    dedupe: { enabled: true },
    defaults,
  });
  return create(CreateSecurityScanRequestSchema, {
    name: target.name,
    spec,
    useSavedCredentials: true,
    policies: options.policies ?? resolvedTriggerPolicies(undefined),
  });
}

/**
 * ProgramTargetImportStatus classifies a program scan target against the
 * caller's existing configurations: `new` has no configuration yet,
 * `update-available` has one whose program-defined target fields drifted from
 * the program, and `up-to-date` already matches.
 */
export type ProgramTargetImportStatus = "new" | "update-available" | "up-to-date";

function sameParameterValues(
  left: Record<string, string>,
  right: Record<string, string>,
): boolean {
  const leftKeys = Object.keys(left);
  if (leftKeys.length !== Object.keys(right).length) return false;
  return leftKeys.every((key) => right[key] === left[key]);
}

/**
 * programTargetDrift lists the human-readable target fields on which an
 * existing configuration differs from its security-program target. Only the
 * fields the program defines are compared; schedule, model defaults, budgets,
 * notifications, and other operator choices are never considered drift.
 */
export function programTargetDrift(
  target: ProgramScanTarget,
  config: SecurityScanConfig,
): string[] {
  const spec = config.spec;
  if (!spec) return ["configuration"];
  const drift: string[] = [];
  if ((spec.repoUrl ?? "") !== target.repoUrl) drift.push("repository");
  if ((spec.targetUrl ?? "") !== target.targetUrl) drift.push("target URL");
  if (target.repoUrl && (spec.baseBranch || "main") !== target.baseBranch) drift.push("base branch");
  if ((spec.workflowRef ?? "") !== target.workflowRef) drift.push("workflow");
  if ((spec.policyPackRef ?? "") !== target.policyPackRef) drift.push("policy pack");
  if ((spec.securityProgramRef ?? "") !== target.securityProgramRef) drift.push("program");
  if (!sameParameterValues(spec.parameterValues ?? {}, target.parameterValues)) drift.push("parameters");
  return drift;
}

export function programTargetImportStatus(
  target: ProgramScanTarget,
  config: SecurityScanConfig | undefined,
): ProgramTargetImportStatus {
  if (!config) return "new";
  return programTargetDrift(target, config).length > 0 ? "update-available" : "up-to-date";
}

/**
 * buildImportedScanUpdateRequest refreshes an existing configuration from its
 * security-program target. Only the program-defined target fields are
 * rewritten; everything else on the configuration (schedule, run defaults,
 * triggers, budgets, notifications, suspension, …) is carried over untouched.
 */
export function buildImportedScanUpdateRequest(
  target: ProgramScanTarget,
  config: SecurityScanConfig,
  options: { useSavedCredentials: boolean },
): UpdateSecurityScanRequest {
  const spec = config.spec
    ? clone(SecurityScanConfigSpecSchema, config.spec)
    : create(SecurityScanConfigSpecSchema, {});
  spec.repoUrl = target.repoUrl;
  spec.targetUrl = target.targetUrl;
  spec.baseBranch = target.repoUrl ? target.baseBranch : "";
  spec.workflowRef = target.workflowRef;
  // workflow_ref and an inline workflow are mutually exclusive; a program
  // target always names a reusable workflow.
  if (target.workflowRef) spec.workflow = [];
  spec.policyPackRef = target.policyPackRef;
  spec.securityProgramRef = target.securityProgramRef;
  spec.parameterValues = { ...target.parameterValues };
  return create(UpdateSecurityScanRequestSchema, {
    namespace: config.namespace,
    name: config.name,
    spec,
    useSavedCredentials: options.useSavedCredentials,
  });
}
