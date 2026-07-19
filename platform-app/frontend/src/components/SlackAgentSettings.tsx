import { useCallback, useEffect, useState } from "react";

import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft } from "@/lib/status";
import { REASONING_LEVELS } from "@/lib/reasoning";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { RuntimeImagePicker } from "@/components/RuntimeImagePicker";
import { RepoUrlListInput } from "@/components/RepoUrlListInput";
import { toast } from "@/components/ui/toaster";
import type { SlackAgent, SlackWorkspace } from "@/rpc/platform/service_pb";

const providers = ["anthropic", "openai", "gemini", "openrouter", "groq", "xai", "copilot"];

function workspaceKey(ws: { namespace: string; name: string }): string {
  return `${ws.namespace}/${ws.name}`;
}

function splitCommaList(value: string): string[] {
  return value
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean);
}

export function SavedPill() {
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

/** Titled group inside the settings surface, separated by hairlines. */
function FormSection({
  title,
  description,
  aside,
  children,
}: {
  title: string;
  description?: React.ReactNode;
  aside?: React.ReactNode;
  children?: React.ReactNode;
}) {
  return (
    <section className="space-y-3.5 border-t border-border/60 pt-4 first:border-t-0 first:pt-0">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h3 className="text-[13px] font-semibold tracking-[-0.005em]">{title}</h3>
          {description && (
            <p className="mt-0.5 max-w-[62ch] text-[11.5px] leading-relaxed text-muted-foreground">
              {description}
            </p>
          )}
        </div>
        {aside && <div className="shrink-0 pt-0.5">{aside}</div>}
      </div>
      {children}
    </section>
  );
}

function Field({
  id,
  label,
  hint,
  aside,
  className,
  children,
}: {
  id?: string;
  label: string;
  hint?: React.ReactNode;
  aside?: React.ReactNode;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      <div className="flex h-4 items-center justify-between gap-2">
        <Label htmlFor={id} className="text-[12.5px]">
          {label}
        </Label>
        {aside}
      </div>
      {children}
      {hint && <p className="text-[11px] leading-relaxed text-muted-foreground">{hint}</p>}
    </div>
  );
}

function SwitchRow({
  id,
  label,
  hint,
  checked,
  onCheckedChange,
}: {
  id: string;
  label: string;
  hint?: React.ReactNode;
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <Label htmlFor={id} className="text-[12.5px]">
          {label}
        </Label>
        {hint && (
          <p className="mt-0.5 max-w-[56ch] text-[11px] leading-relaxed text-muted-foreground">
            {hint}
          </p>
        )}
      </div>
      <Switch id={id} checked={checked} onCheckedChange={onCheckedChange} />
    </div>
  );
}

/**
 * Full configuration form for one Slack agent, shown on the agent's detail
 * page. Saving assigns the whole config: every field managed here is sent on
 * every save.
 */
export function SlackAgentSettings({
  agent,
  onSaved,
  onDeleted,
}: {
  agent: SlackAgent;
  onSaved?: () => void;
  onDeleted?: () => void;
}) {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const [slackUserId, setSlackUserId] = useState("");
  const [connectionMode, setConnectionMode] = useState<"dedicated" | "workspace">("dedicated");
  const [workspaces, setWorkspaces] = useState<SlackWorkspace[]>([]);
  const [selectedWorkspace, setSelectedWorkspace] = useState("");
  const [botToken, setBotToken] = useState("");
  const [userToken, setUserToken] = useState("");
  const [appToken, setAppToken] = useState("");
  const [model, setModel] = useState("");
  const [reasoningLevel, setReasoningLevel] = useState("");
  const [additionalRepoUrls, setAdditionalRepoUrls] = useState<string[]>([]);
  const [provider, setProvider] = useState("anthropic");
  const [authMode, setAuthMode] = useState("api-key");
  const [useSaved, setUseSaved] = useState(true);
  const [apiKey, setApiKey] = useState("");
  const [githubToken, setGithubToken] = useState("");
  const [clearGithubToken, setClearGithubToken] = useState(false);
  const [channelReplyMode, setChannelReplyMode] = useState("require-approval");
  const [commanders, setCommanders] = useState("");
  const [appHomeHeader, setAppHomeHeader] = useState("");
  const [appHomeText, setAppHomeText] = useState("");
  const [mcpServerRefs, setMcpServerRefs] = useState<string[]>([]);
  const [skillRefs, setSkillRefs] = useState<string[]>([]);
  const [sessionIdleMinutes, setSessionIdleMinutes] = useState(0);
  const [suspend, setSuspend] = useState(false);
  const [runtimeProfileRef, setRuntimeProfileRef] = useState("");
  const [configureRuntimeProfile, setConfigureRuntimeProfile] = useState(false);
  const [image, setImage] = useState("");
  const [permissionMode, setPermissionMode] = useState("workspace-write");
  const [egressMode, setEgressMode] = useState("unrestricted");
  const [mcpPolicyRef, setMcpPolicyRef] = useState("");
  const [configureMcpPolicy, setConfigureMcpPolicy] = useState(false);
  const [mcpPolicyDefaultAction, setMcpPolicyDefaultAction] = useState("Deny");
  const [mcpPolicyAllowedServers, setMcpPolicyAllowedServers] = useState("");

  const [availableModels, setAvailableModels] = useState<string[]>([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [availableServers, setAvailableServers] = useState<
    { name: string; version: string; description: string }[]
  >([]);
  const [availableSkills, setAvailableSkills] = useState<
    { name: string; version: string; description: string; mcpServerRefs: string[] }[]
  >([]);

  const effectiveAuthMode = provider === "copilot" ? "oauth" : authMode;

  const apply = useCallback((a: SlackAgent) => {
    setSlackUserId(a.slackUserId);
    setConnectionMode(a.workspaceRefName ? "workspace" : "dedicated");
    setSelectedWorkspace(
      a.workspaceRefName
        ? `${a.workspaceRefNamespace || a.namespace}/${a.workspaceRefName}`
        : "",
    );
    setModel(a.model);
    setReasoningLevel(a.reasoningLevel || "");
    setAdditionalRepoUrls(a.additionalRepoUrls ?? []);
    setProvider(a.provider || "anthropic");
    setChannelReplyMode(a.channelReplyMode || "require-approval");
    setCommanders((a.commanders ?? []).join(", "));
    setAppHomeHeader(a.appHomeHeader || "");
    setAppHomeText(a.appHomeText || "");
    setMcpServerRefs(a.mcpServerRefs ?? []);
    setSkillRefs(a.skillRefs ?? []);
    setSessionIdleMinutes(a.sessionIdleMinutes || 0);
    setSuspend(a.suspended);
    setRuntimeProfileRef(a.runtimeProfileRef || "");
    setConfigureRuntimeProfile(false);
    setImage(a.image || "");
    setPermissionMode(a.permissionMode || "workspace-write");
    setEgressMode(a.egressMode || "unrestricted");
    setMcpPolicyRef(a.mcpPolicyRef || "");
    setConfigureMcpPolicy(false);
    setMcpPolicyDefaultAction(a.mcpPolicyDefaultAction || "Deny");
    setMcpPolicyAllowedServers((a.mcpPolicyAllowedServers ?? []).join(", "));
    setBotToken("");
    setUserToken("");
    setAppToken("");
    setApiKey("");
    setGithubToken("");
    setClearGithubToken(false);
  }, []);

  useEffect(() => {
    // Re-sync the form whenever a freshly loaded agent object arrives (e.g.
    // after save); token fields reset to their write-only empty state.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    apply(agent);
  }, [agent, apply]);

  useEffect(() => {
    if (!agent.namespace) return;
    const controller = new AbortController();
    void (async () => {
      setAvailableModels([]);
      setModelsLoading(true);
      try {
        const resp = await client.listAvailableModels(
          { namespace: agent.namespace, provider, authMode: effectiveAuthMode },
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
  }, [agent.namespace, provider, effectiveAuthMode]);

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const resp = await client.listMCPServers({});
        if (active) {
          setAvailableServers(
            (resp.servers ?? []) as { name: string; version: string; description: string }[],
          );
        }
      } catch {
        if (active) setAvailableServers([]);
      }
      try {
        const resp = await client.listSkills({});
        if (active) {
          setAvailableSkills(
            (resp.skills ?? []) as {
              name: string;
              version: string;
              description: string;
              mcpServerRefs: string[];
            }[],
          );
        }
      } catch {
        if (active) setAvailableSkills([]);
      }
      try {
        const resp = await client.listSlackWorkspaces({});
        if (active) setWorkspaces((resp.workspaces ?? []) as SlackWorkspace[]);
      } catch {
        if (active) setWorkspaces([]);
      }
    })();
    return () => {
      active = false;
    };
  }, []);

  function toggleMcpServer(name: string, on: boolean) {
    setMcpServerRefs((prev) => {
      const without = prev.filter((n) => n !== name);
      return on ? [...without, name] : without;
    });
  }

  function toggleSkill(name: string, on: boolean) {
    setSkillRefs((prev) => {
      const without = prev.filter((n) => n !== name);
      return on ? [...without, name] : without;
    });
  }

  async function save() {
    setSaving(true);
    setError(null);
    const joiningWorkspace = connectionMode === "workspace";
    const [wsNamespace, wsName] = joiningWorkspace && selectedWorkspace
      ? [selectedWorkspace.split("/")[0], selectedWorkspace.split("/").slice(1).join("/")]
      : ["", ""];
    try {
      await client.updateSlackAgent({
        name: agent.name,
        botToken: joiningWorkspace ? "" : botToken.trim(),
        userToken: joiningWorkspace ? "" : userToken.trim(),
        appToken: joiningWorkspace ? "" : appToken.trim(),
        workspaceName: wsName,
        workspaceNamespace: wsNamespace,
        slackUserId: slackUserId.trim(),
        channelReplyMode,
        commanders: splitCommaList(commanders),
        appHomeHeader: appHomeHeader.trim(),
        appHomeText: appHomeText.trim(),
        mcpServerRefs,
        skillRefs,
        sessionIdleMinutes,
        suspend,
        model: model.trim(),
        reasoningLevel,
        additionalRepoUrls,
        provider,
        authMode: effectiveAuthMode,
        useSavedCredentials: useSaved,
        anthropicApiKey: !useSaved && provider === "anthropic" ? apiKey.trim() : "",
        openaiApiKey: !useSaved && provider !== "anthropic" ? apiKey.trim() : "",
        githubToken: clearGithubToken ? "" : githubToken.trim(),
        clear: clearGithubToken ? ["github-token"] : [],
        runtimeProfileRef: runtimeProfileRef.trim(),
        configureRuntimeProfile,
        image: image.trim(),
        permissionMode,
        egressMode,
        mcpPolicyRef: mcpPolicyRef.trim(),
        configureMcpPolicy,
        mcpPolicyDefaultAction,
        mcpPolicyAllowedServers: splitCommaList(mcpPolicyAllowedServers),
      });
      toast.success("Slack agent saved");
      onSaved?.();
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to save Slack agent";
      setError(message);
      toast.error(message);
    } finally {
      setSaving(false);
    }
  }

  async function remove() {
    try {
      await client.deleteSlackAgent({ name: agent.name });
      toast.success(`Deleted “${agent.name}”`);
      onDeleted?.();
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to delete Slack agent";
      setError(message);
      toast.error(message);
    }
  }

  return (
    <section className="surface-card space-y-4 p-4 sm:p-5">
      <FormSection
        title="Identity"
        description="The agent's name and the Slack user it acts for."
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <Field id="slack-agent-name" label="Agent name" hint="Names are immutable after creation.">
            <Input id="slack-agent-name" value={agent.name} disabled />
          </Field>
          <Field
            id="slack-user-id"
            label="Slack user ID"
            hint="Your member ID (profile → Copy member ID)."
          >
            <Input
              id="slack-user-id"
              value={slackUserId}
              onChange={(e) => setSlackUserId(e.target.value)}
              placeholder="U0123ABC"
              className="font-mono"
            />
          </Field>
        </div>
      </FormSection>

      <FormSection
        title="Slack connection"
        description="Use your own Slack app (own bot identity) or join a shared workspace app someone already installed."
      >
        <div className="grid gap-2 sm:grid-cols-2">
          {([
            {
              value: "dedicated" as const,
              title: "Dedicated app",
              hint: "Your own bot + tokens.",
            },
            {
              value: "workspace" as const,
              title: "Shared workspace app",
              hint: "Join an installed app — no tokens needed.",
            },
          ]).map((opt) => (
            <button
              key={opt.value}
              type="button"
              onClick={() => setConnectionMode(opt.value)}
              className={cn(
                "rounded-[8px] border px-3 py-2.5 text-left transition-colors",
                connectionMode === opt.value
                  ? "border-[color:var(--color-primary)]/50 bg-[color:var(--color-primary)]/8 ring-1 ring-inset ring-[color:var(--color-primary)]/25"
                  : "border-border/70 hover:bg-muted/40",
              )}
            >
              <div className="text-[12.5px] font-medium">{opt.title}</div>
              <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
                {opt.hint}
              </div>
            </button>
          ))}
        </div>

        {connectionMode === "workspace" ? (
          <Field
            id="slack-workspace"
            label="Workspace app"
            hint="Your Slack user ID (above) is required — the shared connector routes your messages by it."
          >
            {workspaces.length > 0 ? (
              <Select
                value={selectedWorkspace}
                onValueChange={(v) => setSelectedWorkspace((v as string) ?? "")}
              >
                <SelectTrigger id="slack-workspace" className="w-full">
                  <SelectValue placeholder="Select a workspace app…" />
                </SelectTrigger>
                <SelectContent>
                  {workspaces.map((ws) => (
                    <SelectItem key={workspaceKey(ws)} value={workspaceKey(ws)}>
                      {ws.name}
                      {ws.resolvedTeamId || ws.teamId ? ` · ${ws.resolvedTeamId || ws.teamId}` : ""}
                      {ws.mine ? " · yours" : ""}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <p className="rounded-md border border-dashed border-border/70 px-3 py-2.5 text-[12px] text-muted-foreground">
                No shared workspace apps yet — create one from the Slack page, then join it here.
              </p>
            )}
          </Field>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2">
            <Field
              id="slack-bot-token"
              label="Bot token"
              aside={agent.botTokenPresent ? <SavedPill /> : undefined}
            >
              <Input
                id="slack-bot-token"
                type="password"
                value={botToken}
                onChange={(e) => setBotToken(e.target.value)}
                placeholder={agent.botTokenPresent ? "•••• saved — enter to replace" : "xoxb-..."}
                autoComplete="off"
                className="font-mono"
              />
            </Field>
            <Field
              id="slack-app-token"
              label="App-level token"
              aside={agent.appTokenPresent ? <SavedPill /> : undefined}
            >
              <Input
                id="slack-app-token"
                type="password"
                value={appToken}
                onChange={(e) => setAppToken(e.target.value)}
                placeholder={agent.appTokenPresent ? "•••• saved — enter to replace" : "xapp-..."}
                autoComplete="off"
                className="font-mono"
              />
            </Field>
            <Field
              id="slack-user-token"
              label="User token"
              hint="Optional — enables the agent's slack_search tool (Slack only permits search with a user token)."
              aside={agent.userTokenPresent ? <SavedPill /> : undefined}
              className="sm:col-span-2"
            >
              <Input
                id="slack-user-token"
                type="password"
                value={userToken}
                onChange={(e) => setUserToken(e.target.value)}
                placeholder={agent.userTokenPresent ? "•••• saved — enter to replace" : "xoxp-..."}
                autoComplete="off"
                className="font-mono"
              />
            </Field>
            <p className="text-[11px] leading-relaxed text-muted-foreground sm:col-span-2">
              Create the app from the manifest in the Overview tab, install it, then paste its
              tokens here.
            </p>
          </div>
        )}
      </FormSection>

      <FormSection title="Model & credentials" description="Which model and provider power replies.">
        <div className="grid gap-3 sm:grid-cols-2">
          <Field id="slack-provider" label="Provider">
            <Select value={provider} onValueChange={(v) => setProvider((v as string) ?? "anthropic")}>
              <SelectTrigger id="slack-provider" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {providers.map((p) => (
                  <SelectItem key={p} value={p}>
                    {p}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field
            id="slack-model"
            label="Model"
            aside={
              modelsLoading ? (
                <span className="text-[10.5px] text-muted-foreground">loading…</span>
              ) : undefined
            }
          >
            {availableModels.length > 0 ? (
              <Select value={model} onValueChange={(v) => setModel((v as string) ?? "")}>
                <SelectTrigger id="slack-model" className="w-full">
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
                id="slack-model"
                value={model}
                onChange={(e) => setModel(e.target.value)}
                placeholder="claude-sonnet-4-6"
                className="font-mono"
              />
            )}
          </Field>
          <Field
            id="slack-reasoning-level"
            label="Reasoning level"
            hint="Default reasoning effort for runs; empty = provider default."
          >
            <Select
              value={reasoningLevel || "default"}
              onValueChange={(v) => setReasoningLevel(v === "default" ? "" : ((v as string) ?? ""))}
            >
              <SelectTrigger id="slack-reasoning-level" className="w-full">
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
          </Field>
          {provider !== "copilot" && (
            <Field id="slack-auth-mode" label="Auth mode">
              <Select value={authMode} onValueChange={(v) => setAuthMode((v as string) ?? "api-key")}>
                <SelectTrigger id="slack-auth-mode" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="api-key">api-key</SelectItem>
                  <SelectItem value="oauth">oauth</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          )}
        </div>
        <SwitchRow
          id="slack-use-saved"
          label="Use saved provider credentials"
          hint="Reuse the credentials from Settings → Credentials instead of a one-off key."
          checked={useSaved}
          onCheckedChange={setUseSaved}
        />
        {!useSaved && (
          <Field
            id="slack-api-key"
            label={`${provider === "anthropic" ? "Anthropic" : "Provider"} API key`}
          >
            <Input
              id="slack-api-key"
              type="password"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
              placeholder="sk-..."
              autoComplete="off"
              className="font-mono"
            />
          </Field>
        )}
      </FormSection>

      <FormSection
        title="GitHub access"
        description="Runs this agent creates default to the GitHub token saved under Settings → Credentials. Set an agent-specific token to override it for this agent only."
      >
        <Field
          id="slack-github-token"
          label="Agent-specific GitHub token"
          aside={agent.githubTokenPresent && !clearGithubToken ? <SavedPill /> : undefined}
        >
          <Input
            id="slack-github-token"
            type="password"
            value={githubToken}
            onChange={(e) => setGithubToken(e.target.value)}
            placeholder={
              agent.githubTokenPresent && !clearGithubToken
                ? "•••• saved — enter to replace"
                : "ghp_... / github_pat_..."
            }
            autoComplete="off"
            className="font-mono"
            disabled={clearGithubToken}
          />
        </Field>
        {agent.githubTokenPresent &&
          (clearGithubToken ? (
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-[11.5px] text-amber-600">
                The agent-specific token will be removed on save — runs fall back to your saved
                GitHub token.
              </p>
              <Button variant="ghost" size="xs" onClick={() => setClearGithubToken(false)}>
                Keep agent token
              </Button>
            </div>
          ) : (
            <Button
              variant="outline"
              size="xs"
              onClick={() => {
                setClearGithubToken(true);
                setGithubToken("");
              }}
            >
              Use saved token instead
            </Button>
          ))}
      </FormSection>

      <FormSection
        title="Channel access"
        description="Who can command the agent by @mentioning it in channels. You can always command it; everyone else must be listed here."
      >
        <Field
          id="slack-commanders"
          label="Allowed commanders"
          hint="Slack user IDs, comma-separated. Empty = only you."
        >
          <Input
            id="slack-commanders"
            value={commanders}
            onChange={(e) => setCommanders(e.target.value)}
            placeholder="U012ABC, U345DEF"
            className="font-mono"
          />
        </Field>
        <Field
          id="slack-channel-reply-mode"
          label="Channel replies"
          hint="Replies in channels, private channels, and group DMs. Your 1:1 DM with the agent is always direct."
          className="max-w-xs"
        >
          <Select
            value={channelReplyMode}
            onValueChange={(v) => setChannelReplyMode((v as string) ?? "require-approval")}
          >
            <SelectTrigger id="slack-channel-reply-mode" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="require-approval">Ask me to approve before posting</SelectItem>
              <SelectItem value="auto">Post directly</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </FormSection>

      <FormSection
        title="App Home tab"
        description="The static copy shown on the app's Home tab in Slack. Anyone in the workspace who opens the app can see it, so it never includes live status or drafts."
      >
        <Field
          id="slack-app-home-header"
          label="Header"
          hint="Plain text, up to 150 characters. Empty = the agent's name."
        >
          <Input
            id="slack-app-home-header"
            value={appHomeHeader}
            onChange={(e) => setAppHomeHeader(e.target.value)}
            placeholder={agent.name}
            maxLength={150}
          />
        </Field>
        <Field
          id="slack-app-home-text"
          label="Info line"
          hint="One short line under the header (Slack mrkdwn). Empty = a generic pointer to this dashboard."
        >
          <Input
            id="slack-app-home-text"
            value={appHomeText}
            onChange={(e) => setAppHomeText(e.target.value)}
            placeholder="DM me to get things done. This agent is managed from its owner's dashboard."
            maxLength={1000}
          />
        </Field>
      </FormSection>

      <FormSection
        title="MCP servers & skills"
        description="MCP server configs and reusable skills attached to this agent's runs — e.g. Grafana observability tools. A skill's required servers are attached automatically."
      >
        {availableServers.length === 0 && mcpServerRefs.length === 0 ? (
          <p className="text-[12px] text-muted-foreground">
            No MCP servers in your namespace. Create one under Resources → MCP servers to enable
            tools here.
          </p>
        ) : (
          <div className="space-y-2.5">
            {availableServers.map((server) => (
              <div key={server.name} className="flex items-start justify-between gap-3">
                <div>
                  <div className="text-[12.5px] font-medium">
                    {server.name}
                    {server.version && (
                      <span className="ml-1.5 text-[11px] font-normal text-muted-foreground">
                        v{server.version}
                      </span>
                    )}
                  </div>
                  {server.description && (
                    <p className="text-[12px] text-muted-foreground">{server.description}</p>
                  )}
                </div>
                <Switch
                  aria-label={`Attach ${server.name}`}
                  checked={mcpServerRefs.includes(server.name)}
                  onCheckedChange={(on) => toggleMcpServer(server.name, on)}
                />
              </div>
            ))}
            {mcpServerRefs
              .filter((name) => !availableServers.some((s) => s.name === name))
              .map((name) => (
                <div key={name} className="flex items-center justify-between gap-3">
                  <div className="text-[12.5px] font-medium">
                    {name}
                    <span className="ml-1.5 text-[11px] font-normal text-amber-600">
                      not found in your namespace
                    </span>
                  </div>
                  <Switch
                    aria-label={`Detach ${name}`}
                    checked
                    onCheckedChange={(on) => toggleMcpServer(name, on)}
                  />
                </div>
              ))}
            {configureMcpPolicy &&
              mcpPolicyDefaultAction === "Deny" &&
              mcpServerRefs.some(
                (name) => !splitCommaList(mcpPolicyAllowedServers).includes(name),
              ) && (
                <p className="text-[12px] text-amber-600">
                  Your MCP policy denies by default — add the selected server names to its allowed
                  servers or their tools won't load.
                </p>
              )}
          </div>
        )}
        {availableSkills.length === 0 && skillRefs.length === 0 ? (
          <p className="text-[12px] text-muted-foreground">
            No skills in your namespace. Create one under Resources → Skills to attach reusable
            instructions here.
          </p>
        ) : (
          <div className="space-y-2.5 border-t pt-3">
            {availableSkills.map((skill) => (
              <div key={skill.name} className="flex items-start justify-between gap-3">
                <div>
                  <div className="text-[12.5px] font-medium">
                    {skill.name}
                    {skill.version && (
                      <span className="ml-1.5 text-[11px] font-normal text-muted-foreground">
                        v{skill.version}
                      </span>
                    )}
                    {(skill.mcpServerRefs ?? []).length > 0 && (
                      <span className="ml-1.5 text-[11px] font-normal text-muted-foreground">
                        brings: {skill.mcpServerRefs.join(", ")}
                      </span>
                    )}
                  </div>
                  {skill.description && (
                    <p className="text-[12px] text-muted-foreground">{skill.description}</p>
                  )}
                </div>
                <Switch
                  aria-label={`Attach ${skill.name}`}
                  checked={skillRefs.includes(skill.name)}
                  onCheckedChange={(on) => toggleSkill(skill.name, on)}
                />
              </div>
            ))}
            {skillRefs
              .filter((name) => !availableSkills.some((s) => s.name === name))
              .map((name) => (
                <div key={name} className="flex items-center justify-between gap-3">
                  <div className="text-[12.5px] font-medium">
                    {name}
                    <span className="ml-1.5 text-[11px] font-normal text-amber-600">
                      not found in your namespace
                    </span>
                  </div>
                  <Switch
                    aria-label={`Detach ${name}`}
                    checked
                    onCheckedChange={(on) => toggleSkill(name, on)}
                  />
                </div>
              ))}
          </div>
        )}
      </FormSection>

      <FormSection
        title="Conversation memory"
        description="A new message continues the same conversation if the last activity was within this window; otherwise a fresh run starts. DMs are one conversation per person, channels one per thread."
      >
        <Field id="slack-idle-minutes" label="Memory window (minutes)" className="max-w-xs">
          <Input
            id="slack-idle-minutes"
            type="number"
            min={0}
            value={sessionIdleMinutes || ""}
            onChange={(e) => setSessionIdleMinutes(Math.max(0, Number(e.target.value) || 0))}
            placeholder="720 (12h default)"
          />
        </Field>
      </FormSection>

      <FormSection
        title="Runtime profile"
        description="Sandbox permissions and network egress for the agent's runs."
        aside={
          <Switch
            aria-label="Create or update RuntimeProfile"
            checked={configureRuntimeProfile}
            onCheckedChange={setConfigureRuntimeProfile}
          />
        }
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <Field id="slack-image" label="Runtime image">
            <RuntimeImagePicker id="slack-image" value={image} onChange={setImage} />
          </Field>
          <Field id="slack-runtime-ref" label="RuntimeProfile ref">
            <Input
              id="slack-runtime-ref"
              value={runtimeProfileRef}
              onChange={(e) => setRuntimeProfileRef(e.target.value)}
              placeholder="support-runtime"
              className="font-mono"
            />
          </Field>
          {configureRuntimeProfile && (
            <>
              <Field id="slack-permission-mode" label="Permission mode">
                <Select
                  value={permissionMode}
                  onValueChange={(v) => setPermissionMode((v as string) ?? "workspace-write")}
                >
                  <SelectTrigger id="slack-permission-mode" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="read-only">read-only</SelectItem>
                    <SelectItem value="workspace-write">workspace-write</SelectItem>
                    <SelectItem value="danger-full-access">danger-full-access</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field id="slack-egress-mode" label="Network egress">
                <Select
                  value={egressMode}
                  onValueChange={(v) => setEgressMode((v as string) ?? "unrestricted")}
                >
                  <SelectTrigger id="slack-egress-mode" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="unrestricted">unrestricted</SelectItem>
                    <SelectItem value="restricted">restricted</SelectItem>
                    <SelectItem value="disabled">disabled</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </>
          )}
        </div>
        <Field
          id="slack-additional-repo-0"
          label="Additional repositories"
          hint="Extra git repositories cloned into every run this agent creates (under repos/<name>)."
        >
          <RepoUrlListInput
            idPrefix="slack-additional-repo"
            value={additionalRepoUrls}
            onChange={setAdditionalRepoUrls}
          />
        </Field>
      </FormSection>

      <FormSection
        title="MCP policy"
        description="Which MCP servers the agent may reach."
        aside={
          <Switch
            aria-label="Create or update MCPPolicy"
            checked={configureMcpPolicy}
            onCheckedChange={setConfigureMcpPolicy}
          />
        }
      >
        <div className="grid gap-3 sm:grid-cols-2">
          <Field id="slack-mcp-ref" label="MCPPolicy ref">
            <Input
              id="slack-mcp-ref"
              value={mcpPolicyRef}
              onChange={(e) => setMcpPolicyRef(e.target.value)}
              placeholder="support-policy"
              className="font-mono"
            />
          </Field>
          {configureMcpPolicy && (
            <>
              <Field id="slack-mcp-default" label="Default action">
                <Select
                  value={mcpPolicyDefaultAction}
                  onValueChange={(v) => setMcpPolicyDefaultAction((v as string) ?? "Deny")}
                >
                  <SelectTrigger id="slack-mcp-default" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="Deny">Deny</SelectItem>
                    <SelectItem value="Allow">Allow</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
              <Field
                id="slack-mcp-servers"
                label="Allowed MCP servers"
                hint="Comma-separated server names."
              >
                <Input
                  id="slack-mcp-servers"
                  value={mcpPolicyAllowedServers}
                  onChange={(e) => setMcpPolicyAllowedServers(e.target.value)}
                  placeholder="fetch, github"
                  className="font-mono"
                />
              </Field>
            </>
          )}
        </div>
      </FormSection>

      <FormSection title="Availability">
        <SwitchRow
          id="slack-suspend"
          label="Suspend connector"
          hint="Disconnects from Slack without deleting configuration or history."
          checked={suspend}
          onCheckedChange={setSuspend}
        />
      </FormSection>

      <div className="flex flex-wrap items-center gap-2 border-t border-border/60 pt-4">
        <Button
          size="sm"
          onClick={() => void save()}
          disabled={
            saving ||
            (connectionMode === "workspace" && (!selectedWorkspace || !slackUserId.trim()))
          }
        >
          {saving ? "Saving…" : "Save changes"}
        </Button>
        {error && (
          <p className="text-[12px] text-destructive" role="alert">
            {error}
          </p>
        )}
      </div>

      <FormSection
        title="Danger zone"
        description="Deleting removes the Slack agent, its tokens, and its draft history. The Slack app itself is not touched."
      >
        <Button variant="destructive" size="sm" onClick={() => setConfirmDelete(true)}>
          Delete agent
        </Button>
      </FormSection>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete “${agent.name}”?`}
        description="This removes the Slack agent, its tokens, and its draft history. The Slack app itself is not touched."
        confirmLabel="Delete agent"
        destructive
        onConfirm={() => remove()}
      />
    </section>
  );
}
