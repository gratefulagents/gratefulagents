import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { resolvedTriggerPolicies } from "@/components/TriggerDefaultsDialog";
import { AgentRunDefaultsSchema } from "@/rpc/platform/service_pb";

const source = {
  permissionMode: "workspace-write",
  egressMode: "unrestricted",
  mcpPolicyDefaultAction: "Deny",
  mcpPolicyAllowedServers: [],
};

describe("resolvedTriggerPolicies", () => {
  it("enables managed runtime profiles for fresh trigger creates", () => {
    expect(resolvedTriggerPolicies().configureRuntimeProfile).toBe(true);
  });

  it("preserves existing triggers without a runtime profile", () => {
    const policies = resolvedTriggerPolicies({
      ...source,
      defaults: create(AgentRunDefaultsSchema, {}),
    });

    expect(policies.configureRuntimeProfile).toBe(false);
  });

  it("preserves existing triggers with a runtime profile", () => {
    const policies = resolvedTriggerPolicies({
      ...source,
      defaults: create(AgentRunDefaultsSchema, { runtimeProfileRef: "nightly-runtime" }),
    });

    expect(policies.configureRuntimeProfile).toBe(true);
  });
});
