import { create } from "@bufbuild/protobuf";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  Blocks,
  Cpu,
  Eye,
  EyeOff,
  FolderGit2,
  KeyRound,
  Loader2,
  Plus,
  Settings2,
  ShieldCheck,
  Sparkles,
} from "lucide-react";
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
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import {
  Chip,
  FlowField,
  FlowSwitchRow,
  OptionRow,
  OptionRows,
  Segmented,
} from "@/components/create-flow/create-flow";
import { PROVIDERS, providerMeta } from "@/components/create-flow/providers";
import { useCreateProject } from "@/hooks/useCreateProject";
import { RuntimeImagePicker } from "@/components/RuntimeImagePicker";
import { RepoUrlListInput } from "@/components/RepoUrlListInput";
import { ProjectReviewLoopOption } from "@/components/ProjectReviewLoopOption";
import { MCPServerPicker } from "@/components/MCPServerPicker";
import { UserSecretPicker, type UserSecretOption } from "@/components/UserSecretPicker";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { REASONING_LEVELS } from "@/lib/reasoning";
import { mcpPolicyBlocksServers } from "@/lib/resourceNames";
import { toneText } from "@/lib/status";
import { CreateProjectRequestSchema } from "@/rpc/platform/service_pb";

interface CredentialPresence {
  namespace: string;
  anthropicApiKey: boolean;
  openaiApiKey: boolean;
  openrouterApiKey: boolean;
  xaiApiKey: boolean;
  anthropicOauth: boolean;
  openaiOauth: boolean;
  copilotOauth: boolean;
  githubToken: boolean;
}

const emptyPresence: CredentialPresence = {
  namespace: "",
  anthropicApiKey: false,
  openaiApiKey: false,
  openrouterApiKey: false,
  xaiApiKey: false,
  anthropicOauth: false,
  openaiOauth: false,
  copilotOauth: false,
  githubToken: false,
};

type FormState = {
  name: string;
  displayName: string;
  repoUrl: string;
  additionalRepoUrls: string[];
  reviewLoopDisabled: boolean;
  provider: string;
  model: string;
  reasoningLevel: string;
  baseBranch: string;
  timeout: string;
  customInstructions: string;
  allowedModels: string;
  image: string;
  authMode: "api-key" | "oauth";
  openaiOauthSecret: string;
  githubToken: string;
  anthropicApiKey: string;
  openaiApiKey: string;
  useSavedCredentials: boolean;
  configureRuntimeProfile: boolean;
  runtimeProfileRef: string;
  permissionMode: string;
  egressMode: string;
  configureMcpPolicy: boolean;
  mcpPolicyRef: string;
  mcpPolicyDefaultAction: string;
  mcpPolicyAllowedServers: string;
  mcpServerRefs: string[];
};

const initialForm: FormState = {
  name: "",
  displayName: "",
  repoUrl: "",
  additionalRepoUrls: [],
  reviewLoopDisabled: true,
  provider: "anthropic",
  model: "",
  reasoningLevel: "",
  baseBranch: "",
  timeout: "",
  customInstructions: "",
  allowedModels: "",
  image: "",
  authMode: "api-key",
  openaiOauthSecret: "",
  githubToken: "",
  anthropicApiKey: "",
  openaiApiKey: "",
  useSavedCredentials: true,
  configureRuntimeProfile: true,
  runtimeProfileRef: "",
  permissionMode: "workspace-write",
  egressMode: "unrestricted",
  configureMcpPolicy: false,
  mcpPolicyRef: "",
  mcpPolicyDefaultAction: "Deny",
  mcpPolicyAllowedServers: "",
  mcpServerRefs: [],
};

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

function splitCommaList(value: string): string[] {
  return value
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean);
}

// savedCredentialAvailable reports whether the user has any saved credential
// usable for the selected provider, regardless of auth mode: the server
// prefers OAuth when present and falls back gracefully to whatever exists.
function savedCredentialAvailable(p: CredentialPresence, provider: string): boolean {
  if (provider === "copilot") return p.copilotOauth;
  if (provider === "anthropic") return p.anthropicApiKey || p.anthropicOauth;
  if (provider === "openai") return p.openaiApiKey || p.openaiOauth;
  if (provider === "openrouter") return p.openrouterApiKey;
  if (provider === "xai") return p.xaiApiKey;
  if (provider === "gemini" || provider === "groq") return p.openaiApiKey;
  return false;
}

