import { create } from "@bufbuild/protobuf";
import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCircle2,
  ExternalLink,
  Loader2,
  Sparkles,
} from "lucide-react";

import { AnthropicOAuthConnect } from "@/components/AnthropicOAuthConnect";
import { CopilotOAuthConnect } from "@/components/CopilotOAuthConnect";
import { ImportLocalCredentials } from "@/components/ImportLocalCredentials";
import { OpenAIOAuthConnect } from "@/components/OpenAIOAuthConnect";
import { RuntimeImagePicker } from "@/components/RuntimeImagePicker";
import { Chip } from "@/components/create-flow/create-flow";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { useAuth } from "@/contexts/AuthContext";
import { ApiKeyVerificationNote, useApiKeyVerification } from "@/hooks/useApiKeyVerification";
import { useAvailableModels } from "@/hooks/useAvailableModels";
import { useCreateProject } from "@/hooks/useCreateProject";
import { useMyCredentials } from "@/hooks/useMyCredentials";
import { useMyModelDefaults } from "@/hooks/useMyModelDefaults";
import { useProjects } from "@/hooks/useWatchedList";
import { client } from "@/lib/client";
import { writeLastProject } from "@/lib/lastProject";
import {
  MODEL_PROVIDERS,
  credentialAuthModes,
  flagsFromPresence,
  hasProviderCredential,
  preferredAuthMode,
  providerLabel,
  type ProviderAuthMode,
} from "@/lib/model-providers";
import { openExternal } from "@/lib/native";
import { isTauri } from "@/lib/platform";
import { REASONING_LEVELS } from "@/lib/reasoning";
import {
  dismissOnboarding,
  emptyCredentialPresence,
  projectNameFromRepo,
  setupProgress,
  type CredentialPresence,
  type ServerCredentialPresence,
} from "@/lib/onboarding";
import { toneSoft, toneSolid, toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
import { CreateProjectRequestSchema, type ModelDefaults, type Project } from "@/rpc/platform/service_pb";

/**
 * OnboardingWizard is the full-screen first-login journey: connect a model
 * provider, add a GitHub token, set commit authorship, and create the first
 * project. Every step (and the whole flow) is skippable; progress reflects
 * live server state so returning to /welcome shows what's already done.
 */

const STEPS = [
  { title: "Model provider", hint: "The account your agents think with" },
  { title: "GitHub", hint: "Clone, branch, and open pull requests" },
  { title: "Git identity", hint: "Who agent commits are authored as" },
  { title: "Default model", hint: "What new projects start from" },
  { title: "First project", hint: "Point an agent at a repository" },
] as const;

/** Providers the wizard offers for saved-credential wiring (registry entries with a credential surface). */
const WIZARD_PROVIDERS = MODEL_PROVIDERS.filter((p) => p.userCredentials);

/** Wizard providers with an API-key field — the ones a save can live-verify. */
const WIZARD_KEY_PROVIDERS = WIZARD_PROVIDERS.filter((p) => p.authModes.includes("api-key")).map(
  (p) => p.id,
);

export function OnboardingWizard() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { user } = useAuth();
  const { presence: loaded, reload, apply } = useMyCredentials();
  const presence = loaded ?? emptyCredentialPresence;
  const { projects } = useProjects();
  const [gitIdentityName, setGitIdentityName] = useState("");
  const [gitIdentityEmail, setGitIdentityEmail] = useState("");
  const [savedGitIdentityName, setSavedGitIdentityName] = useState("");
  const [savedGitIdentityEmail, setSavedGitIdentityEmail] = useState("");
  const [gitIdentityLoaded, setGitIdentityLoaded] = useState(false);
  const [gitIdentityLoading, setGitIdentityLoading] = useState(true);
  const [gitIdentityLoadError, setGitIdentityLoadError] = useState<string | null>(null);
  const [gitIdentityReload, setGitIdentityReload] = useState(0);

  useEffect(() => {
    let cancelled = false;

    async function loadGitIdentity() {
      setGitIdentityLoading(true);
      setGitIdentityLoaded(false);
      setGitIdentityLoadError(null);
      try {
        const identity = await client.getMyGitIdentity({});
        if (cancelled) return;
        const savedName = identity.name.trim();
        const savedEmail = identity.email.trim();
        setSavedGitIdentityName(savedName);
        setSavedGitIdentityEmail(savedEmail);
        setGitIdentityName(savedName || user?.name || "");
        setGitIdentityEmail(savedEmail || user?.email || "");
        setGitIdentityLoaded(true);
      } catch (err) {
        if (cancelled) return;
        setGitIdentityLoadError(
          err instanceof Error ? err.message : "Failed to load your git identity",
        );
      } finally {
        if (!cancelled) setGitIdentityLoading(false);
      }
    }

    void loadGitIdentity();
    return () => {
      cancelled = true;
    };
  }, [gitIdentityReload, user?.email, user?.name]);

  const initialStep = useMemo(() => {
    const requested = searchParams.get("step") ?? "";
    if (requested === "git-identity") return 2;
    if (requested === "default-model") return 3;
    if (requested === "project") return 4;
    const parsed = Number.parseInt(requested, 10);
    // Keep the original ?step=3 project deep link working. New steps use a
    // semantic key so future additions do not silently reassign old URLs.
    if (Number.isFinite(parsed) && parsed >= 3) return 4;
    return Number.isFinite(parsed) ? Math.min(Math.max(parsed - 1, 0), 1) : 0;
  }, [searchParams]);
  const [step, setStep] = useState(initialStep);
  const [createdProject, setCreatedProject] = useState<Project | null>(null);

  const progress = setupProgress(presence, projects.length);
  const gitIdentitySaved = Boolean(savedGitIdentityName && savedGitIdentityEmail);
  const gitIdentityDirty =
    gitIdentityName.trim() !== savedGitIdentityName ||
    gitIdentityEmail.trim() !== savedGitIdentityEmail;
  const {
    defaults: myModelDefaults,
    loaded: modelDefaultsLoaded,
    apply: applyModelDefaultsResponse,
  } = useMyModelDefaults();
  const modelDefaultsDone = Boolean(myModelDefaults?.updatedAt);

  const gitIdentityDone = gitIdentitySaved && !gitIdentityDirty;
  const stepDone = [
    progress.provider,
    progress.github,
    gitIdentityDone,
    modelDefaultsDone,
    progress.project,
  ];

  function leave(to = "/") {
    // Any exit counts as "seen it" — never bounce the user back here on the
    // next login from this device.
    dismissOnboarding(user?.id);
    navigate(to);
  }

  const firstName = (user?.name || user?.username || "").split(" ")[0];

  if (createdProject) {
    return (
      <FinishedScreen
        project={createdProject}
        onStartChatting={() => leave("/")}
        onOpenProject={() =>
          leave(`/projects/${createdProject.namespace}/${createdProject.name}`)
        }
      />
    );
  }

  const doneCount = stepDone.filter(Boolean).length;

  return (
    <div className="flex h-full flex-col overflow-hidden bg-background lg:flex-row">
      {/* Desktop rail: a progress spine that reflects live server state. */}
      <aside className="hidden w-[300px] shrink-0 flex-col overflow-y-auto border-r bg-muted/20 px-6 py-8 lg:flex">
        <div className="flex items-center gap-2.5">
          <div className="grid size-8 shrink-0 place-items-center rounded-lg bg-primary text-primary-foreground">
            <Sparkles className="size-4" />
          </div>
          <span className="font-mono text-[10.5px] uppercase tracking-[0.16em] text-muted-foreground">
            First-run setup
          </span>
        </div>

        <div className="mt-7">
          <h1 className="text-[24px] font-semibold leading-tight tracking-[-0.02em]">
            Welcome{firstName ? `, ${firstName}` : ""}
          </h1>
          <p className="mt-1.5 text-[13px] leading-relaxed text-muted-foreground">
            A few minutes of setup and your agents are ready to work. Every step is optional.
          </p>
        </div>

        <ol aria-label="Setup steps" className="mt-8 flex flex-col gap-1">
          {STEPS.map((s, i) => (
            <li key={s.title} className="relative">
              {i < STEPS.length - 1 && (
                <span
                  aria-hidden
                  className={cn(
                    "absolute bottom-[-10px] left-[20px] top-[36px] w-px",
                    stepDone[i]
                      ? "bg-[color-mix(in_oklch,var(--tone-success)_55%,transparent)]"
                      : "bg-[color-mix(in_oklch,var(--muted-foreground)_28%,transparent)]",
                  )}
                />
              )}
              <button
                type="button"
                onClick={() => setStep(i)}
                aria-current={step === i ? "step" : undefined}
                className={cn(
                  "group relative flex w-full items-start gap-3 rounded-lg px-2 py-2 text-left transition-colors",
                  step === i ? "bg-background shadow-[inset_0_0_0_1px_var(--border)]" : "hover:bg-muted/50",
                )}
              >
                <span
                  className={cn(
                    "mt-px grid size-[25px] shrink-0 place-items-center rounded-full font-mono text-[11px] transition-colors",
                    stepDone[i]
                      ? cn(toneSolid.success)
                      : step === i
                        ? "bg-primary text-primary-foreground"
                        : "border border-border bg-background text-muted-foreground",
                  )}
                >
                  {stepDone[i] ? <Check className="size-3" /> : i + 1}
                </span>
                <span className="min-w-0">
                  <span
                    className={cn(
                      "block truncate text-[13px] leading-[25px]",
                      step === i ? "font-medium text-foreground" : "text-muted-foreground",
                    )}
                  >
                    {s.title}
                  </span>
                  <span className="block text-[11.5px] leading-snug text-muted-foreground/70">
                    {s.hint}
                  </span>
                </span>
              </button>
            </li>
          ))}
        </ol>

        <div className="mt-auto flex flex-col gap-3 pt-10">
          <div aria-hidden className="flex items-center gap-1">
            {STEPS.map((s, i) => (
              <span
                key={s.title}
                className={cn(
                  "h-1 flex-1 rounded-full transition-colors",
                  stepDone[i]
                    ? "bg-[color:var(--tone-success)]"
                    : i === step
                      ? "bg-primary/50"
                      : "bg-border",
                )}
              />
            ))}
          </div>
          <div className="flex items-center justify-between">
            <span className="font-mono text-[11px] text-muted-foreground">
              {doneCount}/{STEPS.length} configured
            </span>
            <Button
              variant="ghost"
              size="sm"
              className="-mr-2 h-7 px-2 text-muted-foreground"
              onClick={() => leave()}
            >
              Skip setup
            </Button>
          </div>
        </div>
      </aside>

      {/* Compact header for phones and tablets. */}
      <header className="flex flex-col gap-3 border-b px-5 pb-4 pt-5 lg:hidden">
        <div className="flex items-start justify-between gap-3">
          <div className="flex items-center gap-2.5">
            <div className="grid size-8 shrink-0 place-items-center rounded-lg bg-primary text-primary-foreground">
              <Sparkles className="size-4" />
            </div>
            <div>
              <h1 className="text-[17px] font-semibold leading-none tracking-[-0.02em]">
                Welcome{firstName ? `, ${firstName}` : ""}
              </h1>
              <p className="mt-1 font-mono text-[11px] text-muted-foreground">
                {doneCount}/{STEPS.length} configured
              </p>
            </div>
          </div>
          <Button
            variant="ghost"
            size="sm"
            className="text-muted-foreground"
            onClick={() => leave()}
          >
            Skip
          </Button>
        </div>
        <div className="flex items-center gap-1" role="tablist" aria-label="Setup steps">
          {STEPS.map((s, i) => (
            <button
              key={s.title}
              type="button"
              role="tab"
              aria-label={s.title}
              aria-selected={step === i}
              onClick={() => setStep(i)}
              className={cn(
                "h-1.5 flex-1 rounded-full transition-colors",
                stepDone[i]
                  ? "bg-[color:var(--tone-success)]"
                  : i === step
                    ? "bg-primary"
                    : "bg-border",
              )}
            />
          ))}
        </div>
      </header>

      <main id="main-content" className="flex-1 overflow-y-auto">
        <div className="mx-auto flex min-h-full w-full max-w-[720px] flex-col px-5 py-8 sm:px-8 lg:px-10 lg:py-[8vh]">
          <p className="mb-4 font-mono text-[10.5px] uppercase tracking-[0.16em] text-muted-foreground">
            Step {step + 1} of {STEPS.length} — {STEPS[step].title}
          </p>

          <section className="flex-1">
            {step === 0 && (
              <ProviderStep presence={presence} apply={apply} onImported={() => void reload()} />
            )}
            {step === 1 && <GitHubStep presence={presence} apply={apply} />}
            {step === 2 && (
              <GitIdentityStep
                name={gitIdentityName}
                email={gitIdentityEmail}
                saved={gitIdentityDone}
                dirty={gitIdentityDirty}
                loaded={gitIdentityLoaded}
                loading={gitIdentityLoading}
                loadError={gitIdentityLoadError}
                onNameChange={setGitIdentityName}
                onEmailChange={setGitIdentityEmail}
                onRetry={() => setGitIdentityReload((attempt) => attempt + 1)}
                onSaved={(name, email) => {
                  const savedName = name.trim();
                  const savedEmail = email.trim();
                  setGitIdentityName(savedName);
                  setGitIdentityEmail(savedEmail);
                  setSavedGitIdentityName(savedName);
                  setSavedGitIdentityEmail(savedEmail);
                  setGitIdentityLoadError(null);
                }}
              />
            )}
            {step === 3 && (
              <ModelDefaultsStep
                key={modelDefaultsLoaded ? "loaded" : "loading"}
                defaults={myModelDefaults}
                presence={presence}
                saved={modelDefaultsDone}
                onSaved={applyModelDefaultsResponse}
              />
            )}
            {step === 4 && (
              <ProjectStep
                presence={presence}
                onCreated={(project) => {
                  writeLastProject({ namespace: project.namespace, name: project.name });
                  dismissOnboarding(user?.id);
                  setCreatedProject(project);
                }}
                onGoToStep={setStep}
              />
            )}
          </section>

          <footer className="mt-10 flex items-center justify-between gap-3 border-t pt-5">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setStep((s) => Math.max(s - 1, 0))}
              className={cn("text-muted-foreground", step === 0 && "invisible")}
            >
              <ArrowLeft data-icon="inline-start" />
              Back
            </Button>
            {step < STEPS.length - 1 ? (
              <Button
                size="sm"
                variant={stepDone[step] ? "default" : "outline"}
                onClick={() => setStep((s) => Math.min(s + 1, STEPS.length - 1))}
              >
                {stepDone[step] ? "Continue" : "Skip for now"}
                <ArrowRight data-icon="inline-end" />
              </Button>
            ) : (
              <Button size="sm" variant="outline" onClick={() => leave()}>
                Skip for now
              </Button>
            )}
          </footer>
        </div>
      </main>
    </div>
  );
}

