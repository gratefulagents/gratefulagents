import { useEffect, useState } from "react";
import { SlidersHorizontal } from "lucide-react";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import { PROVIDERS, providerName } from "@/components/create-flow/providers";
import { SettingsSection } from "@/components/settings-section";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { toast } from "@/components/ui/toaster";
import { client } from "@/lib/client";
import { REASONING_LEVELS } from "@/lib/reasoning";
import type { ModelDefaults, MyCredentials } from "@/rpc/platform/service_pb";

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

type AuthMode = "api-key" | "oauth";

function authModesFor(credentials: MyCredentials, provider: string): AuthMode[] {
  switch (provider) {
    case "anthropic":
      return [
        ...(credentials.anthropicApiKeyPresent ? ["api-key" as const] : []),
        ...(credentials.anthropicOauthPresent ? ["oauth" as const] : []),
      ];
    case "openai":
      return [
        ...(credentials.openaiApiKeyPresent ? ["api-key" as const] : []),
        ...(credentials.openaiOauthPresent ? ["oauth" as const] : []),
      ];
    case "copilot":
      return credentials.copilotOauthPresent ? ["oauth"] : [];
    case "openrouter":
      return credentials.openrouterApiKeyPresent ? ["api-key"] : [];
    case "xai":
      return credentials.xaiApiKeyPresent ? ["api-key"] : [];
    default:
      return [];
  }
}

function reconciledAuthMode(
  credentials: MyCredentials,
  provider: string,
  preferred: string,
): AuthMode | "" {
  const modes = authModesFor(credentials, provider);
  return modes.includes(preferred as AuthMode) ? (preferred as AuthMode) : modes[0] ?? "";
}

