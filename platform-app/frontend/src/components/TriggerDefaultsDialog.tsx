import { clone, create } from "@bufbuild/protobuf";
import { useState } from "react";
import { Loader2, Settings } from "lucide-react";

import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { OptionRows } from "@/components/create-flow/create-flow";
import { RunDefaultsRows } from "@/components/run-defaults/RunDefaultsRows";
import { TriggerPolicyRows } from "@/components/run-defaults/TriggerPolicyRows";
import { emptyDefaults } from "@/components/run-defaults/helpers";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import {
  AgentRunDefaultsSchema,
  TriggerPoliciesSchema,
  type AgentRunDefaults,
  type ProviderKeyRef,
  type TriggerPolicies,
} from "@/rpc/platform/service_pb";

/**
 * True when the defaults reference an explicit provider credential secret.
 * The trigger's GitHub token secret is deliberately ignored: token-onboarded
 * triggers always carry one, and the server preserves it independently of
 * the saved-credentials toggle.
 */
function hasExplicitProviderCredentials(defaults: AgentRunDefaults): boolean {
  return Boolean(
    defaults.claudeApiKeySecret.trim() ||
      defaults.openaiOauthSecret.trim() ||
      defaults.providerKeys.length,
  );
}

/**
 * The slice of a trigger read model (Cron, GitHubRepository, LinearProject)
 * that prefills the run-defaults editor: the canonical defaults message plus
 * the runtime/MCP policy fields the server resolves from the referenced
 * RuntimeProfile/MCPPolicy.
 */
export interface TriggerDefaultsSource {
  namespace?: string;
  defaults?: AgentRunDefaults;
  permissionMode: string;
  egressMode: string;
  mcpPolicyDefaultAction: string;
  mcpPolicyAllowedServers: string[];
}

/**
 * resolvedTriggerPolicies builds the TriggerPolicies editor state for a
 * trigger read model: the configure_* toggles reflect whether the defaults
 * reference a RuntimeProfile/MCPPolicy, and the mode fields come from the
 * server-resolved policy values (falling back to the platform defaults).
 * Fresh create flows default to provisioning a managed RuntimeProfile.
 */
export function resolvedTriggerPolicies(source?: TriggerDefaultsSource): TriggerPolicies {
  return create(TriggerPoliciesSchema, {
    configureRuntimeProfile: source
      ? Boolean(source.defaults?.runtimeProfileRef)
      : true,
    permissionMode: source?.permissionMode || "workspace-write",
    egressMode: source?.egressMode || "unrestricted",
    configureMcpPolicy: Boolean(source?.defaults?.mcpPolicyRef),
    mcpPolicyDefaultAction: source?.mcpPolicyDefaultAction || "Deny",
    mcpPolicyAllowedServers: source?.mcpPolicyAllowedServers ?? [],
  });
}

/**
 * normalizeTriggerDefaults trims free-text fields, drops empty list entries,
 * and clears explicit secret refs when the caller's saved credentials are
 * used instead — the same normalization buildCronRequest applies before a
 * cron save.
 */
export function normalizeTriggerDefaults(
  d: AgentRunDefaults,
  useSavedCredentials: boolean,
): AgentRunDefaults {
  const saved = useSavedCredentials;
  const providerKeys: ProviderKeyRef[] = saved
    ? []
    : d.providerKeys.filter((key) => key.provider.trim() && key.secretName.trim());
  return create(AgentRunDefaultsSchema, {
    repoUrl: d.repoUrl.trim(),
    additionalRepoUrls: d.additionalRepoUrls.map((url) => url.trim()).filter(Boolean),
    baseBranch: d.baseBranch.trim(),
    image: d.image.trim(),
    model: d.model.trim(),
    allowedModels: d.allowedModels.map((m) => m.trim()).filter(Boolean),
    provider: d.provider,
    authMode: d.authMode,
    reasoningLevel: d.reasoningLevel,
    openaiBaseUrl: d.openaiBaseUrl.trim(),
    openaiApi: d.openaiApi,
    timeout: d.timeout.trim(),
    customInstructions: d.customInstructions.trim(),
    claudeApiKeySecret: saved ? "" : d.claudeApiKeySecret.trim(),
    openaiOauthSecret: saved ? "" : d.openaiOauthSecret.trim(),
    githubTokenSecret: saved ? "" : d.githubTokenSecret.trim(),
    providerKeys,
    runtimeProfileRef: d.runtimeProfileRef.trim(),
    mcpPolicyRef: d.mcpPolicyRef.trim(),
    mcpServerRefs: d.mcpServerRefs.map((ref) => ref.trim()).filter(Boolean),
    skillRefs: d.skillRefs.map((ref) => ref.trim()).filter(Boolean),
    workflowMode: d.workflowMode,
    modeRef: d.modeRef.trim(),
    executionMode: d.executionMode,
  });
}

