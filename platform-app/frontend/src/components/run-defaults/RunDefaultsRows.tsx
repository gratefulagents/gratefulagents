import { useEffect, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Blocks, Cpu, FolderGit2, Plus, Settings2, Sparkles, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import {
  Chip,
  FlowField,
  FlowSwitchRow,
  OptionRow,
  Segmented,
} from "@/components/create-flow/create-flow";
import { PROVIDERS, providerMeta } from "@/components/create-flow/providers";
import { RepoUrlListInput } from "@/components/RepoUrlListInput";
import { RuntimeImagePicker } from "@/components/RuntimeImagePicker";
import { MCPServerPicker } from "@/components/MCPServerPicker";
import { SkillPicker } from "@/components/SkillPicker";
import { UserSecretKeyPicker, UserSecretPicker, type UserSecretOption } from "@/components/UserSecretPicker";
import {
  advancedSummary,
  modelSummary,
  repoSummary,
  runtimeSummary,
  toolsSummary,
} from "@/components/run-defaults/helpers";
import { useMySecretInventory } from "@/hooks/useMySecretInventory";
import { client } from "@/lib/client";
import { REASONING_LEVELS } from "@/lib/reasoning";
import {
  ProviderKeyRefSchema,
  type AgentRunDefaults,
  type MyCredentials,
} from "@/rpc/platform/service_pb";

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

export interface RunDefaultsRowsProps {
  value: AgentRunDefaults;
  onChange: (value: AgentRunDefaults) => void;
  /** When true, explicit secret ref fields are hidden and the server wires the caller's saved credentials. */
  useSavedCredentials: boolean;
  onUseSavedCredentialsChange: (value: boolean) => void;
  /** Prefix for element ids so multiple forms can coexist. */
  idPrefix?: string;
  /** Hide the Repository row when the host dialog derives the repo itself (e.g. owner/repo triggers). */
  hideRepository?: boolean;
  /** Hide workflow/execution/mode fields when the host fixes orchestration (e.g. auto reviewers). */
  hideOrchestration?: boolean;
  /** With a fixed primary repo, still expose base branch and additional repos. */
  showRepositoryOptions?: boolean;
  /** Hide GitHub token input when repository onboarding owns GitHub auth. */
  hideGitHubCredentials?: boolean;
  /** Namespace where these Secret refs resolve; caller-only options are hidden when it is shared. */
  resourceNamespace?: string;
}

// savedCredentialPresent reports whether any saved credential exists that the
// backend can use for the provider, regardless of the selected auth mode: the
// server prefers OAuth when present and falls back gracefully, and reuses the
// saved OpenAI API key for OpenAI-compatible providers.
function savedCredentialPresent(creds: MyCredentials, provider: string): boolean {
  switch (provider) {
    case "anthropic":
      return creds.anthropicApiKeyPresent || creds.anthropicOauthPresent;
    case "openai":
      return creds.openaiApiKeyPresent || creds.openaiOauthPresent;
    case "copilot":
      return creds.copilotOauthPresent;
    case "openrouter":
      return creds.openrouterApiKeyPresent;
    case "xai":
      return creds.xaiApiKeyPresent;
    case "gemini":
    case "groq":
      return creds.openaiApiKeyPresent;
    default:
      return false;
  }
}

/**
 * Trigger-agnostic editor for an AgentRunDefaults message, rendered as flat
 * OptionRow disclosures (Repository / Model / Runtime / Tools / Advanced)
 * whose collapsed state summarizes the current values. Compose inside an
 * <OptionRows> stack. Used by the Cron create/edit dialog and intended to be
 * reused by other trigger editors (Project, GitHubRepository, LinearProject).
 */
export function RunDefaultsRows({
  value,
  onChange,
  useSavedCredentials,
  onUseSavedCredentialsChange,
  idPrefix = "run-defaults",
  hideRepository = false,
  hideOrchestration = false,
  showRepositoryOptions = false,
  hideGitHubCredentials = false,
  resourceNamespace,
}: RunDefaultsRowsProps) {
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);
  const [savedCreds, setSavedCreds] = useState<MyCredentials | null>(null);
  const secretInventory = useMySecretInventory(resourceNamespace);

  const meta = providerMeta(value.provider);
  const isCopilot = value.provider === "copilot";
  const useSaved = useSavedCredentials && Boolean(meta.savedSupported);

  function set<K extends keyof AgentRunDefaults>(field: K, fieldValue: AgentRunDefaults[K]) {
    onChange({ ...value, [field]: fieldValue });
  }

  useEffect(() => {
    if (!useSaved) return;
    const controller = new AbortController();
    client
      .listMyCredentials({}, { signal: controller.signal })
      .then((creds) => {
        if (!controller.signal.aborted) setSavedCreds(creds);
      })
      .catch(() => {
        if (!controller.signal.aborted) setSavedCreds(null);
      });
    return () => controller.abort();
  }, [useSaved]);

  // When only one credential kind is saved for the provider, align the auth
  // mode to it so the run doesn't target a credential that doesn't exist.
  // Only flips when the current mode's credential is absent and the other is
  // present, so it never loops.
  useEffect(() => {
    if (!useSaved || !savedCreds || meta.oauthOnly) return;
    let apiKeyPresent: boolean;
    let oauthPresent: boolean;
    if (value.provider === "anthropic") {
      apiKeyPresent = savedCreds.anthropicApiKeyPresent;
      oauthPresent = savedCreds.anthropicOauthPresent;
    } else if (value.provider === "openai") {
      apiKeyPresent = savedCreds.openaiApiKeyPresent;
      oauthPresent = savedCreds.openaiOauthPresent;
    } else {
      return;
    }
    if (value.authMode === "oauth" && !oauthPresent && apiKeyPresent) {
      onChange({ ...value, authMode: "api-key" });
    } else if (value.authMode !== "oauth" && !apiKeyPresent && oauthPresent) {
      onChange({ ...value, authMode: "oauth" });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [useSaved, savedCreds, meta.oauthOnly, value.provider, value.authMode]);

  // Offer the provider's models as suggestions whenever any saved credential
  // exists for it; the backend honors auth_mode with graceful fallback to
  // whatever credential is actually saved.
  useEffect(() => {
    const provider = value.provider;
    const authMode = meta.oauthOnly ? "oauth" : value.authMode;
    const controller = new AbortController();

    async function loadProviderModels() {
      if (!useSaved || !savedCreds || !savedCredentialPresent(savedCreds, provider)) {
        if (!controller.signal.aborted) {
          setAvailableModels([]);
          setModelsLoading(false);
          setModelsError(null);
        }
        return;
      }
      setAvailableModels([]);
      setModelsLoading(true);
      setModelsError(null);
      try {
        const resp = await client.listAvailableModels(
          { namespace: savedCreds.namespace, provider, authMode },
          { signal: controller.signal },
        );
        if (!controller.signal.aborted) setAvailableModels(resp.models);
      } catch (error) {
        if (!controller.signal.aborted) {
          setAvailableModels([]);
          setModelsError(error instanceof Error ? error.message : "Failed to load provider models");
        }
      } finally {
        if (!controller.signal.aborted) setModelsLoading(false);
      }
    }

    void loadProviderModels();
    return () => controller.abort();
  }, [useSaved, savedCreds, value.provider, value.authMode, meta.oauthOnly]);

  return (
    <>
      {/* Repository */}
      {!hideRepository ? (
      <OptionRow
        icon={FolderGit2}
        title="Repository"
        summary={repoSummary(value)}
        modified={Boolean(value.repoUrl.trim() || value.additionalRepoUrls.length)}
      >
        <FlowField
          id={`${idPrefix}-repo-url`}
          label="Repository URL"
          hint="Optional — leave empty for repoless runs."
        >
          <Input
            id={`${idPrefix}-repo-url`}
            value={value.repoUrl}
            onChange={(event) => set("repoUrl", event.target.value)}
            placeholder="https://github.com/org/repo"
          />
        </FlowField>
        <div className="grid gap-4 sm:grid-cols-2">
          <FlowField id={`${idPrefix}-base-branch`} label="Base branch">
            <Input
              id={`${idPrefix}-base-branch`}
              value={value.baseBranch}
              onChange={(event) => set("baseBranch", event.target.value)}
              placeholder="main"
            />
          </FlowField>
        </div>
        <FlowField
          id={`${idPrefix}-additional-repo-0`}
          label="Additional repositories"
          hint="Extra repos cloned into every run."
        >
          <RepoUrlListInput
            idPrefix={`${idPrefix}-additional-repo`}
            value={value.additionalRepoUrls}
            onChange={(urls) => set("additionalRepoUrls", urls)}
          />
        </FlowField>
      </OptionRow>
      ) : showRepositoryOptions ? (
        <OptionRow
          icon={FolderGit2}
          title="Repository options"
          summary={value.additionalRepoUrls.length ? `${value.additionalRepoUrls.length} additional` : value.baseBranch || "PR defaults"}
          modified={Boolean(value.baseBranch.trim() || value.additionalRepoUrls.length)}
        >
          <FlowField id={`${idPrefix}-base-branch`} label="Fallback base branch" hint="The pull request base branch takes precedence.">
            <Input
              id={`${idPrefix}-base-branch`}
              value={value.baseBranch}
              onChange={(event) => set("baseBranch", event.target.value)}
              placeholder="main"
            />
          </FlowField>
          <FlowField
            id={`${idPrefix}-additional-repo-0`}
            label="Additional repositories"
            hint="Extra repos cloned alongside the pull request repository."
          >
            <RepoUrlListInput
              idPrefix={`${idPrefix}-additional-repo`}
              value={value.additionalRepoUrls}
              onChange={(urls) => set("additionalRepoUrls", urls)}
            />
          </FlowField>
        </OptionRow>
      ) : null}

      {/* Model & credentials */}
      <OptionRow
        icon={Sparkles}
        title="Model"
        summary={modelSummary(value, useSaved)}
        modified={value.provider !== "anthropic" || Boolean(value.model.trim()) || !useSaved}
      >
        <div className="flex flex-wrap gap-1.5">
          {PROVIDERS.map((p) => (
            <Chip
              key={p.id}
              selected={value.provider === p.id}
              onSelect={() =>
                onChange({
                  ...value,
                  provider: p.id,
                  authMode: p.oauthOnly ? "oauth" : value.authMode,
                })
              }
            >
              {p.name}
            </Chip>
          ))}
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <FlowField
            id={`${idPrefix}-model`}
            label="Model"
            hint={isCopilot ? "Required for Copilot." : undefined}
            required={isCopilot}
          >
            {availableModels.length > 0 ? (
              <select
                id={`${idPrefix}-model`}
                value={value.model}
                onChange={(event) => set("model", event.target.value)}
                className={selectClassName}
              >
                {!value.model.trim() ? <option value="">Choose a model</option> : null}
                {value.model.trim() && !availableModels.includes(value.model) ? (
                  <option value={value.model}>{value.model}</option>
                ) : null}
                {availableModels.map((model) => (
                  <option key={model} value={model}>{model}</option>
                ))}
              </select>
            ) : (
              <Input
                id={`${idPrefix}-model`}
                value={value.model}
                onChange={(event) => set("model", event.target.value)}
                placeholder={isCopilot ? "gpt-4.1" : "Provider default"}
              />
            )}
            <p className="text-[11px] text-muted-foreground" aria-live="polite">
              {modelsLoading
                ? `Loading ${meta.name} models…`
                : modelsError
                  ? `Could not load ${meta.name} models: ${modelsError}`
                  : availableModels.length > 0
                    ? `${availableModels.length} ${meta.name} models available`
                    : "Enter a model ID"}
            </p>
          </FlowField>
          <FlowField id={`${idPrefix}-reasoning-level`} label="Reasoning level">
            <select
              id={`${idPrefix}-reasoning-level`}
              value={value.reasoningLevel}
              onChange={(event) => set("reasoningLevel", event.target.value)}
              className={selectClassName}
            >
              {REASONING_LEVELS.map((level) => (
                <option key={level || "default"} value={level}>
                  {level || "default"}
                </option>
              ))}
            </select>
          </FlowField>
        </div>
        {!meta.oauthOnly ? (
          <FlowSwitchRow
            label="Authentication"
            control={
              <Segmented
                aria-label="Authentication"
                value={value.authMode === "oauth" ? "oauth" : "api-key"}
                onChange={(mode) => set("authMode", mode)}
                options={[
                  { value: "api-key", label: "API key" },
                  { value: "oauth", label: "OAuth" },
                ]}
              />
            }
          />
        ) : (
          <p className="text-[11px] leading-relaxed text-muted-foreground">
            {meta.name} uses OAuth — sign in once under Settings and reuse it here.
          </p>
        )}
        <FlowSwitchRow
          id={`${idPrefix}-use-saved`}
          label="Use my saved credentials"
          hint={
            useSavedCredentials
              ? "Your saved provider credential and GitHub token are wired in automatically."
              : undefined
          }
          control={
            <Switch
              id={`${idPrefix}-use-saved`}
              checked={useSavedCredentials}
              onCheckedChange={onUseSavedCredentialsChange}
            />
          }
        />
        {!useSavedCredentials ? (
          <div className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <FlowField
                id={`${idPrefix}-claude-secret`}
                label="Claude API key secret"
                hint="Secret name."
              >
                <UserSecretPicker
                  id={`${idPrefix}-claude-secret`}
                  value={value.claudeApiKeySecret}
                  secrets={secretInventory.secrets}
                  loading={secretInventory.loading}
                  onOpen={() => void secretInventory.reload()}
                  onChange={(secretName) => set("claudeApiKeySecret", secretName)}
                />
              </FlowField>
              <FlowField
                id={`${idPrefix}-oauth-secret`}
                label="OAuth secret"
                hint="Secret with auth.json."
              >
                <UserSecretPicker
                  id={`${idPrefix}-oauth-secret`}
                  value={value.openaiOauthSecret}
                  secrets={secretInventory.secrets}
                  loading={secretInventory.loading}
                  onOpen={() => void secretInventory.reload()}
                  onChange={(secretName) => set("openaiOauthSecret", secretName)}
                />
              </FlowField>
              {!hideGitHubCredentials ? (
                <FlowField
                  id={`${idPrefix}-github-secret`}
                  label="GitHub token secret"
                  hint="Secret name."
                >
                  <UserSecretPicker
                    id={`${idPrefix}-github-secret`}
                    value={value.githubTokenSecret}
                    secrets={secretInventory.secrets}
                    loading={secretInventory.loading}
                    onOpen={() => void secretInventory.reload()}
                    onChange={(secretName) => set("githubTokenSecret", secretName)}
                  />
                </FlowField>
              ) : null}
            </div>
            <ProviderKeysEditor
              idPrefix={idPrefix}
              value={value.providerKeys}
              secrets={secretInventory.secrets}
              loading={secretInventory.loading}
              onOpen={() => void secretInventory.reload()}
              onChange={(keys) => set("providerKeys", keys)}
            />
          </div>
        ) : null}
      </OptionRow>

      {/* Runtime */}
      <OptionRow
        icon={Cpu}
        title="Runtime"
        summary={runtimeSummary(value)}
        modified={Boolean(value.image.trim() || value.timeout.trim())}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <FlowField id={`${idPrefix}-image`} label="Runtime image" hint="Pick your language.">
            <RuntimeImagePicker
              id={`${idPrefix}-image`}
              value={value.image}
              onChange={(image) => set("image", image)}
            />
          </FlowField>
          <FlowField id={`${idPrefix}-timeout`} label="Timeout" hint="e.g. 30m.">
            <Input
              id={`${idPrefix}-timeout`}
              value={value.timeout}
              onChange={(event) => set("timeout", event.target.value)}
              placeholder="30m"
            />
          </FlowField>
        </div>
      </OptionRow>

      {/* Tools */}
      <OptionRow
        icon={Blocks}
        title="Tools & skills"
        summary={toolsSummary(value)}
        modified={Boolean(value.mcpServerRefs.length || value.skillRefs.length)}
      >
        <FlowField label="MCP servers" hint="Server configs attached to these runs.">
          <MCPServerPicker
            selected={value.mcpServerRefs}
            onChange={(names) => set("mcpServerRefs", names)}
          />
        </FlowField>
        <FlowField label="Skills" hint="Reusable agent skills attached to these runs.">
          <SkillPicker
            selected={value.skillRefs}
            onChange={(names) => set("skillRefs", names)}
          />
        </FlowField>
      </OptionRow>

      {/* Advanced */}
      <OptionRow icon={Settings2} title="Advanced" summary={advancedSummary(value)}>
        <div className="grid gap-4 sm:grid-cols-2">
          <FlowField
            id={`${idPrefix}-allowed-models`}
            label="Allowed models"
            hint="Comma-separated."
          >
            <Input
              id={`${idPrefix}-allowed-models`}
              value={value.allowedModels.join(", ")}
              onChange={(event) =>
                set(
                  "allowedModels",
                  event.target.value.split(",").map((part) => part.trim()).filter(Boolean),
                )
              }
              placeholder="model-a, model-b"
            />
          </FlowField>
          <FlowField id={`${idPrefix}-openai-base-url`} label="OpenAI base URL">
            <Input
              id={`${idPrefix}-openai-base-url`}
              value={value.openaiBaseUrl}
              onChange={(event) => set("openaiBaseUrl", event.target.value)}
              placeholder="https://api.openai.com/v1"
            />
          </FlowField>
          <FlowField id={`${idPrefix}-openai-api`} label="OpenAI API">
            <select
              id={`${idPrefix}-openai-api`}
              value={value.openaiApi}
              onChange={(event) => set("openaiApi", event.target.value)}
              className={selectClassName}
            >
              <option value="">default</option>
              <option value="responses">responses</option>
              <option value="chat-completions">chat-completions</option>
            </select>
          </FlowField>
          {!hideOrchestration ? (
            <>
              <FlowField id={`${idPrefix}-execution-mode`} label="Execution mode">
                <select
                  id={`${idPrefix}-execution-mode`}
                  value={value.executionMode}
                  onChange={(event) => set("executionMode", event.target.value)}
                  className={selectClassName}
                >
                  <option value="">default</option>
                  <option value="linear">linear</option>
                  <option value="team">team</option>
                </select>
              </FlowField>
              <FlowField id={`${idPrefix}-mode-ref`} label="Mode ref" hint="ModeTemplate name.">
                <Input
                  id={`${idPrefix}-mode-ref`}
                  value={value.modeRef}
                  onChange={(event) => set("modeRef", event.target.value)}
                  placeholder="my-mode"
                />
              </FlowField>
            </>
          ) : null}
        </div>
        <FlowField id={`${idPrefix}-custom-instructions`} label="Custom instructions">
          <Textarea
            id={`${idPrefix}-custom-instructions`}
            value={value.customInstructions}
            onChange={(event) => set("customInstructions", event.target.value)}
            className="min-h-24"
            placeholder="Extra guidance for the agent…"
          />
        </FlowField>
      </OptionRow>
    </>
  );
}

function ProviderKeysEditor({
  idPrefix,
  value,
  secrets,
  loading,
  onOpen,
  onChange,
}: {
  idPrefix: string;
  value: AgentRunDefaults["providerKeys"];
  secrets: UserSecretOption[];
  loading: boolean;
  onOpen: () => void;
  onChange: (keys: AgentRunDefaults["providerKeys"]) => void;
}) {
  function updateAt(index: number, patch: Partial<{ provider: string; secretName: string; secretKey: string }>) {
    onChange(value.map((key, i) => (i === index ? { ...key, ...patch } : key)));
  }
  return (
    <div className="space-y-2">
      <div className="flex items-baseline justify-between gap-2">
        <Label className="text-[12.5px]">Provider keys</Label>
        <span className="text-[11px] text-muted-foreground">Secret refs per provider</span>
      </div>
      {value.map((key, index) => (
        <div key={index} className="flex items-center gap-2">
          <Input
            id={`${idPrefix}-provider-key-${index}-provider`}
            value={key.provider}
            onChange={(event) => updateAt(index, { provider: event.target.value })}
            placeholder="provider"
            aria-label="Provider"
          />
          <UserSecretPicker
            id={`${idPrefix}-provider-key-${index}-secret`}
            value={key.secretName}
            secrets={secrets}
            loading={loading}
            onOpen={onOpen}
            onChange={(secretName) => updateAt(index, { secretName, secretKey: "" })}
            ariaLabel="Secret name"
          />
          <UserSecretKeyPicker
            id={`${idPrefix}-provider-key-${index}-key`}
            value={key.secretKey}
            secretName={key.secretName}
            secrets={secrets}
            onChange={(secretKey) => updateAt(index, { secretKey })}
            ariaLabel="Secret key"
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            aria-label="Remove provider key"
            onClick={() => onChange(value.filter((_, i) => i !== index))}
          >
            <X className="size-4" />
          </Button>
        </div>
      ))}
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => onChange([...value, create(ProviderKeyRefSchema, { secretKey: "api-key" })])}
      >
        <Plus className="size-3.5" />
        Add provider key
      </Button>
    </div>
  );
}
