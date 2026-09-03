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
import { toneText } from "@/lib/status";

import {
  agentSummary,
  createRequestFromForm,
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

/**
 * Create project — one decision.
 *
 * Paste a repository URL and press Create. The name is derived from the URL,
 * the model and credentials come from the user's saved defaults, and every
 * other setting has a working default the user can revisit in project
 * settings. The Model receipt stays visible because it is the one thing
 * that can block the first run (no saved credential); everything else hides
 * behind "More options".
 */
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
    setOpen(nextOpen);
    if (!nextOpen) {
      c.reset();
      setFormError(null);
      setMoreOpen(false);
      setModelOpen(false);
      clearError();
    }
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFormError(null);
    const validationError = c.validate();
    if (validationError) {
      setFormError(validationError);
      if (/credential|API key|OAuth|model/i.test(validationError)) setModelOpen(true);
      return;
    }
    try {
      const project = await createProject(createRequestFromForm(form));
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
              Point it at a repository and start running. Everything else can be changed later in
              project settings.
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <RepositoryUrlField
              c={c}
              autoFocus
              hint="Optional — leave empty to start without a repository."
            />

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
          </div>

          <div className="flex items-center justify-between gap-3 border-t px-6 py-4">
            <p className="min-w-0 truncate text-xs text-muted-foreground">
              Creates{" "}
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground">
                {namespace}/{name || "…"}
              </code>
            </p>
            <div className="flex shrink-0 items-center gap-2">
              <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
                Cancel
              </DialogClose>
              <Button type="submit" size="sm" disabled={submitting || !name}>
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