function cloneDefaults(source: TriggerDefaultsSource): AgentRunDefaults {
  return source.defaults ? clone(AgentRunDefaultsSchema, source.defaults) : emptyDefaults();
}

/**
 * Shared edit dialog for an existing trigger's run defaults and managed
 * runtime/MCP policies. Prefills from the trigger read model and hands the
 * normalized defaults + policies to `onSubmit`, which performs the update
 * RPC (updateGitHubRepository, updateLinearProject, …). The update RPCs
 * replace the trigger's defaults wholesale, so every field is prefilled from
 * the read model to avoid clobbering values the user didn't touch.
 */
export function TriggerDefaultsDialog({
  source,
  trigger,
  title,
  description,
  hideRepository = false,
  idPrefix = "trigger-defaults",
  onSubmit,
}: {
  source: TriggerDefaultsSource;
  trigger: React.ReactElement;
  title: string;
  description: string;
  hideRepository?: boolean;
  idPrefix?: string;
  onSubmit: (
    defaults: AgentRunDefaults,
    policies: TriggerPolicies,
    useSavedCredentials: boolean,
  ) => Promise<void>;
}) {
  const [open, setOpen] = useState(false);
  const [defaults, setDefaults] = useState<AgentRunDefaults>(() => cloneDefaults(source));
  const [policies, setPolicies] = useState<TriggerPolicies>(() => resolvedTriggerPolicies(source));
  const [useSavedCredentials, setUseSavedCredentials] = useState(
    () => !hasExplicitProviderCredentials(source.defaults ?? emptyDefaults()),
  );
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function reset() {
    setDefaults(cloneDefaults(source));
    setPolicies(resolvedTriggerPolicies(source));
    setUseSavedCredentials(!hasExplicitProviderCredentials(source.defaults ?? emptyDefaults()));
    setError(null);
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!defaults.model.trim()) {
      setError("Choose a model — runs created by this trigger require an explicit model.");
      return;
    }
    setError(null);
    setSubmitting(true);
    try {
      await onSubmit(normalizeTriggerDefaults(defaults, useSavedCredentials), policies, useSavedCredentials);
      setOpen(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save run defaults");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        // Re-seed from the (possibly refetched) read model on every open.
        if (nextOpen) reset();
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent
        className="flex w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl max-h-[92vh]"
        showCloseButton
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <div className="flex items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <Settings className="size-4" />
              </span>
              <DialogTitle className="text-base">{title}</DialogTitle>
            </div>
            <DialogDescription>{description}</DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <OptionRows label="Run defaults" className="pt-1">
              <RunDefaultsRows
                idPrefix={idPrefix}
                hideRepository={hideRepository}
                resourceNamespace={source.namespace}
                value={defaults}
                onChange={setDefaults}
                useSavedCredentials={useSavedCredentials}
                onUseSavedCredentialsChange={setUseSavedCredentials}
              />
              <TriggerPolicyRows
                idPrefix={`${idPrefix}-policies`}
                policies={policies}
                onPoliciesChange={setPolicies}
                runtimeProfileRef={defaults.runtimeProfileRef}
                onRuntimeProfileRefChange={(ref) =>
                  setDefaults((prev) => ({ ...prev, runtimeProfileRef: ref }))
                }
                mcpPolicyRef={defaults.mcpPolicyRef}
                onMcpPolicyRefChange={(ref) =>
                  setDefaults((prev) => ({ ...prev, mcpPolicyRef: ref }))
                }
              />
            </OptionRows>

            {error && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            )}
          </div>

          <div className="flex items-center justify-end gap-2 border-t px-6 py-4">
            <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
              Cancel
            </DialogClose>
            <Button type="submit" size="sm" disabled={submitting}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : null}
              {submitting ? "Saving…" : "Save changes"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
