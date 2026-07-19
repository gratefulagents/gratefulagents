import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Check, ChevronRight, Copy, MessageSquare, Plus } from "lucide-react";

import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft } from "@/lib/status";
import { slackAgentStatus } from "@/lib/slackStatus";
import { buildSlackManifest } from "@/lib/slackManifest";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ListRowSkeleton } from "@/components/ui/list-state";
import { filterByQuery } from "@/components/ui/list-search";
import { Switch } from "@/components/ui/switch";
import { ResourceListPage } from "@/components/list-page";
import { SlackAgentCreateDialog } from "@/components/SlackAgentCreateDialog";
import { toast } from "@/components/ui/toaster";
import type { SlackAgent, SlackWorkspace } from "@/rpc/platform/service_pb";

function workspaceKey(ws: { namespace: string; name: string }): string {
  return `${ws.namespace}/${ws.name}`;
}

/* ── Agent rows ───────────────────────────────────────────────── */

function AgentRow({ agent }: { agent: SlackAgent }) {
  const status = slackAgentStatus(agent);
  const ids = [agent.teamId, agent.botUserId].filter(Boolean).join(" · ");
  return (
    <li>
      <Link
        to={`/slack/${agent.namespace}/${agent.name}`}
        className={cn(
          "surface-card group flex items-center justify-between gap-3 p-3.5 transition-colors",
          "hover:bg-muted/40",
        )}
      >
        <div className="min-w-0 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate text-[13.5px] font-medium">{agent.name}</span>
            <span
              className={cn(
                "inline-flex h-5 items-center gap-1.5 rounded-full pl-1.5 pr-2 text-[11px] font-medium whitespace-nowrap select-none",
                toneSoft[status.tone],
              )}
            >
              <span className="relative inline-flex size-1.5 rounded-full bg-current">
                {agent.connected && !agent.suspended && (
                  <span className="absolute inset-0 rounded-full bg-current opacity-60 motion-safe:animate-ping" />
                )}
              </span>
              {status.label}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-[11.5px] text-muted-foreground">
            <span>
              {agent.workspaceRefName
                ? `shared workspace app · ${agent.workspaceRefName}`
                : "dedicated app"}
            </span>
            {ids && (
              <>
                <span aria-hidden className="text-muted-foreground/40">·</span>
                <span className="font-mono text-[11px]">{ids}</span>
              </>
            )}
            {(agent.model || agent.provider) && (
              <>
                <span aria-hidden className="text-muted-foreground/40">·</span>
                <span className="font-mono text-[11px]">
                  {[agent.model, agent.provider].filter(Boolean).join(" · ")}
                </span>
              </>
            )}
          </div>
          {agent.lastError && (
            <div className="truncate text-[11.5px] text-[color:var(--tone-danger-fg)]" title={agent.lastError}>
              {agent.lastError}
            </div>
          )}
        </div>
        <ChevronRight className="size-4 shrink-0 text-muted-foreground/50 transition-colors group-hover:text-foreground" />
      </Link>
    </li>
  );
}

/* ── Shared workspace apps ────────────────────────────────────── */