function FinishedScreen({
  project,
  onStartChatting,
  onOpenProject,
}: {
  project: Project;
  onStartChatting: () => void;
  onOpenProject: () => void;
}) {
  return (
    <div className="grid h-full place-items-center overflow-auto bg-background px-6">
      <div className="flex max-w-[460px] flex-col items-center gap-5 py-12 text-center">
        <div
          className={cn(
            "grid size-14 place-items-center rounded-full ring-8 ring-[color-mix(in_oklch,var(--tone-success)_6%,transparent)]",
            toneSoft.success,
          )}
        >
          <CheckCircle2 className="size-7" />
        </div>
        <div>
          <p className="font-mono text-[10.5px] uppercase tracking-[0.16em] text-muted-foreground">
            Setup complete
          </p>
          <h1 className="mt-2 text-[24px] font-semibold tracking-[-0.02em]">You're all set</h1>
          <p className="mt-2 text-[13px] leading-relaxed text-muted-foreground">
            <span className="font-medium text-foreground">
              {project.displayName || project.name}
            </span>{" "}
            is ready. Describe a task and the agent takes it from there.
          </p>
        </div>
        <code className="rounded-md border bg-muted/30 px-2.5 py-1 font-mono text-[11.5px] text-muted-foreground">
          {project.namespace}/{project.name}
        </code>
        <div className="mt-1 flex items-center gap-2">
          <Button onClick={onStartChatting}>Start chatting</Button>
          <Button variant="outline" onClick={onOpenProject}>
            Open project
          </Button>
        </div>
      </div>
    </div>
  );
}

