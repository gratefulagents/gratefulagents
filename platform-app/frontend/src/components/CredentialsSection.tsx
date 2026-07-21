import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import {
  Blocks,
  Bot,
  ChevronDown,
  GitBranch,
  KeyRound,
  Route,
  Share2,
  Sparkles,
  Zap,
} from "lucide-react";

import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft } from "@/lib/status";
import { AnthropicOAuthConnect } from "@/components/AnthropicOAuthConnect";
import { CopilotOAuthConnect } from "@/components/CopilotOAuthConnect";
import { ImportLocalCredentials } from "@/components/ImportLocalCredentials";
import { OpenAIOAuthConnect } from "@/components/OpenAIOAuthConnect";
import { SettingsSection } from "@/components/settings-section";
import {
  ShareCredentialsDialog,
  type ShareableCredential,
} from "@/components/ShareCredentialsDialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { toast } from "@/components/ui/toaster";

interface Presence {
  anthropicApiKey: boolean;
  openaiApiKey: boolean;
  openrouterApiKey: boolean;
  xaiApiKey: boolean;
  anthropicOauth: boolean;
  openaiOauth: boolean;
  copilotOauth: boolean;
  githubToken: boolean;
}

interface ServerCredentials {
  namespace: string;
  anthropicApiKeyPresent: boolean;
  openaiApiKeyPresent: boolean;
  openrouterApiKeyPresent: boolean;
  xaiApiKeyPresent: boolean;
  anthropicOauthPresent: boolean;
  openaiOauthPresent: boolean;
  copilotOauthPresent: boolean;
  githubTokenPresent: boolean;
  integrations?: { name: string; keys: string[] }[];
}

type ClearKey =
  | "anthropic-api-key"
  | "openai-api-key"
  | "openrouter-api-key"
  | "xai-api-key"
  | "anthropic-oauth"
  | "openai-oauth"
  | "copilot-oauth"
  | "github-token";

const emptyPresence: Presence = {
  anthropicApiKey: false,
  openaiApiKey: false,
  openrouterApiKey: false,
  xaiApiKey: false,
  anthropicOauth: false,
  openaiOauth: false,
  copilotOauth: false,
  githubToken: false,
};

type UpdateFields = Partial<{
  anthropicApiKey: string;
  openaiApiKey: string;
  openrouterApiKey: string;
  xaiApiKey: string;
  anthropicOauthJson: string;
  openaiOauthJson: string;
  openaiAccountId: string;
  copilotOauthJson: string;
  githubToken: string;
}>;

/**
 * "My Credentials" settings, redesigned as a provider list: each provider is
 * a row showing exactly what's saved, and expands into a focused panel with
 * the sign-in flow, an API-key field that saves on its own, and manual JSON
 * paste under an Advanced disclosure. Secrets stay write-only — the server
 * only ever reports presence.
 */
