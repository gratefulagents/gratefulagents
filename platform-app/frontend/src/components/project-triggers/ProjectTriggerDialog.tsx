import { useState } from "react";
import type { ComponentType } from "react";
import { CalendarClock, ChevronRight, GitBranch, Layers, Loader2, MessageSquare, Plus, Settings } from "lucide-react";

import { CRON_PRESETS, describeCron, FieldHint, ProviderCard } from "@/components/project-triggers/connection-guides";
import type { ProjectConnection, ProjectTrigger, TriggerSource } from "@/components/project-triggers/types";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cn } from "@/lib/utils";

// ─── Types ────────────────────────────────────────────────────────────────────

type FormState = {
  name: string;
  type: TriggerSource;
  connectionRef: string;
  repository: string;
  issueEvents: boolean;
  commentEvents: boolean;
  channel: string;
  channelReplyMode: "require-approval" | "auto";
  commanders: string;
  sessionIdleMinutes: string;
  schedule: string;
  timeZone: string;
  prompt: string;
  team: string;
  project: string;
};

// ─── Helpers ──────────────────────────────────────────────────────────────────

function field(source: Record<string, unknown> | undefined, name: string): string {
  const value = source?.[name];
  return typeof value === "string" ? value : "";
}

function stringList(source: Record<string, unknown> | undefined, name: string): string[] {
  const value = source?.[name];
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === "string") : [];
}

function positiveNumber(source: Record<string, unknown> | undefined, name: string): string {
  const value = source?.[name];
  return typeof value === "number" && value > 0 ? String(value) : "";
}

function splitSlackUserIds(value: string): string[] {
  return [...new Set(value.split(",").map((item) => item.trim()).filter(Boolean))];
}

function sourceFor(trigger: ProjectTrigger): TriggerSource {
  const type = trigger.type.toLowerCase();
  if (type.includes("slack")) return "slack";
  if (type.includes("cron") || type.includes("schedule")) return "cron";
  if (type.includes("linear")) return "linear";
  return "github";
}

function initialForm(trigger?: ProjectTrigger): FormState {
  const type = trigger ? sourceFor(trigger) : "github";
  const github = trigger?.github;
  const slack = trigger?.slack;
  const replyMode = field(slack, "channelReplyMode");
  return {
    name: trigger?.name ?? "",
    type,
    connectionRef:
      field(github, "connectionRef") ||
      field(trigger?.slack, "connectionRef") ||
      field(trigger?.linear, "connectionRef"),
    repository: [field(github, "owner"), field(github, "repo")].filter(Boolean).join("/"),
    issueEvents: Boolean(github?.issues),
    commentEvents: Boolean(github?.comments),
    channel: field(slack, "channel"),
    channelReplyMode: replyMode === "auto" ? "auto" : "require-approval",
    commanders: stringList(slack, "commanders").join(", "),
    sessionIdleMinutes: positiveNumber(slack, "sessionIdleMinutes"),
    schedule: field(trigger?.cron, "schedule"),
    timeZone: field(trigger?.cron, "timeZone"),
    prompt: field(trigger?.cron, "prompt"),
    team: field(trigger?.linear, "teamId"),
    project: field(trigger?.linear, "projectId"),
  };
}

function buildTrigger(form: FormState, existing?: ProjectTrigger): ProjectTrigger {
  const source = form.type;
  const [owner = "", repo = ""] = form.repository.trim().split("/", 2);
  return {
    name: form.name.trim(),
    type: source,
    enabled: existing?.enabled ?? true,
    github:
      source === "github"
        ? {
            connectionRef: form.connectionRef.trim(),
            owner,
            repo,
            issues: form.issueEvents,
            comments: form.commentEvents,
          }
        : undefined,
    slack:
      source === "slack"
        ? {
            connectionRef: form.connectionRef.trim(),
            channel: form.channel.trim(),
            channelReplyMode: form.channelReplyMode,
            commanders: splitSlackUserIds(form.commanders),
            sessionIdleMinutes: form.sessionIdleMinutes
              ? Number(form.sessionIdleMinutes)
              : undefined,
          }
        : undefined,
    cron:
      source === "cron"
        ? {
            schedule: form.schedule.trim(),
            timeZone: form.timeZone.trim() || "UTC",
            prompt: form.prompt.trim(),
          }
        : undefined,
    linear:
      source === "linear"
        ? {
            connectionRef: form.connectionRef.trim(),
            teamId: form.team.trim(),
            projectId: form.project.trim(),
          }
        : undefined,
  };
}

