import { create } from "@bufbuild/protobuf";

import { buildCronRequest, emptyDefaults } from "@/components/run-defaults/helpers";
import { resolvedTriggerPolicies } from "@/components/TriggerDefaultsDialog";
import { applyModelDefaults, hasActiveModelDefaults } from "@/lib/modelDefaults";
import type { ProgramScanTarget } from "@/lib/programTargetCatalog";
import {
  AgentRunDefaultsSchema,
  CreateSecurityScanRequestSchema,
  SecurityScanConfigSpecSchema,
  type AgentRunDefaults,
  type CreateSecurityScanRequest,
  type ModelDefaults,
  type TriggerPolicies,
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