export function CredentialsSection() {
  const [namespace, setNamespace] = useState("");
  const [presence, setPresence] = useState<Presence>(emptyPresence);
  const [integrations, setIntegrations] = useState<{ name: string; keys: string[] }[]>([]);

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [shareOpen, setShareOpen] = useState(false);
  const [openProvider, setOpenProvider] = useState<string | null>(null);

  const applyCredentials = useCallback((c: ServerCredentials) => {
    setNamespace(c.namespace);
    setPresence({
      anthropicApiKey: c.anthropicApiKeyPresent,
      openaiApiKey: c.openaiApiKeyPresent,
      openrouterApiKey: c.openrouterApiKeyPresent,
      xaiApiKey: c.xaiApiKeyPresent,
      anthropicOauth: c.anthropicOauthPresent,
      openaiOauth: c.openaiOauthPresent,
      copilotOauth: c.copilotOauthPresent,
      githubToken: c.githubTokenPresent,
    });
    setIntegrations(c.integrations ?? []);
  }, []);

  const reload = useCallback(async () => {
    try {
      const c = await client.listMyCredentials({});
      applyCredentials(c);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load credentials");
    }
  }, [applyCredentials]);

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const c = await client.listMyCredentials({});
        if (active) applyCredentials(c);
      } catch (err) {
        if (active) setError(err instanceof Error ? err.message : "Failed to load credentials");
      } finally {
        if (active) setLoading(false);
      }
    })();
    return () => {
      active = false;
    };
  }, [applyCredentials]);

  /** Per-field save: sends only the given fields; empty values are ignored server-side. */
  const saveFields = useCallback(
    async (fields: UpdateFields) => {
      const c = await client.updateMyCredentials(fields);
      applyCredentials(c);
      setError(null);
      toast.success("Credential saved");
    },
    [applyCredentials],
  );

  const remove = useCallback(
    async (which: ClearKey) => {
      try {
        const c = await client.updateMyCredentials({ clear: [which] });
        applyCredentials(c);
        setError(null);
        toast.success("Credential removed");
      } catch (err) {
        toast.error(err instanceof Error ? err.message : "Failed to update credentials");
      }
    },
    [applyCredentials],
  );

  const onOAuthSaved = useCallback(
    (label: string) => (credentials: ServerCredentials) => {
      applyCredentials(credentials);
      setError(null);
      toast.success(`${label} credentials saved`);
    },
    [applyCredentials],
  );

  // Everything the user has saved, in the shape the share dialog needs. Each
  // entry copies the whole credential secret (e.g. "anthropic" carries both the
  // API key and OAuth material when both are saved).
  const shareableCredentials = useMemo<ShareableCredential[]>(() => {
    const providerDetail = (apiKey: boolean, oauth: boolean) =>
      [apiKey ? "API key" : null, oauth ? "OAuth" : null].filter(Boolean).join(" + ");
    const out: ShareableCredential[] = [];
    if (presence.anthropicApiKey || presence.anthropicOauth) {
      out.push({
        id: "anthropic",
        label: "Anthropic / Claude",
        detail: providerDetail(presence.anthropicApiKey, presence.anthropicOauth),
      });
    }
    if (presence.openaiApiKey || presence.openaiOauth) {
      out.push({
        id: "openai",
        label: "OpenAI / ChatGPT",
        detail: providerDetail(presence.openaiApiKey, presence.openaiOauth),
      });
    }
    if (presence.openrouterApiKey) {
      out.push({ id: "openrouter", label: "OpenRouter", detail: "API key" });
    }
    if (presence.xaiApiKey) {
      out.push({ id: "xai", label: "xAI / Grok", detail: "API key" });
    }
    if (presence.copilotOauth) {
      out.push({ id: "copilot", label: "GitHub Copilot", detail: "OAuth" });
    }
    if (presence.githubToken) {
      out.push({ id: "github", label: "GitHub token", detail: "token" });
    }
    for (const integration of integrations) {
      out.push({
        id: integration.name,
        label: integration.name,
        detail: integration.keys.join(", "),
      });
    }
    return out;
  }, [presence, integrations]);

  const toggle = (id: string) => setOpenProvider((current) => (current === id ? null : id));

  return (
    <div className="flex flex-col gap-5">
      <ImportLocalCredentials onImported={() => void reload()} />

      <SettingsSection
        icon={<KeyRound />}
        title="Providers"
        aside={
          <Button
            variant="outline"
            size="sm"
            onClick={() => setShareOpen(true)}
            disabled={loading || shareableCredentials.length === 0}
            title={
              shareableCredentials.length === 0
                ? "Save a credential first to share it"
                : "Copy saved credentials to a teammate"
            }
          >
            <Share2 data-icon="inline-start" />
            Share
          </Button>
        }
        description={
          <>
            Connect once; stored privately in your namespace
            {namespace ? (
              <>
                {" "}
                (<code className="font-mono">{namespace}</code>)
              </>
            ) : null}{" "}
            and selectable when you create a project.
          </>
        }
      >
        {error && (
          <p role="alert" className="text-[12px] text-destructive">
            {error}
          </p>
        )}
        <ul className="overflow-hidden rounded-lg border divide-y divide-border/60">
          <ProviderRow
            id="anthropic"
            icon={<Sparkles />}
            label="Anthropic / Claude"
            parts={[
              presence.anthropicApiKey ? "API key" : null,
              presence.anthropicOauth ? "Claude account" : null,
            ]}
            open={openProvider === "anthropic"}
            onToggle={toggle}
          >
            <AnthropicOAuthConnect onSaved={onOAuthSaved("Claude")} />
            {presence.anthropicOauth && (
              <SavedCredential
                label="Claude account (OAuth)"
                onRemove={() => void remove("anthropic-oauth")}
              />
            )}
            <SecretField
              label="API key"
              present={presence.anthropicApiKey}
              placeholder="sk-ant-..."
              onSave={(value) => saveFields({ anthropicApiKey: value })}
              onRemove={() => void remove("anthropic-api-key")}
            />
            <Advanced>
              <SecretField
                label="Claude OAuth JSON"
                hint={
                  <>
                    Paste <code className="font-mono">~/.claude/.credentials.json</code> from a
                    machine where Claude Code is signed in.
                  </>
                }
                present={presence.anthropicOauth}
                placeholder="{ … }"
                multiline
                onSave={(value) => saveFields({ anthropicOauthJson: value })}
                onRemove={() => void remove("anthropic-oauth")}
              />
            </Advanced>
          </ProviderRow>

          <ProviderRow
            id="openai"
            icon={<Bot />}
            label="OpenAI / ChatGPT"
            parts={[
              presence.openaiApiKey ? "API key" : null,
              presence.openaiOauth ? "ChatGPT account" : null,
            ]}
            open={openProvider === "openai"}
            onToggle={toggle}
          >
            <OpenAIOAuthConnect onSaved={onOAuthSaved("ChatGPT")} />
            {presence.openaiOauth && (
              <SavedCredential
                label="ChatGPT account (OAuth)"
                onRemove={() => void remove("openai-oauth")}
              />
            )}
            <SecretField
              label="API key"
              present={presence.openaiApiKey}
              placeholder="sk-..."
              onSave={(value) => saveFields({ openaiApiKey: value })}
              onRemove={() => void remove("openai-api-key")}
            />
            <Advanced>
              <SecretField
                label="OpenAI OAuth JSON"
                hint={
                  <>
                    Paste <code className="font-mono">~/.codex/auth.json</code> from a machine where
                    Codex is signed in.
                  </>
                }
                present={presence.openaiOauth}
                placeholder="{ … }"
                multiline
                onSave={(value) => saveFields({ openaiOauthJson: value })}
                onRemove={() => void remove("openai-oauth")}
              />
              <SecretField
                label="Account ID"
                hint="Optional — only needed when the OAuth JSON doesn't carry it."
                placeholder="org-… / account id"
                onSave={(value) => saveFields({ openaiAccountId: value })}
              />
            </Advanced>
          </ProviderRow>

          <ProviderRow
            id="copilot"
            icon={<GitBranch />}
            label="GitHub Copilot"
            parts={[presence.copilotOauth ? "Copilot account" : null]}
            open={openProvider === "copilot"}
            onToggle={toggle}
          >
            <CopilotOAuthConnect onSaved={onOAuthSaved("Copilot")} />
            {presence.copilotOauth && (
              <SavedCredential
                label="Copilot account (OAuth)"
                onRemove={() => void remove("copilot-oauth")}
              />
            )}
            <Advanced>
              <SecretField
                label="Copilot OAuth JSON"
                hint="Paste Copilot auth JSON or apps.json from a signed-in machine."
                present={presence.copilotOauth}
                placeholder="{ … }"
                multiline
                onSave={(value) => saveFields({ copilotOauthJson: value })}
                onRemove={() => void remove("copilot-oauth")}
              />
            </Advanced>
          </ProviderRow>

          <ProviderRow
            id="openrouter"
            icon={<Route />}
            label="OpenRouter"
            parts={[presence.openrouterApiKey ? "API key" : null]}
            open={openProvider === "openrouter"}
            onToggle={toggle}
          >
            <SecretField
              label="API key"
              present={presence.openrouterApiKey}
              placeholder="sk-or-v1-..."
              onSave={(value) => saveFields({ openrouterApiKey: value })}
              onRemove={() => void remove("openrouter-api-key")}
            />
          </ProviderRow>

          <ProviderRow
            id="xai"
            icon={<Zap />}
            label="xAI / Grok"
            parts={[presence.xaiApiKey ? "API key" : null]}
            open={openProvider === "xai"}
            onToggle={toggle}
          >
            <SecretField
              label="API key"
              present={presence.xaiApiKey}
              placeholder="xai-..."
              onSave={(value) => saveFields({ xaiApiKey: value })}
              onRemove={() => void remove("xai-api-key")}
            />
          </ProviderRow>

          <ProviderRow
            id="github"
            icon={<GitBranch />}
            label="GitHub"
            parts={[presence.githubToken ? "Personal access token" : null]}
            open={openProvider === "github"}
            onToggle={toggle}
          >
            <SecretField
              label="Personal access token"
              hint="Used by runs to clone private repositories and open pull requests."
              present={presence.githubToken}
              placeholder="ghp_... / github_pat_..."
              onSave={(value) => saveFields({ githubToken: value })}
              onRemove={() => void remove("github-token")}
            />
          </ProviderRow>
        </ul>
      </SettingsSection>

      <SettingsSection
        icon={<Blocks />}
        title="Integrations"
        description={
          <>
            Free-form named secrets for tool integrations (e.g.{" "}
            <code className="font-mono">grafana</code> with <code className="font-mono">url</code>{" "}
            and <code className="font-mono">token</code>). Skill packages reference them to
            authenticate their tools. Values are write-only.
          </>
        }
      >
        <IntegrationCredentials
          integrations={integrations}
          onChanged={(c) => {
            applyCredentials(c);
            setError(null);
          }}
        />
      </SettingsSection>

      <ShareCredentialsDialog
        open={shareOpen}
        onOpenChange={setShareOpen}
        credentials={shareableCredentials}
      />
    </div>
  );
}

