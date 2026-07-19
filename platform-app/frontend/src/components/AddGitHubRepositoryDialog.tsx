import { create } from "@bufbuild/protobuf";
import { useEffect, useMemo, useState } from "react";
import { FolderCog, GitBranch as GithubIcon, Plus } from "lucide-react";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  FlowField,
  OptionRow,
  OptionRows,
  Segmented,
} from "@/components/create-flow/create-flow";
import { RunDefaultsRows } from "@/components/run-defaults/RunDefaultsRows";
import { TriggerPolicyRows } from "@/components/run-defaults/TriggerPolicyRows";
import { emptyDefaults } from "@/components/run-defaults/helpers";
import {
  normalizeTriggerDefaults,
  resolvedTriggerPolicies,
} from "@/components/TriggerDefaultsDialog";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import {
  CreateGitHubRepositoryFromInstallationRequestSchema,
  CreateGitHubRepositoryFromTokenRequestSchema,
  ListGitHubAppInstallationRepositoriesRequestSchema,
  type AgentRunDefaults,
  type GitHubAppConfig,
  type GitHubAppInstallation,
  type GitHubAppInstallationRepository,
  type MyCredentials,
  type TriggerPolicies,
} from "@/rpc/platform/service_pb";

type Method = "app" | "token";

type FormState = {
  namespace: string;
  name: string;
  repoInput: string;
  githubToken: string;
  defaultBranch: string;
};

const initialForm: FormState = {
  namespace: "",
  name: "",
  repoInput: "",
  githubToken: "",
  defaultBranch: "",
};

