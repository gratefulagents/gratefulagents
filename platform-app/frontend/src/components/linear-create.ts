import { create } from "@bufbuild/protobuf";
import {
  AgentRunDefaultsSchema,
  CreateLinearProjectRequestSchema,
  TriggerPoliciesSchema,
  type CreateLinearProjectRequest,
} from "@/rpc/platform/service_pb";

export type LinearCreateValues = {
  name: string;
  linearApiKey: string;
  projectId: string;
  teamId: string;
  pollInterval: string;
  approvedLabel: string;
  autoCreateTasks: boolean;
  model: string;
  provider: string;
  authMode: string;
  useSavedCredentials: boolean;
  configureRuntimeProfile: boolean;
  permissionMode: string;
  egressMode: string;
  configureMcpPolicy: boolean;
  mcpPolicyDefaultAction: string;
  mcpPolicyAllowedServers: string;
};

export const initialLinearCreateValues: LinearCreateValues = {
  name: "",
  linearApiKey: "",
  projectId: "",
  teamId: "",
  pollInterval: "1m",
  approvedLabel: "ai-approved",
  autoCreateTasks: false,
  model: "",
  provider: "anthropic",
  authMode: "api-key",
  useSavedCredentials: true,
  configureRuntimeProfile: true,
  permissionMode: "workspace-write",
  egressMode: "restricted",
  configureMcpPolicy: false,
  mcpPolicyDefaultAction: "Deny",
  mcpPolicyAllowedServers: "",
};

function csv(value: string): string[] {
  return value.split(",").map((entry) => entry.trim()).filter(Boolean);
}

export function buildLinearCreateRequest(v: LinearCreateValues): CreateLinearProjectRequest {
  return create(CreateLinearProjectRequestSchema, {
    name: v.name.trim(),
    linearApiKey: v.linearApiKey.trim(),
    projectId: v.projectId.trim(),
    teamId: v.teamId.trim(),
    pollInterval: v.pollInterval.trim(),
    approvedLabel: v.approvedLabel.trim(),
    autoCreateTasks: v.autoCreateTasks,
    useSavedCredentials: v.useSavedCredentials,
    defaults: create(AgentRunDefaultsSchema, {
      model: v.model.trim(),
      allowedModels: [v.model.trim()],
      provider: v.provider.trim(),
      authMode: v.authMode.trim(),
    }),
    policies: create(TriggerPoliciesSchema, {
      configureRuntimeProfile: v.configureRuntimeProfile,
      permissionMode: v.permissionMode,
      egressMode: v.egressMode,
      configureMcpPolicy: v.configureMcpPolicy,
      mcpPolicyDefaultAction: v.mcpPolicyDefaultAction,
      mcpPolicyAllowedServers: csv(v.mcpPolicyAllowedServers),
    }),
  });
}