/**
 * One provider in the list: a summary row (icon, name, what's saved) that
 * expands into that provider's connect/manage panel. Only one panel is open
 * at a time so the page never becomes a wall of forms.
 */
function ProviderRow({
  id,
  icon,
  label,
  parts,
  open,
  onToggle,
  children,
}: {
  id: string;
  icon: ReactNode;
  label: string;
  parts: (string | null)[];
  open: boolean;
  onToggle: (id: string) => void;
  children: ReactNode;
}) {
  const saved = parts.filter(Boolean) as string[];
  const connected = saved.length > 0;
  const panelId = `credentials-${id}-panel`;
  return (
    <li className={cn(open && "bg-muted/20")}>
      <button
        type="button"
        onClick={() => onToggle(id)}
        aria-expanded={open}
        aria-controls={panelId}
        className="flex w-full items-center gap-3 px-3.5 py-3 text-left transition-colors duration-[var(--dur-fast)] hover:bg-muted/40"
      >
        <span
          className={cn(
            "grid size-8 shrink-0 place-items-center rounded-lg [&_svg]:size-4",
            toneSoft.neutral,
          )}
        >
          {icon}
        </span>
        <span className="min-w-0 flex-1">
          <span className="block truncate text-[13px] font-medium tracking-tight">{label}</span>
          <span className="block truncate text-[11.5px] text-muted-foreground">
            {connected ? saved.join(" · ") : "Not connected"}
          </span>
        </span>
        {connected && (
          <span
            className={cn(
              "inline-flex h-[18px] shrink-0 items-center rounded-full px-1.5 text-[10.5px] font-medium select-none",
              toneSoft.success,
            )}
          >
            Connected
          </span>
        )}
        <ChevronDown
          className={cn(
            "size-4 shrink-0 text-muted-foreground transition-transform duration-[var(--dur-fast)]",
            open && "rotate-180",
          )}
        />
      </button>
      {open && (
        <div id={panelId} className="space-y-4 border-t border-border/60 px-3.5 py-4">
          {children}
        </div>
      )}
    </li>
  );
}

