import { useEffect, useState } from "react";
import { KeyRound, Loader2, MessageSquare } from "lucide-react";

import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import { REASONING_LEVELS } from "@/lib/reasoning";
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
import { Switch } from "@/components/ui/switch";
import {
  Chip,
  FlowField,
  FlowSwitchRow,
  OptionRow,
  OptionRows,
  Segmented,
} from "@/components/create-flow/create-flow";
import { PROVIDERS, providerName } from "@/components/create-flow/providers";
import type { SlackAgent, SlackWorkspace } from "@/rpc/platform/service_pb";

function workspaceKey(ws: { namespace: string; name: string }): string {
  return `${ws.namespace}/${ws.name}`;
}

/**
 * Create-only dialog for Slack agents: just the essentials (identity, Slack
 * connection, model + credentials). Everything else — channel access, App
 * Home, tools, runtime, policies — lives on the agent page after creation.
 */
export function SlackAgentCreateDialog({
  namespace,
  workspaces,
  trigger,
  onCreated,
}: {
  namespace: string;
  workspaces: SlackWorkspace[];
  trigger: React.ReactElement;
  onCreated?: (agent: SlackAgent) => void;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [slackUserId, setSlackUserId] = useState("");
  const [connectionMode, setConnectionMode] = useState<"dedicated" | "workspace">("dedicated");
  const [selectedWorkspace, setSelectedWorkspace] = useState("");
  const [botToken, setBotToken] = useState("");
  const [userToken, setUserToken] = useState("");
  const [appToken, setAppToken] = useState("");
  const [provider, setProvider] = useState("anthropic");
  const [model, setModel] = useState("");
  const [reasoningLevel, setReasoningLevel] = useState("");
  const [authMode, setAuthMode] = useState("api-key");
  const [useSaved, setUseSaved] = useState(true);
  const [apiKey, setApiKey] = useState("");
  const [githubToken, setGithubToken] = useState("");
  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const effectiveAuthMode = provider === "copilot" ? "oauth" : authMode;

  useEffect(() => {
    if (!open || !namespace) return;
    const controller = new AbortController();
    void (async () => {
      setAvailableModels([]);
      setModelsLoading(true);
      try {
        const resp = await client.listAvailableModels(
          { namespace, provider, authMode: effectiveAuthMode },
          { signal: controller.signal },
        );
        if (controller.signal.aborted) return;
        setAvailableModels(resp.models ?? []);
        if ((resp.models?.length ?? 0) > 0) {
          setModel((prev) => (prev.trim() ? prev : resp.models[0]));
        }
      } catch {
        if (!controller.signal.aborted) setAvailableModels([]);
      } finally {
        if (!controller.signal.aborted) setModelsLoading(false);
      }
    })();
    return () => controller.abort();
  }, [open, namespace, provider, effectiveAuthMode]);

  function reset() {
    setName("");
    setSlackUserId("");
    setConnectionMode("dedicated");
    setSelectedWorkspace("");
    setBotToken("");
    setUserToken("");
    setAppToken("");
    setProvider("anthropic");
    setModel("");
    setAuthMode("api-key");
    setUseSaved(true);
    setApiKey("");
    setGithubToken("");
    setError(null);
  }

  async function handleSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    const joiningWorkspace = connectionMode === "workspace";
    if (joiningWorkspace && !selectedWorkspace) {
      setError("Pick the shared workspace app to join.");
      return;
    }
    if (joiningWorkspace && !slackUserId.trim()) {
      setError("Your Slack user ID is required to join a shared workspace app.");
      return;
    }
    const [wsNamespace, wsName] = joiningWorkspace
      ? [selectedWorkspace.split("/")[0], selectedWorkspace.split("/").slice(1).join("/")]
      : ["", ""];
    setSubmitting(true);
    try {
      const saved = await client.updateSlackAgent({
        name: name.trim(),
        botToken: joiningWorkspace ? "" : botToken.trim(),
        userToken: joiningWorkspace ? "" : userToken.trim(),
        appToken: joiningWorkspace ? "" : appToken.trim(),
        workspaceName: wsName,
        workspaceNamespace: wsNamespace,
        slackUserId: slackUserId.trim(),
        channelReplyMode: "require-approval",
        commanders: [],
        appHomeHeader: "",
        appHomeText: "",
        mcpServerRefs: [],
        skillRefs: [],
        sessionIdleMinutes: 0,
        suspend: false,
        model: model.trim(),
        reasoningLevel,
        provider,
        authMode: effectiveAuthMode,
        useSavedCredentials: useSaved,
        anthropicApiKey: !useSaved && provider === "anthropic" ? apiKey.trim() : "",
        openaiApiKey: !useSaved && provider !== "anthropic" ? apiKey.trim() : "",
        githubToken: githubToken.trim(),
        runtimeProfileRef: "",
        configureRuntimeProfile: true,
        image: "",
        permissionMode: "workspace-write",
        egressMode: "unrestricted",
        mcpPolicyRef: "",
        configureMcpPolicy: false,
        mcpPolicyDefaultAction: "Deny",
        mcpPolicyAllowedServers: [],
      });
      setOpen(false);
      reset();
      onCreated?.(saved);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to create Slack agent");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(nextOpen) => {
        setOpen(nextOpen);
        if (!nextOpen) reset();
      }}
    >
      <DialogTrigger render={trigger} />
      <DialogContent
        className="flex w-full max-w-2xl flex-col gap-0 overflow-hidden p-0 sm:max-w-2xl max-h-[92vh]"
        showCloseButton
      >
        <form onSubmit={handleSubmit} className="flex min-h-0 flex-1 flex-col">
          <DialogHeader className="space-y-1 border-b px-6 py-5">
            <div className="flex items-center gap-2.5">
              <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                <MessageSquare className="size-4" />
              </span>
              <DialogTitle className="text-base">New Slack agent</DialogTitle>
            </div>
            <DialogDescription>
              A personal agent that chats as you in Slack. Advanced settings — channel access, App
              Home, tools, runtime — live on the agent page after creation.
            </DialogDescription>
          </DialogHeader>

          <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-6 py-5">
            <div className="grid gap-4 sm:grid-cols-2">
              <FlowField id="slack-create-name" label="Agent name" required>
                <Input
                  id="slack-create-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="my-agent"
                  autoFocus
                  required
                />
              </FlowField>
              <FlowField
                id="slack-create-user-id"
                label="Slack user ID"
                hint="Your member ID (profile → Copy member ID)."
              >
                <Input
                  id="slack-create-user-id"
                  value={slackUserId}
                  onChange={(e) => setSlackUserId(e.target.value)}
                  placeholder="U0123ABC"
                  className="font-mono"
                />
              </FlowField>
            </div>

            <FlowSwitchRow
              label="Slack connection"
              hint="Dedicated uses your own Slack app and tokens; shared joins an app someone already installed."
              control={
                <Segmented
                  aria-label="Connection mode"
                  value={connectionMode}
                  onChange={setConnectionMode}
                  options={[
                    { value: "dedicated", label: "Dedicated app" },
                    { value: "workspace", label: "Shared workspace app" },
                  ]}
                />
              }
            />

            {connectionMode === "workspace" ? (
              <FlowField
                id="slack-create-workspace"
                label="Workspace app"
                hint="Your Slack user ID (above) is required — the shared connector routes your messages by it."
              >
                {workspaces.length > 0 ? (
                  <Select
                    value={selectedWorkspace}
                    onValueChange={(v) => setSelectedWorkspace((v as string) ?? "")}
                  >
                    <SelectTrigger id="slack-create-workspace" className="w-full">
                      <SelectValue placeholder="Select a workspace app…" />
                    </SelectTrigger>
                    <SelectContent>
                      {workspaces.map((ws) => (
                        <SelectItem key={workspaceKey(ws)} value={workspaceKey(ws)}>
                          {ws.name}
                          {ws.resolvedTeamId || ws.teamId
                            ? ` · ${ws.resolvedTeamId || ws.teamId}`
                            : ""}
                          {ws.mine ? " · yours" : ""}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ) : (
                  <p className="rounded-md border border-dashed border-border/70 px-3 py-2.5 text-[12px] text-muted-foreground">
                    No shared workspace apps yet — create one from the Slack page, then join it
                    here.
                  </p>
                )}
              </FlowField>
            ) : (
              <div className="grid gap-4 sm:grid-cols-2">
                <FlowField id="slack-create-bot-token" label="Bot token">
                  <Input
                    id="slack-create-bot-token"
                    type="password"
                    value={botToken}
                    onChange={(e) => setBotToken(e.target.value)}
                    placeholder="xoxb-..."
                    autoComplete="off"
                    className="font-mono"
                  />
                </FlowField>
                <FlowField id="slack-create-app-token" label="App-level token">
                  <Input
                    id="slack-create-app-token"
                    type="password"
                    value={appToken}
                    onChange={(e) => setAppToken(e.target.value)}
                    placeholder="xapp-..."
                    autoComplete="off"
                    className="font-mono"
                  />
                </FlowField>
                <FlowField
                  id="slack-create-user-token"
                  label="User token"
                  hint="Optional — enables the agent's slack_search tool. Create the app from the manifest on the agent page after creating."
                  className="sm:col-span-2"
                >
                  <Input
                    id="slack-create-user-token"
                    type="password"
                    value={userToken}
                    onChange={(e) => setUserToken(e.target.value)}
                    placeholder="xoxp-..."
                    autoComplete="off"
                    className="font-mono"
                  />
                </FlowField>
              </div>
            )}

            <FlowField label="Provider">
              <div className="flex flex-wrap gap-1.5">
                {PROVIDERS.map((p) => (
                  <Chip key={p.id} selected={provider === p.id} onSelect={() => setProvider(p.id)}>
                    {p.name}
                  </Chip>
                ))}
              </div>
            </FlowField>

            <FlowField
              id="slack-create-model"
              label="Model"
              aside={
                modelsLoading ? (
                  <span className="text-[10.5px] text-muted-foreground">loading…</span>
                ) : undefined
              }
            >
              {availableModels.length > 0 ? (
                <Select value={model} onValueChange={(v) => setModel((v as string) ?? "")}>
                  <SelectTrigger id="slack-create-model" className="w-full">
                    <SelectValue placeholder="Select model…" />
                  </SelectTrigger>
                  <SelectContent>
                    {!availableModels.includes(model) && model ? (
                      <SelectItem value={model}>{model}</SelectItem>
                    ) : null}
                    {availableModels.map((m) => (
                      <SelectItem key={m} value={m}>
                        {m}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              ) : (
                <Input
                  id="slack-create-model"
                  value={model}
                  onChange={(e) => setModel(e.target.value)}
                  placeholder="claude-sonnet-4-6"
                  className="font-mono"
                />
              )}
            </FlowField>

            <FlowField
              id="slack-create-reasoning-level"
              label="Reasoning level"
              hint="Default reasoning effort for runs; empty = provider default."
            >
              <Select
                value={reasoningLevel || "default"}
                onValueChange={(v) => setReasoningLevel(v === "default" ? "" : ((v as string) ?? ""))}
              >
                <SelectTrigger id="slack-create-reasoning-level" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {REASONING_LEVELS.map((level) => (
                    <SelectItem key={level || "default"} value={level || "default"}>
                      {level || "default"}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </FlowField>

            {provider !== "copilot" && (
              <FlowSwitchRow
                label="Auth mode"
                control={
                  <Segmented
                    aria-label="Auth mode"
                    value={authMode === "oauth" ? "oauth" : "api-key"}
                    onChange={setAuthMode}
                    options={[
                      { value: "api-key", label: "api-key" },
                      { value: "oauth", label: "oauth" },
                    ]}
                  />
                }
              />
            )}

            <FlowSwitchRow
              id="slack-create-use-saved"
              label="Use saved provider credentials"
              hint="Reuse the credentials from Settings → Credentials instead of a one-off key."
              control={
                <Switch id="slack-create-use-saved" checked={useSaved} onCheckedChange={setUseSaved} />
              }
            />
            {!useSaved && (
              <FlowField id="slack-create-api-key" label={`${providerName(provider)} API key`}>
                <Input
                  id="slack-create-api-key"
                  type="password"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder="sk-..."
                  autoComplete="off"
                  className="font-mono"
                />
              </FlowField>
            )}

            <OptionRows label="Optional" className="pt-1">
              <OptionRow
                icon={KeyRound}
                title="GitHub access"
                summary={githubToken.trim() ? "agent-specific token" : "your saved GitHub token"}
                modified={Boolean(githubToken.trim())}
              >
                <FlowField
                  id="slack-create-github-token"
                  label="GitHub token"
                  hint="Runs default to the GitHub token saved under Settings → Credentials. Set one here if you don't have a saved token or want this agent to use its own."
                >
                  <Input
                    id="slack-create-github-token"
                    type="password"
                    value={githubToken}
                    onChange={(e) => setGithubToken(e.target.value)}
                    placeholder="ghp_... / github_pat_..."
                    autoComplete="off"
                    className="font-mono"
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

          <div className="flex items-center justify-end gap-2 border-t px-6 py-4">
            <DialogClose render={<Button type="button" variant="ghost" size="sm" />}>
              Cancel
            </DialogClose>
            <Button type="submit" size="sm" disabled={submitting || !name.trim()}>
              {submitting ? <Loader2 className="size-4 animate-spin" /> : null}
              {submitting ? "Creating…" : "Create Slack agent"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
