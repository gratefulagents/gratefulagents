import { create } from "@bufbuild/protobuf";
import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCircle2,
  ExternalLink,
  FolderGit2,
  GitBranch,
  GitCommitHorizontal,
  KeyRound,
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
import { useCreateProject } from "@/hooks/useCreateProject";
import { useMyCredentials } from "@/hooks/useMyCredentials";
import { useProjects } from "@/hooks/useWatchedList";
import { client } from "@/lib/client";
import { writeLastProject } from "@/lib/lastProject";
import { openExternal } from "@/lib/native";
import {
  dismissOnboarding,
  emptyCredentialPresence,
  projectNameFromRepo,
  setupProgress,
  type CredentialPresence,
  type ServerCredentialPresence,
} from "@/lib/onboarding";
import { toneSoft, toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
import { CreateProjectRequestSchema, type Project } from "@/rpc/platform/service_pb";

/**
 * OnboardingWizard is the full-screen first-login journey: connect a model
 * provider, add a GitHub token, set commit authorship, and create the first
 * project. Every step (and the whole flow) is skippable; progress reflects
 * live server state so returning to /welcome shows what's already done.
 */

const STEP_TITLES = ["Model provider", "GitHub", "Git identity", "First project"] as const;

/** Providers the wizard offers for the first project (saved-credential wiring). */
const WIZARD_PROVIDERS = [
  { id: "anthropic", label: "Anthropic · Claude" },
  { id: "openai", label: "OpenAI · GPT" },
  { id: "openrouter", label: "OpenRouter" },
  { id: "copilot", label: "GitHub Copilot" },
] as const;

function savedAvailable(p: CredentialPresence, provider: string): boolean {
  if (provider === "anthropic") return p.anthropicApiKey || p.anthropicOauth;
  if (provider === "openai") return p.openaiApiKey || p.openaiOauth;
  if (provider === "openrouter") return p.openrouterApiKey;
  if (provider === "copilot") return p.copilotOauth;
  return false;
}

function pickAuthMode(p: CredentialPresence, provider: string): "api-key" | "oauth" {
  if (provider === "copilot") return "oauth";
  if (provider === "anthropic") return p.anthropicApiKey ? "api-key" : "oauth";
  if (provider === "openai") return p.openaiApiKey ? "api-key" : "oauth";
  return "api-key";
}

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
    if (requested === "project") return 3;
    const parsed = Number.parseInt(requested, 10);
    // Keep the original ?step=3 project deep link working. New steps use a
    // semantic key so future additions do not silently reassign old URLs.
    if (Number.isFinite(parsed) && parsed >= 3) return 3;
    return Number.isFinite(parsed) ? Math.min(Math.max(parsed - 1, 0), 1) : 0;
  }, [searchParams]);
  const [step, setStep] = useState(initialStep);
  const [createdProject, setCreatedProject] = useState<Project | null>(null);

  const progress = setupProgress(presence, projects.length);
  const gitIdentitySaved = Boolean(savedGitIdentityName && savedGitIdentityEmail);
  const gitIdentityDirty =
    gitIdentityName.trim() !== savedGitIdentityName ||
    gitIdentityEmail.trim() !== savedGitIdentityEmail;
  const gitIdentityDone = gitIdentitySaved && !gitIdentityDirty;
  const stepDone = [progress.provider, progress.github, gitIdentityDone, progress.project];

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

  return (
    <div className="h-full overflow-auto bg-background">
      <div className="mx-auto flex min-h-full w-full max-w-[720px] flex-col px-6 pt-[7vh] pb-10">
        <header className="mb-7 flex items-start justify-between gap-4">
          <div className="flex items-center gap-2.5">
            <div className="grid size-8 place-items-center rounded-lg bg-primary text-primary-foreground">
              <Sparkles className="size-4" />
            </div>
            <div>
              <h1 className="text-[22px] font-semibold leading-none tracking-[-0.02em]">
                Welcome{firstName ? `, ${firstName}` : ""}
              </h1>
              <p className="mt-1 text-[13px] text-muted-foreground">
                Four quick steps and your agents are ready to work.
              </p>
            </div>
          </div>
          <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={() => leave()}>
            Skip setup
          </Button>
        </header>

        <nav aria-label="Setup steps" className="mb-6 flex items-center gap-2">
          {STEP_TITLES.map((title, i) => (
            <button
              key={title}
              type="button"
              onClick={() => setStep(i)}
              aria-current={step === i ? "step" : undefined}
              className={cn(
                "flex min-w-0 flex-1 items-center gap-2 rounded-lg border px-3 py-2 text-left transition-colors",
                step === i
                  ? "border-[color:var(--color-primary)]/40 bg-[color:var(--color-primary)]/8"
                  : "border-border/70 hover:bg-muted/40",
              )}
            >
              <span
                className={cn(
                  "grid size-5 shrink-0 place-items-center rounded-full text-[11px] font-medium",
                  stepDone[i]
                    ? cn(toneSoft.success)
                    : step === i
                      ? "bg-primary text-primary-foreground"
                      : "bg-muted text-muted-foreground",
                )}
              >
                {stepDone[i] ? <Check className="size-3" /> : i + 1}
              </span>
              <span
                className={cn(
                  "truncate text-[12.5px]",
                  step === i ? "font-medium" : "text-muted-foreground",
                )}
              >
                {title}
              </span>
            </button>
          ))}
        </nav>

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

        <footer className="mt-8 flex items-center justify-between gap-3 border-t pt-4">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setStep((s) => Math.max(s - 1, 0))}
            className={cn(step === 0 && "invisible")}
          >
            <ArrowLeft data-icon="inline-start" />
            Back
          </Button>
          {step < 3 ? (
            <Button
              size="sm"
              variant={stepDone[step] ? "default" : "outline"}
              onClick={() => setStep((s) => Math.min(s + 1, 3))}
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
      <div className="flex max-w-[440px] flex-col items-center gap-4 py-12 text-center">
        <div className={cn("grid size-12 place-items-center rounded-full", toneSoft.success)}>
          <CheckCircle2 className="size-6" />
        </div>
        <div>
          <h1 className="text-[20px] font-semibold tracking-[-0.02em]">You're all set</h1>
          <p className="mt-1.5 text-[13px] text-muted-foreground">
            <span className="font-medium text-foreground">
              {project.displayName || project.name}
            </span>{" "}
            is ready. Describe a task and the agent takes it from there.
          </p>
        </div>
        <div className="mt-2 flex items-center gap-2">
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
  icon: Icon,
  title,
  done,
  doneNote,
  children,
}: {
  icon: typeof KeyRound;
  title: string;
  done: boolean;
  doneNote?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="mb-4">
      <div className="flex items-center gap-2">
        <Icon className="size-4 text-muted-foreground" />
        <h2 className="text-[15px] font-semibold tracking-[-0.01em]">{title}</h2>
        {done && (
          <span
            className={cn(
              "inline-flex h-[18px] items-center gap-1 rounded-full px-1.5 text-[10.5px] font-medium",
              toneSoft.success,
            )}
          >
            <Check className="size-3" />
            {doneNote || "Done"}
          </span>
        )}
      </div>
      <p className="mt-1 text-[12.5px] leading-relaxed text-muted-foreground">{children}</p>
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
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const connected = [
    (presence.anthropicApiKey || presence.anthropicOauth) && "Claude",
    (presence.openaiApiKey || presence.openaiOauth) && "OpenAI",
    presence.openrouterApiKey && "OpenRouter",
    presence.copilotOauth && "Copilot",
  ].filter(Boolean) as string[];

  async function saveKeys() {
    if (!anthropicKey.trim() && !openaiKey.trim() && !openrouterKey.trim()) return;
    setSaving(true);
    setStatus(null);
    setError(null);
    try {
      const c = await client.updateMyCredentials({
        anthropicApiKey: anthropicKey.trim(),
        openaiApiKey: openaiKey.trim(),
        openrouterApiKey: openrouterKey.trim(),
      });
      apply(c);
      setAnthropicKey("");
      setOpenaiKey("");
      setOpenrouterKey("");
      setStatus("API key saved");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to save API keys");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <StepIntro
        icon={KeyRound}
        title="Connect a model provider"
        done={connected.length > 0}
        doneNote={connected.join(" · ")}
      >
        Agents run on your own model account — Claude, OpenAI, OpenRouter, or GitHub Copilot.
        Credentials are stored privately in your namespace and can be changed anytime in Settings
        → Credentials.
      </StepIntro>

      {/* Local import and Copilot are desktop-only; Claude and OpenAI OAuth also work on web. */}
      <ImportLocalCredentials onImported={onImported} />
      <AnthropicOAuthConnect onSaved={apply} />
      <OpenAIOAuthConnect onSaved={apply} />
      <CopilotOAuthConnect onSaved={apply} />

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
      </div>

      <div className="flex items-center gap-3">
        <Button
          size="sm"
          onClick={() => void saveKeys()}
          disabled={
            saving || (!anthropicKey.trim() && !openaiKey.trim() && !openrouterKey.trim())
          }
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
      <StepIntro icon={GitBranch} title="Connect GitHub" done={presence.githubToken} doneNote="Token saved">
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
        icon={GitCommitHorizontal}
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

/* ── Step 4: first project ────────────────────────────────────── */

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

  const available = WIZARD_PROVIDERS.filter((p) => savedAvailable(presence, p.id));
  const [provider, setProvider] = useState("");
  const effectiveProvider = provider || available[0]?.id || "";
  const effectiveProviderLabel =
    WIZARD_PROVIDERS.find((candidate) => candidate.id === effectiveProvider)?.label ??
    effectiveProvider;
  const effectiveAuthMode = pickAuthMode(presence, effectiveProvider);

  // Every first project gets an explicit model. Suggestions come from the live
  // catalog for the currently selected provider and its saved auth mode.
  const [models, setModels] = useState<string[]>([]);
  const [model, setModel] = useState("");
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);
  useEffect(() => {
    if (!effectiveProvider || !presence.namespace) return;
    const controller = new AbortController();

    async function loadModels() {
      setModelsLoading(true);
      setModelsError(null);
      try {
        const resp = await client.listAvailableModels(
          {
            namespace: presence.namespace,
            provider: effectiveProvider,
            authMode: effectiveAuthMode,
          },
          { signal: controller.signal },
        );
        if (controller.signal.aborted) return;
        setModels(resp.models);
      } catch (err) {
        if (controller.signal.aborted) return;
        setModels([]);
        setModelsError(err instanceof Error ? err.message : "Failed to load provider models");
      } finally {
        if (!controller.signal.aborted) setModelsLoading(false);
      }
    }

    void loadModels();
    return () => controller.abort();
  }, [effectiveAuthMode, effectiveProvider, presence.namespace]);

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
      <StepIntro icon={FolderGit2} title="Create your first project" done={false}>
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
                    setModels([]);
                    setModelsError(null);
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
