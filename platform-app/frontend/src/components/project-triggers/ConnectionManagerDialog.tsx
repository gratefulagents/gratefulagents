import { useState } from "react";
import type { ComponentType } from "react";
import { ChevronDown, ChevronRight, GitBranch, Layers, Loader2, MessageSquare, Pencil, Plus, Trash2 } from "lucide-react";

import {
  ExternalLinkButton,
  FieldHint,
  HowToHeader,
  HowToStep,
  isDnsLabel,
  ProviderCard,
  STORED_PLACEHOLDER,
} from "@/components/project-triggers/connection-guides";
import type { ConnectionType, ProjectConnection } from "@/components/project-triggers/types";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

type GitHubMode = "token" | "app";

type FormFields = {
  name: string;
  nameEdited: boolean;
  githubMode: GitHubMode;
  // raw paste fields (write-only to server)
  token: string;
  privateKey: string;
  appId: string;
  installationId: string;
  botToken: string;
  appToken: string;
  // shared optional fields
  teamId: string;
  apiKey: string;
  workspaceId: string;
  // advanced: existing secret refs
  tokenSecret: string;
  privateKeySecret: string;
  tokensSecret: string;
  apiKeySecret: string;
};

function defaultForm(_provider: ConnectionType, connection?: ProjectConnection): FormFields {
  const editing = Boolean(connection);
  return {
    name: connection?.name ?? "",
    nameEdited: editing,
    githubMode: connection?.github?.appId ? "app" : "token",
    token: "",
    privateKey: "",
    appId: connection?.github?.appId?.toString() ?? "",
    installationId: connection?.github?.installationId?.toString() ?? "",
    botToken: "",
    appToken: "",
    teamId: connection?.slack?.teamId ?? "",
    apiKey: "",
    workspaceId: connection?.linear?.workspaceId ?? "",
    tokenSecret: connection?.github?.tokenSecret ?? "",
    privateKeySecret: connection?.github?.privateKeySecret ?? "",
    tokensSecret: connection?.slack?.tokensSecret ?? "",
    apiKeySecret: connection?.linear?.apiKeySecret ?? "",
  };
}

function buildConnection(form: FormFields, provider: ConnectionType): ProjectConnection {
  const connection: ProjectConnection = { name: form.name.trim(), type: provider };
  if (provider === "github") {
    connection.github =
      form.githubMode === "token"
        ? {
            ...(form.token
              ? { token: form.token }
              : { tokenSecret: form.tokenSecret || undefined }),
          }
        : {
            appId: form.appId ? BigInt(form.appId) : undefined,
            installationId: form.installationId ? BigInt(form.installationId) : undefined,
            ...(form.privateKey
              ? { privateKey: form.privateKey }
              : { privateKeySecret: form.privateKeySecret || undefined }),
          };
  } else if (provider === "slack") {
    connection.slack = {
      teamId: form.teamId.trim() || undefined,
      ...(form.botToken ? { botToken: form.botToken } : {}),
      ...(form.appToken ? { appToken: form.appToken } : {}),
      // Keep the existing tokens Secret reference so pasting a single token on
      // edit merges into it instead of demanding a complete new pair.
      ...(form.tokensSecret ? { tokensSecret: form.tokensSecret } : {}),
    };
  } else if (provider === "linear") {
    connection.linear = {
      workspaceId: form.workspaceId.trim() || undefined,
      ...(form.apiKey
        ? { apiKey: form.apiKey }
        : { apiKeySecret: form.apiKeySecret || undefined }),
    };
  }
  return connection;
}

