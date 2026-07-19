import { useState } from "react";
import { Link } from "react-router-dom";
import { CalendarClock, GitBranch, Layers, Loader2, MessageSquare, MoreHorizontal, Pencil, Plus, Trash2 } from "lucide-react";

import { ConnectionManagerDialog } from "@/components/project-triggers/ConnectionManagerDialog";
import { ProjectTriggerDialog } from "@/components/project-triggers/ProjectTriggerDialog";
import { triggerSource, type ProjectConnection, type ProjectTrigger, type ProjectTriggerClient } from "@/components/project-triggers/types";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Switch } from "@/components/ui/switch";
import { client } from "@/lib/client";
import { formatScheduleTime } from "@/lib/format";
import { writeLastProject } from "@/lib/lastProject";
import { cn } from "@/lib/utils";

const triggerClient = client as unknown as ProjectTriggerClient;

type TriggerStatus = "ready" | "degraded" | "applying" | "disabled";

function values(...sources: Array<Record<string, unknown> | undefined>): Record<string, unknown> {
  return Object.assign({}, ...sources.map((source) => source ?? {}));
}

function text(source: Record<string, unknown>, ...names: string[]): string {
  for (const name of names) {
    const value = source[name];
    if (typeof value === "string" && value) return value;
  }
  return "";
}

function timestamp(source: Record<string, unknown>, ...names: string[]): bigint {
  for (const name of names) {
    const value = source[name];
    if (typeof value === "bigint") return value;
    if (typeof value === "number") return BigInt(value);
    if (value && typeof value === "object" && "seconds" in value && typeof value.seconds === "bigint") return value.seconds;
  }
  return 0n;
}

function statusFor(trigger: ProjectTrigger): TriggerStatus {
  if (trigger.enabled === false) return "disabled";
  const ready = trigger.conditions?.find((condition) => condition.type === "Ready");
  const status = text(ready ?? {}, "status").toLowerCase();
  const reason = text(ready ?? {}, "reason", "message").toLowerCase();
  if (!ready || trigger.observedGeneration === 0n || reason.includes("apply") || reason.includes("pending")) return "applying";
  if (status !== "true" || trigger.lastError) return "degraded";
  return "ready";
}

function summaryFor(trigger: ProjectTrigger): string {
  const source = triggerSource(trigger);
  if (source === "github") {
    const details = values(trigger.github);
    const repository = text(details, "repository") || [text(details, "owner"), text(details, "repo")].filter(Boolean).join("/");
    return [repository, text(details, "events") || [details.issues === true && "issues", details.comments === true && "comments"].filter(Boolean).join(", ")].filter(Boolean).join(" · ") || "GitHub events";
  }
  if (source === "slack") return text(values(trigger.slack), "channel", "channelId") || "Slack messages";
  if (source === "cron") {
    const details = values(trigger.cron);
    return [text(details, "schedule", "expression"), text(details, "timeZone", "timezone")].filter(Boolean).join(" · ") || "Scheduled run";
  }
  const details = values(trigger.linear);
  return [text(details, "team", "teamId"), text(details, "project", "projectId")].filter(Boolean).join(" · ") || "Linear updates";
}

function activityFor(trigger: ProjectTrigger): string {
  const last = timestamp({ lastActivityTime: trigger.lastActivityTime }, "lastActivityTime");
  const next = timestamp({ nextActivityTime: trigger.nextActivityTime }, "nextActivityTime");
  return [last > 0n ? `last ${formatScheduleTime(last)}` : "no activity yet", next > 0n ? `next ${formatScheduleTime(next)}` : ""].filter(Boolean).join(" · ");
}

function SourceIcon({ trigger }: { trigger: ProjectTrigger }) {
  const source = triggerSource(trigger);
  if (source === "slack") return <MessageSquare className="size-4" aria-hidden />;
  if (source === "cron") return <CalendarClock className="size-4" aria-hidden />;
  if (source === "linear") return <Layers className="size-4" aria-hidden />;
  return <GitBranch className="size-4" aria-hidden />;
}

function StatusBadge({ status }: { status: TriggerStatus }) {
  const tone = {
    ready: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
    degraded: "bg-amber-500/10 text-amber-700 dark:text-amber-400",
    applying: "bg-sky-500/10 text-sky-700 dark:text-sky-400",
    disabled: "bg-muted text-muted-foreground",
  }[status];
  return <span className={cn("rounded-full px-2 py-0.5 text-[10.5px] font-medium capitalize", tone)}>{status}</span>;
}

