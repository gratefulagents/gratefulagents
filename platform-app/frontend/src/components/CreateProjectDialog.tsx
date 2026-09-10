import type { ReactElement } from "react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  Blocks,
  Bot,
  ChevronDown,
  Cpu,
  FolderGit2,
  Loader2,
  Plus,
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
import { FlowField, OptionRow, OptionRows } from "@/components/create-flow/create-flow";
import { useCreateProject } from "@/hooks/useCreateProject";
import { cn } from "@/lib/utils";
import { client } from "@/lib/client";
import { describeRpcError, isTransientRpcError } from "@/lib/rpc-errors";
import { toneText } from "@/lib/status";

import {
  agentSummary,
  createRequestFromForm,
  deriveProjectName,
  modelSummary,
  repoSummary,
  runtimeSummary,
  toolsSummary,
} from "./project-settings/projectForm";
import {
  AgentFields,
  ModelFields,
  RepositoryDetailsFields,
  RepositoryUrlField,
  RuntimeFields,
  ToolsFields,
} from "./project-settings/ProjectFormSections";
import { useProjectForm } from "./project-settings/useProjectForm";

export function CreateProjectDialog({
  trigger,
}: {
  /** Optional custom trigger element; defaults to a "New project" button. */
  trigger?: ReactElement<Record<string, unknown>>;
} = {}) {
  const navigate = useNavigate();
  const { createProject, submitting, error, clearError } = useCreateProject();
  const [open, setOpen] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [moreOpen, setMoreOpen] = useState(false);
  const [newRepository, setNewRepository] = useState(false);
  const [repositoryName, setRepositoryName] = useState("");
  const [organization, setOrganization] = useState("");
  const [publicRepository, setPublicRepository] = useState(false);
  const [creatingRepository, setCreatingRepository] = useState(false);
  const [createdRepository, setCreatedRepository] = useState("");
  const busy = submitting || creatingRepository;

  const c = useProjectForm({ mode: "create", enabled: open, idPrefix: "project" });
  const { form } = c;

  // Surface a missing credential before the user hits Create: pop the Model
  // row open once we know their saved credentials cannot cover the provider.
  const credentialGap = c.credentialsLoaded && c.useSaved && !c.savedReady;
  const [modelOpen, setModelOpen] = useState(false);
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reveal the row when a credential gap appears
    if (credentialGap) setModelOpen(true);
  }, [credentialGap]);

  function handleOpenChange(nextOpen: boolean) {
    if (busy) return;
    setOpen(nextOpen);
    if (!nextOpen) {
      c.reset();
      setNewRepository(false);
      setRepositoryName("");
      setOrganization("");
      setPublicRepository(false);
      setCreatedRepository("");
      setFormError(null);
      setMoreOpen(false);
      setModelOpen(false);
      clearError();
    }
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setFormError(null);
    clearError();
    const validationError = c.validate();
    if (validationError) {
      setFormError(validationError);
      if (/credential|API key|OAuth|model/i.test(validationError)) setModelOpen(true);
      return;
    }
    let projectForm = form;
    if (newRepository) {
      if (!repositoryName.trim() || !c.credentials.githubToken) {
        setFormError("Enter a repository name and save a GitHub token in Settings first.");
        return;
      }
      setCreatingRepository(true);
      try {
        const repository = await client.createGitHubRemoteRepository({
          name: repositoryName.trim(),
          organization: organization.trim(),
          public: publicRepository,
        });
        projectForm = { ...form, repoUrl: repository.repoUrl, baseBranch: repository.defaultBranch };
        c.setForm(projectForm);
        setCreatedRepository(repository.repoUrl);
        setNewRepository(false);
      } catch (err) {
        setFormError(isTransientRpcError(err)
          ? "GitHub repository creation could not be confirmed. Check GitHub before retrying; if it exists, use its URL."
          : describeRpcError(err, "create the GitHub repository"));
        return;
      } finally {
        setCreatingRepository(false);
      }
    }
    try {
      const project = await createProject(createRequestFromForm(projectForm));
      handleOpenChange(false);
      navigate(`/projects/${project.namespace}/${project.name}`);
    } catch {
      // Error surfaced via the hook's `error` state; keep the dialog open.
    }
  }

  const name = form.name.trim();
  const namespace = c.credentials.namespace || "your namespace";

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      {trigger ? (
        <DialogTrigger render={trigger} />
      ) : (
        <DialogTrigger render={<Button size="sm" />}>
          <Plus />
          New project
        </DialogTrigger>
      )}
      <DialogContent
        className="flex w-full max-w-xl flex-col gap-0 overflow-hidden p-0 sm:max-w-xl max-h-[88vh]"
        showCloseButton
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 px-6 pt-5 pb-1">
            <div className="flex items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <FolderGit2 className="size-4" />
              </span>
              <DialogTitle className="text-base">New project</DialogTitle>
            </div>
            <DialogDescription>
              Connect an existing repository or create a new one on GitHub. Everything else can be changed later in
              project settings.
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 overflow-y-auto px-6 py-5">
            <fieldset disabled={busy} className="min-w-0 space-y-5">
              <div className="flex flex-wrap gap-2" role="group" aria-label="Repository setup">
                <Button type="button" size="sm" variant={!newRepository ? "secondary" : "ghost"}
                  aria-pressed={!newRepository} onClick={() => setNewRepository(false)}>
                  Existing repository / none
                </Button>
                <Button type="button" size="sm" variant={newRepository ? "secondary" : "ghost"}
                  aria-pressed={newRepository} onClick={() => setNewRepository(true)}>
                  Create GitHub repository
                </Button>
              </div>
              {newRepository ? (
                <div className="space-y-4">
                  <FlowField id="project-repository-name" label="Repository name" required>
                    <Input id="project-repository-name" value={repositoryName} required
                      pattern="[A-Za-z0-9_.-]+" maxLength={100} placeholder="payments-api"
                      onChange={(event) => {
                        setRepositoryName(event.target.value);
                        if (!c.nameTouched) c.setForm((prev) => ({ ...prev, name: deriveProjectName(event.target.value) }));
                      }} />
                  </FlowField>
                  <FlowField id="project-organization" label="GitHub organization"
                    hint="Optional — leave empty to create under your token's personal account.">
                    <Input id="project-organization" value={organization} placeholder="my-organization"
                      onChange={(event) => setOrganization(event.target.value)} />
                  </FlowField>
                  <FlowField id="project-repository-visibility" label="Visibility">
                    <select id="project-repository-visibility" className="h-9 w-full rounded-md border bg-background px-3 text-sm"
                      value={publicRepository ? "public" : "private"}
                      onChange={(event) => setPublicRepository(event.target.value === "public")}>
                      <option value="private">Private — only people with access</option>
                      <option value="public">Public — visible to everyone</option>
                    </select>
                  </FlowField>
                  <p className="text-xs text-muted-foreground">
                    Uses your saved GitHub token (Settings). It must have permission to create repositories
                    for the selected account. The repository starts with a README.
                  </p>
                  {c.credentialsLoaded && !c.credentials.githubToken && (
                    <p role="alert" className="text-sm text-destructive">Save a GitHub token in Settings before creating a repository.</p>
                  )}
                </div>
              ) : <RepositoryUrlField
                c={c}
                autoFocus
                hint="Optional — leave empty to start without a repository."
              />}
              {createdRepository && (
                <p role="status" className="text-xs text-muted-foreground">
                  Created {createdRepository}. It will remain on GitHub even if project setup is cancelled or fails.
                </p>
              )}

              <FlowField
                id="project-name"
                label="Name"
                required
                hint={
                  !c.nameTouched && name
                    ? "Suggested from the repository. Lowercase letters, digits and dashes."
                    : "Lowercase letters, digits and dashes."
                }
              >
                <Input
                  id="project-name"
                  value={form.name}
                  onChange={(event) => c.update("name", event.target.value)}
                  placeholder="payments-api"
                  required
                  autoComplete="off"
                  spellCheck={false}
                  className="font-mono text-[13px]"
                />
              </FlowField>

              <OptionRows className="pt-1">
                <OptionRow
                  icon={Sparkles}
                  title="Model"
                  summary={modelSummary(form, c.savedReady)}
                  open={modelOpen}
                  onOpenChange={setModelOpen}
                  tone={credentialGap ? "warning" : undefined}
                >
                  <ModelFields c={c} />
                </OptionRow>

                {moreOpen ? (
                  <>
                    <OptionRow icon={FolderGit2} title="Repository" summary={repoSummary(form)}>
                      <RepositoryDetailsFields c={c} />
                    </OptionRow>
                    <OptionRow icon={Bot} title="Agent" summary={agentSummary(form)}>
                      <AgentFields c={c} enabled={open} />
                    </OptionRow>
                    <OptionRow icon={Cpu} title="Runtime" summary={runtimeSummary(form)}>
                      <RuntimeFields c={c} />
                    </OptionRow>
                    <OptionRow icon={Blocks} title="Tools" summary={toolsSummary(form)}>
                      <ToolsFields c={c} />
                    </OptionRow>
                  </>
                ) : (
                  <button
                    type="button"
                    onClick={() => setMoreOpen(true)}
                    className="group flex w-full items-center gap-2.5 py-2.5 text-left text-[12.5px] text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 focus-visible:ring-inset"
                  >
                    <ChevronDown className="size-3.5 shrink-0 text-muted-foreground/70" />
                    <span className="flex-1">More options</span>
                    <span className="truncate text-[12px] text-muted-foreground/70">
                      repository · agent · runtime · tools
                    </span>
                  </button>
                )}
              </OptionRows>

              {(formError || error) && (
                <p role="alert" className={cn("text-sm", toneText.danger)}>
                  {formError ?? error}
                </p>
              )}
            </fieldset>
          </div>

          <div className="flex items-center justify-between gap-3 border-t px-6 py-4">
            <p className="min-w-0 truncate text-xs text-muted-foreground">
              Creates{" "}
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground">
                {namespace}/{name || "…"}
              </code>
            </p>
            <div className="flex shrink-0 items-center gap-2">
              <DialogClose render={<Button type="button" variant="ghost" size="sm" disabled={busy} />}>
                Cancel
              </DialogClose>
              <Button type="submit" size="sm" disabled={busy || !name || (newRepository && (!repositoryName.trim() || !c.credentials.githubToken))}>
                {busy ? <Loader2 className="size-4 animate-spin" /> : <Plus className="size-4" />}
                {busy ? "Creating…" : "Create project"}
              </Button>
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