export function ConnectionManagerDialog({
  open,
  onOpenChange,
  connections,
  onCreate,
  onUpdate,
  onDelete,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  connections: ProjectConnection[];
  onCreate: (connection: ProjectConnection) => Promise<void>;
  onUpdate: (connection: ProjectConnection, existing: ProjectConnection) => Promise<void>;
  onDelete: (connection: ProjectConnection) => Promise<void>;
}) {
  type View = "list" | "pick-provider" | "form";
  const [view, setView] = useState<View>("list");
  const [provider, setProvider] = useState<ConnectionType>("github");
  const [editingConnection, setEditingConnection] = useState<ProjectConnection | undefined>();
  const [deleting, setDeleting] = useState<ProjectConnection | undefined>();

  function reset() {
    setView("list");
    setEditingConnection(undefined);
  }

  function handleClose(next: boolean) {
    if (!next) reset();
    onOpenChange(next);
  }

  function startEdit(connection: ProjectConnection) {
    setProvider(connection.type);
    setEditingConnection(connection);
    setView("form");
  }

  function startCreate() {
    setProvider("github");
    setEditingConnection(undefined);
    setView("pick-provider");
  }

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent className="flex max-h-[92vh] w-full max-w-xl flex-col gap-0 overflow-hidden p-0 sm:max-w-xl">
        {view === "list" && (
          <ListView
            connections={connections}
            onEdit={startEdit}
            onDelete={setDeleting}
            onNew={startCreate}
            onClose={() => handleClose(false)}
          />
        )}
        {view === "pick-provider" && (
          <PickProviderView
            onBack={() => setView("list")}
            onSelect={(p) => {
              setProvider(p);
              setView("form");
            }}
          />
        )}
        {view === "form" && (
          <ConnectionFormView
            provider={provider}
            connection={editingConnection}
            onBack={() => setView(editingConnection ? "list" : "pick-provider")}
            onSave={async (connection) => {
              if (editingConnection) {
                await onUpdate(connection, editingConnection);
              } else {
                await onCreate(connection);
              }
              reset();
            }}
          />
        )}
      </DialogContent>
      <ConfirmDialog
        open={Boolean(deleting)}
        onOpenChange={(next) => !next && setDeleting(undefined)}
        title={`Delete ${deleting?.name ?? "connection"}?`}
        description="Triggers that reference this connection will stop working."
        confirmLabel="Delete"
        destructive
        onConfirm={() => (deleting ? onDelete(deleting) : Promise.resolve())}
      />
    </Dialog>
  );
}

// ─── List View ────────────────────────────────────────────────────────────────

function ListView({
  connections,
  onEdit,
  onDelete,
  onNew,
  onClose,
}: {
  connections: ProjectConnection[];
  onEdit: (c: ProjectConnection) => void;
  onDelete: (c: ProjectConnection) => void;
  onNew: () => void;
  onClose: () => void;
}) {
  return (
    <>
      <DialogHeader className="border-b px-5 py-4 sm:px-6 sm:py-5">
        <DialogTitle className="text-base">Manage connections</DialogTitle>
        <DialogDescription>
          Reusable credentials for this namespace&apos;s project triggers.
        </DialogDescription>
      </DialogHeader>
      <div className="min-h-0 flex-1 space-y-2 overflow-y-auto px-5 py-4 sm:px-6">
        {connections.length === 0 ? (
          <p className="text-[12.5px] text-muted-foreground">
            No connections yet. Add one to power GitHub, Slack, or Linear triggers.
          </p>
        ) : (
          connections.map((c) => (
            <div key={c.name} className="flex items-center gap-3 rounded-md border px-3 py-2.5">
              <div className="min-w-0 flex-1">
                <p className="truncate text-[13px] font-medium">{c.name}</p>
                <p className="text-[11.5px] capitalize text-muted-foreground">{c.type}</p>
              </div>
              <Button
                size="icon-xs"
                variant="ghost"
                aria-label={`Edit ${c.name}`}
                onClick={() => onEdit(c)}
              >
                <Pencil className="size-4" />
              </Button>
              <Button
                size="icon-xs"
                variant="ghost"
                aria-label={`Delete ${c.name}`}
                onClick={() => onDelete(c)}
              >
                <Trash2 className="size-4" />
              </Button>
            </div>
          ))
        )}
      </div>
      <div className="flex justify-between border-t px-5 py-4 sm:px-6">
        <Button size="sm" onClick={onNew}>
          <Plus className="size-4" />
          New connection
        </Button>
        <Button size="sm" variant="ghost" onClick={onClose}>
          Done
        </Button>
      </div>
    </>
  );
}

