import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Code } from "@connectrpc/connect";
import { ArrowUp, ChevronDown, FolderKanban, SlidersHorizontal } from "lucide-react";

import { client } from "@/lib/client";
import { useProjects } from "@/hooks/useWatchedList";
import { readLastProject, writeLastProject } from "@/lib/lastProject";
import { cn } from "@/lib/utils";
import { connectCodeOf } from "@/lib/rpc-errors";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Field, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupTextarea,
} from "@/components/ui/input-group";
import { Spinner } from "@/components/ui/spinner";
import { Toggle } from "@/components/ui/toggle";
import type { MyCredentials } from "@/rpc/platform/service_pb";

interface PersonalWorkspaceDefaults {
  provider: string;
  authMode: string;
  model: string;
}

async function personalWorkspaceDefaults(
  credentials: MyCredentials,
  modelOverride: string,
): Promise<PersonalWorkspaceDefaults> {
  if (credentials.anthropicApiKeyPresent) {
    return { provider: "anthropic", authMode: "api-key", model: "" };
  }
  if (credentials.anthropicOauthPresent) {
    return { provider: "anthropic", authMode: "oauth", model: "" };
  }
  if (credentials.openaiApiKeyPresent) {
    return { provider: "openai", authMode: "api-key", model: "" };
  }
  if (credentials.openaiOauthPresent) {
    return { provider: "openai", authMode: "oauth", model: "" };
  }
  if (credentials.openrouterApiKeyPresent) {
    if (modelOverride) return { provider: "openrouter", authMode: "api-key", model: modelOverride };
    const models = await client.listAvailableModels({
      namespace: credentials.namespace,
      provider: "openrouter",
      authMode: "api-key",
    });
    const model = models.models[0];
    if (model) return { provider: "openrouter", authMode: "api-key", model };
    throw new Error("No OpenRouter models are available. Choose a model in Options.");
  }
  if (credentials.xaiApiKeyPresent) {
    if (modelOverride) return { provider: "xai", authMode: "api-key", model: modelOverride };
    const models = await client.listAvailableModels({
      namespace: credentials.namespace,
      provider: "xai",
      authMode: "api-key",
    });
    const model = models.models[0];
    if (model) return { provider: "xai", authMode: "api-key", model };
    throw new Error("No xAI models are available. Choose a model in Options.");
  }
  if (credentials.copilotOauthPresent) {
    if (modelOverride) return { provider: "copilot", authMode: "oauth", model: modelOverride };
    const models = await client.listAvailableModels({
      namespace: credentials.namespace,
      provider: "copilot",
      authMode: "oauth",
    });
    const model = models.models[0];
    if (model) return { provider: "copilot", authMode: "oauth", model };
    throw new Error("No GitHub Copilot models are available. Choose a model in Options.");
  }
  throw new Error("Connect a model provider in Settings to start chatting.");
}

export interface NewChatComposerProps {
  /** Pin the chat to a specific project; hides the picker. */
  fixedNamespace?: string;
  fixedProject?: string;
  variant?: "hero" | "compact";
  autoFocus?: boolean;
  placeholder?: string;
  className?: string;
}

/**
 * Chat-first composer. Picks a project automatically (pinned → last-used →
 * first), then starts an AgentRun and routes to the chat. Keep this the single
 * entry point so chat is always one keystroke away.
 */
