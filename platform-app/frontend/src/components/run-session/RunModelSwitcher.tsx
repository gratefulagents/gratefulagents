import { useEffect, useState, type FormEvent } from "react";
import { ChevronsUpDown } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { client } from "@/lib/client";
import { REASONING_LEVELS } from "@/lib/reasoning";
import { cn } from "@/lib/utils";
import type { AgentRun } from "@/rpc/platform/service_pb";

export const runtimeProviderOptions = ["anthropic", "openai", "openrouter", "gemini", "groq", "xai", "copilot"];
const runtimeInputClass =
  "h-8 w-full rounded-md border bg-background px-2 text-xs focus:outline-none focus:ring-2 focus:ring-ring";

export type RuntimeConfigUpdate = {
  provider: string;
  model: string;
  reasoningLevel: string;
};

// Immutable copy of the run's credential refs taken when the editor opens,
// so model fetches are not retriggered by streaming run updates.
type CredsSnapshot = {
  namespace: string;
  provider: string;
  authMode: string;
  claudeApiKeySecret: string;
  openaiOauthSecret: string;
  providerKeys: Array<{ provider: string; secretName: string; secretKey: string }>;
  openaiBaseUrl: string;
};

export function splitRunModel(model: string): { provider: string; model: string } {
  const trimmed = model.trim();
  const slash = trimmed.indexOf("/");
  if (slash > 0) {
    const prefix = trimmed.slice(0, slash).toLowerCase();
    if (runtimeProviderOptions.includes(prefix)) {
      return { provider: prefix, model: trimmed.slice(slash + 1) };
    }
  }
  return { provider: "openai", model: trimmed };
}

/** Whether the run authenticates against the provider with OAuth or an API key. */
export function runAuthLabel(run: AgentRun, provider: string): "oauth" | "api" {
  if (provider === "copilot") return "oauth";
  return run.authMode === "oauth" ? "oauth" : "api";
}

const oauthCapableProviders = ["openai", "anthropic", "copilot"];

/**
 * Mirrors the backend UpdateAgentRunRuntimeConfig switch classification: a
 * provider change applies live when the pod already has credentials mounted
 * for the target — an API key ref (providerKeys) or, on OAuth runs, the
 * target provider's OAuth material (providerOauthSecrets; new runs mount
 * every saved credential). Otherwise the backend rewrites the run's
 * credentials from the caller's saved credentials and restarts the run's
 * compute; the session resumes from its persisted state.
 */
export function providerSwitchRestarts(run: AgentRun, currentProvider: string, target: string): boolean {
  if (target === currentProvider) {
    return false;
  }
  if (runAuthLabel(run, currentProvider) === "oauth" && oauthCapableProviders.includes(target)) {
    return !run.providerOauthSecrets.some(
      (ref) => ref.provider.trim().toLowerCase() === target && ref.secretName.trim() !== "",
    );
  }
  return !run.providerKeys.some(
    (key) => key.provider.trim().toLowerCase() === target && key.secretName.trim() !== "",
  );
}

/**
 * Always-visible provider / auth / model readout for the composer meta row,
 * doubling as the switcher: clicking it opens a popover to change provider,
 * model, and reasoning level without digging into the run-details dialog.
 */