// ─── Pick Provider View ───────────────────────────────────────────────────────

const PROVIDERS: {
  id: ConnectionType;
  icon: ComponentType<{ className?: string }>;
  label: string;
  description: string;
}[] = [
  {
    id: "github",
    icon: GitBranch,
    label: "GitHub",
    description: "React to issues and comments in a repository",
  },
  {
    id: "slack",
    icon: MessageSquare,
    label: "Slack",
    description: "Chat with your agents from a Slack channel",
  },
  {
    id: "linear",
    icon: Layers,
    label: "Linear",
    description: "Turn Linear issues into agent runs",
  },
];

function PickProviderView({
  onBack,
  onSelect,
}: {
  onBack: () => void;
  onSelect: (provider: ConnectionType) => void;
}) {
  return (
    <>
      <DialogHeader className="border-b px-5 py-4 sm:px-6 sm:py-5">
        <div className="flex items-center gap-2">
          <Button type="button" variant="ghost" size="icon-xs" onClick={onBack} aria-label="Back">
            <ChevronRight className="size-4 rotate-180" />
          </Button>
          <DialogTitle className="text-base">New connection</DialogTitle>
        </div>
        <DialogDescription>Choose the service this connection authenticates to.</DialogDescription>
      </DialogHeader>
      <div className="flex flex-col gap-2.5 px-5 py-5 sm:px-6">
        {PROVIDERS.map((p) => (
          <ProviderCard
            key={p.id}
            icon={p.icon}
            label={p.label}
            description={p.description}
            onClick={() => onSelect(p.id)}
          />
        ))}
      </div>
    </>
  );
}

// ─── Connection Form View ─────────────────────────────────────────────────────