/** A saved OAuth credential: presence line with a remove action. */
function SavedCredential({ label, onRemove }: { label: string; onRemove: () => void }) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-md border bg-background/70 px-3 py-2">
      <span className="flex min-w-0 items-center gap-2 text-[12.5px]">
        <span
          className={cn(
            "inline-flex h-[18px] shrink-0 items-center rounded-full px-1.5 text-[10.5px] font-medium select-none",
            toneSoft.success,
          )}
        >
          Saved
        </span>
        <span className="truncate">{label}</span>
      </span>
      <button
        type="button"
        onClick={onRemove}
        className="shrink-0 rounded-sm text-[11px] text-muted-foreground underline-offset-2 transition-colors hover:text-destructive hover:underline"
      >
        Remove
      </button>
    </div>
  );
}

/**
 * Write-only secret editor with its own save: input (or JSON textarea) plus
 * a Save button, and a Saved pill + Remove when the server reports presence.
 */
function SecretField({
  label,
  hint,
  present,
  placeholder,
  multiline,
  onSave,
  onRemove,
}: {
  label: string;
  hint?: ReactNode;
  present?: boolean;
  placeholder: string;
  multiline?: boolean;
  onSave: (value: string) => Promise<void>;
  onRemove?: () => void;
}) {
  const [value, setValue] = useState("");
  const [saving, setSaving] = useState(false);

  async function save() {
    const next = value.trim();
    if (!next || saving) return;
    setSaving(true);
    try {
      await onSave(next);
      setValue("");
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save credential");
    } finally {
      setSaving(false);
    }
  }

  return (
    <div>
      <div className="mb-1.5 flex min-h-5 items-center justify-between gap-2">
        <Label className="text-[12.5px]">{label}</Label>
        {present && (
          <span className="flex items-center gap-2">
            <span
              className={cn(
                "inline-flex h-[18px] items-center rounded-full px-1.5 text-[10.5px] font-medium select-none",
                toneSoft.success,
              )}
            >
              Saved
            </span>
            {onRemove && (
              <button
                type="button"
                onClick={onRemove}
                className="rounded-sm text-[11px] text-muted-foreground underline-offset-2 transition-colors hover:text-destructive hover:underline"
              >
                Remove
              </button>
            )}
          </span>
        )}
      </div>
      {hint && <p className="mb-1.5 text-[11.5px] text-muted-foreground">{hint}</p>}
      {multiline ? (
        <div className="space-y-2">
          <Textarea
            value={value}
            onChange={(e) => setValue(e.target.value)}
            placeholder={present ? "•••• (saved) — paste to replace" : placeholder}
            className="min-h-[72px] resize-none font-mono text-xs"
            autoComplete="off"
          />
          <Button size="sm" variant="outline" onClick={() => void save()} disabled={saving || !value.trim()}>
            {saving ? "Saving…" : present ? "Replace" : "Save"}
          </Button>
        </div>
      ) : (
        <div className="flex gap-2">
          <Input
            type="password"
            value={value}
            onChange={(e) => setValue(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                void save();
              }
            }}
            placeholder={present ? "•••• (saved) — enter to replace" : placeholder}
            autoComplete="off"
            className="flex-1"
          />
          <Button size="sm" variant="outline" onClick={() => void save()} disabled={saving || !value.trim()}>
            {saving ? "Saving…" : present ? "Replace" : "Save"}
          </Button>
        </div>
      )}
    </div>
  );
}