export function NewChatComposer({
  fixedNamespace,
  fixedProject,
  variant = "hero",
  autoFocus,
  placeholder = "Describe a task, or ask anything…",
  className,
}: NewChatComposerProps) {
  const navigate = useNavigate();
  const { projects, loading: projectsLoading, error: projectsError } = useProjects();
  const [text, setText] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [model, setModel] = useState("");
  const [picked, setPicked] = useState<{ namespace: string; name: string } | null>(
    fixedNamespace && fixedProject ? { namespace: fixedNamespace, name: fixedProject } : null,
  );
  const taRef = useRef<HTMLTextAreaElement>(null);

  const project = useMemo(() => {
    if (fixedProject) {
      return (
        projects.find((p) => p.name === fixedProject && p.namespace === fixedNamespace) ?? {
          namespace: fixedNamespace ?? "",
          name: fixedProject,
          displayName: fixedProject,
        }
      );
    }
    if (picked) {
      const m = projects.find((p) => p.name === picked.name && p.namespace === picked.namespace);
      if (m) return m;
    }
    const last = readLastProject();
    if (last) {
      const m = projects.find((p) => p.name === last.name && p.namespace === last.namespace);
      if (m) return m;
    }
    return projects[0];
  }, [projects, picked, fixedProject, fixedNamespace]);

  useEffect(() => {
    const ta = taRef.current;
    if (!ta) return;
    ta.style.height = "auto";
    ta.style.height = `${Math.min(ta.scrollHeight, variant === "hero" ? 280 : 180)}px`;
  }, [text, variant]);

  async function submit() {
    const request = text.trim();
    if (!request || projectsLoading || projectsError || submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      let chatProject = project;
      if (!chatProject) {
        const credentials = await client.listMyCredentials({});
        const defaults = await personalWorkspaceDefaults(credentials, model.trim());
        try {
          chatProject = await client.createProject({
            name: "personal-workspace",
            displayName: "Personal workspace",
            provider: defaults.provider,
            model: defaults.model,
            authMode: defaults.authMode,
            useSavedCredentials: true,
            configureRuntimeProfile: true,
            permissionMode: "workspace-write",
            egressMode: "unrestricted",
            reviewLoopDisabled: true,
          });
        } catch (err) {
          if (connectCodeOf(err) !== Code.AlreadyExists) throw err;
          const existing = await client.listProjects({ namespace: credentials.namespace });
          const personalWorkspace = existing.projects.find(
            (candidate) => candidate.name === "personal-workspace",
          );
          if (!personalWorkspace) throw err;
          chatProject = personalWorkspace;
        }
      }
      const res = await client.createAgentRun({
        namespace: chatProject.namespace,
        userRequest: request,
        model: model.trim(),
        source: { kind: "Project", name: chatProject.name },
      });
      writeLastProject({ namespace: chatProject.namespace, name: chatProject.name });
      navigate(`/runs/${res.namespace}/${res.name}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to start chat");
      setSubmitting(false);
    }
  }

  const canSubmit = Boolean(text.trim()) && !projectsLoading && !projectsError && !submitting;

  return (
    <div className="flex flex-col gap-2">
      <InputGroup className={className}>
        <InputGroupTextarea
          ref={taRef}
          autoFocus={autoFocus}
          rows={variant === "hero" ? 2 : 1}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void submit();
            }
          }}
          placeholder={placeholder}
          disabled={projectsLoading || Boolean(projectsError) || submitting}
          className={cn(
            "px-4 pt-3.5 leading-relaxed",
            variant === "hero" ? "min-h-16" : "min-h-10",
          )}
        />
        <InputGroupAddon align="block-end">
          {!fixedProject && (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={<InputGroupButton variant="outline" className="max-w-[220px]" />}
              >
                <FolderKanban data-icon="inline-start" className="text-primary" />
                <span className="truncate">
                  {project ? project.displayName || project.name : "Personal workspace"}
                </span>
                <ChevronDown data-icon="inline-end" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="max-h-[320px] w-[260px] overflow-auto">
                <DropdownMenuGroup>
                  {projects.map((p) => (
                    <DropdownMenuItem
                      key={`${p.namespace}/${p.name}`}
                      onClick={() => setPicked({ namespace: p.namespace, name: p.name })}
                    >
                      <FolderKanban />
                      <span className="truncate">{p.displayName || p.name}</span>
                      <span className="ml-auto font-mono text-[10px] text-muted-foreground/60">
                        {p.namespace}
                      </span>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuGroup>
              </DropdownMenuContent>
            </DropdownMenu>
          )}
          <Toggle
            size="sm"
            pressed={showAdvanced}
            onPressedChange={setShowAdvanced}
            aria-label="Options"
            title="Options"
            className="min-w-0 px-1.5"
          >
            <SlidersHorizontal />
          </Toggle>
          <InputGroupButton
            variant="default"
            size="sm"
            className="ml-auto"
            disabled={!canSubmit}
            onClick={() => void submit()}
          >
            {submitting ? (
              <Spinner data-icon="inline-start" />
            ) : (
              <ArrowUp data-icon="inline-start" />
            )}
            Start
          </InputGroupButton>
        </InputGroupAddon>
      </InputGroup>

      {showAdvanced && (
        <Field orientation="horizontal">
          <FieldLabel htmlFor="new-chat-model" className="shrink-0">
            Model
          </FieldLabel>
          <Input
            id="new-chat-model"
            value={model}
            onChange={(e) => setModel(e.target.value)}
            placeholder="Model override (inherits project default)"
          />
        </Field>
      )}

      {(error || projectsError) && <FieldError>{error || projectsError}</FieldError>}
    </div>
  );
}