function ConnectionFormView({
  provider,
  connection,
  onBack,
  onSave,
}: {
  provider: ConnectionType;
  connection?: ProjectConnection;
  onBack: () => void;
  onSave: (connection: ProjectConnection) => Promise<void>;
}) {
  const editing = Boolean(connection);
  const [form, setForm] = useState<FormFields>(() => defaultForm(provider, connection));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [nameError, setNameError] = useState<string | null>(null);
  const [advancedOpen, setAdvancedOpen] = useState(false);

  function update<K extends keyof FormFields>(key: K, value: FormFields[K]) {
    setForm((prev) => ({ ...prev, [key]: value }));
    if (key === "name") setNameError(null);
  }

  function updateWithAutoName<K extends keyof FormFields>(key: K, value: FormFields[K]) {
    setForm((prev) => {
      const next = { ...prev, [key]: value };
      if (!prev.nameEdited) next.name = provider;
      return next;
    });
  }

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const name = form.name.trim();
    if (!name) {
      setNameError("Give this connection a name.");
      return;
    }
    if (!isDnsLabel(name)) {
      setNameError("Name must be lowercase letters, numbers, and hyphens only.");
      return;
    }
    // Slack: when using raw tokens, both are required for a new connection
    if (provider === "slack" && !editing) {
      const hasRaw = form.botToken || form.appToken;
      const hasAdvanced = form.tokensSecret;
      if (hasRaw && (!form.botToken || !form.appToken)) {
        setError(
          "Paste both the bot token (xoxb-) and the app token (xapp-) to create a Slack connection.",
        );
        return;
      }
      if (!hasRaw && !hasAdvanced) {
        setError("Paste a bot token and app token, or enter a tokens secret name in Advanced.");
        return;
      }
    }
    setSaving(true);
    setError(null);
    try {
      await onSave(buildConnection(form, provider));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Failed to save connection");
    } finally {
      setSaving(false);
    }
  }

  const providerLabel = PROVIDERS.find((p) => p.id === provider)?.label ?? provider;

  return (
    <form onSubmit={submit} className="flex min-h-0 flex-1 flex-col">
      <DialogHeader className="border-b px-5 py-4 sm:px-6 sm:py-5">
        <div className="flex items-center gap-2">
          <Button type="button" variant="ghost" size="icon-xs" onClick={onBack} aria-label="Back">
            <ChevronRight className="size-4 rotate-180" />
          </Button>
          <DialogTitle className="text-base">
            {editing ? `Edit ${connection?.name}` : `Connect ${providerLabel}`}
          </DialogTitle>
        </div>
        <DialogDescription>
          {editing
            ? "Update credentials for this connection."
            : "Follow the steps below, then paste your credentials."}
        </DialogDescription>
      </DialogHeader>

      <div className="min-h-0 flex-1 space-y-5 overflow-y-auto px-5 py-5 sm:px-6">
        {provider === "github" && (
          <GitHubGuide form={form} editing={editing} update={updateWithAutoName} />
        )}
        {provider === "slack" && (
          <SlackGuide form={form} editing={editing} update={updateWithAutoName} />
        )}
        {provider === "linear" && (
          <LinearGuide form={form} editing={editing} update={updateWithAutoName} />
        )}

        {/* Name field */}
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">Connection name</Label>
          <Input
            value={form.name}
            onChange={(e) => {
              const v = e.target.value;
              setNameError(null);
              setForm((prev) => ({ ...prev, name: v, nameEdited: true }));
            }}
            disabled={editing}
            placeholder={provider}
            autoComplete="off"
            aria-label="Connection name"
          />
          {nameError ? (
            <p role="alert" className="mt-1 text-[12px] text-destructive">
              {nameError}
            </p>
          ) : (
            <FieldHint>
              {editing
                ? "Name cannot be changed after creation."
                : `Lowercase letters, numbers, and hyphens (e.g. \u201c${provider}\u201d).`}
            </FieldHint>
          )}
        </div>

        {/* Advanced: secret refs for power users */}
        <div className="rounded-md border border-border/60">
          <button
            type="button"
            className="flex w-full items-center justify-between px-3.5 py-2.5 text-left"
            onClick={() => setAdvancedOpen((o) => !o)}
            aria-expanded={advancedOpen}
          >
            <span className="text-[12px] font-medium text-muted-foreground">Advanced</span>
            <ChevronDown
              className={cn(
                "size-3.5 text-muted-foreground/60 transition-transform",
                advancedOpen && "rotate-180",
              )}
            />
          </button>
          {advancedOpen && (
            <div className="space-y-3.5 border-t px-3.5 pb-4 pt-3.5">
              <p className="text-[11.5px] text-muted-foreground">
                Reference existing Kubernetes Secret names instead of pasting raw credentials.
              </p>
              {provider === "github" && form.githubMode === "token" && (
                <div>
                  <Label className="mb-1.5 block text-[12px] text-muted-foreground">
                    Token secret name
                  </Label>
                  <Input
                    value={form.tokenSecret}
                    onChange={(e) => update("tokenSecret", e.target.value)}
                    placeholder="github-token"
                    autoComplete="off"
                  />
                </div>
              )}
              {provider === "github" && form.githubMode === "app" && (
                <div>
                  <Label className="mb-1.5 block text-[12px] text-muted-foreground">
                    Private key secret name
                  </Label>
                  <Input
                    value={form.privateKeySecret}
                    onChange={(e) => update("privateKeySecret", e.target.value)}
                    placeholder="github-app-private-key"
                    autoComplete="off"
                  />
                </div>
              )}
              {provider === "slack" && (
                <div>
                  <Label className="mb-1.5 block text-[12px] text-muted-foreground">
                    Tokens secret name
                  </Label>
                  <Input
                    value={form.tokensSecret}
                    onChange={(e) => update("tokensSecret", e.target.value)}
                    placeholder="slack-tokens"
                    autoComplete="off"
                  />
                </div>
              )}
              {provider === "linear" && (
                <div>
                  <Label className="mb-1.5 block text-[12px] text-muted-foreground">
                    API key secret name
                  </Label>
                  <Input
                    value={form.apiKeySecret}
                    onChange={(e) => update("apiKeySecret", e.target.value)}
                    placeholder="linear-api-key"
                    autoComplete="off"
                  />
                </div>
              )}
            </div>
          )}
        </div>

        {error && (
          <p role="alert" className="text-[12px] text-destructive">
            {error}
          </p>
        )}
      </div>

      <div className="flex flex-col-reverse gap-2 border-t px-5 py-4 sm:flex-row sm:justify-end sm:px-6">
        <Button type="button" variant="ghost" size="sm" disabled={saving} onClick={onBack}>
          Back
        </Button>
        <Button type="submit" size="sm" disabled={saving}>
          {saving && <Loader2 className="size-4 animate-spin" />}
          {saving ? "Saving…" : editing ? "Save changes" : "Create connection"}
        </Button>
      </div>
    </form>
  );
}

