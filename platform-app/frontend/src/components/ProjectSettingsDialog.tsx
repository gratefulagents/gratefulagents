import { create } from "@bufbuild/protobuf";
import {
  Blocks,
  Bot,
  Cpu,
  FolderGit2,
  KeyRound,
  Loader2,
  Settings,
  Settings2,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { RuntimeImagePicker } from "@/components/RuntimeImagePicker";
import { RepoUrlListInput } from "@/components/RepoUrlListInput";
import { ProjectReviewLoopOption } from "@/components/ProjectReviewLoopOption";
import { MCPServerPicker } from "@/components/MCPServerPicker";
import { ModeTemplateSelect } from "@/components/ModeTemplateSelect";
import { UserSecretKeyPicker, UserSecretPicker, type UserSecretOption } from "@/components/UserSecretPicker";
import {
  Chip,
  FlowField,
  FlowSwitchRow,
  OptionRow,
  OptionRows,
  Segmented,
} from "@/components/create-flow/create-flow";
import { PROVIDERS, providerName } from "@/components/create-flow/providers";
import { useAuth } from "@/contexts/AuthContext";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import {
  authModeForProviderSwitch,
  emptyPresence,
  oauthProviders,
  oauthSecretForProviderSwitch,
  projectUsesSavedCredentials,
  providerKeyFor as libProviderKeyFor,
  savedCredentialAvailable,
  savedCredentialProviders,
} from "@/lib/projectCredentialForm";
import type { CredentialPresence } from "@/lib/projectCredentialForm";
import { REASONING_LEVELS } from "@/lib/reasoning";
import { mcpPolicyBlocksServers } from "@/lib/resourceNames";
import { toneText } from "@/lib/status";
import { UpdateProjectRequestSchema } from "@/rpc/platform/service_pb";
import type { Project } from "@/rpc/platform/service_pb";

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

type FormState = {
  displayName: string;
  repoUrl: string;
  additionalRepoUrls: string[];
  reviewLoopDisabled: boolean;
  baseBranch: string;
  provider: string;
  authMode: "api-key" | "oauth";
  model: string;
  reasoningLevel: string;
  modeRef: string;
  image: string;
  timeout: string;
  allowedModels: string;
  customInstructions: string;
  useSavedCredentials: boolean;
  openaiOauthSecret: string;
  githubTokenSecret: string;
  claudeApiKeySecret: string;
  providerKeySecret: string;
  providerKeyKey: string;
  configureRuntimeProfile: boolean;
  runtimeProfileRef: string;
  permissionMode: string;
  egressMode: string;
  configureMcpPolicy: boolean;
  mcpPolicyRef: string;
  mcpPolicyDefaultAction: string;
  mcpPolicyAllowedServers: string;
  mcpServerRefs: string[];
  kubernetesAdmin: boolean;
};

function splitCommaList(value: string): string[] {
  return value
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean);
}

function listKey(values: readonly string[]): string {
  return values.map((value) => value.trim()).filter(Boolean).join("\u0000");
}

const providerKeyFor = libProviderKeyFor;

function formFromProject(project: Project): FormState {
  const provider = project.provider || "openai";
  const key = providerKeyFor(project, provider);
  const authMode = provider === "copilot" ? "oauth" : ((project.authMode as "api-key" | "oauth") || "api-key");
  return {
    displayName: project.displayName || project.name,
    repoUrl: project.repoUrl || "",
    additionalRepoUrls: [...(project.additionalRepoUrls ?? [])],
    reviewLoopDisabled: project.reviewLoopDisabled,
    baseBranch: project.baseBranch || "",
    provider,
    authMode,
    model: project.model || "",
    reasoningLevel: project.reasoningLevel || "",
    modeRef: project.modeRef || "",
    image: project.image || "",
    timeout: project.timeout || "",
    allowedModels: project.allowedModels.join(", "),
    customInstructions: project.customInstructions || "",
    useSavedCredentials: projectUsesSavedCredentials(project, provider, authMode),
    openaiOauthSecret: project.openaiOauthSecret || "",
    githubTokenSecret: project.githubTokenSecret || "",
    claudeApiKeySecret: project.claudeApiKeySecret || "",
    providerKeySecret: key?.secretName || "",
    providerKeyKey: key?.secretKey || "api-key",
    configureRuntimeProfile: false,
    runtimeProfileRef: project.runtimeProfileRef || "",
    permissionMode: project.permissionMode || "workspace-write",
    egressMode: project.egressMode || "unrestricted",
    configureMcpPolicy: false,
    mcpPolicyRef: project.mcpPolicyRef || "",
    mcpPolicyDefaultAction: project.mcpPolicyDefaultAction || "Deny",
    mcpPolicyAllowedServers: (project.mcpPolicyAllowedServers ?? []).join(", "),
    mcpServerRefs: [...(project.mcpServerRefs ?? [])],
    kubernetesAdmin: project.kubernetesAdmin,
  };
}