function slugName(owner: string, repo: string): string {
  return `${owner}-${repo}`
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function suggestedName(repo?: GitHubAppInstallationRepository): string {
  if (!repo) return "";
  return slugName(repo.owner, repo.name);
}

// parseRepoInput accepts "owner/repo" or a full GitHub URL
// (https://github.com/owner/repo, optionally with .git or trailing slash).
function parseRepoInput(input: string): { owner: string; repo: string } | null {
  const value = input.trim();
  if (!value) return null;
  const urlMatch = value.match(/^(?:https?:\/\/)?(?:www\.)?github\.com\/([^/\s]+)\/([^/\s]+?)(?:\.git)?\/?$/i);
  if (urlMatch) {
    return { owner: urlMatch[1], repo: urlMatch[2] };
  }
  const plainMatch = value.match(/^([^/\s]+)\/([^/\s]+?)(?:\.git)?$/);
  if (plainMatch) {
    return { owner: plainMatch[1], repo: plainMatch[2] };
  }
  return null;
}

export function AddGitHubRepositoryDialog({ onCreated }: { onCreated?: () => void }) {
  const [open, setOpen] = useState(false);
  const [method, setMethod] = useState<Method>("token");
  const [config, setConfig] = useState<GitHubAppConfig | null>(null);
  const [credentials, setCredentials] = useState<MyCredentials | null>(null);
  const [installations, setInstallations] = useState<GitHubAppInstallation[]>([]);
  const [repositories, setRepositories] = useState<GitHubAppInstallationRepository[]>([]);
  const [installationId, setInstallationId] = useState("");
  const [repoFullName, setRepoFullName] = useState("");
  const [form, setForm] = useState<FormState>(initialForm);
  const [defaults, setDefaults] = useState<AgentRunDefaults>(() => emptyDefaults());
  const [policies, setPolicies] = useState<TriggerPolicies>(() => resolvedTriggerPolicies());
  const [useSavedCredentials, setUseSavedCredentials] = useState(true);
  const [loading, setLoading] = useState(true);
  const [repoLoading, setRepoLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    async function loadConfig() {
      try {
        const next = await client.getGitHubAppConfig({});
        if (!cancelled) {
          setConfig(next);
          setMethod(next.configured ? "app" : "token");
        }
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : "Failed to load GitHub App config");
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    void loadConfig();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    void client
      .listMyCredentials({})
      .then((next) => {
        if (!cancelled) setCredentials(next);
      })
      .catch(() => {
        if (!cancelled) setCredentials(null);
      });
    return () => {
      cancelled = true;
    };
  }, [open]);

  useEffect(() => {
    if (!open || !config?.configured) return;
    let cancelled = false;
    async function loadInstallations() {
      setLoading(true);
      setError(null);
      try {
        const resp = await client.listGitHubAppInstallations({});
        if (!cancelled) setInstallations(resp.installations);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : "Failed to load installations");
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    void loadInstallations();
    return () => {
      cancelled = true;
    };
  }, [open, config?.configured]);

  const selectedRepo = useMemo(
    () => repositories.find((repo) => repo.fullName === repoFullName),
    [repositories, repoFullName]
  );
  const parsedTokenRepo = useMemo(() => parseRepoInput(form.repoInput), [form.repoInput]);

  function update<K extends keyof FormState>(field: K, value: FormState[K]) {
    setForm((prev) => ({ ...prev, [field]: value }));
  }

  async function handleInstallationChange(value: string | null) {
    const next = value ?? "";
    setInstallationId(next);
    setRepoFullName("");
    setRepositories([]);
    if (!next) return;
    setRepoLoading(true);
    setError(null);
    try {
      const resp = await client.listGitHubAppInstallationRepositories(
        create(ListGitHubAppInstallationRepositoriesRequestSchema, { installationId: BigInt(next) })
      );
      setRepositories(resp.repositories);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load repositories");
    } finally {
      setRepoLoading(false);
    }
  }

  function handleRepoChange(value: string | null) {
    const next = value ?? "";
    setRepoFullName(next);
    const repo = repositories.find((item) => item.fullName === next);
    if (repo) {
      setForm((prev) => ({
        ...prev,
        name: prev.name || suggestedName(repo),
      }));
    }
  }

  function reset() {
    setForm(initialForm);
    setDefaults(emptyDefaults());
    setPolicies(resolvedTriggerPolicies());
    setUseSavedCredentials(true);
    setInstallationId("");
    setRepoFullName("");
    setRepositories([]);
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (method === "app" && (!selectedRepo || !installationId)) return;
    let tokenRepo: { owner: string; repo: string } | null = null;
    if (method === "token") {
      tokenRepo = parsedTokenRepo;
      if (!tokenRepo) {
        setError("Enter the repository as owner/repo or a GitHub URL.");
        return;
      }
      if (!form.githubToken.trim() && !credentials?.githubTokenPresent) {
        setError("No saved GitHub token — paste one or save it in Settings.");
        return;
      }
    }
    if (!defaults.model.trim()) {
      setError("Choose a model — runs created by this trigger require an explicit model.");
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      const requestDefaults = normalizeTriggerDefaults(defaults, useSavedCredentials);
      if (method === "app" && selectedRepo) {
        await client.createGitHubRepositoryFromInstallation(
          create(CreateGitHubRepositoryFromInstallationRequestSchema, {
            installationId: BigInt(installationId),
            owner: selectedRepo.owner,
            repo: selectedRepo.name,
            namespace: form.namespace.trim(),
            name: form.name.trim(),
            defaultBranch: selectedRepo.defaultBranch || "main",
            defaults: requestDefaults,
            policies,
            useSavedCredentials,
          })
        );
      } else if (method === "token" && tokenRepo) {
        await client.createGitHubRepositoryFromToken(
          create(CreateGitHubRepositoryFromTokenRequestSchema, {
            owner: tokenRepo.owner,
            repo: tokenRepo.repo,
            namespace: form.namespace.trim(),
            name: form.name.trim(),
            defaultBranch: form.defaultBranch.trim(),
            defaults: requestDefaults,
            policies,
            useSavedCredentials,
            githubToken: form.githubToken.trim(),
          })
        );
      }
      setOpen(false);
      reset();
      onCreated?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to add repository");
    } finally {
      setSubmitting(false);
    }
  }

  const derivedName =
    method === "app"
      ? suggestedName(selectedRepo)
      : parsedTokenRepo
        ? slugName(parsedTokenRepo.owner, parsedTokenRepo.repo)
        : "";
  const placementName = form.name || derivedName || "…";
  const personalNamespace = credentials?.namespace || "";
  const placementNamespace = form.namespace.trim() || personalNamespace || "personal namespace";
  const submitDisabled =
    submitting ||
    (method === "app" && (!selectedRepo || selectedRepo.alreadyOnboarded));

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button size="sm" />}>
        <Plus />
        Add repository
      </DialogTrigger>
      <DialogContent
        className="flex w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl max-h-[92vh]"
        showCloseButton
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <div className="flex items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <GithubIcon className="size-4" />
              </span>
              <DialogTitle className="text-base">Add repository</DialogTitle>
            </div>
            <DialogDescription>
              Connect a GitHub repository and turn it into an agent trigger.
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <FlowField label="Method">
              <Segmented
                aria-label="Method"
                value={method}
                onChange={setMethod}
                options={[
                  { value: "app", label: "GitHub App" },
                  { value: "token", label: "Personal token" },
                ]}
              />
            </FlowField>

            {method === "app" ? (
              <>
                {config && !config.configured ? (
                  <p className="text-[12px] text-muted-foreground">
                    The GitHub App is not configured.{" "}
                    {config.installUrl ? (
                      <a
                        href={config.installUrl}
                        target="_blank"
                        rel="noreferrer"
                        className="underline underline-offset-4"
                      >
                        Install GitHub App
                      </a>
                    ) : (
                      "Use a personal token instead."
                    )}
                  </p>
                ) : null}
                <FlowField id="github-app-installation" label="Installation" required>
                  <Select
                    value={installationId}
                    onValueChange={handleInstallationChange}
                    disabled={loading || submitting}
                  >
                    <SelectTrigger id="github-app-installation" className="w-full">
                      <SelectValue placeholder={loading ? "Loading…" : "Choose installation"} />
                    </SelectTrigger>
                    <SelectContent>
                      {installations.map((installation) => (
                        <SelectItem key={installation.id.toString()} value={installation.id.toString()}>
                          {installation.accountLogin} ({installation.accountType || "account"})
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </FlowField>

                <FlowField id="github-app-repo" label="Repository" required>
                  <Select
                    value={repoFullName}
                    onValueChange={handleRepoChange}
                    disabled={!installationId || repoLoading || submitting}
                  >
                    <SelectTrigger id="github-app-repo" className="w-full">
                      <SelectValue placeholder={repoLoading ? "Loading…" : "Choose repository"} />
                    </SelectTrigger>
                    <SelectContent>
                      {repositories.map((repo) => (
                        <SelectItem key={repo.fullName} value={repo.fullName} disabled={repo.alreadyOnboarded}>
                          {repo.fullName} {repo.private ? "· private" : ""}{" "}
                          {repo.alreadyOnboarded ? "· already added" : ""}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {selectedRepo ? (
                    <p
                      className={cn(
                        "text-[12px] text-muted-foreground",
                        selectedRepo.alreadyOnboarded && toneText.warning
                      )}
                    >
                      {[
                        `${selectedRepo.defaultBranch || "main"} default branch`,
                        selectedRepo.private ? "private" : null,
                        selectedRepo.alreadyOnboarded ? "already added" : null,
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </p>
                  ) : null}
                </FlowField>
              </>
            ) : (
              <>
                <FlowField id="github-token-repo" label="Repository" required>
                  <Input
                    id="github-token-repo"
                    value={form.repoInput}
                    onChange={(event) => update("repoInput", event.target.value)}
                    placeholder="owner/repo or https://github.com/owner/repo"
                    required
                  />
                </FlowField>
                <FlowField
                  id="github-token-token"
                  label="GitHub token"
                  hint="Leave empty to use your saved GitHub token (from Settings)."
                >
                  <Input
                    id="github-token-token"
                    type="password"
                    autoComplete="off"
                    value={form.githubToken}
                    onChange={(event) => update("githubToken", event.target.value)}
                    placeholder="ghp_… / github_pat_…"
                  />
                </FlowField>
                <FlowField
                  id="github-token-branch"
                  label="Default branch"
                  hint="Optional — detected from GitHub if empty."
                >
                  <Input
                    id="github-token-branch"
                    value={form.defaultBranch}
                    onChange={(event) => update("defaultBranch", event.target.value)}
                    placeholder="main"
                  />
                </FlowField>
              </>
            )}

            <OptionRows label="Options" className="pt-1">
              <RunDefaultsRows
                idPrefix="github-defaults"
                hideRepository
                value={defaults}
                onChange={setDefaults}
                useSavedCredentials={useSavedCredentials}
                onUseSavedCredentialsChange={setUseSavedCredentials}
              />
              <TriggerPolicyRows
                idPrefix="github-policies"
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

              <OptionRow
                icon={FolderCog}
                title="Placement"
                summary={`${placementNamespace}/${placementName}`}
                modified={Boolean(form.name) || Boolean(form.namespace.trim())}
              >
                <FlowField
                  id="github-app-namespace"
                  label="Namespace"
                  hint="Leave empty to use your personal namespace."
                >
                  <Input
                    id="github-app-namespace"
                    value={form.namespace}
                    onChange={(event) => update("namespace", event.target.value)}
                    placeholder={personalNamespace || "your personal namespace"}
                  />
                </FlowField>
                <FlowField
                  id="github-app-name"
                  label="Resource name"
                  hint="Optional — derived from the repository if empty."
                >
                  <Input
                    id="github-app-name"
                    value={form.name}
                    onChange={(event) => update("name", event.target.value)}
                    placeholder={derivedName}
                  />
                </FlowField>
              </OptionRow>
            </OptionRows>

            {error && (
              <p role="alert" className={cn("text-sm", toneText.danger)}>
                {error}
              </p>
            )}
          </div>

          <div className="flex items-center justify-between gap-3 border-t px-6 py-4">
            <p className="min-w-0 truncate text-xs text-muted-foreground">
              Creates{" "}
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground">
                {placementNamespace}/{placementName}
              </code>
            </p>
            <div className="flex shrink-0 items-center gap-2">
              <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
                Cancel
              </DialogClose>
              <Button type="submit" size="sm" disabled={submitDisabled}>
                {submitting ? "Adding…" : "Add repository"}
              </Button>
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