function SavedPill() {
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

/**
 * Create/manage shared workspace apps: one Slack app the whole workspace
 * installs once; teammates join it from the agent create flow instead of
 * creating their own apps. Deliberately quiet — collapsed by default.
 */
function SlackWorkspacesSection({
  workspaces,
  onChanged,
}: {
  workspaces: SlackWorkspace[];
  onChanged: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [botToken, setBotToken] = useState("");
  const [appToken, setAppToken] = useState("");
  const [teamId, setTeamId] = useState("");
  const [suspend, setSuspend] = useState(false);
  const [saving, setSaving] = useState(false);
  const [copied, setCopied] = useState(false);
  const [confirmDeleteWs, setConfirmDeleteWs] = useState(false);

  const selected = workspaces.find((ws) => ws.name === name.trim() && ws.mine);

  function edit(ws: SlackWorkspace) {
    setName(ws.name);
    setTeamId(ws.teamId || "");
    setSuspend(ws.suspended);
    setBotToken("");
    setAppToken("");
  }

  function copyManifest() {
    void navigator.clipboard?.writeText(buildSlackManifest(name.trim() || "Team Agent")).then(() => {
      setCopied(true);
      toast.success("Manifest copied — create the app from it, install once, paste tokens here");
      window.setTimeout(() => setCopied(false), 1500);
    });
  }

  async function saveWorkspace() {
    setSaving(true);
    try {
      await client.updateSlackWorkspace({
        name: name.trim(),
        botToken: botToken.trim(),
        appToken: appToken.trim(),
        teamId: teamId.trim(),
        suspend,
      });
      toast.success(selected ? "Workspace app saved" : "Workspace app created");
      setBotToken("");
      setAppToken("");
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save workspace app");
    } finally {
      setSaving(false);
    }
  }

  async function removeWorkspace() {
    try {
      await client.deleteSlackWorkspace({ name: name.trim() });
      toast.success(`Deleted workspace app “${name.trim()}”`);
      setName("");
      setTeamId("");
      setSuspend(false);
      onChanged();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to delete workspace app");
    }
  }

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <section className="rounded-[8px] border border-border/60">
        <CollapsibleTrigger
          render={
            <button
              type="button"
              className="flex w-full items-center gap-2 px-3.5 py-2.5 text-left transition-colors hover:bg-muted/40"
            />
          }
        >
          <ChevronRight
            className={cn(
              "size-3.5 shrink-0 text-muted-foreground transition-transform duration-[var(--dur-fast)]",
              open && "rotate-90",
            )}
          />
          <span className="text-[13px] font-medium">Shared workspace apps</span>
          <span className="ml-auto font-mono text-[10.5px] text-muted-foreground/60">
            {workspaces.length === 0
              ? "none yet"
              : `${workspaces.length} app${workspaces.length === 1 ? "" : "s"}`}
          </span>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <div className="space-y-4 border-t border-border/60 p-3.5 sm:p-4">
            <p className="max-w-[72ch] text-[11.5px] leading-relaxed text-muted-foreground">
              One Slack app for the whole workspace: an admin creates it from the manifest, installs
              it once, and everyone joins it from the agent create flow — no per-user apps or
              tokens. A single connector serves all members and routes messages by Slack user ID.
              Keep the app non-distributed so it can only ever live in your workspace.
            </p>

            {workspaces.length > 0 && (
              <ul className="space-y-1.5">
                {workspaces.map((ws) => (
                  <li
                    key={workspaceKey(ws)}
                    className="flex flex-wrap items-center justify-between gap-2 rounded-[8px] border border-border/60 px-3 py-2"
                  >
                    <div className="min-w-0">
                      <div className="flex items-center gap-2 text-[12.5px] font-medium">
                        {ws.name}
                        <span
                          className={cn(
                            "inline-flex h-[18px] items-center rounded-full px-1.5 text-[10.5px] font-medium",
                            ws.suspended
                              ? toneSoft.neutral
                              : ws.ready
                                ? toneSoft.success
                                : toneSoft.warning,
                          )}
                        >
                          {ws.suspended ? "Suspended" : ws.ready ? "Ready" : "Pending"}
                        </span>
                        {ws.mine && (
                          <span className="text-[10.5px] font-normal text-muted-foreground">
                            yours
                          </span>
                        )}
                      </div>
                      <div className="mt-0.5 font-mono text-[11px] text-muted-foreground">
                        {(ws.resolvedTeamId || ws.teamId || "team unresolved") +
                          ` · ${ws.memberCount} member${ws.memberCount === 1 ? "" : "s"}`}
                      </div>
                      {ws.lastError && (
                        <div className="mt-0.5 text-[11px] text-[color:var(--tone-danger-fg)]">
                          {ws.lastError}
                        </div>
                      )}
                    </div>
                    {ws.mine && (
                      <Button variant="outline" size="xs" onClick={() => edit(ws)}>
                        Edit
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}

            <div className="grid gap-3 sm:grid-cols-2">
              <Field id="slack-ws-name" label="Workspace app name">
                <Input
                  id="slack-ws-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="acme-agents"
                />
              </Field>
              <Field
                id="slack-ws-team"
                label="Team ID (optional pin)"
                hint="When set, the connector refuses events from any other Slack workspace."
              >
                <Input
                  id="slack-ws-team"
                  value={teamId}
                  onChange={(e) => setTeamId(e.target.value)}
                  placeholder="T0123ABC"
                  className="font-mono"
                />
              </Field>
              <Field
                id="slack-ws-bot-token"
                label="Bot token"
                aside={selected?.botTokenPresent ? <SavedPill /> : undefined}
              >
                <Input
                  id="slack-ws-bot-token"
                  type="password"
                  value={botToken}
                  onChange={(e) => setBotToken(e.target.value)}
                  placeholder={selected?.botTokenPresent ? "•••• saved — enter to replace" : "xoxb-..."}
                  autoComplete="off"
                  className="font-mono"
                />
              </Field>
              <Field
                id="slack-ws-app-token"
                label="App-level token"
                aside={selected?.appTokenPresent ? <SavedPill /> : undefined}
              >
                <Input
                  id="slack-ws-app-token"
                  type="password"
                  value={appToken}
                  onChange={(e) => setAppToken(e.target.value)}
                  placeholder={selected?.appTokenPresent ? "•••• saved — enter to replace" : "xapp-..."}
                  autoComplete="off"
                  className="font-mono"
                />
              </Field>
            </div>
            <div className="flex items-start justify-between gap-4">
              <div className="min-w-0">
                <Label htmlFor="slack-ws-suspend" className="text-[12.5px]">
                  Suspend connector
                </Label>
                <p className="mt-0.5 max-w-[56ch] text-[11px] leading-relaxed text-muted-foreground">
                  Disconnects every member from Slack without deleting configuration.
                </p>
              </div>
              <Switch id="slack-ws-suspend" checked={suspend} onCheckedChange={setSuspend} />
            </div>
            <div className="flex flex-wrap items-center gap-2 border-t border-border/60 pt-4">
              <Button size="sm" onClick={() => void saveWorkspace()} disabled={saving || !name.trim()}>
                {saving ? "Saving…" : selected ? "Save workspace app" : "Create workspace app"}
              </Button>
              <Button variant="outline" size="sm" onClick={copyManifest} disabled={!name.trim()}>
                {copied ? <Check data-icon="inline-start" /> : <Copy data-icon="inline-start" />}
                Copy app manifest
              </Button>
              {selected && (
                <Button variant="destructive" size="sm" onClick={() => setConfirmDeleteWs(true)}>
                  Delete
                </Button>
              )}
            </div>
          </div>
        </CollapsibleContent>

        <ConfirmDialog
          open={confirmDeleteWs}
          onOpenChange={setConfirmDeleteWs}
          title={`Delete workspace app “${name.trim()}”?`}
          description="Members must leave it first. The Slack app itself is not touched."
          confirmLabel="Delete workspace app"
          destructive
          onConfirm={() => removeWorkspace()}
        />
      </section>
    </Collapsible>
  );
}

/* ── Page ─────────────────────────────────────────────────────── */

export function SlackAgentsPage() {
  const navigate = useNavigate();
  const [agents, setAgents] = useState<SlackAgent[]>([]);
  const [namespace, setNamespace] = useState("");
  const [workspaces, setWorkspaces] = useState<SlackWorkspace[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  const loadAgents = useCallback(async () => {
    try {
      const resp = await client.listSlackAgents({});
      setNamespace(resp.namespace);
      setAgents([...(resp.agents ?? [])].sort((a, b) => a.name.localeCompare(b.name)));
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load Slack agents");
    } finally {
      setLoading(false);
    }
  }, []);

  const loadWorkspaces = useCallback(async () => {
    try {
      const resp = await client.listSlackWorkspaces({});
      setWorkspaces((resp.workspaces ?? []) as SlackWorkspace[]);
    } catch {
      setWorkspaces([]);
    }
  }, []);

  useEffect(() => {
    // Async loaders flip loading/error state after awaiting; the rule can't
    // see through the indirection.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void loadAgents();
    void loadWorkspaces();
  }, [loadAgents, loadWorkspaces]);

  const filtered = filterByQuery(agents, query, (a) => [
    a.name,
    a.teamId,
    a.botUserId,
    a.model,
    a.provider,
    a.slackUserId,
    a.workspaceRefName,
  ]);

  const createDialog = (
    <SlackAgentCreateDialog
      namespace={namespace}
      workspaces={workspaces}
      trigger={
        <Button size="sm">
          <Plus data-icon="inline-start" />
          New agent
        </Button>
      }
      onCreated={(agent) => {
        void loadAgents();
        navigate(`/slack/${agent.namespace || namespace}/${agent.name}`);
      }}
    />
  );

  return (
    <div className="space-y-5">
      <ResourceListPage
        title="Slack"
        description="Personal agents that chat as you in Slack — DMs, channels, and drafted replies."
        query={query}
        onQuery={setQuery}
        searchPlaceholder="Search Slack agents…"
        loading={loading}
        error={error}
        onRetry={() => {
          setLoading(true);
          void loadAgents();
        }}
        empty={filtered.length === 0}
        skeleton={<ListRowSkeleton rows={3} />}
        emptyIcon={<MessageSquare className="size-6" />}
        emptyTitle={query ? `No matches for "${query}"` : "No Slack agents yet"}
        emptyDescription={
          query
            ? "Clear the search to see all Slack agents."
            : "Create your first agent: connect a Slack app (or join a shared workspace app), pick a model, and it starts chatting as you."
        }
        emptyAction={!query ? createDialog : undefined}
        actions={createDialog}
      >
        <ul className="space-y-2">
          {filtered.map((agent) => (
            <AgentRow key={`${agent.namespace}/${agent.name}`} agent={agent} />
          ))}
        </ul>
      </ResourceListPage>

      <SlackWorkspacesSection workspaces={workspaces} onChanged={() => void loadWorkspaces()} />
    </div>
  );
}
