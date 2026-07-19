/* eslint-disable react-hooks/set-state-in-effect */
import { useState, useEffect, useMemo } from "react";
import { useNavigate } from "react-router-dom";
import { Cpu, Eye, FolderCog, FolderGit2, KeyRound, Loader2, Play, Sparkles } from "lucide-react";
import { client } from "@/lib/client";
import { useProjects } from "@/hooks/useWatchedList";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { Switch } from "@/components/ui/switch";
import {
  Chip,
  FlowField,
  OptionRow,
  OptionRows,
} from "@/components/create-flow/create-flow";
import { PROVIDERS, providerName } from "@/components/create-flow/providers";
import { RuntimeImagePicker } from "@/components/RuntimeImagePicker";
import { RepoUrlListInput } from "@/components/RepoUrlListInput";
import { cn } from "@/lib/utils";
import { REASONING_LEVELS } from "@/lib/reasoning";
import { toneText } from "@/lib/status";

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

/** "org/repo" from a git URL, for collapsed-row receipts. */
function shortRepo(url: string): string {
  const trimmed = url.trim().replace(/\/+$/, "").replace(/\.git$/, "");
  return trimmed.replace(/^https?:\/\/[^/]+\//, "") || trimmed;
}

function normalizedList(urls: string[]): string {
  return urls.map((url) => url.trim()).filter(Boolean).join(",");
}

export function CreateRunDialog({
  defaultSource,
  defaultNamespace,
}: {
  defaultSource?: string;
  defaultNamespace?: string;
} = {}) {
  const navigate = useNavigate();
  const { projects } = useProjects();
  const [open, setOpen] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [initialized, setInitialized] = useState(false);
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);

  const [overseer, setOverseer] = useState({
    enabled: false,
    modeRefName: "",
    modeRefVersion: "",
    modeRefChannel: "",
    model: "",
    authority: "advise",
    intervalMinutes: "10",
    maxInterventions: "5",
  });

  const [form, setForm] = useState({
    namespace: defaultNamespace || "",
    repoUrl: "",
    additionalRepoUrls: [] as string[],
    baseBranch: "",
    model: "",
    provider: "anthropic",
    reasoningLevel: "",
    image: "",
    userRequest: "",
    sourceName: defaultSource || "",
    claudeApiKeySecret: "",
    githubTokenSecret: "",
  });

  useEffect(() => {
    if (!defaultNamespace) return;
    setForm((prev) =>
      prev.namespace === defaultNamespace ? prev : { ...prev, namespace: defaultNamespace },
    );
  }, [defaultNamespace]);

  useEffect(() => {
    if (!open || defaultNamespace) return;
    let active = true;
    void client
      .listMyCredentials({})
      .then((credentials) => {
        if (!active || !credentials.namespace) return;
        setForm((prev) =>
          prev.namespace.trim() ? prev : { ...prev, namespace: credentials.namespace },
        );
      })
      .catch(() => {
        // The create-run RPC also defaults namespace server-side; leave the field editable.
      });
    return () => {
      active = false;
    };
  }, [open, defaultNamespace]);

  // Auto-fill defaults when items load and defaultSource is set
  useEffect(() => {
    if (!defaultSource || projects.length === 0 || initialized) return;
    const item =
      projects.find((p) => p.name === defaultSource && (!defaultNamespace || p.namespace === defaultNamespace)) ??
      projects.find((p) => p.name === defaultSource);
    if (!item) return;
    setForm((prev) => ({
      ...prev,
      namespace: item.namespace || defaultNamespace || prev.namespace,
      sourceName: defaultSource,
      repoUrl: item.repoUrl || prev.repoUrl,
      additionalRepoUrls: item.additionalRepoUrls.length ? [...item.additionalRepoUrls] : prev.additionalRepoUrls,
      baseBranch: item.baseBranch || prev.baseBranch,
      model: item.model || prev.model,
      provider: item.provider || prev.provider || "anthropic",
      image: item.image || prev.image,
      claudeApiKeySecret: item.claudeApiKeySecret || prev.claudeApiKeySecret,
      githubTokenSecret: item.githubTokenSecret || prev.githubTokenSecret,
    }));
    setInitialized(true);
  }, [defaultSource, defaultNamespace, projects, initialized]);

  function update<K extends keyof typeof form>(field: K, value: (typeof form)[K]) {
    setForm((prev) => ({ ...prev, [field]: value }));
  }

  function handleSourceChange(name: string) {
    update("sourceName", name);
    const item =
      projects.find((p) => p.name === name && (!form.namespace || p.namespace === form.namespace)) ??
      projects.find((p) => p.name === name);
    if (item) {
      setForm((prev) => ({
        ...prev,
        namespace: item.namespace || prev.namespace,
        sourceName: name,
        repoUrl: item.repoUrl || prev.repoUrl,
        additionalRepoUrls: item.additionalRepoUrls.length ? [...item.additionalRepoUrls] : prev.additionalRepoUrls,
        baseBranch: item.baseBranch || prev.baseBranch,
        model: item.model || prev.model,
        provider: item.provider || prev.provider || "anthropic",
        image: item.image || prev.image,
        claudeApiKeySecret: item.claudeApiKeySecret || prev.claudeApiKeySecret,
        githubTokenSecret: item.githubTokenSecret || prev.githubTokenSecret,
      }));
    }
  }

  useEffect(() => {
    const sourceName = form.sourceName.trim();
    const namespace = form.namespace.trim();
    if (!sourceName || !namespace) {
      setAvailableModels([]);
      setModelsError(null);
      return;
    }

    const controller = new AbortController();
    setModelsLoading(true);
    setModelsError(null);

    (async () => {
      try {
        const resp = await client.listAvailableModels(
          {
            namespace,
            source: { kind: "Project", name: sourceName },
            provider: form.provider,
          },
          { signal: controller.signal }
        );
        setAvailableModels(resp.models);
        // Keep the model aligned with the selected provider: if the current
        // value isn't offered by this provider, pick its first model so a
        // provider switch always submits a valid provider-prefixed model.
        setForm((prev) => {
          const current = prev.model.trim();
          if (resp.models.length === 0 || (current && resp.models.includes(current))) return prev;
          return { ...prev, model: resp.models[0] };
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setAvailableModels([]);
        setModelsError(err instanceof Error ? err.message : "Failed to load provider models");
      } finally {
        if (!controller.signal.aborted) {
          setModelsLoading(false);
        }
      }
    })();

    return () => controller.abort();
  }, [form.sourceName, form.namespace, form.provider]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    const intervalMinutes = Number(overseer.intervalMinutes);
    const maxInterventions = Number(overseer.maxInterventions);
    if (overseer.enabled) {
      if ((overseer.modeRefVersion.trim() || overseer.modeRefChannel.trim()) && !overseer.modeRefName.trim()) {
        setError("Overseer mode name is required when a version or channel is set.");
        return;
      }
      if (!Number.isInteger(intervalMinutes) || intervalMinutes < 1 || intervalMinutes > 1440) {
        setError("Overseer interval must be a whole number between 1 and 1440 minutes.");
        return;
      }
      if (!Number.isInteger(maxInterventions) || maxInterventions < 0 || maxInterventions > 100) {
        setError("Overseer max interventions must be a whole number between 0 and 100.");
        return;
      }
    }
    setSubmitting(true);
    try {
      const provider = form.provider.trim();
      const bareModel = form.model.trim();
      // CreateAgentRunRequest has no provider field; the provider is expressed
      // via the model prefix, so prefix bare models with the selection.
      const model = bareModel && provider && !bareModel.includes("/") ? `${provider}/${bareModel}` : bareModel;
      const result = await client.createAgentRun({
        namespace: form.namespace.trim(),
        repoUrl: form.repoUrl,
        additionalRepoUrls: form.additionalRepoUrls.map((url) => url.trim()).filter(Boolean),
        baseBranch: form.baseBranch,
        model,
        reasoningLevel: form.reasoningLevel,
        image: form.image,
        userRequest: form.userRequest,
        claudeApiKeySecret: form.claudeApiKeySecret,
        githubTokenSecret: form.githubTokenSecret,
        source: { kind: "Project", name: form.sourceName },
        overseer: overseer.enabled
          ? {
              modeRefName: overseer.modeRefName.trim(),
              modeRefVersion: overseer.modeRefVersion.trim(),
              modeRefChannel: overseer.modeRefChannel.trim(),
              model: overseer.model.trim(),
              authority: overseer.authority,
              intervalMinutes,
              maxInterventions,
            }
          : undefined,
      });
      setOpen(false);
      navigate(`/runs/${result.namespace}/${result.name}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to start run");
    } finally {
      setSubmitting(false);
    }
  }

  const selectedProject = useMemo(
    () =>
      projects.find(
        (p) => p.name === form.sourceName && (!form.namespace || p.namespace === form.namespace),
      ) ?? projects.find((p) => p.name === form.sourceName),
    [projects, form.sourceName, form.namespace],
  );

  /* Collapsed-row receipts — live values, with the modified dot lighting up
     only when a value diverges from the selected project's defaults. */

  const extraRepoCount = form.additionalRepoUrls.filter((url) => url.trim()).length;
  const repoSummaryText = form.repoUrl.trim()
    ? [
        form.baseBranch.trim()
          ? `${shortRepo(form.repoUrl)} @ ${form.baseBranch.trim()}`
          : shortRepo(form.repoUrl),
        ...(extraRepoCount ? [`+${extraRepoCount} more`] : []),
      ].join(" · ")
    : extraRepoCount
      ? `From project · +${extraRepoCount} more`
      : "From project";
  const repoModified = Boolean(
    selectedProject &&
      (form.repoUrl.trim() !== (selectedProject.repoUrl || "").trim() ||
        form.baseBranch.trim() !== (selectedProject.baseBranch || "").trim() ||
        normalizedList(form.additionalRepoUrls) !== normalizedList(selectedProject.additionalRepoUrls)),
  );

  const modelSummaryText = [
    providerName(form.provider),
    form.model.trim() || "project default",
    ...(form.reasoningLevel ? [form.reasoningLevel] : []),
  ].join(" · ");
  const modelModified = Boolean(
    selectedProject &&
      (form.model.trim() !== (selectedProject.model || "").trim() ||
        form.provider !== (selectedProject.provider || "anthropic") ||
        Boolean(form.reasoningLevel)),
  );

  const imageTail = form.image.trim() ? (form.image.trim().split("/").pop() ?? form.image.trim()) : "";
  const runtimeSummaryText = imageTail || "Project default";
  const runtimeModified = Boolean(
    selectedProject && form.image.trim() !== (selectedProject.image || "").trim(),
  );

  const credentialsSummaryText =
    form.claudeApiKeySecret.trim() || form.githubTokenSecret.trim()
      ? [
          ...(form.claudeApiKeySecret.trim() ? ["API key secret"] : []),
          ...(form.githubTokenSecret.trim() ? ["GitHub token secret"] : []),
        ].join(" · ")
      : "From project";
  const credentialsModified = Boolean(
    selectedProject &&
      (form.claudeApiKeySecret.trim() !== (selectedProject.claudeApiKeySecret || "").trim() ||
        form.githubTokenSecret.trim() !== (selectedProject.githubTokenSecret || "").trim()),
  );

  const placementNamespace = form.namespace.trim() || "your personal namespace";
  const placementModified = Boolean(
    selectedProject && form.namespace.trim() !== (selectedProject.namespace || "").trim(),
  );

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger render={<Button variant="default" size="sm" />}>
        <Play />
        New Run
      </DialogTrigger>
      <DialogContent
        className="flex w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl max-h-[92vh]"
        showCloseButton
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <div className="flex items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <Play className="size-4" />
              </span>
              <DialogTitle className="text-base">Start run</DialogTitle>
            </div>
            <DialogDescription>
              Launch an agent run from a project, overriding its defaults where needed.
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <FlowField id="create-run-project" label="Project" required>
              <Select value={form.sourceName} onValueChange={(value) => handleSourceChange(value ?? "")}>
                <SelectTrigger id="create-run-project" className="w-full">
                  <SelectValue placeholder="Choose project" />
                </SelectTrigger>
                <SelectContent>
                  {projects.map((p) => (
                    <SelectItem key={`${p.namespace}/${p.name}`} value={p.name}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </FlowField>

            <FlowField
              id="create-run-request"
              label="What should the agent work on?"
              hint="Optional — you can also steer the agent in chat once the run starts."
            >
              <Textarea
                id="create-run-request"
                className="min-h-24 resize-none"
                value={form.userRequest}
                onChange={(e) => update("userRequest", e.target.value)}
                placeholder="Describe the feature, fix, or implementation goal for this run…"
              />
            </FlowField>

            <OptionRows label="Overrides" className="pt-1">
              <OptionRow
                icon={Sparkles}
                title="Model"
                summary={modelSummaryText}
                modified={modelModified}
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
                  <FlowField id="create-run-model" label="Model">
                    <Input
                      id="create-run-model"
                      value={form.model}
                      onChange={(e) => update("model", e.target.value)}
                      placeholder={availableModels.length ? "Choose a model" : "Inherited from project"}
                      list={availableModels.length ? "create-run-model-options" : undefined}
                    />
                    {availableModels.length > 0 ? (
                      <datalist id="create-run-model-options">
                        {availableModels.map((model) => (
                          <option key={model} value={model} />
                        ))}
                      </datalist>
                    ) : null}
                    {form.sourceName ? (
                      <p className="text-[11px] text-muted-foreground" aria-live="polite">
                        {modelsLoading
                          ? `Loading ${providerName(form.provider)} models…`
                          : modelsError
                            ? `Could not load provider models: ${modelsError}`
                            : availableModels.length
                              ? `${availableModels.length} ${providerName(form.provider)} models available`
                              : "No provider models available"}
                      </p>
                    ) : null}
                  </FlowField>
                  <FlowField id="create-run-reasoning-level" label="Reasoning level">
                    <select
                      id="create-run-reasoning-level"
                      value={form.reasoningLevel}
                      onChange={(e) => update("reasoningLevel", e.target.value)}
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
              </OptionRow>

              <OptionRow
                icon={FolderGit2}
                title="Repository"
                summary={repoSummaryText}
                modified={repoModified}
              >
                <FlowField
                  id="create-run-repo"
                  label="Repository URL"
                  hint="Leave empty to inherit the project's repository."
                >
                  <Input
                    id="create-run-repo"
                    value={form.repoUrl}
                    onChange={(e) => update("repoUrl", e.target.value)}
                    placeholder="https://github.com/org/repo"
                  />
                </FlowField>
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="create-run-branch" label="Base branch">
                    <Input
                      id="create-run-branch"
                      value={form.baseBranch}
                      onChange={(e) => update("baseBranch", e.target.value)}
                      placeholder="Inherited from project"
                    />
                  </FlowField>
                </div>
                <FlowField
                  id="create-run-additional-repo-0"
                  label="Additional repositories"
                  hint="Extra repos cloned into the run alongside the primary repository."
                >
                  <RepoUrlListInput
                    idPrefix="create-run-additional-repo"
                    value={form.additionalRepoUrls}
                    onChange={(urls) => update("additionalRepoUrls", urls)}
                  />
                </FlowField>
              </OptionRow>

              <OptionRow
                icon={Cpu}
                title="Runtime"
                summary={runtimeSummaryText}
                modified={runtimeModified}
              >
                <FlowField id="create-run-image" label="Runtime image" hint="Pick your language.">
                  <RuntimeImagePicker
                    id="create-run-image"
                    value={form.image}
                    onChange={(image) => update("image", image)}
                  />
                </FlowField>
              </OptionRow>

              <OptionRow
                icon={KeyRound}
                title="Credentials"
                summary={credentialsSummaryText}
                modified={credentialsModified}
              >
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField
                    id="create-run-apikey"
                    label="API key secret"
                    hint="Secret name."
                  >
                    <Input
                      id="create-run-apikey"
                      value={form.claudeApiKeySecret}
                      onChange={(e) => update("claudeApiKeySecret", e.target.value)}
                      placeholder="Inherited from project"
                    />
                  </FlowField>
                  <FlowField
                    id="create-run-ghtoken"
                    label="GitHub token secret"
                    hint="Secret name."
                  >
                    <Input
                      id="create-run-ghtoken"
                      value={form.githubTokenSecret}
                      onChange={(e) => update("githubTokenSecret", e.target.value)}
                      placeholder="Inherited from project"
                    />
                  </FlowField>
                </div>
              </OptionRow>

              <OptionRow
                icon={Eye}
                title="Overseer"
                summary={overseer.enabled ? `${overseer.authority} · every ${overseer.intervalMinutes} min` : "Off"}
                modified={overseer.enabled}
              >
                <div className="flex items-center justify-between gap-3 rounded-md border p-3">
                  <div>
                    <label htmlFor="create-run-overseer-enabled" className="text-sm font-medium">
                      Enable overseer
                    </label>
                    <p className="text-xs text-muted-foreground">Monitor and guide this run at checkpoints.</p>
                  </div>
                  <Switch
                    id="create-run-overseer-enabled"
                    checked={overseer.enabled}
                    onCheckedChange={(enabled) => setOverseer((prev) => ({ ...prev, enabled }))}
                  />
                </div>
                {overseer.enabled && (
                  <div className="grid gap-4 sm:grid-cols-2">
                    <FlowField id="create-run-overseer-mode" label="Mode name" hint="Blank uses the default overseer mode.">
                      <Input id="create-run-overseer-mode" value={overseer.modeRefName} onChange={(e) => setOverseer((prev) => ({ ...prev, modeRefName: e.target.value }))} />
                    </FlowField>
                    <FlowField id="create-run-overseer-model" label="Model" hint="Blank uses the platform default or primary model.">
                      <Input id="create-run-overseer-model" value={overseer.model} onChange={(e) => setOverseer((prev) => ({ ...prev, model: e.target.value }))} />
                    </FlowField>
                    <FlowField id="create-run-overseer-version" label="Mode version">
                      <Input id="create-run-overseer-version" value={overseer.modeRefVersion} onChange={(e) => setOverseer((prev) => ({ ...prev, modeRefVersion: e.target.value }))} />
                    </FlowField>
                    <FlowField id="create-run-overseer-channel" label="Mode channel">
                      <Input id="create-run-overseer-channel" value={overseer.modeRefChannel} onChange={(e) => setOverseer((prev) => ({ ...prev, modeRefChannel: e.target.value }))} />
                    </FlowField>
                    <FlowField id="create-run-overseer-authority" label="Authority">
                      <Select value={overseer.authority} onValueChange={(authority) => setOverseer((prev) => ({ ...prev, authority: authority ?? "advise" }))}>
                        <SelectTrigger id="create-run-overseer-authority"><SelectValue /></SelectTrigger>
                        <SelectContent>
                          <SelectItem value="observe">Observe</SelectItem>
                          <SelectItem value="advise">Advise</SelectItem>
                          <SelectItem value="enforce">Enforce</SelectItem>
                        </SelectContent>
                      </Select>
                    </FlowField>
                    <FlowField id="create-run-overseer-interval" label="Interval (minutes)">
                      <Input id="create-run-overseer-interval" type="number" min={1} max={1440} step={1} value={overseer.intervalMinutes} onChange={(e) => setOverseer((prev) => ({ ...prev, intervalMinutes: e.target.value }))} />
                    </FlowField>
                    <FlowField id="create-run-overseer-max" label="Max interventions">
                      <Input id="create-run-overseer-max" type="number" min={0} max={100} step={1} value={overseer.maxInterventions} onChange={(e) => setOverseer((prev) => ({ ...prev, maxInterventions: e.target.value }))} />
                    </FlowField>
                  </div>
                )}
              </OptionRow>

              <OptionRow
                icon={FolderCog}
                title="Placement"
                summary={placementNamespace}
                modified={placementModified}
              >
                <FlowField
                  id="create-run-namespace"
                  label="Namespace"
                  hint="Leave empty to use your personal namespace."
                >
                  <Input
                    id="create-run-namespace"
                    value={form.namespace}
                    onChange={(e) => update("namespace", e.target.value)}
                    placeholder="Your personal namespace"
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
              Runs in{" "}
              <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground">
                {placementNamespace}
              </code>
            </p>
            <div className="flex shrink-0 items-center gap-2">
              <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
                Cancel
              </DialogClose>
              <Button type="submit" size="sm" disabled={submitting || !form.sourceName}>
                {submitting ? <Loader2 className="size-4 animate-spin" /> : <Play className="size-4" />}
                {submitting ? "Starting…" : "Start run"}
              </Button>
            </div>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