export function ProjectSettingsDialog({
  project,
  onUpdated,
}: {
  project: Project;
  onUpdated?: (project: Project) => void;
}) {
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<FormState>(() => formFromProject(project));
  const [credentials, setCredentials] = useState<CredentialPresence>(emptyPresence);
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [userSecrets, setUserSecrets] = useState<UserSecretOption[]>([]);
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  const savedSupported = savedCredentialProviders.has(form.provider);
  const oauthSupported = oauthProviders.has(form.provider);
  const effectiveAuthMode = form.provider === "copilot" ? "oauth" : oauthSupported ? form.authMode : "api-key";
  const useSavedCredentials = form.useSavedCredentials && savedSupported;
  const savedReady = savedCredentialAvailable(credentials, form.provider, effectiveAuthMode);

  const allowedModels = useMemo(() => splitCommaList(form.allowedModels), [form.allowedModels]);
  const mcpPolicyAllowedServers = useMemo(
    () => splitCommaList(form.mcpPolicyAllowedServers),
    [form.mcpPolicyAllowedServers],
  );

  useEffect(() => {
    if (!open) return;
    let active = true;
    void client
      .listMyCredentials({})
      .then((next) => {
        if (!active) return;
        setUserSecrets(next.namespace === project.namespace ? (next.secrets ?? []) : []);
        setCredentials({
          anthropicApiKey: next.anthropicApiKeyPresent,
          openaiApiKey: next.openaiApiKeyPresent,
          openrouterApiKey: next.openrouterApiKeyPresent,
          xaiApiKey: next.xaiApiKeyPresent,
          anthropicOauth: next.anthropicOauthPresent,
          openaiOauth: next.openaiOauthPresent,
          copilotOauth: next.copilotOauthPresent,
        });
      })
      .catch(() => {
        if (active) setCredentials(emptyPresence);
      });
    return () => {
      active = false;
    };
  }, [open, project]);

  // Resolve the live provider catalog through the project's persisted
  // credential refs. A provider override lets switches to OpenRouter use the
  // caller's saved OpenAI API key when the project has no OpenRouter key yet.
  useEffect(() => {
    if (!open || !form.provider) return;

    const controller = new AbortController();
    const provider = form.provider;

    async function loadProviderModels() {
      setAvailableModels([]);
      setModelsLoading(true);
      setModelsError(null);
      try {
        const response = await client.listAvailableModels(
          {
            namespace: project.namespace,
            source: { kind: "Project", name: project.name },
            provider,
          },
          { signal: controller.signal },
        );
        if (!controller.signal.aborted) setAvailableModels(response.models);
      } catch (nextError) {
        if (controller.signal.aborted) return;
        setAvailableModels([]);
        setModelsError(
          nextError instanceof Error ? nextError.message : `Failed to load ${providerName(provider)} models`,
        );
      } finally {
        if (!controller.signal.aborted) setModelsLoading(false);
      }
    }

    void loadProviderModels();

    return () => controller.abort();
  }, [open, project.namespace, project.name, form.provider]);

  function handleOpenChange(nextOpen: boolean) {
    if (nextOpen) {
      setForm(formFromProject(project));
      setError(null);
      setAvailableModels([]);
      setModelsLoading(false);
      setModelsError(null);
    }
    setOpen(nextOpen);
  }

  function update<K extends keyof FormState>(field: K, value: FormState[K]) {
    setForm((prev) => ({ ...prev, [field]: value }));
  }

  async function refreshUserSecrets() {
    try {
      const credentials = await client.listMyCredentials({});
      setUserSecrets(credentials.namespace === project.namespace ? (credentials.secrets ?? []) : []);
    } catch {
      // Keep the last successful inventory while the picker remains usable.
    }
  }

  function updateProvider(provider: string) {
    const key = providerKeyFor(project, provider);
    setForm((prev) => {
      // Model fields name models of the previous provider; on a switch either
      // restore the project's own values (switching back) or clear them so the
      // new provider's default applies.
      const projectProvider = project.provider || "openai";
      const model =
        provider === prev.provider ? prev.model : provider === projectProvider ? project.model || "" : "";
      const allowedModels =
        provider === prev.provider
          ? prev.allowedModels
          : provider === projectProvider
            ? project.allowedModels.join(", ")
            : "";
      return {
      ...prev,
      provider,
      model,
      allowedModels,
      authMode: authModeForProviderSwitch(prev.authMode, provider, prev.useSavedCredentials, credentials),
      useSavedCredentials: savedCredentialProviders.has(provider) ? prev.useSavedCredentials : false,
      // Re-derive credential refs for the new provider instead of carrying
      // the old provider's refs along: a stale OAuth secret (e.g. keeping
      // usercred-copilot after switching to anthropic) persists wiring that
      // crashes every new run at pod startup.
      openaiOauthSecret: oauthSecretForProviderSwitch(prev.openaiOauthSecret, provider, credentials),
      claudeApiKeySecret: provider === "anthropic" ? prev.claudeApiKeySecret : "",
      providerKeySecret: key?.secretName || "",
      providerKeyKey: key?.secretKey || "api-key",
      };
    });
  }

  function validate(): string | null {
    if (!form.displayName.trim()) return "Display name is required.";
    if (useSavedCredentials && !savedReady) {
      return `No saved ${form.provider} credential is available for ${effectiveAuthMode}.`;
    }
    if (!useSavedCredentials) {
      if (effectiveAuthMode === "oauth" && !form.openaiOauthSecret.trim()) {
        return "OAuth auth mode requires an OAuth Secret name.";
      }
      if (effectiveAuthMode === "api-key") {
        if (
          form.provider === "anthropic" &&
          !form.claudeApiKeySecret.trim() &&
          !form.providerKeySecret.trim()
        ) {
          return "Anthropic API-key auth requires an Anthropic Secret ref.";
        }
        if (form.provider !== "anthropic" && !form.providerKeySecret.trim()) {
          return `${form.provider} API-key auth requires a provider key Secret ref.`;
        }
      }
    }
    return null;
  }

  function providerKeysForSubmit() {
    const currentProvider = form.provider.toLowerCase();
    const keys = project.providerKeys
      .filter((key) => key.provider.toLowerCase() !== currentProvider)
      .map((key) => ({
        provider: key.provider,
        secretName: key.secretName,
        secretKey: key.secretKey,
      }));
    if (!useSavedCredentials && form.providerKeySecret.trim()) {
      keys.push({
        provider: currentProvider,
        secretName: form.providerKeySecret.trim(),
        secretKey: form.providerKeyKey.trim() || "api-key",
      });
    }
    return keys;
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    const validationError = validate();
    if (validationError) {
      setError(validationError);
      return;
    }
    setSaving(true);
    try {
      const updated = await client.updateProject(create(UpdateProjectRequestSchema, {
        namespace: project.namespace,
        name: project.name,
        displayName: form.displayName.trim(),
        repoUrl: form.repoUrl.trim(),
        additionalRepoUrls: form.additionalRepoUrls.map((url) => url.trim()).filter(Boolean),
        reviewLoopDisabled: form.reviewLoopDisabled,
        baseBranch: form.baseBranch.trim(),
        provider: form.provider,
        authMode: effectiveAuthMode,
        model: form.model.trim(),
        reasoningLevel: form.reasoningLevel,
        // Omit an unchanged mode so name-only dashboard reads do not erase a
        // version/channel pin configured through the Kubernetes API.
        ...(form.modeRef.trim() !== project.modeRef.trim() ? { modeRef: form.modeRef.trim() } : {}),
        image: form.image.trim(),
        timeout: form.timeout.trim(),
        allowedModels,
        customInstructions: form.customInstructions.trim(),
        useSavedCredentials,
        openaiOauthSecret: useSavedCredentials ? "" : form.openaiOauthSecret.trim(),
        githubTokenSecret: useSavedCredentials ? "" : form.githubTokenSecret.trim(),
        claudeApiKeySecret: useSavedCredentials ? "" : form.claudeApiKeySecret.trim(),
        providerKeys: useSavedCredentials ? [] : providerKeysForSubmit(),
        configureRuntimeProfile: form.configureRuntimeProfile,
        runtimeProfileRef: form.runtimeProfileRef.trim(),
        permissionMode: form.permissionMode,
        egressMode: form.egressMode,
        configureMcpPolicy: form.configureMcpPolicy,
        mcpPolicyRef: form.mcpPolicyRef.trim(),
        mcpPolicyDefaultAction: form.mcpPolicyDefaultAction,
        mcpPolicyAllowedServers,
        mcpServerRefs: form.mcpServerRefs,
        skillRefs: [],
        ...(isAdmin ? { kubernetesAdmin: form.kubernetesAdmin } : {}),
      }));
      onUpdated?.(updated);
      setOpen(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to update project");
    } finally {
      setSaving(false);
    }
  }

  /* Collapsed rows act as a live receipt; the dot marks an unsaved change. */
  const initial = formFromProject(project);
  const modelSummaryText = [
    providerName(form.provider),
    form.model.trim() || "provider default",
    form.reasoningLevel || "default reasoning",
    useSavedCredentials ? "saved credentials" : "Secret refs",
  ].join(" · ");
  const modelModified =
    form.provider !== initial.provider ||
    form.authMode !== initial.authMode ||
    form.model.trim() !== initial.model.trim() ||
    form.reasoningLevel !== initial.reasoningLevel ||
    useSavedCredentials !== initial.useSavedCredentials ||
    form.openaiOauthSecret.trim() !== initial.openaiOauthSecret.trim() ||
    form.githubTokenSecret.trim() !== initial.githubTokenSecret.trim() ||
    form.claudeApiKeySecret.trim() !== initial.claudeApiKeySecret.trim() ||
    form.providerKeySecret.trim() !== initial.providerKeySecret.trim() ||
    form.providerKeyKey.trim() !== initial.providerKeyKey.trim();

  const repoExtrasCount = form.additionalRepoUrls.filter((url) => url.trim()).length;
  const repoSummaryText = [
    form.baseBranch.trim() ? `base ${form.baseBranch.trim()}` : "default branch",
    ...(repoExtrasCount
      ? [`+${repoExtrasCount} extra repo${repoExtrasCount === 1 ? "" : "s"}`]
      : []),
  ].join(" · ");
  const repoModified =
    form.repoUrl.trim() !== initial.repoUrl.trim() ||
    form.baseBranch.trim() !== initial.baseBranch.trim() ||
    listKey(form.additionalRepoUrls) !== listKey(initial.additionalRepoUrls);

  const imageTail = form.image.trim()
    ? (form.image.trim().split("/").pop() ?? form.image.trim())
    : "default image";
  const runtimeSummaryText = [
    imageTail,
    form.timeout.trim() || "default timeout",
    form.runtimeProfileRef.trim() || "no profile",
  ].join(" · ");
  const runtimeModified =
    form.image.trim() !== initial.image.trim() ||
    form.timeout.trim() !== initial.timeout.trim() ||
    form.runtimeProfileRef.trim() !== initial.runtimeProfileRef.trim() ||
    form.configureRuntimeProfile !== initial.configureRuntimeProfile ||
    form.permissionMode !== initial.permissionMode ||
    form.egressMode !== initial.egressMode;

  const toolsCount = form.mcpServerRefs.length;
  const toolsSummaryText = toolsCount
    ? `${toolsCount} MCP server${toolsCount === 1 ? "" : "s"}`
    : "None";
  const toolsModified = listKey(form.mcpServerRefs) !== listKey(initial.mcpServerRefs);

  const policySummaryText = form.configureMcpPolicy
    ? `${form.mcpPolicyDefaultAction} by default · updating policy`
    : form.mcpPolicyRef.trim() || "Off";
  const policyModified =
    form.mcpPolicyRef.trim() !== initial.mcpPolicyRef.trim() ||
    form.configureMcpPolicy !== initial.configureMcpPolicy ||
    form.mcpPolicyDefaultAction !== initial.mcpPolicyDefaultAction ||
    listKey(mcpPolicyAllowedServers) !== listKey(splitCommaList(initial.mcpPolicyAllowedServers));

  const advancedCount = [Boolean(form.allowedModels.trim()), Boolean(form.customInstructions.trim())].filter(
    Boolean,
  ).length;
  const advancedModified =
    listKey(allowedModels) !== listKey(splitCommaList(initial.allowedModels)) ||
    form.customInstructions.trim() !== initial.customInstructions.trim();

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogTrigger render={<Button variant="outline" size="sm" />}>
        <Settings className="mr-1 h-3.5 w-3.5" />
        Settings
      </DialogTrigger>
      <DialogContent
        className="flex w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl max-h-[92vh]"
        showCloseButton
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <div className="flex items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <FolderGit2 className="size-4" />
              </span>
              <DialogTitle className="text-base">Project settings</DialogTitle>
            </div>
            <DialogDescription>
              Update the defaults inherited by new agent runs from this project.
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <FlowField id="project-settings-display" label="Display name" required>
              <Input
                id="project-settings-display"
                value={form.displayName}
                onChange={(event) => update("displayName", event.target.value)}
                autoFocus
                required
              />
            </FlowField>
            <FlowField
              id="project-settings-repo"
              label="Repository URL"
              hint="Leave empty to run without a primary repository."
            >
              <div className="relative">
                <FolderGit2 className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  id="project-settings-repo"
                  value={form.repoUrl}
                  onChange={(event) => update("repoUrl", event.target.value)}
                  placeholder="https://github.com/org/repo"
                  className="pl-9"
                />
              </div>
            </FlowField>

            <OptionRows label="Settings" className="pt-1">
              <OptionRow
                icon={Sparkles}
                title="Model & credentials"
                summary={modelSummaryText}
                modified={modelModified}
                defaultOpen
              >
                <div className="flex flex-wrap gap-1.5">
                  {PROVIDERS.map((provider) => (
                    <Chip
                      key={provider.id}
                      selected={form.provider === provider.id}
                      onSelect={() => updateProvider(provider.id)}
                    >
                      {provider.name}
                    </Chip>
                  ))}
                </div>

                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="project-settings-model" label="Model">
                    <Input
                      id="project-settings-model"
                      value={form.model}
                      onChange={(event) => update("model", event.target.value)}
                      placeholder={availableModels.length ? "Choose a model" : "Provider default"}
                      list={availableModels.length ? "project-settings-model-options" : undefined}
                    />
                    {availableModels.length > 0 ? (
                      <datalist id="project-settings-model-options">
                        {availableModels.map((model) => (
                          <option key={model} value={model} />
                        ))}
                      </datalist>
                    ) : null}
                    {(modelsLoading || modelsError || availableModels.length > 0) && (
                      <p className="text-[11px] text-muted-foreground" aria-live="polite">
                        {modelsLoading
                          ? `Loading ${providerName(form.provider)} models…`
                          : modelsError
                            ? `Could not load ${providerName(form.provider)} models: ${modelsError}`
                            : `${availableModels.length} ${providerName(form.provider)} models available`}
                      </p>
                    )}
                  </FlowField>
                  <FlowField id="project-settings-reasoning" label="Reasoning level">
                    <select
                      id="project-settings-reasoning"
                      value={form.reasoningLevel}
                      onChange={(event) => update("reasoningLevel", event.target.value)}
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

                {form.provider !== "copilot" && oauthSupported ? (
                  <FlowSwitchRow
                    label="Authentication"
                    control={
                      <Segmented
                        aria-label="Authentication"
                        value={form.authMode}
                        onChange={(value) => update("authMode", value)}
                        options={[
                          { value: "api-key", label: "API key" },
                          { value: "oauth", label: "OAuth" },
                        ]}
                      />
                    }
                  />
                ) : (
                  <p className="text-[11px] leading-relaxed text-muted-foreground">
                    {form.provider === "copilot"
                      ? "GitHub Copilot uses OAuth authentication."
                      : `${providerName(form.provider)} uses API-key authentication.`}
                  </p>
                )}

                {savedSupported ? (
                  <FlowSwitchRow
                    id="project-settings-use-saved"
                    label="Use my saved provider credentials"
                    hint={
                      useSavedCredentials ? (
                        savedReady ? (
                          <span className="inline-flex items-center gap-1.5">
                            <ShieldCheck className="size-3.5 shrink-0 text-[color:var(--tone-success-fg)]" />
                            Your saved {providerName(form.provider)} credential is ready.
                          </span>
                        ) : (
                          `No saved ${providerName(form.provider)} credential is available for ${effectiveAuthMode}.`
                        )
                      ) : (
                        "Use Kubernetes Secret references managed with this project."
                      )
                    }
                    control={
                      <Switch
                        id="project-settings-use-saved"
                        checked={useSavedCredentials}
                        disabled={!savedSupported}
                        onCheckedChange={(checked) => update("useSavedCredentials", checked)}
                      />
                    }
                  />
                ) : null}

                {!useSavedCredentials ? (
                  <div className="grid gap-4 sm:grid-cols-2">
                    {effectiveAuthMode === "oauth" ? (
                      <FlowField id="project-settings-oauth" label="OAuth Secret">
                      <UserSecretPicker
                        id="project-settings-oauth"
                        value={form.openaiOauthSecret}
                        secrets={userSecrets}
                        onOpen={() => void refreshUserSecrets()}
                        onChange={(secretName) => update("openaiOauthSecret", secretName)}
                      />
                      </FlowField>
                    ) : (
                      <>
                        {form.provider === "anthropic" ? (
                          <FlowField id="project-settings-anthropic-secret" label="Anthropic Secret">
                          <UserSecretPicker
                            id="project-settings-anthropic-secret"
                            value={form.claudeApiKeySecret}
                            secrets={userSecrets}
                            onOpen={() => void refreshUserSecrets()}
                            onChange={(secretName) => update("claudeApiKeySecret", secretName)}
                          />
                          </FlowField>
                        ) : null}
                        <FlowField id="project-settings-provider-secret" label="Provider key Secret">
                          <UserSecretPicker
                            id="project-settings-provider-secret"
                            value={form.providerKeySecret}
                            secrets={userSecrets}
                            onOpen={() => void refreshUserSecrets()}
                            onChange={(secretName) => {
                              update("providerKeySecret", secretName);
                              update("providerKeyKey", "");
                            }}
                          />
                        </FlowField>
                        <FlowField id="project-settings-provider-key" label="Provider key field">
                          <UserSecretKeyPicker
                            id="project-settings-provider-key"
                            value={form.providerKeyKey}
                            secretName={form.providerKeySecret}
                            secrets={userSecrets}
                            onChange={(secretKey) => update("providerKeyKey", secretKey)}
                          />
                        </FlowField>
                      </>
                    )}
                    <FlowField id="project-settings-github-secret" label="GitHub token Secret">
                      <UserSecretPicker
                        id="project-settings-github-secret"
                        value={form.githubTokenSecret}
                        secrets={userSecrets}
                        onOpen={() => void refreshUserSecrets()}
                        onChange={(secretName) => update("githubTokenSecret", secretName)}
                      />
                    </FlowField>
                  </div>
                ) : null}
              </OptionRow>

              <OptionRow
                icon={Bot}
                title="Default mode"
                summary={form.modeRef.trim() || "Interactive"}
                modified={form.modeRef.trim() !== initial.modeRef.trim()}
              >
                <FlowField
                  id="project-settings-mode-ref"
                  label="Mode template"
                  hint="Sets the behavior and tool policy inherited by new runs in this project."
                >
                  <ModeTemplateSelect
                    id="project-settings-mode-ref"
                    value={form.modeRef}
                    enabled={open}
                    onChange={(value) => update("modeRef", value)}
                  />
                </FlowField>
              </OptionRow>

              <OptionRow
                icon={FolderGit2}
                title="Repository details"
                summary={repoSummaryText}
                modified={repoModified}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="project-settings-branch" label="Base branch">
                    <Input
                      id="project-settings-branch"
                      value={form.baseBranch}
                      onChange={(event) => update("baseBranch", event.target.value)}
                      placeholder="main"
                    />
                  </FlowField>
                </div>
                <FlowField
                  id="project-settings-additional-repo-0"
                  label="Additional repositories"
                  hint="Extra repos cloned into every run alongside the primary one."
                >
                  <RepoUrlListInput
                    idPrefix="project-settings-additional-repo"
                    value={form.additionalRepoUrls}
                    onChange={(urls) => update("additionalRepoUrls", urls)}
                  />
                </FlowField>
              </OptionRow>

              <ProjectReviewLoopOption
                id="project-settings-review-loop-disabled"
                disabled={form.reviewLoopDisabled}
                modified={form.reviewLoopDisabled !== initial.reviewLoopDisabled}
                onDisabledChange={(checked) => update("reviewLoopDisabled", checked)}
              />

              <OptionRow
                icon={Cpu}
                title="Runtime"
                summary={runtimeSummaryText}
                modified={runtimeModified}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="project-settings-image" label="Runtime image" hint="Pick your language.">
                    <RuntimeImagePicker
                      id="project-settings-image"
                      value={form.image}
                      onChange={(image) => update("image", image)}
                    />
                  </FlowField>
                  <FlowField id="project-settings-timeout" label="Timeout" hint="For example, 30m.">
                    <Input
                      id="project-settings-timeout"
                      value={form.timeout}
                      onChange={(event) => update("timeout", event.target.value)}
                      placeholder="30m"
                    />
                  </FlowField>
                </div>
                <FlowSwitchRow
                  id="project-settings-configure-runtime"
                  label="Create/update a RuntimeProfile"
                  hint="Controls sandbox permissions and network egress for this project's runs."
                  control={
                    <Switch
                      id="project-settings-configure-runtime"
                      checked={form.configureRuntimeProfile}
                      onCheckedChange={(checked) => update("configureRuntimeProfile", checked)}
                    />
                  }
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="project-settings-runtime" label="RuntimeProfile ref">
                    <Input
                      id="project-settings-runtime"
                      value={form.runtimeProfileRef}
                      onChange={(event) => update("runtimeProfileRef", event.target.value)}
                      placeholder={`${project.name}-runtime`}
                    />
                  </FlowField>
                  {form.configureRuntimeProfile ? (
                    <>
                      <FlowField id="project-settings-permission" label="Permission mode">
                        <select
                          id="project-settings-permission"
                          value={form.permissionMode}
                          onChange={(event) => update("permissionMode", event.target.value)}
                          className={selectClassName}
                        >
                          <option value="read-only">read-only</option>
                          <option value="workspace-write">workspace-write</option>
                          <option value="danger-full-access">danger-full-access</option>
                        </select>
                      </FlowField>
                      <FlowField id="project-settings-egress" label="Network egress">
                        <select
                          id="project-settings-egress"
                          value={form.egressMode}
                          onChange={(event) => update("egressMode", event.target.value)}
                          className={selectClassName}
                        >
                          <option value="unrestricted">unrestricted</option>
                          <option value="restricted">restricted</option>
                          <option value="disabled">disabled</option>
                        </select>
                      </FlowField>
                    </>
                  ) : null}
                </div>
              </OptionRow>

              <OptionRow
                icon={Blocks}
                title="Tools"
                summary={toolsSummaryText}
                modified={toolsModified}
              >
                <FlowField label="MCP servers" hint="Server configs attached to this project's runs.">
                  <MCPServerPicker
                    selected={form.mcpServerRefs}
                    onChange={(names) => update("mcpServerRefs", names)}
                  />
                </FlowField>
                {mcpPolicyBlocksServers(
                  form.configureMcpPolicy,
                  form.mcpPolicyDefaultAction,
                  mcpPolicyAllowedServers,
                  form.mcpServerRefs,
                ) && (
                  <p className={cn("text-[12px]", toneText.warning)}>
                    Your MCP policy denies by default — add the selected server names to its allowed
                    servers or their tools won't load.
                  </p>
                )}
              </OptionRow>

              <OptionRow
                icon={KeyRound}
                title="MCP policy"
                summary={policySummaryText}
                modified={policyModified}
              >
                <FlowSwitchRow
                  id="project-settings-configure-mcp"
                  label="Create/update an MCPPolicy"
                  hint="Restricts which MCP servers this project's runs may reach."
                  control={
                    <Switch
                      id="project-settings-configure-mcp"
                      checked={form.configureMcpPolicy}
                      onCheckedChange={(checked) => update("configureMcpPolicy", checked)}
                    />
                  }
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="project-settings-mcp" label="MCPPolicy ref">
                    <Input
                      id="project-settings-mcp"
                      value={form.mcpPolicyRef}
                      onChange={(event) => update("mcpPolicyRef", event.target.value)}
                      placeholder={`${project.name}-mcp-policy`}
                    />
                  </FlowField>
                  {form.configureMcpPolicy ? (
                    <>
                      <FlowField id="project-settings-mcp-action" label="Default action">
                        <select
                          id="project-settings-mcp-action"
                          value={form.mcpPolicyDefaultAction}
                          onChange={(event) => update("mcpPolicyDefaultAction", event.target.value)}
                          className={selectClassName}
                        >
                          <option value="Deny">Deny</option>
                          <option value="Allow">Allow</option>
                        </select>
                      </FlowField>
                      <FlowField
                        id="project-settings-mcp-servers"
                        label="Allowed MCP servers"
                        hint="Comma-separated."
                      >
                        <Input
                          id="project-settings-mcp-servers"
                          value={form.mcpPolicyAllowedServers}
                          onChange={(event) => update("mcpPolicyAllowedServers", event.target.value)}
                          placeholder="fetch, github"
                        />
                      </FlowField>
                    </>
                  ) : null}
                </div>
              </OptionRow>

              {isAdmin ? (
                <OptionRow
                  icon={ShieldCheck}
                  title="Cluster access"
                  summary={form.kubernetesAdmin ? "Kubernetes admin" : "Standard access"}
                  modified={form.kubernetesAdmin !== initial.kubernetesAdmin}
                >
                  <FlowSwitchRow
                    id="project-settings-kubernetes-admin"
                    label="Kubernetes admin"
                    hint="Grant this project's runs cluster-admin RBAC and read-only platform introspection tools."
                    control={
                      <Switch
                        id="project-settings-kubernetes-admin"
                        checked={form.kubernetesAdmin}
                        onCheckedChange={(checked) => update("kubernetesAdmin", checked)}
                      />
                    }
                  />
                </OptionRow>
              ) : null}

              <OptionRow
                icon={Settings2}
                title="Advanced"
                summary={advancedCount ? `${advancedCount} configured` : "Defaults"}
                modified={advancedModified}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="project-settings-allowed" label="Allowed models" hint="Comma-separated.">
                    <Input
                      id="project-settings-allowed"
                      value={form.allowedModels}
                      onChange={(event) => update("allowedModels", event.target.value)}
                      placeholder="model-a, model-b"
                    />
                  </FlowField>
                </div>
                <FlowField id="project-settings-instructions" label="Custom instructions">
                  <Textarea
                    id="project-settings-instructions"
                    value={form.customInstructions}
                    onChange={(event) => update("customInstructions", event.target.value)}
                    className="min-h-24"
                    placeholder="Project-specific guidance for the agent…"
                  />
                </FlowField>
              </OptionRow>
            </OptionRows>

            {error ? (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            ) : null}
          </div>

          <div className="flex items-center justify-between gap-3 border-t px-6 py-4">
            <p className="min-w-0 truncate text-xs text-muted-foreground">
              Editing{" "}
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground">
                {project.namespace}/{project.name}
              </code>
            </p>
            <div className="flex shrink-0 items-center gap-2">
              <DialogClose
                render={<Button type="button" variant="ghost" size="sm" disabled={saving} />}
              >
                Cancel
              </DialogClose>
              <Button type="submit" size="sm" disabled={saving}>
                {saving ? <Loader2 className="size-4 animate-spin" /> : <Settings className="size-4" />}
                {saving ? "Saving…" : "Save settings"}
              </Button>
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