// ─── GitHub Guide ─────────────────────────────────────────────────────────────

function GitHubGuide({
  form,
  editing,
  update,
}: {
  form: FormFields;
  editing: boolean;
  update: <K extends keyof FormFields>(key: K, value: FormFields[K]) => void;
}) {
  const hasTokenSecret = Boolean(form.tokenSecret);
  const hasPrivKeySecret = Boolean(form.privateKeySecret);

  return (
    <div className="space-y-4">
      {/* Mode toggle */}
      <div>
        <p className="mb-2 text-[12px] font-medium text-muted-foreground">Authentication mode</p>
        <div className="flex gap-2">
          {(["token", "app"] as const).map((mode) => (
            <button
              key={mode}
              type="button"
              onClick={() => update("githubMode", mode)}
              className={cn(
                "rounded-md border px-3 py-1.5 text-[12.5px] transition-colors",
                form.githubMode === mode
                  ? "border-primary/40 bg-primary/5 font-medium text-primary"
                  : "border-border/70 text-muted-foreground hover:bg-muted/40",
              )}
            >
              {mode === "token" ? "Personal access token" : "GitHub App"}
            </button>
          ))}
        </div>
      </div>

      {form.githubMode === "token" ? (
        <div className="space-y-3.5">
          <div className="space-y-2">
            <HowToHeader>How to get a token</HowToHeader>
            <HowToStep n={1}>
              Open{" "}
              <ExternalLinkButton href="https://github.com/settings/personal-access-tokens/new">
                github.com/settings/personal-access-tokens/new
              </ExternalLinkButton>
            </HowToStep>
            <HowToStep n={2}>
              Grant: <strong>Repository → Issues (read/write)</strong>,{" "}
              <strong>Pull requests (read/write)</strong>, and optionally{" "}
              <strong>Contents (read/write)</strong> if your agent should push commits.
            </HowToStep>
            <HowToStep n={3}>
              Click <strong>Generate token</strong> and copy it.
            </HowToStep>
            <HowToStep n={4}>Paste it below.</HowToStep>
          </div>
          <div>
            <Label className="mb-1.5 block text-[12.5px] font-medium">Token</Label>
            <Input
              type="password"
              value={form.token}
              onChange={(e) => update("token", e.target.value)}
              placeholder={editing && hasTokenSecret ? STORED_PLACEHOLDER : "github_pat_…"}
              autoComplete="new-password"
              aria-label="GitHub personal access token"
            />
            {editing && hasTokenSecret && !form.token && (
              <FieldHint>Leave empty to keep the stored token.</FieldHint>
            )}
          </div>
        </div>
      ) : (
        <div className="space-y-3.5">
          <div className="space-y-2">
            <HowToHeader>How to set up a GitHub App</HowToHeader>
            <HowToStep n={1}>
              Go to{" "}
              <ExternalLinkButton href="https://github.com/settings/apps/new">
                github.com/settings/apps/new
              </ExternalLinkButton>{" "}
              and create your app. Note the <strong>App ID</strong> on the General settings page.
            </HowToStep>
            <HowToStep n={2}>
              Install the app on your account or organization. The installation ID is the numeric
              suffix in the URL:{" "}
              <code className="rounded bg-muted px-1 text-[11px]">
                github.com/settings/installations/&lt;ID&gt;
              </code>
            </HowToStep>
            <HowToStep n={3}>
              On the General settings page, scroll to <strong>Private keys</strong>, click{" "}
              <strong>Generate a private key</strong>, and download the{" "}
              <code className="rounded bg-muted px-1 text-[11px]">.pem</code> file.
            </HowToStep>
            <HowToStep n={4}>Open the .pem file in a text editor and paste its full contents below.</HowToStep>
          </div>
          <div className="grid gap-3.5 sm:grid-cols-2">
            <div>
              <Label className="mb-1.5 block text-[12.5px] font-medium">App ID</Label>
              <Input
                inputMode="numeric"
                value={form.appId}
                onChange={(e) => update("appId", e.target.value)}
                placeholder="123456"
                aria-label="GitHub App ID"
              />
            </div>
            <div>
              <Label className="mb-1.5 block text-[12.5px] font-medium">Installation ID</Label>
              <Input
                inputMode="numeric"
                value={form.installationId}
                onChange={(e) => update("installationId", e.target.value)}
                placeholder="78901234"
                aria-label="GitHub App installation ID"
              />
            </div>
            <div className="sm:col-span-2">
              <Label className="mb-1.5 block text-[12.5px] font-medium">Private key (PEM)</Label>
              <textarea
                value={form.privateKey}
                onChange={(e) => update("privateKey", e.target.value)}
                placeholder={
                  editing && hasPrivKeySecret ? STORED_PLACEHOLDER : "Paste the .pem file contents here"
                }
                rows={6}
                aria-label="GitHub App private key PEM"
                className="w-full rounded-md border border-input bg-background px-3 py-2 font-mono text-[11.5px] leading-relaxed placeholder:text-muted-foreground/60 focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
              />
              {editing && hasPrivKeySecret && !form.privateKey && (
                <FieldHint>Leave empty to keep the stored private key.</FieldHint>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// ─── Slack Guide ──────────────────────────────────────────────────────────────

function SlackGuide({
  form,
  editing,
  update,
}: {
  form: FormFields;
  editing: boolean;
  update: <K extends keyof FormFields>(key: K, value: FormFields[K]) => void;
}) {
  const hasTokensSecret = Boolean(form.tokensSecret);
  const botTokenWarn = form.botToken && !form.botToken.startsWith("xoxb-");
  const appTokenWarn = form.appToken && !form.appToken.startsWith("xapp-");

  return (
    <div className="space-y-3.5">
      <div className="space-y-2">
        <HowToHeader>How to create a Slack app</HowToHeader>
        <HowToStep n={1}>
          Go to{" "}
          <ExternalLinkButton href="https://api.slack.com/apps">
            api.slack.com/apps
          </ExternalLinkButton>{" "}
          and click <strong>Create New App → From scratch</strong>.
        </HowToStep>
        <HowToStep n={2}>
          Enable <strong>Socket Mode</strong> (Settings → Socket Mode) and generate an app-level
          token with the{" "}
          <code className="rounded bg-muted px-1 text-[11px]">connections:write</code> scope. This
          is your <strong>xapp-</strong> token.
        </HowToStep>
        <HowToStep n={3}>
          Under <strong>OAuth &amp; Permissions → Bot Token Scopes</strong>, add:{" "}
          <code className="rounded bg-muted px-1 text-[11px]">app_mentions:read</code>,{" "}
          <code className="rounded bg-muted px-1 text-[11px]">channels:history</code>,{" "}
          <code className="rounded bg-muted px-1 text-[11px]">chat:write</code>,{" "}
          <code className="rounded bg-muted px-1 text-[11px]">im:history</code>,{" "}
          <code className="rounded bg-muted px-1 text-[11px]">im:read</code>,{" "}
          <code className="rounded bg-muted px-1 text-[11px]">im:write</code>,{" "}
          <code className="rounded bg-muted px-1 text-[11px]">reactions:write</code>. Then click{" "}
          <strong>Install to workspace</strong> to get your <strong>xoxb-</strong> bot token.
        </HowToStep>
        <HowToStep n={4}>Paste both tokens below.</HowToStep>
      </div>

      <div className="space-y-3">
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">Bot token (xoxb-)</Label>
          <Input
            type="password"
            value={form.botToken}
            onChange={(e) => update("botToken", e.target.value)}
            placeholder={editing && hasTokensSecret ? STORED_PLACEHOLDER : "xoxb-…"}
            autoComplete="new-password"
            aria-label="Slack bot token"
          />
          {botTokenWarn && (
            <p className="mt-1 text-[11.5px] text-amber-600 dark:text-amber-400">
              Bot tokens typically start with xoxb-.
            </p>
          )}
          {editing && hasTokensSecret && !form.botToken && !form.appToken && (
            <FieldHint>Leave empty to keep stored tokens.</FieldHint>
          )}
        </div>
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">App token (xapp-)</Label>
          <Input
            type="password"
            value={form.appToken}
            onChange={(e) => update("appToken", e.target.value)}
            placeholder={editing && hasTokensSecret ? STORED_PLACEHOLDER : "xapp-…"}
            autoComplete="new-password"
            aria-label="Slack app token"
          />
          {appTokenWarn && (
            <p className="mt-1 text-[11.5px] text-amber-600 dark:text-amber-400">
              App-level tokens typically start with xapp-.
            </p>
          )}
        </div>
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">
            Team / workspace ID{" "}
            <span className="font-normal text-muted-foreground">(optional)</span>
          </Label>
          <Input
            value={form.teamId}
            onChange={(e) => update("teamId", e.target.value)}
            placeholder="T0123456789"
            autoComplete="off"
            aria-label="Slack team ID"
          />
          <FieldHint>
            Starts with T — find it in your workspace URL or at slack.com/account/settings.
          </FieldHint>
        </div>
      </div>
    </div>
  );
}

// ─── Linear Guide ─────────────────────────────────────────────────────────────

function LinearGuide({
  form,
  editing,
  update,
}: {
  form: FormFields;
  editing: boolean;
  update: <K extends keyof FormFields>(key: K, value: FormFields[K]) => void;
}) {
  const hasApiKeySecret = Boolean(form.apiKeySecret);

  return (
    <div className="space-y-3.5">
      <div className="space-y-2">
        <HowToHeader>How to get a Linear API key</HowToHeader>
        <HowToStep n={1}>
          Open{" "}
          <ExternalLinkButton href="https://linear.app/settings/account/security">
            linear.app → Settings → Security &amp; access
          </ExternalLinkButton>
        </HowToStep>
        <HowToStep n={2}>
          Under <strong>Personal API keys</strong>, click <strong>Create key</strong>, give it a
          label, and copy the generated key.
        </HowToStep>
        <HowToStep n={3}>Paste it below.</HowToStep>
      </div>

      <div className="space-y-3">
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">API key</Label>
          <Input
            type="password"
            value={form.apiKey}
            onChange={(e) => update("apiKey", e.target.value)}
            placeholder={editing && hasApiKeySecret ? STORED_PLACEHOLDER : "lin_api_…"}
            autoComplete="new-password"
            aria-label="Linear API key"
          />
          {editing && hasApiKeySecret && !form.apiKey && (
            <FieldHint>Leave empty to keep the stored key.</FieldHint>
          )}
        </div>
        <div>
          <Label className="mb-1.5 block text-[12.5px] font-medium">
            Workspace ID <span className="font-normal text-muted-foreground">(optional)</span>
          </Label>
          <Input
            value={form.workspaceId}
            onChange={(e) => update("workspaceId", e.target.value)}
            placeholder="my-team"
            autoComplete="off"
            aria-label="Linear workspace ID"
          />
          <FieldHint>Your workspace URL slug, e.g. linear.app/my-team.</FieldHint>
        </div>
      </div>
    </div>
  );
}