export function CreateProjectDialog() {
  const navigate = useNavigate();
  const { createProject, submitting, error, clearError } = useCreateProject();
  const [open, setOpen] = useState(false);
  const [form, setForm] = useState<FormState>(initialForm);
  const [formError, setFormError] = useState<string | null>(null);
  const [credentials, setCredentials] = useState<CredentialPresence>(emptyPresence);
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);
  const [userSecrets, setUserSecrets] = useState<UserSecretOption[]>([]);

  const meta = providerMeta(form.provider);
  const isCopilot = form.provider === "copilot";
  const oauthSupported = form.provider === "anthropic" || form.provider === "openai";
  const effectiveAuthMode = meta.oauthOnly ? "oauth" : oauthSupported ? form.authMode : "api-key";
  const useSaved = form.useSavedCredentials && Boolean(meta.savedSupported);
  const savedReady = savedCredentialAvailable(credentials, form.provider);
  const modelOptions = useSaved && savedReady ? availableModels : [];
  const modelLoadingVisible = useSaved && savedReady && modelsLoading;
  const modelErrorVisible = useSaved && savedReady ? modelsError : null;

  const allowedModels = useMemo(
    () => form.allowedModels.split(",").map((value) => value.trim()).filter(Boolean),
    [form.allowedModels]
  );
  const mcpPolicyAllowedServers = useMemo(
    () => splitCommaList(form.mcpPolicyAllowedServers),
    [form.mcpPolicyAllowedServers],
  );

  // Load the user's saved credential presence + personal namespace when the
  // dialog opens, so we can offer "use a saved credential" and show where the
  // project will be created.
  useEffect(() => {
    if (!open) return;
    let active = true;
    void client
      .listMyCredentials({})
      .then((c) => {
        if (!active) return;
        setUserSecrets(c.secrets ?? []);
        setCredentials({
          namespace: c.namespace,
          anthropicApiKey: c.anthropicApiKeyPresent,
          openaiApiKey: c.openaiApiKeyPresent,
          openrouterApiKey: c.openrouterApiKeyPresent,
          xaiApiKey: c.xaiApiKeyPresent,
          anthropicOauth: c.anthropicOauthPresent,
          openaiOauth: c.openaiOauthPresent,
          copilotOauth: c.copilotOauthPresent,
          githubToken: c.githubTokenPresent,
        });
      })
      .catch(() => {
        if (active) setCredentials(emptyPresence);
      });
    return () => {
      active = false;
    };
  }, [open]);

  // Load the provider's models through the user's saved credentials (API key
  // or OAuth) so the model fields can offer live suggestions instead of a
  // hardcoded placeholder.
  useEffect(() => {
    if (!open || !useSaved || !savedReady) return;

    const controller = new AbortController();
    const provider = form.provider;

    async function loadProviderModels() {
      setAvailableModels([]);
      setModelsLoading(true);
      setModelsError(null);
      try {
        const resp = await client.listAvailableModels(
          {
            namespace: credentials.namespace,
            provider,
            authMode: effectiveAuthMode,
          },
          { signal: controller.signal },
        );
        if (controller.signal.aborted) return;
        const models = resp.models;
        setAvailableModels(models);
        if (models.length === 0) return;
        setForm((prev) => {
          // Copilot requires an explicit model; preselect the first suggestion.
          if (provider !== "copilot" || prev.provider !== "copilot" || prev.model.trim()) return prev;
          return { ...prev, model: models[0] };
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setAvailableModels([]);
        setModelsError(err instanceof Error ? err.message : `Failed to load ${provider} models`);
      } finally {
        if (!controller.signal.aborted) {
          setModelsLoading(false);
        }
      }
    }

    void loadProviderModels();

    return () => controller.abort();
  }, [open, useSaved, savedReady, form.provider, effectiveAuthMode, credentials.namespace]);

  // When only one credential kind is saved for the provider, align the auth
  // mode to it so the project doesn't target a credential that doesn't exist.
  // Only flips when the current mode's credential is absent and the other is
  // present, so it never loops.
  useEffect(() => {
    if (!open || !useSaved || meta.oauthOnly) return;
    let apiKeyPresent: boolean;
    let oauthPresent: boolean;
    if (form.provider === "anthropic") {
      apiKeyPresent = credentials.anthropicApiKey;
      oauthPresent = credentials.anthropicOauth;
    } else if (form.provider === "openai") {
      apiKeyPresent = credentials.openaiApiKey;
      oauthPresent = credentials.openaiOauth;
    } else {
      return;
    }
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setForm((prev) => {
      if (prev.authMode === "oauth" && !oauthPresent && apiKeyPresent) {
        return { ...prev, authMode: "api-key" };
      }
      if (prev.authMode !== "oauth" && !apiKeyPresent && oauthPresent) {
        return { ...prev, authMode: "oauth" };
      }
      return prev;
    });
  }, [open, useSaved, meta.oauthOnly, form.provider, credentials]);

  function update<K extends keyof FormState>(field: K, value: FormState[K]) {
    setForm((prev) => ({ ...prev, [field]: value }));
  }

  async function refreshUserSecrets() {
    try {
      const credentials = await client.listMyCredentials({});
      setUserSecrets(credentials.secrets ?? []);
    } catch {
      // Keep the last successful inventory while the picker remains usable.
    }
  }

  const reset = useCallback(() => {
    setForm(initialForm);
    setFormError(null);
    setAvailableModels([]);
    setModelsLoading(false);
    setModelsError(null);
    clearError();
  }, [clearError]);

  // validate mirrors the server's credential requirements so users get instant
  // feedback instead of a round-trip RPC error.
  function validate(): string | null {
    if (!form.name.trim()) return "Give the project a name.";
    if (useSaved) {
      if (!savedReady) {
        return `No saved ${meta.name} credential. Add it in Settings, or turn off "Use my saved credentials".`;
      }
    } else {
      if (effectiveAuthMode === "oauth" && !form.openaiOauthSecret.trim()) {
        return "OAuth auth mode requires an existing OAuth secret name.";
      }
      if (effectiveAuthMode === "api-key") {
        if (form.provider === "anthropic" && !form.anthropicApiKey.trim()) {
          return "Anthropic with API-key auth requires an Anthropic API key.";
        }
        if (form.provider === "openai" && !form.openaiApiKey.trim()) {
          return "OpenAI with API-key auth requires an OpenAI API key.";
        }
      }
    }
    if (isCopilot && !form.model.trim()) return "Choose a GitHub Copilot model.";
    return null;
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFormError(null);
    const validationError = validate();
    if (validationError) {
      setFormError(validationError);
      return;
    }
    try {
      const project = await createProject(create(CreateProjectRequestSchema, {
        name: form.name.trim(),
        // The server requires a display name; fall back to the name so the
        // field can stay optional in the UI.
        displayName: form.displayName.trim() || form.name.trim(),
        repoUrl: form.repoUrl.trim(),
        additionalRepoUrls: form.additionalRepoUrls.map((url) => url.trim()).filter(Boolean),
        reviewLoopDisabled: form.reviewLoopDisabled,
        provider: form.provider,
        model: form.model.trim(),
        reasoningLevel: form.reasoningLevel,
        baseBranch: form.baseBranch.trim(),
        timeout: form.timeout.trim(),
        customInstructions: form.customInstructions.trim(),
        allowedModels,
        authMode: effectiveAuthMode,
        useSavedCredentials: useSaved,
        openaiOauthSecret: useSaved ? "" : form.openaiOauthSecret.trim(),
        githubToken: useSaved ? "" : form.githubToken.trim(),
        anthropicApiKey: useSaved ? "" : form.anthropicApiKey.trim(),
        openaiApiKey: useSaved ? "" : form.openaiApiKey.trim(),
        configureRuntimeProfile: form.configureRuntimeProfile,
        runtimeProfileRef: form.runtimeProfileRef.trim(),
        permissionMode: form.permissionMode,
        egressMode: form.egressMode,
        configureMcpPolicy: form.configureMcpPolicy,
        mcpPolicyRef: form.mcpPolicyRef.trim(),
        mcpPolicyDefaultAction: form.mcpPolicyDefaultAction,
        mcpPolicyAllowedServers,
        mcpServerRefs: form.mcpServerRefs,
        image: form.image.trim(),
      }));
      setOpen(false);
      reset();
      navigate(`/projects/${project.namespace}/${project.name}`);
    } catch {
      // Error surfaced via the hook's `error` state; keep the dialog open.
    }
  }

  /* Collapsed-row summaries — the receipt of what will be created. */

  const modelSummaryText = [
    meta.name,
    ...(form.model.trim() ? [form.model.trim()] : []),
    useSaved ? "saved credentials" : "inline credentials",
  ].join(" · ");

  const repoExtrasCount = form.additionalRepoUrls.filter((url) => url.trim()).length;
  const repoSummaryText = [
    form.baseBranch.trim() ? `base ${form.baseBranch.trim()}` : "default branch",
    ...(repoExtrasCount ? [`+${repoExtrasCount} extra repo${repoExtrasCount === 1 ? "" : "s"}`] : []),
  ].join(" · ");

  const runtimeSummaryText = form.configureRuntimeProfile
    ? `${form.permissionMode} · ${form.egressMode}`
    : form.runtimeProfileRef.trim() || "Project defaults";

  const toolsCount = form.mcpServerRefs.length;
  const toolsSummaryText = toolsCount
    ? `${toolsCount} MCP server${toolsCount === 1 ? "" : "s"}`
    : "None";

  const policySummaryText = form.configureMcpPolicy
    ? `${form.mcpPolicyDefaultAction} by default`
    : "Off";

  const advancedCount = [
    Boolean(form.reasoningLevel),
    Boolean(form.allowedModels.trim()),
    Boolean(form.customInstructions.trim()),
  ].filter(Boolean).length;

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (!nextOpen) reset();
      }}
    >
      <DialogTrigger render={<Button size="sm" />}>
        <Plus />
        Create Project
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
              <DialogTitle className="text-base">Create project</DialogTitle>
            </div>
            <DialogDescription>
              A project bundles repository defaults and provider credentials for your agent runs.
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            {/* Essentials */}
            <div className="grid gap-4 sm:grid-cols-2">
              <FlowField id="project-name" label="Name" required>
                <Input
                  id="project-name"
                  value={form.name}
                  onChange={(event) => update("name", event.target.value)}
                  placeholder="payments-api"
                  autoFocus
                  required
                />
              </FlowField>
              <FlowField
                id="project-display-name"
                label="Display name"
                hint="Optional — defaults to the name."
              >
                <Input
                  id="project-display-name"
                  value={form.displayName}
                  onChange={(event) => update("displayName", event.target.value)}
                  placeholder="Payments API"
                />
              </FlowField>
            </div>
            <FlowField
              id="project-repo-url"
              label="Repository URL"
              hint="Optional — leave empty to start without a repo."
            >
              <div className="relative">
                <FolderGit2 className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
                <Input
                  id="project-repo-url"
                  value={form.repoUrl}
                  onChange={(event) => update("repoUrl", event.target.value)}
                  placeholder="https://github.com/org/repo"
                  className="pl-9"
                />
              </div>
            </FlowField>
            <div className="grid gap-4 sm:grid-cols-2">
              <FlowField id="project-image" label="Runtime image" hint="Pick your language.">
                <RuntimeImagePicker
                  id="project-image"
                  value={form.image}
                  onChange={(image) => update("image", image)}
                />
              </FlowField>
              <FlowField id="project-timeout" label="Timeout" hint="Maximum run duration, e.g. 30m.">
                <Input
                  id="project-timeout"
                  value={form.timeout}
                  onChange={(event) => update("timeout", event.target.value)}
                  placeholder="30m"
                />
              </FlowField>
            </div>

            <OptionRows label="Options" className="pt-1">
              {/* Model & credentials */}
              <OptionRow
                icon={Sparkles}
                title="Model"
                summary={modelSummaryText}
                modified={form.provider !== "anthropic" || Boolean(form.model.trim()) || !useSaved}
                defaultOpen
              >
                <div className="flex flex-wrap gap-1.5">
                  {PROVIDERS.map((p) => (
                    <Chip
                      key={p.id}
                      selected={form.provider === p.id}
                      onSelect={() => update("provider", p.id)}
                    >
                      {p.name}
                    </Chip>
                  ))}
                </div>

                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField
                    id="project-model"
                    label={isCopilot ? "Copilot model" : "Default model"}
                    required={isCopilot}
                  >
                    <Input
                      id="project-model"
                      value={form.model}
                      onChange={(event) => update("model", event.target.value)}
                      placeholder={
                        modelOptions.length
                          ? "Choose a model"
                          : isCopilot
                            ? "gpt-4.1"
                            : "Provider default"
                      }
                      list={modelOptions.length ? "project-model-options" : undefined}
                    />
                    {modelOptions.length > 0 ? (
                      <datalist id="project-model-options">
                        {modelOptions.map((model) => (
                          <option key={model} value={model} />
                        ))}
                      </datalist>
                    ) : null}
                    {(modelLoadingVisible || modelErrorVisible || modelOptions.length > 0 || isCopilot) && (
                      <p className="text-[11px] text-muted-foreground" aria-live="polite">
                        {modelLoadingVisible
                          ? `Loading ${meta.name} models...`
                          : modelErrorVisible
                            ? `Could not load ${meta.name} models: ${modelErrorVisible}`
                            : modelOptions.length
                              ? `${modelOptions.length} ${meta.name} models available`
                              : savedReady
                                ? "Enter a Copilot model name."
                                : "Connect Copilot in Settings to load models."}
                      </p>
                    )}
                  </FlowField>
                  <FlowField id="project-reasoning-level" label="Reasoning level">
                    <select
                      id="project-reasoning-level"
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

                {oauthSupported ? (
                  <FlowSwitchRow
                    label="Authentication"
                    control={
                      <Segmented
                        aria-label="Authentication"
                        value={form.authMode}
                        onChange={(v) => update("authMode", v)}
                        options={[
                          { value: "api-key", label: "API key" },
                          { value: "oauth", label: "OAuth" },
                        ]}
                      />
                    }
                  />
                ) : meta.oauthOnly ? (
                  <p className="text-[11px] leading-relaxed text-muted-foreground">
                    {meta.name} uses OAuth — sign in once under Settings and reuse it here.
                  </p>
                ) : (
                  <p className="text-[11px] leading-relaxed text-muted-foreground">
                    {meta.name} uses API-key authentication.
                  </p>
                )}

                {meta.savedSupported ? (
                  <FlowSwitchRow
                    id="project-use-saved"
                    label="Use my saved credentials"
                    hint={
                      useSaved ? (
                        savedReady ? (
                          <span className="inline-flex items-center gap-1.5">
                            <ShieldCheck className="size-3.5 shrink-0 text-[color:var(--tone-success-fg)]" />
                            Using your saved {meta.name} credential
                            {credentials.githubToken ? " + GitHub token" : ""}, stored privately in
                            your namespace.
                          </span>
                        ) : (
                          <span>
                            No saved {meta.name} credential yet —{" "}
                            <Link to="/settings/credentials" className="underline underline-offset-4">
                              add it in Settings
                            </Link>{" "}
                            or turn this off to enter one inline.
                          </span>
                        )
                      ) : undefined
                    }
                    control={
                      <Switch
                        id="project-use-saved"
                        checked={form.useSavedCredentials}
                        onCheckedChange={(checked) => update("useSavedCredentials", checked)}
                      />
                    }
                  />
                ) : null}

                {!useSaved ? (
                  <div className="grid gap-4 sm:grid-cols-2">
                    <FlowField
                      id="project-github-token"
                      label="GitHub token"
                      hint="Optional, for cloning."
                    >
                      <SecretInput
                        id="project-github-token"
                        value={form.githubToken}
                        onChange={(event) => update("githubToken", event.target.value)}
                        placeholder="ghp_… / github_pat_…"
                      />
                    </FlowField>
                    {effectiveAuthMode === "oauth" ? (
                      <FlowField
                        id="project-openai-oauth-secret"
                        label="OAuth secret"
                        hint="Name of an existing Secret containing auth.json."
                      >
                        <UserSecretPicker
                          id="project-openai-oauth-secret"
                          value={form.openaiOauthSecret}
                          secrets={userSecrets}
                          onOpen={() => void refreshUserSecrets()}
                          onChange={(secretName) => update("openaiOauthSecret", secretName)}
                        />
                      </FlowField>
                    ) : form.provider === "anthropic" ? (
                      <FlowField id="project-anthropic-api-key" label="Anthropic API key">
                        <SecretInput
                          id="project-anthropic-api-key"
                          value={form.anthropicApiKey}
                          onChange={(event) => update("anthropicApiKey", event.target.value)}
                          placeholder="sk-ant-…"
                        />
                      </FlowField>
                    ) : (
                      <FlowField
                        id="project-openai-api-key"
                        label={form.provider === "openai" ? "OpenAI API key" : "API key (OpenAI-compatible)"}
                      >
                        <SecretInput
                          id="project-openai-api-key"
                          value={form.openaiApiKey}
                          onChange={(event) => update("openaiApiKey", event.target.value)}
                          placeholder="sk-…"
                        />
                      </FlowField>
                    )}
                  </div>
                ) : null}
              </OptionRow>

              {/* Repository details */}
              <OptionRow
                icon={FolderGit2}
                title="Repository details"
                summary={repoSummaryText}
                modified={Boolean(form.baseBranch.trim()) || repoExtrasCount > 0}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="project-base-branch" label="Base branch">
                    <Input
                      id="project-base-branch"
                      value={form.baseBranch}
                      onChange={(event) => update("baseBranch", event.target.value)}
                      placeholder="main"
                    />
                  </FlowField>
                </div>
                <FlowField
                  id="project-additional-repo-0"
                  label="Additional repositories"
                  hint="Extra repos cloned into every run alongside the primary one."
                >
                  <RepoUrlListInput
                    idPrefix="project-additional-repo"
                    value={form.additionalRepoUrls}
                    onChange={(urls) => update("additionalRepoUrls", urls)}
                  />
                </FlowField>
              </OptionRow>

              <ProjectReviewLoopOption
                id="project-review-loop-disabled"
                disabled={form.reviewLoopDisabled}
                modified={form.reviewLoopDisabled}
                onDisabledChange={(checked) => update("reviewLoopDisabled", checked)}
              />

              {/* Runtime policy */}
              <OptionRow
                icon={Cpu}
                title="Runtime policy"
                summary={runtimeSummaryText}
                modified={
                  Boolean(form.runtimeProfileRef.trim()) ||
                  form.permissionMode !== "workspace-write" ||
                  form.egressMode !== "unrestricted"
                }
              >
                <FlowSwitchRow
                  id="project-configure-runtime"
                  label="Create/update a RuntimeProfile"
                  hint="Controls sandbox permissions and network egress for this project's runs."
                  control={
                    <Switch
                      id="project-configure-runtime"
                      checked={form.configureRuntimeProfile}
                      onCheckedChange={(checked) => update("configureRuntimeProfile", checked)}
                    />
                  }
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="project-runtime-profile-ref" label="RuntimeProfile ref">
                    <Input
                      id="project-runtime-profile-ref"
                      value={form.runtimeProfileRef}
                      onChange={(event) => update("runtimeProfileRef", event.target.value)}
                      placeholder={form.name.trim() ? `${form.name.trim()}-runtime` : "project-runtime"}
                    />
                  </FlowField>
                  {form.configureRuntimeProfile ? (
                    <>
                      <FlowField id="project-permission-mode" label="Permission mode">
                        <select
                          id="project-permission-mode"
                          value={form.permissionMode}
                          onChange={(event) => update("permissionMode", event.target.value)}
                          className={selectClassName}
                        >
                          <option value="read-only">read-only</option>
                          <option value="workspace-write">workspace-write</option>
                          <option value="danger-full-access">danger-full-access</option>
                        </select>
                      </FlowField>
                      <FlowField id="project-egress-mode" label="Network egress">
                        <select
                          id="project-egress-mode"
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

              {/* Tools */}
              <OptionRow
                icon={Blocks}
                title="Tools"
                summary={toolsSummaryText}
                modified={toolsCount > 0}
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

              {/* MCP policy */}
              <OptionRow
                icon={KeyRound}
                title="MCP policy"
                summary={policySummaryText}
                modified={form.configureMcpPolicy || Boolean(form.mcpPolicyRef.trim())}
              >
                <FlowSwitchRow
                  id="project-configure-mcp-policy"
                  label="Create/update an MCPPolicy"
                  hint="Restricts which MCP servers this project's runs may reach."
                  control={
                    <Switch
                      id="project-configure-mcp-policy"
                      checked={form.configureMcpPolicy}
                      onCheckedChange={(checked) => update("configureMcpPolicy", checked)}
                    />
                  }
                />
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="project-mcp-policy-ref" label="MCPPolicy ref">
                    <Input
                      id="project-mcp-policy-ref"
                      value={form.mcpPolicyRef}
                      onChange={(event) => update("mcpPolicyRef", event.target.value)}
                      placeholder={form.name.trim() ? `${form.name.trim()}-mcp-policy` : "project-mcp-policy"}
                    />
                  </FlowField>
                  {form.configureMcpPolicy ? (
                    <>
                      <FlowField id="project-mcp-policy-default-action" label="Default action">
                        <select
                          id="project-mcp-policy-default-action"
                          value={form.mcpPolicyDefaultAction}
                          onChange={(event) => update("mcpPolicyDefaultAction", event.target.value)}
                          className={selectClassName}
                        >
                          <option value="Deny">Deny</option>
                          <option value="Allow">Allow</option>
                        </select>
                      </FlowField>
                      <FlowField
                        id="project-mcp-policy-allowed"
                        label="Allowed MCP servers"
                        hint="Comma-separated."
                      >
                        <Input
                          id="project-mcp-policy-allowed"
                          value={form.mcpPolicyAllowedServers}
                          onChange={(event) => update("mcpPolicyAllowedServers", event.target.value)}
                          placeholder="fetch, github"
                        />
                      </FlowField>
                    </>
                  ) : null}
                </div>
              </OptionRow>

              {/* Advanced */}
              <OptionRow
                icon={Settings2}
                title="Advanced"
                summary={advancedCount ? `${advancedCount} customized` : "Defaults"}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="project-allowed-models" label="Allowed models" hint="Comma-separated.">
                    <Input
                      id="project-allowed-models"
                      value={form.allowedModels}
                      onChange={(event) => update("allowedModels", event.target.value)}
                      placeholder="model-a, model-b"
                    />
                  </FlowField>
                </div>
                <FlowField id="project-custom-instructions" label="Custom instructions">
                  <Textarea
                    id="project-custom-instructions"
                    value={form.customInstructions}
                    onChange={(event) => update("customInstructions", event.target.value)}
                    className="min-h-24"
                    placeholder="Project-specific guidance for the agent…"
                  />
                </FlowField>
              </OptionRow>
            </OptionRows>

            {(formError || error) && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {formError ?? error}
              </p>
            )}
          </div>

          <div className="flex items-center justify-between gap-3 border-t px-6 py-4">
            <p className="min-w-0 truncate text-xs text-muted-foreground">
              Creates{" "}
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground">
                {credentials.namespace || "personal namespace"}/{form.name.trim() || "…"}
              </code>
            </p>
            <div className="flex shrink-0 items-center gap-2">
              <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
                Cancel
              </DialogClose>
              <Button type="submit" size="sm" disabled={submitting}>
                {submitting ? <Loader2 className="size-4 animate-spin" /> : <Plus className="size-4" />}
                {submitting ? "Creating…" : "Create project"}
              </Button>
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function SecretInput(props: React.ComponentProps<typeof Input>) {
  const [revealed, setRevealed] = useState(false);

  return (
    <div className="relative">
      <Input
        {...props}
        type={revealed ? "text" : "password"}
        autoComplete="new-password"
        className={cn("pr-9", props.className)}
      />
      <button
        type="button"
        className="absolute right-2 top-1/2 -translate-y-1/2 rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        onClick={() => setRevealed((value) => !value)}
        aria-label={revealed ? "Hide secret" : "Reveal secret"}
      >
        {revealed ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
      </button>
    </div>
  );
}