/* ── Shared step bits ─────────────────────────────────────────── */

function StepIntro({
  title,
  done,
  doneNote,
  children,
}: {
  title: string;
  done: boolean;
  doneNote?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="mb-6">
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <h2 className="text-[21px] font-semibold leading-tight tracking-[-0.02em]">{title}</h2>
        {done && (
          <span
            className={cn(
              "inline-flex h-[19px] items-center gap-1 rounded-full px-2 text-[10.5px] font-medium",
              toneSoft.success,
            )}
          >
            <Check className="size-3" />
            {doneNote || "Done"}
          </span>
        )}
      </div>
      <p className="mt-2 max-w-[56ch] text-[13px] leading-relaxed text-muted-foreground">
        {children}
      </p>
    </div>
  );
}

function SavedChip() {
  return (
    <span
      className={cn(
        "inline-flex h-[18px] items-center rounded-full px-1.5 text-[10.5px] font-medium select-none",
        toneSoft.success,
      )}
    >
      Saved
    </span>
  );
}

/**
 * ProviderRow frames one sign-in option: name and promise on the left, the
 * live connect control (compact OAuth component) on the right.
 */
function ProviderRow({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex flex-col gap-3 border-b p-4 last:border-b-0 sm:flex-row sm:items-center sm:justify-between sm:gap-6 sm:px-5 sm:py-4">
      <div className="min-w-0">
        <h3 className="text-[13px] font-medium">{title}</h3>
        <p className="mt-0.5 max-w-[44ch] text-[11.5px] leading-relaxed text-muted-foreground">
          {description}
        </p>
      </div>
      <div className="min-w-0 sm:max-w-[58%] sm:shrink-0">{children}</div>
    </div>
  );
}