export function ModelDefaultsSection() {
  const [credentials, setCredentials] = useState<MyCredentials | null>(null);
  const [provider, setProvider] = useState("");
  const [authMode, setAuthMode] = useState<AuthMode | "">("");
  const [model, setModel] = useState("");
  const [reasoningLevel, setReasoningLevel] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [updatedAt, setUpdatedAt] = useState<Timestamp | undefined>();
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);

  function applyServer(defaults: ModelDefaults, savedCredentials: MyCredentials | null) {
    const availableProviders = savedCredentials
      ? PROVIDERS.filter((candidate) => authModesFor(savedCredentials, candidate.id).length > 0)
      : [];
    const nextProvider = availableProviders.some((candidate) => candidate.id === defaults.provider)
      ? defaults.provider
      : availableProviders[0]?.id ?? "";
    setProvider(nextProvider);
    setAuthMode(
      savedCredentials ? reconciledAuthMode(savedCredentials, nextProvider, defaults.authMode) : "",
    );
    setModel(nextProvider === defaults.provider ? (defaults.model ?? "") : "");
    setReasoningLevel(defaults.reasoningLevel ?? "");
    setEnabled(!defaults.disabled);
    setUpdatedAt(defaults.updatedAt);
  }

  useEffect(() => {
    let active = true;
    void Promise.allSettled([client.getMyModelDefaults({}), client.listMyCredentials({})]).then(
      ([defaultsResult, credentialsResult]) => {
        if (!active) return;
        const savedCredentials =
          credentialsResult.status === "fulfilled" ? credentialsResult.value : null;
        setCredentials(savedCredentials);
        if (defaultsResult.status === "fulfilled") {
          applyServer(defaultsResult.value, savedCredentials);
        } else if (savedCredentials) {
          applyServer({} as ModelDefaults, savedCredentials);
        }
        const failure =
          defaultsResult.status === "rejected"
            ? defaultsResult.reason
            : credentialsResult.status === "rejected"
              ? credentialsResult.reason
              : null;
        if (failure) {
          setError(failure instanceof Error ? failure.message : "Failed to load model defaults");
        }
        setLoading(false);
      },
    );
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    if (
      !credentials ||
      !provider ||
      !authMode ||
      !authModesFor(credentials, provider).includes(authMode)
    ) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- clear stale suggestions when no saved credential can serve this selection
      setAvailableModels([]);
      setModelsLoading(false);
      setModelsError(null);
      return () => controller.abort();
    }

    setAvailableModels([]);
    setModelsLoading(true);
    setModelsError(null);
    void client
      .listAvailableModels(
        { namespace: credentials.namespace, provider, authMode },
        { signal: controller.signal },
      )
      .then(
        (response) => {
          if (!controller.signal.aborted) setAvailableModels(response.models);
        },
        (cause: unknown) => {
          if (!controller.signal.aborted) {
            setModelsError(cause instanceof Error ? cause.message : "Failed to load provider models");
          }
        },
      )
      .finally(() => {
        if (!controller.signal.aborted) setModelsLoading(false);
      });
    return () => controller.abort();
  }, [credentials, provider, authMode]);

  const availableProviders = credentials
    ? PROVIDERS.filter((candidate) => authModesFor(credentials, candidate.id).length > 0)
    : [];
  const availableAuthModes = credentials ? authModesFor(credentials, provider) : [];

  function selectProvider(nextProvider: string) {
    if (nextProvider !== provider) setModel("");
    setProvider(nextProvider);
    if (credentials) setAuthMode(reconciledAuthMode(credentials, nextProvider, authMode));
  }

  async function save(request: {
    provider: string;
    authMode: string;
    model: string;
    reasoningLevel: string;
    disabled: boolean;
  }) {
    setSaving(true);
    setError(null);
    try {
      applyServer(await client.updateMyModelDefaults(request), credentials);
      toast.success("Default model saved");
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to save model defaults";
      setError(message);
      toast.error(message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <SettingsSection
      icon={<SlidersHorizontal />}
      title="Default model"
      description="Your personal default provider, authentication mode, model, and reasoning level. New projects, triggers, and scan configs start from these values; every form stays editable, so you can skip or override them anywhere. Runs are not affected: they keep following their project's settings."
    >
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <div className="space-y-1.5">
          <Label htmlFor="model-defaults-provider">Provider</Label>
          <select
            id="model-defaults-provider"
            className={selectClassName}
            value={provider}
            onChange={(event) => selectProvider(event.target.value)}
            disabled={loading || availableProviders.length === 0}
          >
            {availableProviders.length === 0 && <option value="">No saved credentials</option>}
            {availableProviders.map((candidate) => (
              <option key={candidate.id} value={candidate.id}>
                {candidate.name}
              </option>
            ))}
          </select>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="model-defaults-auth-mode">Authentication mode</Label>
          <select
            id="model-defaults-auth-mode"
            className={selectClassName}
            value={authMode}
            onChange={(event) => setAuthMode(event.target.value as AuthMode)}
            disabled={loading || availableAuthModes.length <= 1}
          >
            {availableAuthModes.length === 0 && <option value="">Unavailable</option>}
            {availableAuthModes.map((mode) => (
              <option key={mode} value={mode}>
                {mode === "api-key" ? "API key" : "OAuth"}
              </option>
            ))}
          </select>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="model-defaults-model">Model</Label>
          <Input
            id="model-defaults-model"
            value={model}
            onChange={(event) => setModel(event.target.value)}
            placeholder="provider default"
            disabled={loading || !provider}
            list="model-defaults-model-options"
            aria-describedby="model-defaults-model-help"
          />
          <datalist id="model-defaults-model-options">
            {availableModels.map((availableModel) => (
              <option key={availableModel} value={availableModel} />
            ))}
          </datalist>
          <p
            id="model-defaults-model-help"
            className={
              modelsError
                ? "text-[11px] text-destructive"
                : "text-[11px] text-muted-foreground"
            }
            role={modelsError ? "alert" : "status"}
            aria-live="polite"
          >
            {modelsLoading
              ? `Loading ${providerName(provider)} models…`
              : modelsError
                ? modelsError
                : provider
                  ? `${availableModels.length} ${providerName(provider)} models available`
                  : "Save a provider credential to load models."}
          </p>
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="model-defaults-reasoning">Reasoning level</Label>
          <select
            id="model-defaults-reasoning"
            className={selectClassName}
            value={reasoningLevel}
            onChange={(event) => setReasoningLevel(event.target.value)}
            disabled={loading}
          >
            {REASONING_LEVELS.map((level) => (
              <option key={level || "default"} value={level}>
                {level || "default"}
              </option>
            ))}
          </select>
        </div>
      </div>

      <Label className="flex w-fit cursor-pointer items-center gap-2 text-[12px] font-normal">
        <Switch checked={enabled} onCheckedChange={setEnabled} disabled={loading} />
        Apply to new projects, triggers, and scan configs
      </Label>

      <p className="text-[11px] text-muted-foreground" aria-live="polite">
        {loading ? "Loading…" : savedLabel(updatedAt)}
      </p>

      <div className="flex items-center gap-3">
        <Button
          size="sm"
          disabled={saving || loading || !provider || !authMode}
          onClick={() =>
            void save({
              provider,
              authMode,
              model: model.trim(),
              reasoningLevel,
              disabled: !enabled,
            })
          }
        >
          {saving ? "Saving…" : "Save default model"}
        </Button>
        <Button
          size="sm"
          variant="outline"
          disabled={saving || loading}
          onClick={() =>
            void save({
              provider: "",
              authMode: "",
              model: "",
              reasoningLevel: "",
              disabled: false,
            })
          }
        >
          Clear
        </Button>
        {error && (
          <span className="text-[12px] text-destructive" role="alert">
            {error}
          </span>
        )}
      </div>
    </SettingsSection>
  );
}

function savedLabel(updatedAt: Timestamp | undefined) {
  if (!updatedAt) return "No default model saved; new projects, triggers, and scan configs start from the platform default.";
  const millis = Number(updatedAt.seconds) * 1000;
  if (!Number.isFinite(millis) || millis <= 0) return "Saved.";
  return `Last saved ${new Date(millis).toLocaleString()}`;
}