export function RunModelSwitcher({
  run,
  canUpdate,
  updating,
  onUpdate,
  className,
}: {
  run: AgentRun;
  canUpdate: boolean;
  updating: boolean;
  onUpdate: (update: RuntimeConfigUpdate) => void | Promise<void>;
  className?: string;
}) {
  const current = splitRunModel(run.model || run.resolvedModel || "");
  const currentAuth = runAuthLabel(run, current.provider);
  const displayModel = splitRunModel(run.resolvedModel || run.model || "").model || current.model;

  const [open, setOpen] = useState(false);
  const [provider, setProvider] = useState(current.provider);
  const [model, setModel] = useState(current.model);
  const [reasoningLevel, setReasoningLevel] = useState(run.resolvedReasoningLevel || "");
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState<string | null>(null);
  // Credential refs snapshotted when the popover opens. The live run object is
  // replaced on every watch event, so keying the fetch off it would abort and
  // reissue the models request once per second while the editor is open.
  const [creds, setCreds] = useState<CredsSnapshot | null>(null);
  // A switch the pod has no credentials mounted for restarts run compute; the
  // backend remounts the caller's saved credentials and resumes the session.
  const willRestart = providerSwitchRestarts(run, current.provider, provider);

  useEffect(() => {
    if (!open || !creds || !provider) {
      return;
    }
    const controller = new AbortController();

    async function loadModels(snapshot: CredsSnapshot) {
      setModelsLoading(true);
      setModelsError(null);
      setAvailableModels([]);
      try {
        // The run's credential refs belong to its current provider; when
        // previewing a different provider, list models via the caller's saved
        // credentials instead of parsing mismatched OAuth material.
        const request =
          provider === snapshot.provider
            ? {
                namespace: snapshot.namespace,
                provider,
                authMode: provider === "copilot" ? "oauth" : snapshot.authMode,
                claudeApiKeySecret: snapshot.claudeApiKeySecret,
                openaiOauthSecret: snapshot.openaiOauthSecret,
                providerKeys: snapshot.providerKeys,
                openaiBaseUrl: snapshot.openaiBaseUrl,
              }
            : { namespace: snapshot.namespace, provider };
        const response = await client.listAvailableModels(request, { signal: controller.signal });
        if (controller.signal.aborted) return;
        setAvailableModels(response.models);
        if (response.models.length > 0) {
          setModel((value) => (value.trim() && response.models.includes(value.trim()) ? value : response.models[0]));
        }
      } catch (error) {
        if (controller.signal.aborted) return;
        setAvailableModels([]);
        setModelsError(error instanceof Error ? error.message : "Failed to load provider models");
      } finally {
        if (!controller.signal.aborted) {
          setModelsLoading(false);
        }
      }
    }

    void loadModels(creds);
    return () => controller.abort();
  }, [open, creds, provider]);

  function openEditor(nextOpen: boolean) {
    if (nextOpen) {
      setProvider(current.provider);
      setModel(current.model);
      setReasoningLevel(run.resolvedReasoningLevel || "");
      setCreds({
        namespace: run.namespace,
        provider: current.provider,
        authMode: run.authMode,
        claudeApiKeySecret: run.claudeApiKeySecret,
        openaiOauthSecret: run.openaiOauthSecret,
        providerKeys: run.providerKeys.map((key) => ({
          provider: key.provider,
          secretName: key.secretName,
          secretKey: key.secretKey,
        })),
        openaiBaseUrl: run.openaiBaseUrl,
      });
    }
    setOpen(nextOpen);
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const nextModel = model.trim();
    if (!nextModel || updating) {
      return;
    }
    try {
      await onUpdate({ provider, model: nextModel, reasoningLevel });
      setOpen(false);
    } catch {
      // The parent owns user-facing error reporting.
    }
  }

  const summary = (
    <>
      <span className="font-sans text-muted-foreground/70">{current.provider}</span>
      <span className="rounded-sm bg-muted px-1 py-px text-[10px] uppercase tracking-wide text-muted-foreground">
        {currentAuth}
      </span>
      <span className="max-w-[140px] truncate text-foreground/80 md:max-w-[200px]" title={run.resolvedModel || run.model}>
        {displayModel}
      </span>
    </>
  );

  if (!canUpdate) {
    return (
      <span
        className={cn("flex items-center gap-1.5 font-mono text-[11px] text-muted-foreground", className)}
        title="Provider · auth · model"
      >
        {summary}
      </span>
    );
  }

  return (
    <Popover open={open} onOpenChange={openEditor}>
      <PopoverTrigger
        render={
          <button
            type="button"
            className={cn(
              "flex items-center gap-1.5 rounded-md px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
              className,
            )}
            title="Switch provider or model"
            aria-label="Switch provider or model"
          />
        }
      >
        {summary}
        <ChevronsUpDown className="size-3 text-muted-foreground/60" />
      </PopoverTrigger>
      <PopoverContent side="top" align="end" className="w-[min(20rem,calc(100vw-2rem))]">
        <form className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-2 text-xs" onSubmit={submit}>
          <Label htmlFor="composer-runtime-provider" className="self-center text-xs text-muted-foreground">
            Provider
          </Label>
          <select
            id="composer-runtime-provider"
            value={provider}
            onChange={(event) => setProvider(event.target.value)}
            className={runtimeInputClass}
            disabled={updating}
          >
            {runtimeProviderOptions.map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </select>
          {willRestart ? (
            <div className="col-start-2 -mt-1 text-[11px] text-muted-foreground">
              Switching to {provider} restarts the run&apos;s pod with your saved {provider} credentials — the
              session resumes where it left off.
            </div>
          ) : null}

          <Label htmlFor="composer-runtime-model" className="self-center text-xs text-muted-foreground">
            Model
          </Label>
          {availableModels.length > 0 ? (
            <select
              id="composer-runtime-model"
              value={model}
              onChange={(event) => setModel(event.target.value)}
              className={runtimeInputClass}
              disabled={updating}
            >
              {!availableModels.includes(model) && model.trim() ? (
                <option value={model}>{model}</option>
              ) : null}
              {availableModels.map((option) => (
                <option key={option} value={option}>
                  {option}
                </option>
              ))}
            </select>
          ) : (
            <Input
              id="composer-runtime-model"
              value={model}
              onChange={(event) => setModel(event.target.value)}
              className="h-8 text-xs"
              disabled={updating}
            />
          )}
          <div className="col-start-2 -mt-1 text-[11px] text-muted-foreground" aria-live="polite">
            {modelsLoading
              ? "Loading provider models..."
              : modelsError
                ? `Could not load models: ${modelsError}`
                : availableModels.length > 0
                  ? `${availableModels.length} models available`
                  : "Enter a model ID"}
          </div>

          <Label htmlFor="composer-runtime-reasoning" className="self-center text-xs text-muted-foreground">
            Reasoning
          </Label>
          <select
            id="composer-runtime-reasoning"
            value={reasoningLevel}
            onChange={(event) => setReasoningLevel(event.target.value)}
            className={runtimeInputClass}
            disabled={updating}
          >
            {REASONING_LEVELS.map((option) => (
              <option key={option || "default"} value={option}>
                {option || "default"}
              </option>
            ))}
          </select>

          <div className="col-span-2 mt-1 flex justify-end gap-2">
            <Button type="button" variant="outline" size="sm" onClick={() => setOpen(false)} disabled={updating}>
              Cancel
            </Button>
            <Button type="submit" size="sm" disabled={updating || !model.trim()}>
              {updating ? "Saving..." : willRestart ? "Save & restart" : "Save"}
            </Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  );
}
