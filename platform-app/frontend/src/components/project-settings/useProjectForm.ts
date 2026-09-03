import { useCallback, useEffect, useMemo, useState } from "react";

import { providerMeta, providerName } from "@/components/create-flow/providers";
import type { UserSecretOption } from "@/components/UserSecretPicker";
import { useMyModelDefaults } from "@/hooks/useMyModelDefaults";
import { client } from "@/lib/client";
import { applyModelDefaults } from "@/lib/modelDefaults";
import {
  authModeForProviderSwitch,
  emptyPresence as emptyLibPresence,
  oauthSecretForProviderSwitch,
  providerKeyFor,
  savedCredentialAvailable as savedCredentialAvailableStrict,
  savedCredentialProviders,
  type CredentialPresence as LibCredentialPresence,
} from "@/lib/projectCredentialForm";
import type { Project } from "@/rpc/platform/service_pb";

import {
  deriveProjectName,
  effectiveAuthModeFor,
  emptyProjectForm,
  projectFormFromProject,
  projectFormKey,
  savedSupportedFor,
  usesSavedCredentials,
  validateProjectForm,
  type ProjectFormMode,
  type ProjectFormState,
} from "./projectForm";

export type CredentialPresence = LibCredentialPresence & {
  namespace: string;
  githubToken: boolean;
};

const emptyPresence: CredentialPresence = { ...emptyLibPresence, namespace: "", githubToken: false };

// At create time the server prefers OAuth when present and falls back to
// whatever else is saved, so any credential for the provider is enough.
// An existing project already pins an auth mode, so that mode must be saved.
function savedCredentialAvailable(
  presence: CredentialPresence,
  provider: string,
  authMode: string,
  mode: ProjectFormMode,
): boolean {
  if (mode === "edit") return savedCredentialAvailableStrict(presence, provider, authMode);
  if (provider === "copilot") return presence.copilotOauth;
  if (provider === "anthropic") return presence.anthropicApiKey || presence.anthropicOauth;
  if (provider === "openai") return presence.openaiApiKey || presence.openaiOauth;
  if (provider === "openrouter") return presence.openrouterApiKey;
  if (provider === "xai") return presence.xaiApiKey;
  if (provider === "gemini" || provider === "groq") return presence.openaiApiKey;
  return false;
}

export type ProjectFormController = {
  mode: ProjectFormMode;
  project?: Project;
  idPrefix: string;
  form: ProjectFormState;
  initial: ProjectFormState;
  update: <K extends keyof ProjectFormState>(field: K, value: ProjectFormState[K]) => void;
  setForm: React.Dispatch<React.SetStateAction<ProjectFormState>>;
  setProvider: (provider: string) => void;
  reset: () => void;
  /** Adopt the server's persisted project as the new clean baseline. */
  acceptSaved: (saved: Project) => void;
  dirty: boolean;
  /** Derived credential facts the sections render against. */
  effectiveAuthMode: "api-key" | "oauth";
  useSaved: boolean;
  savedSupported: boolean;
  savedReady: boolean;
  oauthSupported: boolean;
  credentials: CredentialPresence;
  credentialsLoaded: boolean;
  userSecrets: UserSecretOption[];
  refreshUserSecrets: () => Promise<void>;
  models: string[];
  modelsLoading: boolean;
  modelsError: string | null;
  validate: () => string | null;
  /** Create only: name was typed by hand so URL changes stop overwriting it. */
  nameTouched: boolean;
};

