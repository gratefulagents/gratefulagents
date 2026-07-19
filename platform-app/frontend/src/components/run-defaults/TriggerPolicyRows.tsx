import { KeyRound, ShieldCheck } from "lucide-react";

import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { FlowField, FlowSwitchRow, OptionRow } from "@/components/create-flow/create-flow";
import { MCPServerPicker } from "@/components/MCPServerPicker";
import type { TriggerPolicies } from "@/rpc/platform/service_pb";

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

export interface TriggerPolicyRowsProps {
  policies: TriggerPolicies;
  onPoliciesChange: (policies: TriggerPolicies) => void;
  runtimeProfileRef: string;
  onRuntimeProfileRefChange: (value: string) => void;
  mcpPolicyRef: string;
  onMcpPolicyRefChange: (value: string) => void;
  /** Prefix for element ids so multiple forms can coexist. */
  idPrefix?: string;
}

function runtimePolicySummary(p: TriggerPolicies, runtimeProfileRef: string): string {
  if (p.configureRuntimeProfile) {
    const parts = [p.permissionMode || "workspace-write", p.egressMode || "unrestricted"];
    if (runtimeProfileRef.trim()) parts.push(runtimeProfileRef.trim());
    return parts.join(" · ");
  }
  return runtimeProfileRef.trim() ? `ref ${runtimeProfileRef.trim()}` : "Default";
}

function mcpPolicySummary(p: TriggerPolicies, mcpPolicyRef: string): string {
  if (p.configureMcpPolicy) {
    const parts = [`${p.mcpPolicyDefaultAction || "Deny"} by default`];
    const allowed = p.mcpPolicyAllowedServers.filter(Boolean).length;
    if (allowed) parts.push(`${allowed} server${allowed === 1 ? "" : "s"} allowed`);
    if (mcpPolicyRef.trim()) parts.push(mcpPolicyRef.trim());
    return parts.join(" · ");
  }
  return mcpPolicyRef.trim() ? `ref ${mcpPolicyRef.trim()}` : "Off";
}

/**
 * Trigger-agnostic editor for the dashboard-managed runtime/MCP policy
 * provisioning options (TriggerPolicies), rendered as OptionRow disclosures
 * in the same style as RunDefaultsRows. When a configure_* toggle is on the
 * server provisions or updates the RuntimeProfile/MCPPolicy named by the
 * optional ref (deriving a managed name when empty); when it is off the ref
 * is stored as-is. Compose inside an <OptionRows> stack alongside
 * RunDefaultsRows in trigger create/edit dialogs (Cron, GitHubRepository,
 * LinearProject).
 */