/** Collapsed-by-default disclosure for rare, expert-only inputs. */
function Advanced({ children }: { children: ReactNode }) {
  return (
    <details className="group rounded-md border border-dashed border-border/70 px-3 py-2">
      <summary className="cursor-pointer select-none list-none text-[12px] text-muted-foreground transition-colors hover:text-foreground [&::-webkit-details-marker]:hidden">
        <ChevronDown className="mr-1 inline size-3 -rotate-90 transition-transform group-open:rotate-0" />
        Advanced: paste credentials manually
      </summary>
      <div className="space-y-4 pb-1 pt-3">{children}</div>
    </details>
  );
}

// IntegrationCredentials manages free-form named secrets for tool integrations
// (e.g. "grafana" with url/token keys) that MCP servers consume via
// secret-backed env. Values are write-only; only names and keys are shown.
function IntegrationCredentials({
  integrations,
  onChanged,
}: {
  integrations: { name: string; keys: string[] }[];
  onChanged: (c: ServerCredentials) => void;
}) {
  const [name, setName] = useState("");
  const [rows, setRows] = useState<{ key: string; value: string }[]>([{ key: "", value: "" }]);
  const [busy, setBusy] = useState(false);

  async function submit(update: {
    name: string;
    entries?: Record<string, string>;
    clearKeys?: string[];
    delete?: boolean;
  }) {
    setBusy(true);
    try {
      const c = (await client.updateMyCredentials({
        integrations: [update],
      })) as unknown as ServerCredentials;
      onChanged(c);
      toast.success("Integration updated");
      return true;
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to update integration");
      return false;
    } finally {
      setBusy(false);
    }
  }

  async function saveNew() {
    const entries: Record<string, string> = {};
    for (const r of rows) {
      if (r.key.trim() && r.value.trim()) entries[r.key.trim()] = r.value;
    }
    if (!name.trim() || Object.keys(entries).length === 0) {
      toast.error("Integration name and at least one key/value are required");
      return;
    }
    if (await submit({ name: name.trim(), entries })) {
      setName("");
      setRows([{ key: "", value: "" }]);
    }
  }

  return (
    <div className="space-y-4">
      {integrations.length > 0 && (
        <ul className="overflow-hidden rounded-lg border divide-y divide-border/60">
          {integrations.map((integ) => (
            <li key={integ.name} className="flex flex-wrap items-center gap-2 px-3.5 py-2.5 text-[12.5px]">
              <span className="font-mono font-medium">{integ.name}</span>
              {integ.keys.map((k) => (
                <span
                  key={k}
                  className={cn(
                    "inline-flex h-[18px] items-center gap-1 rounded-full px-1.5 text-[10.5px] font-medium",
                    toneSoft.success,
                  )}
                >
                  {k}
                  <button
                    type="button"
                    aria-label={`Remove ${integ.name} ${k}`}
                    className="transition-colors hover:text-destructive"
                    onClick={() => void submit({ name: integ.name, clearKeys: [k] })}
                  >
                    ×
                  </button>
                </span>
              ))}
              <button
                type="button"
                onClick={() => void submit({ name: integ.name, delete: true })}
                className="ml-auto rounded-sm text-[11px] text-muted-foreground underline-offset-2 transition-colors hover:text-destructive hover:underline"
              >
                Delete
              </button>
            </li>
          ))}
        </ul>
      )}

      <div className="space-y-2">
        <Input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Integration name (e.g. grafana)"
          className="max-w-[240px] font-mono"
          autoComplete="off"
        />
        {rows.map((row, i) => (
          <div key={i} className="flex gap-2">
            <Input
              value={row.key}
              onChange={(e) => setRows(rows.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))}
              placeholder="key (e.g. token)"
              className="max-w-[200px] font-mono"
              autoComplete="off"
            />
            <Input
              type="password"
              value={row.value}
              onChange={(e) => setRows(rows.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))}
              placeholder="value"
              autoComplete="off"
            />
          </div>
        ))}
        <div className="flex items-center gap-3">
          <Button size="sm" variant="outline" onClick={() => setRows([...rows, { key: "", value: "" }])}>
            Add key
          </Button>
          <Button size="sm" onClick={() => void saveNew()} disabled={busy}>
            {busy ? "Saving…" : "Save integration"}
          </Button>
        </div>
      </div>
    </div>
  );
}