/* ── Step 1: model provider ───────────────────────────────────── */

function ProviderStep({
  presence,
  apply,
  onImported,
}: {
  presence: CredentialPresence;
  apply: (c: ServerCredentialPresence) => void;
  onImported: () => void;
}) {
  const [anthropicKey, setAnthropicKey] = useState("");
  const [openaiKey, setOpenaiKey] = useState("");
  const [openrouterKey, setOpenrouterKey] = useState("");
  const [xaiKey, setXaiKey] = useState("");
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const { verifications, verify } = useApiKeyVerification();

  const connected = [
    (presence.anthropicApiKey || presence.anthropicOauth) && "Claude",
    (presence.openaiApiKey || presence.openaiOauth) && "OpenAI",
    presence.openrouterApiKey && "OpenRouter",
    presence.xaiApiKey && "xAI",
    presence.copilotOauth && "Copilot",
  ].filter(Boolean) as string[];

  const nothingToSave =
    !anthropicKey.trim() && !openaiKey.trim() && !openrouterKey.trim() && !xaiKey.trim();

  async function saveKeys() {
    if (nothingToSave) return;
    const savedProviders = [
      anthropicKey.trim() && "anthropic",
      openaiKey.trim() && "openai",
      openrouterKey.trim() && "openrouter",
      xaiKey.trim() && "xai",
    ].filter(Boolean) as string[];
    setSaving(true);
    setStatus(null);
    setError(null);
    try {
      const c = await client.updateMyCredentials({
        anthropicApiKey: anthropicKey.trim(),
        openaiApiKey: openaiKey.trim(),
        openrouterApiKey: openrouterKey.trim(),
        xaiApiKey: xaiKey.trim(),
      });
      apply(c);
      setAnthropicKey("");
      setOpenaiKey("");
      setOpenrouterKey("");
      setXaiKey("");
      setStatus("API key saved");
      verify(c.namespace || presence.namespace, savedProviders);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save API keys");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <StepIntro
        title="Connect a model provider"
        done={connected.length > 0}
        doneNote={connected.join(" · ")}
      >
        Agents run on your own model account — Claude, OpenAI, OpenRouter, xAI, or GitHub
        Copilot. Credentials are stored privately in your namespace and can be changed anytime in
        Settings → Credentials.
      </StepIntro>

      {/* Local import and Copilot are desktop-only; Claude and OpenAI OAuth also work on web. */}
      <ImportLocalCredentials onImported={onImported} />

      <div className="overflow-hidden rounded-xl border">
        <ProviderRow
          title="Claude"
          description="Sign in with your Claude Pro/Max account; refreshable credentials are stored for new projects."
        >
          <AnthropicOAuthConnect compact onSaved={apply} />
        </ProviderRow>
        <ProviderRow
          title="OpenAI"
          description="Sign in with ChatGPT; refreshable credentials are stored for new projects."
        >
          <OpenAIOAuthConnect compact onSaved={apply} />
        </ProviderRow>
        {isTauri && (
          <ProviderRow
            title="GitHub Copilot"
            description="Connect with GitHub; refreshable Copilot credentials are stored for new projects."
          >
            <CopilotOAuthConnect compact onSaved={apply} />
          </ProviderRow>
        )}
      </div>

      <div className="flex items-center gap-3 pt-1" role="separator" aria-label="or paste an API key">
        <span aria-hidden className="h-px flex-1 bg-border" />
        <span className="font-mono text-[10.5px] uppercase tracking-[0.16em] text-muted-foreground">
          or paste an API key
        </span>
        <span aria-hidden className="h-px flex-1 bg-border" />
      </div>

      <div className="grid gap-x-4 gap-y-4 sm:grid-cols-2">
        <div>
          <div className="mb-1.5 flex h-5 items-center justify-between gap-2">
            <Label className="text-[12.5px]">Anthropic API key</Label>
            {presence.anthropicApiKey && <SavedChip />}
          </div>
          <Input
            type="password"
            value={anthropicKey}
            onChange={(e) => setAnthropicKey(e.target.value)}
            placeholder={presence.anthropicApiKey ? "•••• (saved) — enter to replace" : "sk-ant-..."}
            autoComplete="off"
          />
        </div>
        <div>
          <div className="mb-1.5 flex h-5 items-center justify-between gap-2">
            <Label className="text-[12.5px]">OpenAI API key</Label>
            {presence.openaiApiKey && <SavedChip />}
          </div>
          <Input
            type="password"
            value={openaiKey}
            onChange={(e) => setOpenaiKey(e.target.value)}
            placeholder={presence.openaiApiKey ? "•••• (saved) — enter to replace" : "sk-..."}
            autoComplete="off"
          />
        </div>
        <div>
          <div className="mb-1.5 flex h-5 items-center justify-between gap-2">
            <Label className="text-[12.5px]">OpenRouter API key</Label>
            {presence.openrouterApiKey && <SavedChip />}
          </div>
          <Input
            type="password"
            value={openrouterKey}
            onChange={(e) => setOpenrouterKey(e.target.value)}
            placeholder={
              presence.openrouterApiKey ? "•••• (saved) — enter to replace" : "sk-or-v1-..."
            }
            autoComplete="off"
          />
        </div>
        <div>
          <div className="mb-1.5 flex h-5 items-center justify-between gap-2">
            <Label className="text-[12.5px]">xAI API key</Label>
            {presence.xaiApiKey && <SavedChip />}
          </div>
          <Input
            type="password"
            value={xaiKey}
            onChange={(e) => setXaiKey(e.target.value)}
            placeholder={presence.xaiApiKey ? "•••• (saved) — enter to replace" : "xai-..."}
            autoComplete="off"
          />
        </div>
      </div>

      <div className="flex items-center gap-3">
        <Button
          size="sm"
          onClick={() => void saveKeys()}
          disabled={saving || nothingToSave}
        >
          {saving && <Loader2 className="animate-spin" data-icon="inline-start" />}
          {saving ? "Saving…" : "Save API key"}
        </Button>
        {status && <span className="text-[12px] text-muted-foreground">{status}</span>}
        {error && (
          <span className="text-[12px] text-destructive" role="alert">
            {error}
          </span>
        )}
      </div>

      {WIZARD_KEY_PROVIDERS.some((p) => verifications[p]) && (
        <div className="flex flex-col gap-1">
          {WIZARD_KEY_PROVIDERS.map((p) => (
            <ApiKeyVerificationNote key={p} provider={p} state={verifications[p]} />
          ))}
        </div>
      )}

      <p className="text-[11.5px] text-muted-foreground/80">
        Prefer existing CLI credentials? You can also paste credential JSON in{" "}
        <Link to="/settings/credentials" className="underline underline-offset-2 hover:text-foreground">
          Settings → Credentials
        </Link>
        . GitHub Copilot sign-in and local credential import require the desktop app.
      </p>
    </div>
  );
}

/* ── Step 2: GitHub token ─────────────────────────────────────── */

function GitHubStep({
  presence,
  apply,
}: {
  presence: CredentialPresence;
  apply: (c: ServerCredentialPresence) => void;
}) {
  const [token, setToken] = useState("");
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function saveToken() {
    if (!token.trim()) return;
    setSaving(true);
    setStatus(null);
    setError(null);
    try {
      const c = await client.updateMyCredentials({ githubToken: token.trim() });
      apply(c);
      setToken("");
      setStatus("GitHub token saved");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save the GitHub token");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <StepIntro title="Connect GitHub" done={presence.githubToken} doneNote="Token saved">
        A personal access token lets agents clone private repositories, push branches, and open
        pull requests as you. Use a fine-grained token with read/write access to the repositories
        you'll work on.
      </StepIntro>

      <div className="max-w-xl">
        <div className="mb-1.5 flex h-5 items-center justify-between gap-2">
          <Label className="text-[12.5px]">GitHub personal access token</Label>
          {presence.githubToken && <SavedChip />}
        </div>
        <Input
          type="password"
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder={presence.githubToken ? "•••• (saved) — enter to replace" : "ghp_... / github_pat_..."}
          autoComplete="off"
        />
      </div>

      <div className="flex items-center gap-3">
        <Button size="sm" onClick={() => void saveToken()} disabled={saving || !token.trim()}>
          {saving && <Loader2 className="animate-spin" data-icon="inline-start" />}
          {saving ? "Saving…" : "Save token"}
        </Button>
        <Button
          variant="ghost"
          size="sm"
          className="text-muted-foreground"
          onClick={() => void openExternal("https://github.com/settings/personal-access-tokens/new")}
        >
          Create a token on GitHub
          <ExternalLink data-icon="inline-end" />
        </Button>
      </div>

      <div className="flex items-center gap-3">
        {status && <span className="text-[12px] text-muted-foreground">{status}</span>}
        {error && (
          <span className="text-[12px] text-destructive" role="alert">
            {error}
          </span>
        )}
      </div>

      <p className="text-[11.5px] text-muted-foreground/80">
        Setting up automation for a whole org? The GitHub App integration (Sources → GitHub) is a
        better fit — you can add it later.
      </p>
    </div>
  );
}

/* ── Step 3: git identity ─────────────────────────────────────── */

function GitIdentityStep({
  name,
  email,
  saved,
  dirty,
  loaded,
  loading,
  loadError,
  onNameChange,
  onEmailChange,
  onRetry,
  onSaved,
}: {
  name: string;
  email: string;
  saved: boolean;
  dirty: boolean;
  loaded: boolean;
  loading: boolean;
  loadError: string | null;
  onNameChange: (value: string) => void;
  onEmailChange: (value: string) => void;
  onRetry: () => void;
  onSaved: (name: string, email: string) => void;
}) {
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const trimmedName = name.trim();
  const trimmedEmail = email.trim();
  const incomplete = !trimmedName || !trimmedEmail;

  async function saveIdentity() {
    if (incomplete || !loaded || !dirty) return;
    setSaving(true);
    setStatus(null);
    setError(null);
    try {
      const identity = await client.updateMyGitIdentity({
        name: trimmedName,
        email: trimmedEmail,
      });
      onSaved(identity.name, identity.email);
      setStatus("Git identity saved");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save your git identity");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <StepIntro
        title="Set your git identity"
        done={saved}
        doneNote="Identity saved"
      >
        Choose the name and email agents use when they author commits for you. The gratefulagents
        GitHub App is still credited as a co-author.
      </StepIntro>

      <div className="grid gap-x-4 gap-y-4 sm:grid-cols-2">
        <div>
          <Label htmlFor="onboarding-git-name" className="mb-1.5 block text-[12.5px]">
            Commit author name
          </Label>
          <Input
            id="onboarding-git-name"
            value={name}
            onChange={(event) => {
              onNameChange(event.target.value);
              setStatus(null);
              setError(null);
            }}
            placeholder="Ada Lovelace"
            autoComplete="name"
            disabled={loading || !loaded}
          />
        </div>
        <div>
          <Label htmlFor="onboarding-git-email" className="mb-1.5 block text-[12.5px]">
            Commit author email
          </Label>
          <Input
            id="onboarding-git-email"
            type="email"
            value={email}
            onChange={(event) => {
              onEmailChange(event.target.value);
              setStatus(null);
              setError(null);
            }}
            placeholder="you@example.com"
            autoComplete="email"
            disabled={loading || !loaded}
          />
          <p className="mt-1 text-[11.5px] text-muted-foreground/80">
            Use your GitHub noreply address to keep your personal email out of public commits.
          </p>
        </div>
      </div>

      {trimmedName && trimmedEmail && (
        <div className="rounded-lg border bg-muted/30 px-3 py-2.5">
          <span className="block text-[10.5px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
            Commit author preview
          </span>
          <code className="mt-1 block truncate font-mono text-[12px]">
            {trimmedName} &lt;{trimmedEmail}&gt;
          </code>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <Button
          size="sm"
          onClick={() => void saveIdentity()}
          disabled={saving || loading || !loaded || incomplete || !dirty}
        >
          {saving && <Loader2 className="animate-spin" data-icon="inline-start" />}
          {saving ? "Saving…" : "Save git identity"}
        </Button>
        {loading && <span className="text-[12px] text-muted-foreground">Loading…</span>}
        {loaded && incomplete && (
          <span className="text-[12px] text-muted-foreground">Enter both name and email.</span>
        )}
        {loaded && dirty && !incomplete && !status && (
          <span className="text-[12px] text-muted-foreground">Unsaved changes</span>
        )}
        {status && <span className="text-[12px] text-muted-foreground">{status}</span>}
        {loadError && (
          <>
            <span className="text-[12px] text-destructive" role="alert">
              {loadError}
            </span>
            <Button variant="outline" size="sm" onClick={onRetry}>
              Retry
            </Button>
          </>
        )}
        {error && (
          <span className="text-[12px] text-destructive" role="alert">
            {error}
          </span>
        )}
      </div>
    </div>
  );
}

/* ── Step 4: default model ────────────────────────────────────── */

function ModelDefaultsStep({
  defaults,
  presence,
  saved,
  onSaved,
}: {
  defaults: ModelDefaults | null;
  presence: CredentialPresence;
  saved: boolean;
  onSaved: (defaults: ModelDefaults) => void;
}) {
  const [provider, setProvider] = useState(defaults?.provider || "anthropic");
  const [authMode, setAuthMode] = useState<ProviderAuthMode>(
    defaults?.authMode === "oauth"
      ? "oauth"
      : preferredAuthMode(flagsFromPresence(presence), defaults?.provider || "anthropic"),
  );
  const [model, setModel] = useState(defaults?.model ?? "");
  const [reasoningLevel, setReasoningLevel] = useState(defaults?.reasoningLevel ?? "");
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const flags = flagsFromPresence(presence);
  const effectiveAuthMode = provider === "copilot" ? "oauth" : authMode;
  const credentialReady = hasProviderCredential(flags, provider, effectiveAuthMode);
  const {
    models: modelSuggestions,
    loading: modelsLoading,
    error: modelsError,
  } = useAvailableModels({
    namespace: presence.namespace,
    provider,
    authMode: effectiveAuthMode,
    enabled: credentialReady,
  });

  useEffect(() => {
    const saved = flagsFromPresence(presence);
    const availableProviders = WIZARD_PROVIDERS.filter((candidate) =>
      hasProviderCredential(saved, candidate.id),
    );
    const nextProvider = hasProviderCredential(saved, provider)
      ? provider
      : availableProviders[0]?.id ?? "";
    if (nextProvider === provider && hasProviderCredential(saved, provider, authMode)) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reconcile defaults after credential presence loads or changes
    setProvider(nextProvider);
    setAuthMode(preferredAuthMode(saved, nextProvider));
    if (nextProvider !== provider) setModel("");
  }, [presence, provider, authMode]);

  async function save() {
    setSaving(true);
    setStatus(null);
    setError(null);
    try {
      onSaved(
        await client.updateMyModelDefaults({
          provider,
          authMode: provider === "copilot" ? "oauth" : authMode,
          model: model.trim(),
          reasoningLevel,
          disabled: false,
        }),
      );
      setStatus("Default model saved");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save default model");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <StepIntro
        title="Pick a default model"
        done={saved}
        doneNote="Saved"
      >
        Optional: choose the provider, model, and reasoning level new projects, triggers, and scan configs
        start from. You can override them on any form, change them later in Settings → Models, or
        skip this step entirely.
      </StepIntro>

      <div>
        <Label className="mb-1.5 block text-[12.5px]">Provider</Label>
        <div className="flex flex-wrap gap-1.5">
          {WIZARD_PROVIDERS.filter((p) => hasProviderCredential(flags, p.id)).map((p) => (
            <Chip
              key={p.id}
              selected={provider === p.id}
              onSelect={() => {
                if (p.id !== provider) setModel("");
                setProvider(p.id);
                setAuthMode(preferredAuthMode(flags, p.id));
              }}
            >
              {p.label}
            </Chip>
          ))}
        </div>
      </div>

      <div className="grid gap-x-4 gap-y-4 sm:grid-cols-3">
        <div>
          <Label htmlFor="onboarding-default-auth-mode" className="mb-1.5 block text-[12.5px]">
            Authentication mode
          </Label>
          <select
            id="onboarding-default-auth-mode"
            className="flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            value={effectiveAuthMode}
            onChange={(e) => setAuthMode(e.target.value as ProviderAuthMode)}
            disabled={provider === "copilot"}
          >
            {credentialAuthModes(flags, provider).map((mode) => (
              <option key={mode} value={mode}>
                {mode === "api-key" ? "API key" : "OAuth"}
              </option>
            ))}
          </select>
        </div>
        <div>
          <Label htmlFor="onboarding-default-model" className="mb-1.5 block text-[12.5px]">
            Model
          </Label>
          <Input
            id="onboarding-default-model"
            list="onboarding-default-model-options"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder={modelsLoading ? "Loading models…" : "provider default"}
          />
          <datalist id="onboarding-default-model-options">
            {modelSuggestions.map((candidate) => (
              <option key={candidate} value={candidate} />
            ))}
          </datalist>
          {modelsLoading && (
            <p className="mt-1 text-[11.5px] text-muted-foreground">Loading models…</p>
          )}
          {!modelsLoading && modelSuggestions.length > 0 && (
            <p className="mt-1 text-[11.5px] text-muted-foreground">
              {modelSuggestions.length} {providerLabel(provider)}{" "}
              {modelSuggestions.length === 1 ? "model" : "models"} available
            </p>
          )}
          {modelsError && (
            <p className="mt-1 text-[11.5px] text-destructive">{modelsError}</p>
          )}
        </div>
        <div>
          <Label htmlFor="onboarding-default-reasoning" className="mb-1.5 block text-[12.5px]">
            Reasoning level
          </Label>
          <select
            id="onboarding-default-reasoning"
            className="flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            value={reasoningLevel}
            onChange={(e) => setReasoningLevel(e.target.value)}
          >
            {REASONING_LEVELS.map((level) => (
              <option key={level || "default"} value={level}>
                {level || "default"}
              </option>
            ))}
          </select>
        </div>
      </div>

      <div className="flex items-center gap-3">
        <Button size="sm" onClick={() => void save()} disabled={saving || !credentialReady}>
          {saving && <Loader2 className="animate-spin" data-icon="inline-start" />}
          {saving ? "Saving…" : "Save default model"}
        </Button>
        {status && <span className="text-[12px] text-muted-foreground">{status}</span>}
        {error && (
          <span className="text-[12px] text-destructive" role="alert">
            {error}
          </span>
        )}
      </div>
    </div>
  );
}

/* ── Step 5: first project ────────────────────────────────────── */

function ProjectStep({
  presence,
  onCreated,
  onGoToStep,
}: {
  presence: CredentialPresence;
  onCreated: (project: Project) => void;
  onGoToStep: (step: number) => void;
}) {
  const { createProject, submitting, error } = useCreateProject();
  const [repoUrl, setRepoUrl] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [image, setImage] = useState("");
  const [timeout, setTimeout] = useState("");
  const [formError, setFormError] = useState<string | null>(null);

  const flags = flagsFromPresence(presence);
  const available = WIZARD_PROVIDERS.filter((p) => hasProviderCredential(flags, p.id));
  const [provider, setProvider] = useState("");
  const effectiveProvider = provider || available[0]?.id || "";
  const effectiveProviderLabel = providerLabel(effectiveProvider);
  const effectiveAuthMode = preferredAuthMode(flags, effectiveProvider);

  // Every first project gets an explicit model. Suggestions come from the live
  // catalog for the currently selected provider and its saved auth mode.
  const [model, setModel] = useState("");
  const {
    models,
    loading: modelsLoading,
    error: modelsError,
  } = useAvailableModels({
    namespace: presence.namespace,
    provider: effectiveProvider,
    authMode: effectiveAuthMode,
  });

  const name = projectNameFromRepo(repoUrl, displayName);
  const receipt = `${presence.namespace || "…"}/${name}`;

  async function submit() {
    setFormError(null);
    if (available.length === 0) {
      setFormError("Connect a model provider first — the project needs a credential to run agents.");
      return;
    }
    if (!model.trim()) {
      setFormError("Choose a model for this project.");
      return;
    }
    try {
      const project = await createProject(
        create(CreateProjectRequestSchema, {
          name,
          displayName: displayName.trim() || name,
          repoUrl: repoUrl.trim(),
          image: image.trim(),
          timeout: timeout.trim(),
          provider: effectiveProvider,
          model: model.trim(),
          authMode: effectiveAuthMode,
          useSavedCredentials: true,
          configureRuntimeProfile: true,
          permissionMode: "workspace-write",
          egressMode: "unrestricted",
        }),
      );
      onCreated(project);
    } catch {
      // Error surfaced via the hook's `error` state; keep the form intact.
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <StepIntro title="Create your first project" done={false}>
        A project points agents at a repository and carries its defaults. Choose its provider and
        model now; fine-tune policies and instructions later in project settings.
      </StepIntro>

      {available.length === 0 ? (
        <div className="rounded-lg border border-dashed p-4 text-[12.5px] text-muted-foreground">
          No model provider connected yet, so the project would have no credential to run with.{" "}
          <button
            type="button"
            className={cn("underline underline-offset-2", toneText.info)}
            onClick={() => onGoToStep(0)}
          >
            Connect one in step 1
          </button>{" "}
          — or skip for now and come back from Settings.
        </div>
      ) : (
        <>
          <div>
            <Label className="mb-1.5 block text-[12.5px]">Repository URL</Label>
            <Input
              value={repoUrl}
              onChange={(e) => setRepoUrl(e.target.value)}
              placeholder="https://github.com/you/repo"
              autoComplete="off"
            />
            <p className="mt-1 text-[11.5px] text-muted-foreground/80">
              Private repositories use your saved GitHub token{presence.githubToken ? "" : " (step 2)"}.
            </p>
          </div>

          <div className="grid gap-x-4 gap-y-4 sm:grid-cols-2">
            <div>
              <Label htmlFor="onboarding-project-display-name" className="mb-1.5 block text-[12.5px]">
                Display name
              </Label>
              <Input
                id="onboarding-project-display-name"
                value={displayName}
                onChange={(e) => setDisplayName(e.target.value)}
                placeholder={name !== "my-project" ? name : "My project"}
                autoComplete="off"
              />
            </div>
            <div>
              <Label htmlFor="onboarding-project-timeout" className="mb-1.5 block text-[12.5px]">
                Timeout
              </Label>
              <Input
                id="onboarding-project-timeout"
                value={timeout}
                onChange={(e) => setTimeout(e.target.value)}
                placeholder="30m"
                autoComplete="off"
              />
              <p className="mt-1 text-[11.5px] text-muted-foreground/80">
                Maximum duration for each run, e.g. 30m or 2h.
              </p>
            </div>
          </div>

          <div className="max-w-xl">
            <Label htmlFor="onboarding-project-image" className="mb-1.5 block text-[12.5px]">
              Runtime image
            </Label>
            <RuntimeImagePicker
              id="onboarding-project-image"
              value={image}
              onChange={setImage}
            />
          </div>

          <div>
            <Label className="mb-1.5 block text-[12.5px]">Model provider</Label>
            <div className="flex min-h-9 flex-wrap items-center gap-1.5">
              {available.map((p) => (
                <Chip
                  key={p.id}
                  selected={effectiveProvider === p.id}
                  onSelect={() => {
                    if (p.id === effectiveProvider) return;
                    setProvider(p.id);
                    setModel("");
                    setFormError(null);
                  }}
                >
                  {p.label}
                </Chip>
              ))}
            </div>
          </div>

          <div className="max-w-xl">
            <Label htmlFor="onboarding-project-model" className="mb-1.5 block text-[12.5px]">
              Model
            </Label>
            <Input
              id="onboarding-project-model"
              list="onboarding-project-model-options"
              value={model}
              onChange={(e) => {
                setModel(e.target.value);
                setFormError(null);
              }}
              placeholder={modelsLoading ? "Loading models…" : "Choose or enter a model"}
              autoComplete="off"
            />
            <datalist id="onboarding-project-model-options">
              {models.map((candidate) => (
                <option key={candidate} value={candidate} />
              ))}
            </datalist>
            {modelsLoading && (
              <p className="mt-1 text-[11.5px] text-muted-foreground">Loading models…</p>
            )}
            {!modelsLoading && models.length > 0 && (
              <p className="mt-1 text-[11.5px] text-muted-foreground">
                {models.length} {effectiveProviderLabel} {models.length === 1 ? "model" : "models"}{" "}
                available
              </p>
            )}
            {modelsError && (
              <p className="mt-1 text-[11.5px] text-destructive">{modelsError}</p>
            )}
          </div>

          <div className="flex items-center gap-3">
            <Button size="sm" onClick={() => void submit()} disabled={submitting}>
              {submitting && <Loader2 className="animate-spin" data-icon="inline-start" />}
              {submitting ? "Creating…" : "Create project"}
            </Button>
            <code className="font-mono text-[11.5px] text-muted-foreground">Creates {receipt}</code>
          </div>

          {(formError || error) && (
            <p className="text-[12px] text-destructive" role="alert">
              {formError || error}
            </p>
          )}
        </>
      )}
    </div>
  );
}
