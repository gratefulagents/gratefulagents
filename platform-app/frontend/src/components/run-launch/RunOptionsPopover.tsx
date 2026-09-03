import { useEffect, useState } from "react";
import { Cpu, Eye, FolderGit2, Settings2, Sparkles } from "lucide-react";

import {
  Chip,
  FlowField,
  FlowSwitchRow,
  OptionRow,
  OptionRows,
} from "@/components/create-flow/create-flow";
import { PROVIDERS, providerName } from "@/components/create-flow/providers";
import { RepoUrlListInput } from "@/components/RepoUrlListInput";
import { RuntimeImagePicker } from "@/components/RuntimeImagePicker";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { client } from "@/lib/client";
import { REASONING_LEVELS } from "@/lib/reasoning";

import {
  activeGroups,
  advancedReceipt,
  effectiveProvider,
  emptyRunOverrides,
  inheritedModel,
  modelReceipt,
  overseerReceipt,
  repositoryReceipt,
  runtimeReceipt,
  type ProjectDefaults,
  type RunOverrides,
} from "./runOverrides";

const selectClassName =
  "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring";

/**
 * Per-run overrides, one popover away from the composer. Rows are collapsed
 * receipts of what the run will actually use — the project's value until the
 * user changes it — so a glance answers "what will this run do?" without
 * opening a dialog.
 */