// ─── Public component ─────────────────────────────────────────────────────────

export function ProjectTriggerDialog({
  trigger,
  open,
  onOpenChange,
  onSave,
  connections,
  onManageConnections,
}: {
  trigger?: ProjectTrigger;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onSave: (trigger: ProjectTrigger) => Promise<void>;
  connections: ProjectConnection[];
  onManageConnections: () => void;
}) {
  const editing = Boolean(trigger);
  const [step, setStep] = useState<"type" | "details">(editing ? "details" : "type");
  const [form, setForm] = useState<FormState>(() => initialForm(trigger));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function update<K extends keyof FormState>(key: K, value: FormState[K]) {
    setForm((prev) => ({ ...prev, [key]: value }));
    if (key === "name" || key === "connectionRef") setError(null);
  }

  function handleClose(next: boolean) {
    if (!next) {
      setStep(editing ? "details" : "type");
      setError(null);
    }
    onOpenChange(next);
  }

  function selectType(type: TriggerSource) {
    const firstMatch = connections.find((c) => c.type === type)?.name ?? "";
    setForm((prev) => ({ ...prev, type, connectionRef: firstMatch }));
    setStep("details");
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!form.name.trim()) {
      setError("Give this trigger a name.");
      return;
    }
    if (form.type !== "cron" && !form.connectionRef) {
      setError("Choose a connection.");
      return;
    }
    if (form.type === "slack") {
      const channel = form.channel.trim();
      if (channel && !/^[CGD][A-Z0-9]+$/.test(channel)) {
        setError("Enter a Slack conversation ID starting with C, G, or D (not a #channel name), or leave it empty.");
        return;
      }
      if (splitSlackUserIds(form.commanders).some((id) => !/^[UW][A-Z0-9]+$/.test(id))) {
        setError("Allowed commanders must be Slack user IDs starting with U or W.");
        return;
      }
      if (form.sessionIdleMinutes) {
        const idle = Number(form.sessionIdleMinutes);
        if (!Number.isInteger(idle) || idle <= 0) {
          setError("Conversation memory must be a whole number greater than zero.");
          return;
        }
      }
    }
    setSaving(true);
    setError(null);
    try {
      await onSave(buildTrigger(form, trigger));
      onOpenChange(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Failed to save trigger");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent className="flex max-h-[92vh] w-full max-w-xl flex-col gap-0 overflow-hidden p-0 sm:max-w-xl">
        {step === "type" ? (
          <TypeChoiceView onSelect={selectType} />
        ) : (
          <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
            <DialogHeader className="space-y-1 border-b px-5 py-4 sm:px-6 sm:py-5">
              <div className="flex items-center gap-2">
                {!editing && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    onClick={() => setStep("type")}
                    aria-label="Back"
                  >
                    <ChevronRight className="size-4 rotate-180" />
                  </Button>
                )}
                <span className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
                  <Plus className="size-4" />
                </span>
                <DialogTitle className="text-base">
                  {editing ? `Edit ${trigger?.name}` : "New trigger"}
                </DialogTitle>
              </div>
              <DialogDescription>
                {editing
                  ? "Update this project entry point."
                  : "Configure how this trigger fires."}
              </DialogDescription>
            </DialogHeader>

            <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-5 py-5 sm:px-6">
              {form.type === "github" && (
                <GitHubDetails
                  form={form}
                  connections={connections}
                  editing={editing}
                  update={update}
                  onManageConnections={onManageConnections}
                />
              )}
              {form.type === "slack" && (
                <SlackDetails
                  form={form}
                  connections={connections}
                  editing={editing}
                  update={update}
                  onManageConnections={onManageConnections}
                />
              )}
              {form.type === "cron" && (
                <CronDetails form={form} update={update} />
              )}
              {form.type === "linear" && (
                <LinearDetails
                  form={form}
                  connections={connections}
                  editing={editing}
                  update={update}
                  onManageConnections={onManageConnections}
                />
              )}

              {/* Name field */}
              <div>
                <Label className="mb-1.5 block text-[12.5px] font-medium">Trigger name</Label>
                <Input
                  value={form.name}
                  onChange={(e) => update("name", e.target.value)}
                  autoFocus={editing}
                  placeholder="my-trigger"
                  autoComplete="off"
                  aria-label="Trigger name"
                />
              </div>

              <div className="rounded-md border border-border/70 bg-muted/35 px-3 py-2.5 text-[11.5px] leading-relaxed text-muted-foreground">
                <span className="font-medium text-foreground">Inherited project defaults</span>
                <span className="mt-0.5 block">
                  Repository, model, runtime, credentials, and policies remain managed by this
                  project.
                </span>
              </div>

              {error && (
                <p role="alert" className="text-[12px] text-destructive">
                  {error}
                </p>
              )}
            </div>

            <div className="flex flex-col-reverse gap-2 border-t px-5 py-4 sm:flex-row sm:justify-end sm:px-6">
              <DialogClose render={<Button type="button" variant="ghost" size="sm" disabled={saving} />}>
                Cancel
              </DialogClose>
              <Button type="submit" size="sm" disabled={saving}>
                {saving && <Loader2 className="size-4 animate-spin" />}
                {saving ? "Saving…" : editing ? "Save changes" : "Create trigger"}
              </Button>
            </div>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

// ─── Type choice step ─────────────────────────────────────────────────────────

const TRIGGER_TYPES: {
  id: TriggerSource;
  icon: ComponentType<{ className?: string }>;
  label: string;
  description: string;
}[] = [
  {
    id: "github",
    icon: GitBranch,
    label: "GitHub",
    description: "React to issues, pull requests, and comments in a repository",
  },
  {
    id: "slack",
    icon: MessageSquare,
    label: "Slack",
    description: "Start agent runs from messages in a channel",
  },
  {
    id: "cron",
    icon: CalendarClock,
    label: "Scheduled",
    description: "Run on a recurring schedule — hourly, daily, or custom cron",
  },
  {
    id: "linear",
    icon: Layers,
    label: "Linear",
    description: "Kick off runs when Linear issues change in a team or project",
  },
];

function TypeChoiceView({ onSelect }: { onSelect: (type: TriggerSource) => void }) {
  return (
    <>
      <DialogHeader className="border-b px-5 py-4 sm:px-6 sm:py-5">
        <DialogTitle className="text-base">New trigger</DialogTitle>
        <DialogDescription>
          Choose what will start an agent run for this project.
        </DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-2.5 px-5 py-5 sm:px-6">
        {TRIGGER_TYPES.map((t) => (
          <ProviderCard
            key={t.id}
            icon={t.icon}
            label={t.label}
            description={t.description}
            onClick={() => onSelect(t.id)}
          />
        ))}
      </div>
    </>
  );
}

// ─── Detail panels ────────────────────────────────────────────────────────────

function ConnectionSelect({
  className,
  label,
  value,
  connections,
  emptyLabel,
  onChange,
  onManageConnections,
}: {
  className?: string;
  label: string;
  value: string;
  connections: ProjectConnection[];
  emptyLabel: string;
  onChange: (value: string) => void;
  onManageConnections: () => void;
}) {
  if (connections.length === 0) {
    return (
      <div className={cn("space-y-1.5", className)}>
        <Label className="block text-[12.5px] font-medium">{label}</Label>
        <div className="flex items-center gap-2 rounded-md border border-border/60 bg-muted/30 px-3 py-2.5">
          <p className="flex-1 text-[12.5px] text-muted-foreground">{emptyLabel}</p>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="shrink-0"
            onClick={onManageConnections}
          >
            <Settings className="size-3.5" />
            Add connection
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className={cn("space-y-1.5", className)}>
      <Label className="block text-[12.5px] font-medium">{label}</Label>
      <div className="flex gap-2">
        <Select value={value} onValueChange={(next) => onChange(next ?? "")}>
          <SelectTrigger className="w-full" aria-label={label}>
            <SelectValue placeholder="Choose connection" />
          </SelectTrigger>
          <SelectContent>
            {connections.map((c) => (
              <SelectItem key={c.name} value={c.name}>
                {c.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="shrink-0"
          onClick={onManageConnections}
        >
          Manage
        </Button>
      </div>
    </div>
  );
}

function GitHubDetails({
  form,
  connections,
  editing,
  update,
  onManageConnections,
}: {
  form: FormState;
  connections: ProjectConnection[];
  editing: boolean;
  update: <K extends keyof FormState>(key: K, value: FormState[K]) => void;
  onManageConnections: () => void;
}) {
  const matching = connections.filter((c) => c.type === "github");
  return (
    <div className="space-y-4">
      <ConnectionSelect
        label="Connection"
        value={form.connectionRef}
        connections={matching}
        emptyLabel="No GitHub connection yet — add one to continue."
        onChange={(v) => update("connectionRef", v)}
        onManageConnections={onManageConnections}
      />
      <div>
        <Label className="mb-1.5 block text-[12.5px] font-medium">Repository</Label>
        <Input
          value={form.repository}
          onChange={(e) => update("repository", e.target.value)}
          placeholder="owner/repository"
          autoFocus={!editing}
          autoComplete="off"
          aria-label="Repository"
        />
        <FieldHint>Format: owner/repository — e.g. acme/payments</FieldHint>
      </div>
      <fieldset>
        <legend className="mb-2 text-[12.5px] font-medium">Events to react to</legend>
        <div className="flex flex-col gap-2">
          <label className="flex items-center gap-2.5 text-[12.5px]">
            <input
              type="checkbox"
              checked={form.issueEvents}
              onChange={(e) => update("issueEvents", e.target.checked)}
              className="h-4 w-4 rounded border-input accent-primary"
              aria-label="React to issue events"
            />
            <span>Issues — opened, edited, labeled, closed</span>
          </label>
          <label className="flex items-center gap-2.5 text-[12.5px]">
            <input
              type="checkbox"
              checked={form.commentEvents}
              onChange={(e) => update("commentEvents", e.target.checked)}
              className="h-4 w-4 rounded border-input accent-primary"
              aria-label="React to comment events"
            />
            <span>Comments — issue and pull-request comments</span>
          </label>
        </div>
      </fieldset>
    </div>
  );
}

function SlackDetails({
  form,
  connections,
  editing,
  update,
  onManageConnections,
}: {
  form: FormState;
  connections: ProjectConnection[];
  editing: boolean;
  update: <K extends keyof FormState>(key: K, value: FormState[K]) => void;
  onManageConnections: () => void;
}) {
  const matching = connections.filter((c) => c.type === "slack");
  return (
    <div className="space-y-4">
      <ConnectionSelect
        label="Connection"
        value={form.connectionRef}
        connections={matching}
        emptyLabel="No Slack connection yet — add one to continue."
        onChange={(v) => update("connectionRef", v)}
        onManageConnections={onManageConnections}
      />
      <div>
        <Label className="mb-1.5 block text-[12.5px] font-medium">
          Conversation ID <span className="font-normal text-muted-foreground">(optional)</span>
        </Label>
        <Input
          value={form.channel}
          onChange={(e) => update("channel", e.target.value)}
          placeholder="C0123456789"
          autoFocus={!editing}
          autoComplete="off"
          aria-label="Slack conversation ID"
        />
        <FieldHint>
          Use Slack&apos;s channel ID, not its #name (Channel details → About → Copy channel ID). Leave empty
          to respond wherever the bot is @mentioned. Invite the bot first with{" "}
          <code className="rounded bg-muted px-1 text-[11px]">/invite @your-bot</code>.
        </FieldHint>
      </div>
      <div>
        <Label className="mb-1.5 block text-[12.5px] font-medium">Allowed commanders</Label>
        <Input
          value={form.commanders}
          onChange={(e) => update("commanders", e.target.value)}
          placeholder="U0123ABC, U0456DEF"
          autoComplete="off"
          aria-label="Allowed Slack commanders"
        />
        <FieldHint>
          Additional Slack user IDs that may @mention the agent. Leave empty for owner only.
        </FieldHint>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">Channel replies</Label>
          <Select
            value={form.channelReplyMode}
            onValueChange={(value) => update("channelReplyMode", value === "auto" ? "auto" : "require-approval")}
          >
            <SelectTrigger className="w-full" aria-label="Slack channel reply mode">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="require-approval">Require owner approval</SelectItem>
              <SelectItem value="auto">Post directly</SelectItem>
            </SelectContent>
          </Select>
          <FieldHint>DM replies are always direct. Approval is the safe default for shared channels.</FieldHint>
        </div>
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">
            Conversation memory <span className="font-normal text-muted-foreground">(minutes)</span>
          </Label>
          <Input
            type="number"
            min={1}
            step={1}
            value={form.sessionIdleMinutes}
            onChange={(e) => update("sessionIdleMinutes", e.target.value)}
            placeholder="720"
            inputMode="numeric"
            aria-label="Slack conversation memory minutes"
          />
          <FieldHint>Leave empty for the 12-hour default. A fresh run starts after this idle time.</FieldHint>
        </div>
      </div>
    </div>
  );
}

function CronDetails({
  form,
  update,
}: {
  form: FormState;
  update: <K extends keyof FormState>(key: K, value: FormState[K]) => void;
}) {
  const description = describeCron(form.schedule);

  return (
    <div className="space-y-4">
      <div>
        <Label className="mb-2 block text-[12.5px] font-medium">Schedule presets</Label>
        <div className="flex flex-wrap gap-1.5">
          {CRON_PRESETS.map((preset) => (
            <button
              key={preset.value}
              type="button"
              onClick={() => update("schedule", preset.value)}
              className={cn(
                "rounded-md border px-2.5 py-1 text-[12px] transition-colors",
                form.schedule === preset.value
                  ? "border-primary/40 bg-primary/5 font-medium text-primary"
                  : "border-border/70 text-muted-foreground hover:bg-muted/40",
              )}
              aria-label={`Preset: ${preset.label}`}
            >
              {preset.label}
            </button>
          ))}
        </div>
      </div>
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">Schedule (cron)</Label>
          <Input
            className="font-mono"
            value={form.schedule}
            onChange={(e) => update("schedule", e.target.value)}
            placeholder="0 9 * * *"
            autoComplete="off"
            aria-label="Cron schedule"
          />
          {description && (
            <p className="mt-1 text-[11.5px] text-muted-foreground">{description}</p>
          )}
        </div>
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">Time zone</Label>
          <Input
            value={form.timeZone}
            onChange={(e) => update("timeZone", e.target.value)}
            placeholder="UTC"
            autoComplete="off"
            aria-label="Time zone"
          />
          <FieldHint>IANA name, e.g. Europe/Berlin or America/New_York.</FieldHint>
        </div>
      </div>
      <div>
        <Label className="mb-1.5 block text-[12.5px] font-medium">Prompt</Label>
        <textarea
          value={form.prompt}
          onChange={(e) => update("prompt", e.target.value)}
          placeholder="What should this scheduled run do?"
          rows={3}
          aria-label="Prompt"
          className="w-full rounded-md border border-input bg-background px-3 py-2 text-[13px] leading-relaxed placeholder:text-muted-foreground/60 focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
        />
        <FieldHint>Instructions the agent follows every time this trigger fires.</FieldHint>
      </div>
    </div>
  );
}

function LinearDetails({
  form,
  connections,
  editing,
  update,
  onManageConnections,
}: {
  form: FormState;
  connections: ProjectConnection[];
  editing: boolean;
  update: <K extends keyof FormState>(key: K, value: FormState[K]) => void;
  onManageConnections: () => void;
}) {
  const matching = connections.filter((c) => c.type === "linear");
  return (
    <div className="space-y-4">
      <ConnectionSelect
        className="sm:col-span-2"
        label="Connection"
        value={form.connectionRef}
        connections={matching}
        emptyLabel="No Linear connection yet — add one to continue."
        onChange={(v) => update("connectionRef", v)}
        onManageConnections={onManageConnections}
      />
      <div className="grid gap-4 sm:grid-cols-2">
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">Team ID</Label>
          <Input
            value={form.team}
            onChange={(e) => update("team", e.target.value)}
            placeholder="Engineering"
            autoFocus={!editing}
            autoComplete="off"
            aria-label="Linear team ID"
          />
          <FieldHint>
            Found in Linear under Settings → Teams → your team name/ID.
          </FieldHint>
        </div>
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">
            Project ID <span className="font-normal text-muted-foreground">(optional)</span>
          </Label>
          <Input
            value={form.project}
            onChange={(e) => update("project", e.target.value)}
            placeholder="Roadmap project"
            autoComplete="off"
            aria-label="Linear project ID"
          />
          <FieldHint>
            Leave empty to watch all issues in the team.
          </FieldHint>
        </div>
      </div>
    </div>
  );
}
