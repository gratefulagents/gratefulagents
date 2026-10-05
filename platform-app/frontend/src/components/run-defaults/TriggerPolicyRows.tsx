import { ShieldCheck } from "lucide-react";

import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { FlowField, FlowSwitchRow, OptionRow } from "@/components/create-flow/create-flow";
import type { TriggerPolicies } from "@/rpc/platform/service_pb";

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

export interface TriggerPolicyRowsProps {
  policies: TriggerPolicies;
  onPoliciesChange: (policies: TriggerPolicies) => void;
  runtimeProfileRef: string;
  onRuntimeProfileRefChange: (value: string) => void;
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

/**
 * Trigger-agnostic editor for the dashboard-managed runtime policy
 * provisioning options (TriggerPolicies), rendered as OptionRow disclosures
 * in the same style as RunDefaultsRows. When a configure_* toggle is on the
 * server provisions or updates the RuntimeProfile named by the
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
    </>
  );
}