export function RunOptionsPopover({
  overrides,
  onChange,
  project,
  open,
  onOpenChange,
  trigger,
}: {
  overrides: RunOverrides;
  onChange: (next: RunOverrides) => void;
  /** Project the run starts from; receipts show its defaults. */
  project?: (ProjectDefaults & { name: string }) | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  trigger: React.ReactElement<Record<string, unknown>>;
}) {
  const provider = effectiveProvider(overrides, project ?? undefined);
  const [models, setModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);

  const namespace = project?.namespace ?? "";
  const projectName = project?.name ?? "";
  useEffect(() => {
    if (!open || !projectName || !namespace) return;
    const controller = new AbortController();
    // eslint-disable-next-line react-hooks/set-state-in-effect -- reset the catalog for the new provider before fetching
    setModels([]);
    setModelsLoading(true);
    setModelsError(null);
    void client
      .listAvailableModels(
        { namespace, source: { kind: "Project", name: projectName }, provider },
        { signal: controller.signal },
      )
      .then((resp) => {
        if (controller.signal.aborted) return;
        setModels(resp.models);
      })
      .catch((err) => {
        if (controller.signal.aborted) return;
        setModelsError(err instanceof Error ? err.message : "Failed to load provider models");
      })
      .finally(() => {
        if (!controller.signal.aborted) setModelsLoading(false);
      });
    return () => controller.abort();
  }, [open, namespace, projectName, provider]);

  function set<K extends keyof RunOverrides>(field: K, value: RunOverrides[K]) {
    onChange({ ...overrides, [field]: value });
  }
  function setOverseer<K extends keyof RunOverrides["overseer"]>(
    field: K,
    value: RunOverrides["overseer"][K],
  ) {
    onChange({ ...overrides, overseer: { ...overrides.overseer, [field]: value } });
  }

  const active = activeGroups(overrides, project ?? undefined);
  const p = project ?? undefined;

  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger render={trigger} />
      <PopoverContent
        align="start"
        side="bottom"
        sideOffset={8}
        // Opaque: the default popover surface is translucent, which is fine
        // for a short menu but turns a form into a palimpsest of the page.
        className="w-[min(92vw,520px)] gap-0 bg-card p-0"
        aria-label="Run options"
      >
        <div className="flex items-center justify-between gap-3 px-4 pt-3 pb-2">
          <div className="min-w-0">
            <p className="text-[13px] font-medium">Run options</p>
            <p className="truncate text-[11.5px] text-muted-foreground">
              {p ? (
                <>
                  Overrides for this run only — defaults come from{" "}
                  <span className="font-medium text-foreground">{projectName}</span>.
                </>
              ) : (
                "Overrides for this run only."
              )}
            </p>
          </div>
          {active.length ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="shrink-0 text-muted-foreground"
              onClick={() => onChange(emptyRunOverrides())}
            >
              Reset all
            </Button>
          ) : null}
        </div>

        <div className="max-h-[min(60vh,520px)] overflow-y-auto px-4 pb-3">
          <OptionRows>
            <OptionRow
              icon={Sparkles}
              title="Model"
              summary={modelReceipt(overrides, p, provider ? providerName(provider) : undefined)}
              modified={active.includes("model")}
            >
              <div className="flex flex-wrap gap-1.5" role="group" aria-label="Provider">
                {PROVIDERS.map((item) => (
                  <Chip
                    key={item.id}
                    selected={provider === item.id}
                    onSelect={() => onChange({ ...overrides, provider: item.id, model: "" })}
                  >
                    {item.name}
                  </Chip>
                ))}
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <FlowField id="run-options-model" label="Model">
                  <Input
                    id="run-options-model"
                    value={overrides.model}
                    onChange={(e) => set("model", e.target.value)}
                    placeholder={
                      inheritedModel(overrides, p)
                        ? `${inheritedModel(overrides, p)} (project default)`
                        : models.length
                          ? "Choose a model"
                          : "Provider default"
                    }
                    list={models.length ? "run-options-model-list" : undefined}
                  />
                  {models.length ? (
                    <datalist id="run-options-model-list">
                      {models.map((m) => (
                        <option key={m} value={m} />
                      ))}
                    </datalist>
                  ) : null}
                  {projectName ? (
                    <p className="text-[11px] text-muted-foreground" aria-live="polite">
                      {modelsLoading
                        ? `Loading ${providerName(provider)} models…`
                        : modelsError
                          ? `Could not load models: ${modelsError}`
                          : models.length
                            ? `${models.length} ${providerName(provider)} models available`
                            : "No provider models available"}
                    </p>
                  ) : null}
                </FlowField>
                <FlowField id="run-options-reasoning" label="Reasoning level">
                  <select
                    id="run-options-reasoning"
                    value={overrides.reasoningLevel}
                    onChange={(e) => set("reasoningLevel", e.target.value)}
                    className={selectClassName}
                  >
                    {REASONING_LEVELS.map((level) => (
                      <option key={level || "default"} value={level}>
                        {level || "project default"}
                      </option>
                    ))}
                  </select>
                </FlowField>
              </div>
            </OptionRow>

            <OptionRow
              icon={FolderGit2}
              title="Repository"
              summary={repositoryReceipt(overrides, p)}
              modified={active.includes("repository")}
            >
              <FlowField
                id="run-options-repo"
                label="Repository URL"
                hint="Leave as-is to inherit the project's repository."
              >
                <Input
                  id="run-options-repo"
                  value={overrides.repoUrl ?? p?.repoUrl ?? ""}
                  onChange={(e) => set("repoUrl", e.target.value)}
                  placeholder="https://github.com/org/repo"
                />
              </FlowField>
              <div className="grid gap-4 sm:grid-cols-2">
                <FlowField id="run-options-branch" label="Base branch">
                  <Input
                    id="run-options-branch"
                    value={overrides.baseBranch ?? p?.baseBranch ?? ""}
                    onChange={(e) => set("baseBranch", e.target.value)}
                    placeholder="Inherited from project"
                  />
                </FlowField>
              </div>
              <FlowField
                id="run-options-additional-repo-0"
                label="Additional repositories"
                hint="Extra repos cloned alongside the primary one."
              >
                <RepoUrlListInput
                  idPrefix="run-options-additional-repo"
                  value={overrides.additionalRepoUrls ?? p?.additionalRepoUrls ?? []}
                  onChange={(urls) => set("additionalRepoUrls", urls)}
                />
              </FlowField>
            </OptionRow>

            <OptionRow
              icon={Cpu}
              title="Runtime"
              summary={runtimeReceipt(overrides, p)}
              modified={active.includes("runtime")}
            >
              <FlowField id="run-options-image" label="Runtime image" hint="Toolchains available to the agent.">
                <RuntimeImagePicker
                  id="run-options-image"
                  value={overrides.image}
                  onChange={(image) => set("image", image)}
                />
              </FlowField>
            </OptionRow>

            <OptionRow
              icon={Eye}
              title="Overseer"
              summary={overseerReceipt(overrides)}
              modified={active.includes("overseer")}
            >
              <FlowSwitchRow
                id="run-options-overseer-enabled"
                label="Enable overseer"
                hint="A supervisor that reviews this run at checkpoints and can steer it."
                control={
                  <Switch
                    id="run-options-overseer-enabled"
                    checked={overrides.overseer.enabled}
                    onCheckedChange={(enabled) => setOverseer("enabled", enabled)}
                  />
                }
              />
              {overrides.overseer.enabled ? (
                <div className="grid gap-4 sm:grid-cols-2">
                  <FlowField id="run-options-overseer-mode" label="Mode name" hint="Blank uses the default overseer mode.">
                    <Input
                      id="run-options-overseer-mode"
                      value={overrides.overseer.modeRefName}
                      onChange={(e) => setOverseer("modeRefName", e.target.value)}
                    />
                  </FlowField>
                  <FlowField id="run-options-overseer-model" label="Overseer model" hint="Blank uses the platform default or primary model.">
                    <Input
                      id="run-options-overseer-model"
                      value={overrides.overseer.model}
                      onChange={(e) => setOverseer("model", e.target.value)}
                    />
                  </FlowField>
                  <FlowField id="run-options-overseer-version" label="Mode version">
                    <Input
                      id="run-options-overseer-version"
                      value={overrides.overseer.modeRefVersion}
                      onChange={(e) => setOverseer("modeRefVersion", e.target.value)}
                    />
                  </FlowField>
                  <FlowField id="run-options-overseer-channel" label="Mode channel">
                    <Input
                      id="run-options-overseer-channel"
                      value={overrides.overseer.modeRefChannel}
                      onChange={(e) => setOverseer("modeRefChannel", e.target.value)}
                    />
                  </FlowField>
                  <FlowField id="run-options-overseer-authority" label="Authority">
                    <Select
                      value={overrides.overseer.authority}
                      onValueChange={(authority) => setOverseer("authority", authority ?? "advise")}
                    >
                      <SelectTrigger id="run-options-overseer-authority">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value="observe">Observe</SelectItem>
                        <SelectItem value="advise">Advise</SelectItem>
                        <SelectItem value="enforce">Enforce</SelectItem>
                      </SelectContent>
                    </Select>
                  </FlowField>
                  <FlowField id="run-options-overseer-interval" label="Interval (minutes)">
                    <Input
                      id="run-options-overseer-interval"
                      type="number"
                      min={1}
                      max={1440}
                      step={1}
                      value={overrides.overseer.intervalMinutes}
                      onChange={(e) => setOverseer("intervalMinutes", e.target.value)}
                    />
                  </FlowField>
                  <FlowField id="run-options-overseer-max" label="Max interventions">
                    <Input
                      id="run-options-overseer-max"
                      type="number"
                      min={0}
                      max={100}
                      step={1}
                      value={overrides.overseer.maxInterventions}
                      onChange={(e) => setOverseer("maxInterventions", e.target.value)}
                    />
                  </FlowField>
                </div>
              ) : null}
            </OptionRow>

            <OptionRow
              icon={Settings2}
              title="Advanced"
              summary={advancedReceipt(overrides, p)}
              modified={active.includes("advanced")}
            >
              <div className="grid gap-4 sm:grid-cols-2">
                <FlowField id="run-options-apikey" label="API key Secret" hint="Secret name in the run namespace.">
                  <Input
                    id="run-options-apikey"
                    value={overrides.claudeApiKeySecret}
                    onChange={(e) => set("claudeApiKeySecret", e.target.value)}
                    placeholder="Inherited from project"
                  />
                </FlowField>
                <FlowField id="run-options-ghtoken" label="GitHub token Secret" hint="Secret name in the run namespace.">
                  <Input
                    id="run-options-ghtoken"
                    value={overrides.githubTokenSecret}
                    onChange={(e) => set("githubTokenSecret", e.target.value)}
                    placeholder="Inherited from project"
                  />
                </FlowField>
                <FlowField id="run-options-namespace" label="Namespace" hint="Where the run's pod is created.">
                  <Input
                    id="run-options-namespace"
                    value={overrides.namespace}
                    onChange={(e) => set("namespace", e.target.value)}
                    placeholder={p?.namespace || "Project namespace"}
                  />
                </FlowField>
              </div>
            </OptionRow>
          </OptionRows>
        </div>
      </PopoverContent>
    </Popover>
  );
}