export function ProjectTriggerRail({
  namespace,
  projectName,
  triggers,
  canEdit,
  onChanged,
}: {
  namespace: string;
  projectName: string;
  triggers: ProjectTrigger[];
  canEdit: boolean;
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState<ProjectTrigger | undefined>();
  const [createOpen, setCreateOpen] = useState(false);
  const [deleting, setDeleting] = useState<ProjectTrigger | undefined>();
  const [changing, setChanging] = useState<string | null>(null);
  const [connections, setConnections] = useState<ProjectConnection[]>([]);
  const [connectionsOpen, setConnectionsOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function loadConnections() {
    const response = await triggerClient.listConnections({ namespace });
    setConnections(response.connections ?? []);
  }

  async function openConnections() {
    try {
      await loadConnections();
      setConnectionsOpen(true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Failed to load connections");
    }
  }

  async function openTriggerDialog() {
    try {
      await loadConnections();
      setCreateOpen(true);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Failed to load connections");
    }
  }

  async function saveConnection(connection: ProjectConnection, existing?: ProjectConnection) {
    setError(null);
    if (existing) {
      await triggerClient.updateConnection({ namespace, name: existing.name, connection });
    } else {
      await triggerClient.createConnection({ namespace, name: connection.name, connection });
    }
    await loadConnections();
  }

  async function removeConnection(connection: ProjectConnection) {
    setError(null);
    await triggerClient.deleteConnection({ namespace, name: connection.name });
    await loadConnections();
  }

  async function save(trigger: ProjectTrigger) {
    setError(null);
    if (editing) {
      await triggerClient.updateProjectTrigger({ namespace, project: projectName, name: editing.name, trigger });
    } else {
      await triggerClient.createProjectTrigger({ namespace, project: projectName, name: trigger.name, trigger });
    }
    onChanged();
  }

  async function setEnabled(trigger: ProjectTrigger, enabled: boolean) {
    setChanging(trigger.name);
    setError(null);
    try {
      await triggerClient.setProjectTriggerEnabled({ namespace, project: projectName, name: trigger.name, enabled });
      onChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Failed to update trigger");
    } finally {
      setChanging(null);
    }
  }

  async function remove(trigger: ProjectTrigger) {
    setError(null);
    try {
      await triggerClient.deleteProjectTrigger({ namespace, project: projectName, name: trigger.name });
      onChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Failed to delete trigger");
      throw cause;
    }
  }

  return (
    <section aria-labelledby="project-entry-points" className="border-y border-border/70 py-4 sm:py-5">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <div>
          <h2 id="project-entry-points" className="text-[13px] font-medium text-muted-foreground">Entry points</h2>
          <p className="mt-0.5 text-[11.5px] text-muted-foreground/75">Chats and automations that start work in this project.</p>
        </div>
        {canEdit && <div className="flex gap-2"><Button variant="outline" size="sm" onClick={() => void openConnections()}>Manage connections</Button><Button variant="outline" size="sm" onClick={() => void openTriggerDialog()}><Plus className="size-3.5" />New trigger</Button></div>}
      </div>

      <div className="border-l-2 border-primary/35 pl-3 sm:pl-4">
        <Link to="/" onClick={() => writeLastProject({ namespace, name: projectName })} className="group flex min-h-14 items-center gap-3 rounded-md px-2 py-2 text-left outline-none hover:bg-muted/45 focus-visible:ring-2 focus-visible:ring-ring/60">
          <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary"><MessageSquare className="size-4" aria-hidden /></span>
          <span className="min-w-0 flex-1"><span className="block text-[12.5px] font-medium">Dashboard chat</span><span className="block truncate text-[11.5px] text-muted-foreground">Start a focused conversation with this project</span></span>
          <span className="text-[11px] text-muted-foreground group-hover:text-foreground">Open</span>
        </Link>
        {triggers.map((trigger) => {
          const status = statusFor(trigger);
          const disabled = changing === trigger.name;
          return (
            <div key={trigger.name} className="flex min-h-16 flex-col gap-2 border-t border-border/55 px-2 py-2.5 sm:flex-row sm:items-center sm:gap-3">
              <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-muted text-muted-foreground"><SourceIcon trigger={trigger} /></span>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2"><span className="text-[12.5px] font-medium">{trigger.name}</span><StatusBadge status={status} /></div>
                <p className="truncate text-[11.5px] text-muted-foreground">{summaryFor(trigger)}</p>
                <p className="mt-0.5 text-[10.5px] text-muted-foreground/70">{activityFor(trigger)}</p>
              </div>
              {canEdit && <div className="flex items-center justify-end gap-1 self-end sm:self-auto">
                <Switch size="sm" checked={trigger.enabled !== false} disabled={disabled} onCheckedChange={(enabled) => void setEnabled(trigger, enabled)} aria-label={`${trigger.enabled !== false ? "Disable" : "Enable"} ${trigger.name}`} />
                {disabled && <Loader2 className="size-3 animate-spin text-muted-foreground" aria-label="Updating" />}
                <DropdownMenu>
                  <DropdownMenuTrigger render={<Button variant="ghost" size="icon-xs" aria-label={`Actions for ${trigger.name}`} />}><MoreHorizontal className="size-4" /></DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem onClick={() => setEditing(trigger)}><Pencil />Edit</DropdownMenuItem>
                    <DropdownMenuItem variant="destructive" onClick={() => setDeleting(trigger)}><Trash2 />Delete</DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>
              </div>}
            </div>
          );
        })}
      </div>
      {error && <p role="alert" className="mt-3 text-sm text-destructive">{error}</p>}

      {createOpen && <ProjectTriggerDialog open onOpenChange={setCreateOpen} onSave={save} connections={connections} onManageConnections={() => void openConnections()} />}
      {editing && <ProjectTriggerDialog trigger={editing} open onOpenChange={(open) => !open && setEditing(undefined)} onSave={save} connections={connections} onManageConnections={() => void openConnections()} />}
      <ConnectionManagerDialog
        open={connectionsOpen}
        onOpenChange={setConnectionsOpen}
        connections={connections}
        onCreate={(connection) => saveConnection(connection)}
        onUpdate={(connection, existing) => saveConnection(connection, existing)}
        onDelete={removeConnection}
      />
      <ConfirmDialog open={Boolean(deleting)} onOpenChange={(open) => !open && setDeleting(undefined)} title={`Delete ${deleting?.name ?? "trigger"}?`} description="This permanently removes the trigger. Existing runs are kept." confirmLabel="Delete" destructive onConfirm={() => deleting ? remove(deleting) : Promise.resolve()} />
    </section>
  );
}