export function TriggerPolicyRows({
  policies,
  onPoliciesChange,
  runtimeProfileRef,
  onRuntimeProfileRefChange,
  mcpPolicyRef,
  onMcpPolicyRefChange,
  idPrefix = "trigger-policies",
}: TriggerPolicyRowsProps) {
  function set<K extends keyof TriggerPolicies>(field: K, fieldValue: TriggerPolicies[K]) {
    onPoliciesChange({ ...policies, [field]: fieldValue });
  }

  return (
    <>
      {/* Runtime policy */}
      <OptionRow
        icon={ShieldCheck}
        title="Runtime policy"
        summary={runtimePolicySummary(policies, runtimeProfileRef)}
        modified={policies.configureRuntimeProfile || Boolean(runtimeProfileRef.trim())}
      >
        <FlowSwitchRow
          id={`${idPrefix}-configure-runtime`}
          label="Manage runtime policy"
          hint="Creates/updates a RuntimeProfile controlling sandbox permissions and network egress for these runs."
          control={
            <Switch
              id={`${idPrefix}-configure-runtime`}
              checked={policies.configureRuntimeProfile}
              onCheckedChange={(checked) => set("configureRuntimeProfile", checked)}
            />
          }
        />
        {policies.configureRuntimeProfile ? (
          <div className="grid gap-4 sm:grid-cols-2">
            <FlowField id={`${idPrefix}-permission-mode`} label="Permission mode">
              <select
                id={`${idPrefix}-permission-mode`}
                value={policies.permissionMode}
                onChange={(event) => set("permissionMode", event.target.value)}
                className={selectClassName}
              >
                <option value="read-only">read-only</option>
                <option value="workspace-write">workspace-write</option>
                <option value="danger-full-access">danger-full-access</option>
              </select>
            </FlowField>
            <FlowField id={`${idPrefix}-egress-mode`} label="Network egress">
              <select
                id={`${idPrefix}-egress-mode`}
                value={policies.egressMode}
                onChange={(event) => set("egressMode", event.target.value)}
                className={selectClassName}
              >
                <option value="unrestricted">unrestricted</option>
                <option value="restricted">restricted</option>
                <option value="disabled">disabled</option>
              </select>
            </FlowField>
            <FlowField
              id={`${idPrefix}-runtime-profile-ref`}
              label="Profile name"
              hint="Optional — a managed name is derived when empty."
            >
              <Input
                id={`${idPrefix}-runtime-profile-ref`}
                value={runtimeProfileRef}
                onChange={(event) => onRuntimeProfileRefChange(event.target.value)}
                placeholder="my-runtime"
              />
            </FlowField>
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2">
            <FlowField
              id={`${idPrefix}-runtime-profile-ref`}
              label="RuntimeProfile ref"
              hint="Optional existing RuntimeProfile, stored as-is."
            >
              <Input
                id={`${idPrefix}-runtime-profile-ref`}
                value={runtimeProfileRef}
                onChange={(event) => onRuntimeProfileRefChange(event.target.value)}
                placeholder="my-runtime"
              />
            </FlowField>
          </div>
        )}
      </OptionRow>

      {/* MCP policy */}
      <OptionRow
        icon={KeyRound}
        title="MCP policy"
        summary={mcpPolicySummary(policies, mcpPolicyRef)}
        modified={policies.configureMcpPolicy || Boolean(mcpPolicyRef.trim())}
      >
        <FlowSwitchRow
          id={`${idPrefix}-configure-mcp-policy`}
          label="Manage MCP policy"
          hint="Creates/updates an MCPPolicy restricting which MCP servers these runs may reach."
          control={
            <Switch
              id={`${idPrefix}-configure-mcp-policy`}
              checked={policies.configureMcpPolicy}
              onCheckedChange={(checked) => set("configureMcpPolicy", checked)}
            />
          }
        />
        {policies.configureMcpPolicy ? (
          <>
            <div className="grid gap-4 sm:grid-cols-2">
              <FlowField id={`${idPrefix}-mcp-policy-default-action`} label="Default action">
                <select
                  id={`${idPrefix}-mcp-policy-default-action`}
                  value={policies.mcpPolicyDefaultAction}
                  onChange={(event) => set("mcpPolicyDefaultAction", event.target.value)}
                  className={selectClassName}
                >
                  <option value="Deny">Deny</option>
                  <option value="Allow">Allow</option>
                </select>
              </FlowField>
              <FlowField
                id={`${idPrefix}-mcp-policy-ref`}
                label="Policy name"
                hint="Optional — a managed name is derived when empty."
              >
                <Input
                  id={`${idPrefix}-mcp-policy-ref`}
                  value={mcpPolicyRef}
                  onChange={(event) => onMcpPolicyRefChange(event.target.value)}
                  placeholder="my-mcp-policy"
                />
              </FlowField>
            </div>
            <FlowField label="Allowed MCP servers" hint="Servers these runs may reach.">
              <MCPServerPicker
                selected={policies.mcpPolicyAllowedServers}
                onChange={(names) => set("mcpPolicyAllowedServers", names)}
              />
            </FlowField>
          </>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2">
            <FlowField
              id={`${idPrefix}-mcp-policy-ref`}
              label="MCPPolicy ref"
              hint="Optional existing MCPPolicy, stored as-is."
            >
              <Input
                id={`${idPrefix}-mcp-policy-ref`}
                value={mcpPolicyRef}
                onChange={(event) => onMcpPolicyRefChange(event.target.value)}
                placeholder="my-mcp-policy"
              />
            </FlowField>
          </div>
        )}
      </OptionRow>
    </>
  );
}