export function useProjectForm({
  mode,
  project,
  enabled,
  idPrefix,
}: {
  mode: ProjectFormMode;
  project?: Project;
  /** Defer network loads until the host is visible (e.g. dialog open). */
  enabled: boolean;
  idPrefix: string;
}): ProjectFormController {
  const computedInitial = useMemo(
    () => (mode === "edit" && project ? projectFormFromProject(project) : emptyProjectForm()),
    [mode, project],
  );
  // After a save the persisted values become the baseline immediately, so
  // the form reads clean while the host refetches; once the refetched
  // project arrives its own values take over.
  const computedKey = projectFormKey(computedInitial);
  const [savedBaseline, setSavedBaseline] = useState<{ forKey: string; form: ProjectFormState } | null>(null);
  const initial = savedBaseline?.forKey === computedKey ? savedBaseline.form : computedInitial;
  const [form, setForm] = useState<ProjectFormState>(initial);
  const [nameTouched, setNameTouched] = useState(false);
  const [authModeTouched, setAuthModeTouched] = useState(false);
  const [credentials, setCredentials] = useState<CredentialPresence>(emptyPresence);
  const [credentialsLoaded, setCredentialsLoaded] = useState(false);
  const [userSecrets, setUserSecrets] = useState<UserSecretOption[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);

  // The project we are editing may be refetched (e.g. after save); realign
  // the form with the fresh values while it has no unsaved edits.
  const initialKey = projectFormKey(initial);
  const [lastInitialKey, setLastInitialKey] = useState(initialKey);
  if (lastInitialKey !== initialKey) {
    setLastInitialKey(initialKey);
    if (projectFormKey(form) === lastInitialKey) setForm(initial);
  }

  const meta = providerMeta(form.provider);
  const effectiveAuthMode = effectiveAuthModeFor(form);
  const savedSupported = savedSupportedFor(form);
  const useSaved = usesSavedCredentials(form);
  const oauthSupported = form.provider === "anthropic" || form.provider === "openai";
  const savedReady = savedCredentialAvailable(credentials, form.provider, effectiveAuthMode, mode);

  /* Seed a fresh form from the user's saved model defaults (create only). */
  const { defaults: myModelDefaults, loaded: modelDefaultsLoaded } = useMyModelDefaults(
    enabled && mode === "create",
  );
  useEffect(() => {
    if (mode !== "create" || !enabled || !modelDefaultsLoaded) return;
    const seeded = applyModelDefaults(myModelDefaults);
    // eslint-disable-next-line react-hooks/set-state-in-effect -- one-shot prefill of untouched fields
    setForm((prev) =>
      prev.provider === "anthropic" && !prev.model && !prev.reasoningLevel && !authModeTouched
        ? {
            ...prev,
            provider: seeded.provider,
            authMode:
              seeded.provider === "copilot" ? "oauth" : seeded.authMode || prev.authMode,
            model: seeded.model,
            reasoningLevel: seeded.reasoningLevel,
          }
        : prev,
    );
  }, [mode, enabled, modelDefaultsLoaded, myModelDefaults, authModeTouched]);

  /* Saved credential presence + personal namespace. */
  useEffect(() => {
    if (!enabled) return;
    let active = true;
    void client
      .listMyCredentials({})
      .then((c) => {
        if (!active) return;
        // Secret refs only resolve inside the project's own namespace.
        setUserSecrets(!project || c.namespace === project.namespace ? (c.secrets ?? []) : []);
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
        setCredentialsLoaded(true);
      })
      .catch(() => {
        if (!active) return;
        setCredentials(emptyPresence);
        setCredentialsLoaded(true);
      });
    return () => {
      active = false;
    };
  }, [enabled, project]);

  const refreshUserSecrets = useCallback(async () => {
    try {
      const c = await client.listMyCredentials({});
      setUserSecrets(!project || c.namespace === project.namespace ? (c.secrets ?? []) : []);
    } catch {
      // Keep the last successful inventory while the picker remains usable.
    }
  }, [project]);

  /* Live model catalog. Create resolves through the user's saved credential;
     edit resolves through the project's own credential refs. */
  const catalogEnabled = enabled && (mode === "edit" ? Boolean(project) : useSaved && savedReady);
  const provider = form.provider;
  const namespace = mode === "edit" ? project?.namespace ?? "" : credentials.namespace;
  const projectName = project?.name ?? "";
  useEffect(() => {
    if (!catalogEnabled) return;
    const controller = new AbortController();
    async function load() {
      setModels([]);
      setModelsLoading(true);
      setModelsError(null);
      try {
        const resp = await client.listAvailableModels(
          mode === "edit"
            ? { namespace, source: { kind: "Project", name: projectName }, provider }
            : { namespace, provider, authMode: effectiveAuthMode },
          { signal: controller.signal },
        );
        if (controller.signal.aborted) return;
        setModels(resp.models);
        if (resp.models.length === 0) return;
        setForm((prev) => {
          // Copilot requires an explicit model; preselect the first suggestion.
          if (provider !== "copilot" || prev.provider !== "copilot" || prev.model.trim()) return prev;
          return { ...prev, model: resp.models[0] };
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setModels([]);
        setModelsError(
          err instanceof Error ? err.message : `Failed to load ${providerName(provider)} models`,
        );
      } finally {
        if (!controller.signal.aborted) setModelsLoading(false);
      }
    }
    void load();
    return () => controller.abort();
  }, [catalogEnabled, mode, namespace, projectName, provider, effectiveAuthMode]);

  /* Create: when only one credential kind is saved for the provider, align
     the auth mode to it so the project doesn't target a missing credential. */
  useEffect(() => {
    if (mode !== "create" || !enabled || !useSaved || meta.oauthOnly) return;
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
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reconcile auth mode with saved credential presence
    setForm((prev) => {
      if (prev.authMode !== "api-key" && !oauthPresent && apiKeyPresent) {
        return { ...prev, authMode: "api-key" };
      }
      if (prev.authMode !== "oauth" && !apiKeyPresent && oauthPresent) {
        return { ...prev, authMode: "oauth" };
      }
      return prev;
    });
  }, [mode, enabled, useSaved, meta.oauthOnly, form.provider, credentials]);

  const update = useCallback(
    <K extends keyof ProjectFormState>(field: K, value: ProjectFormState[K]) => {
      if (field === "authMode") setAuthModeTouched(true);
      if (field === "name") setNameTouched(true);
      setForm((prev) => {
        const next = { ...prev, [field]: value };
        // Create: keep the name in step with the repo URL until it is typed.
        if (mode === "create" && field === "repoUrl" && !nameTouched) {
          next.name = deriveProjectName(String(value));
        }
        return next;
      });
    },
    [mode, nameTouched],
  );

  const setProvider = useCallback(
    (nextProvider: string) => {
      setForm((prev) => {
        if (mode === "create") {
          return { ...prev, provider: nextProvider };
        }
        // Edit: model fields name models of the previous provider. Switching
        // back restores the project's own values; otherwise clear them so the
        // new provider's default applies. Credential refs are re-derived
        // instead of carried along — a stale usercred-copilot ref on an
        // Anthropic project crashes every new run at pod startup.
        const key = project ? providerKeyFor(project, nextProvider) : undefined;
        const projectProvider = project?.provider || "openai";
        const backToProject = nextProvider === projectProvider;
        return {
          ...prev,
          provider: nextProvider,
          model:
            nextProvider === prev.provider ? prev.model : backToProject ? project?.model || "" : "",
          allowedModels:
            nextProvider === prev.provider
              ? prev.allowedModels
              : backToProject
                ? (project?.allowedModels ?? []).join(", ")
                : "",
          authMode: authModeForProviderSwitch(
            prev.authMode,
            nextProvider,
            prev.useSavedCredentials,
            credentials,
          ),
          useSavedCredentials: savedCredentialProviders.has(nextProvider)
            ? prev.useSavedCredentials
            : false,
          openaiOauthSecret: oauthSecretForProviderSwitch(
            prev.openaiOauthSecret,
            nextProvider,
            credentials,
          ),
          claudeApiKeySecret: nextProvider === "anthropic" ? prev.claudeApiKeySecret : "",
          providerKeySecret: key?.secretName || "",
          providerKeyKey: key?.secretKey || "api-key",
        };
      });
    },
    [mode, project, credentials],
  );

  const acceptSaved = useCallback(
    (saved: Project) => {
      const next = projectFormFromProject(saved);
      setSavedBaseline({ forKey: computedKey, form: next });
      setForm(next);
    },
    [computedKey],
  );

  const reset = useCallback(() => {
    setForm(initial);
    setNameTouched(false);
    setAuthModeTouched(false);
    setModels([]);
    setModelsLoading(false);
    setModelsError(null);
  }, [initial]);

  const dirty = projectFormKey(form) !== initialKey;

  const validate = useCallback(
    () => validateProjectForm(form, mode, savedReady),
    [form, mode, savedReady],
  );

  return {
    mode,
    project,
    idPrefix,
    form,
    initial,
    update,
    setForm,
    setProvider,
    reset,
    acceptSaved,
    dirty,
    effectiveAuthMode,
    useSaved,
    savedSupported,
    savedReady,
    oauthSupported,
    credentials,
    credentialsLoaded,
    userSecrets,
    refreshUserSecrets,
    models,
    modelsLoading,
    modelsError,
    validate,
    nameTouched,
  };
}
