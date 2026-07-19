import { useCallback, useEffect, useMemo, useState } from "react";
import { KeyRound, Share2 } from "lucide-react";

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

// CredentialsSection is the user's "My Credentials" settings: their personal
// provider credentials (API keys + OAuth) and GitHub token, stored privately in
// their own namespace and selectable when creating a project. On the desktop app
// it can import everything from the installed CLIs in one click.
export function CredentialsSection() {
  const [namespace, setNamespace] = useState("");
  const [presence, setPresence] = useState<Presence>(emptyPresence);
  const [integrations, setIntegrations] = useState<{ name: string; keys: string[] }[]>([]);

  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [shareOpen, setShareOpen] = useState(false);

  // Write-only inputs (never populated from the server).
  const [anthropicKey, setAnthropicKey] = useState("");
  const [openaiKey, setOpenaiKey] = useState("");
  const [openrouterKey, setOpenrouterKey] = useState("");
  const [xaiKey, setXaiKey] = useState("");
  const [anthropicOauth, setAnthropicOauth] = useState("");
  const [openaiOauth, setOpenaiOauth] = useState("");
  const [openaiAccountId, setOpenaiAccountId] = useState("");
  const [copilotOauth, setCopilotOauth] = useState("");
  const [githubToken, setGithubToken] = useState("");

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

  function clearInputs() {
    setAnthropicKey("");
    setOpenaiKey("");
    setOpenrouterKey("");
    setXaiKey("");
    setAnthropicOauth("");
    setOpenaiOauth("");
    setOpenaiAccountId("");
    setCopilotOauth("");
    setGithubToken("");
  }

  async function save() {
    setSaving(true);
    setStatus(null);
    setError(null);
    try {
      const c = await client.updateMyCredentials({
        anthropicApiKey: anthropicKey.trim(),
        openaiApiKey: openaiKey.trim(),
        openrouterApiKey: openrouterKey.trim(),
        xaiApiKey: xaiKey.trim(),
        anthropicOauthJson: anthropicOauth.trim(),
        openaiOauthJson: openaiOauth.trim(),
        openaiAccountId: openaiAccountId.trim(),
        copilotOauthJson: copilotOauth.trim(),
        githubToken: githubToken.trim(),
      });
      applyCredentials(c);
      clearInputs();
      setStatus("Credentials saved");
      toast.success("Credentials saved");
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to save credentials";
      setError(message);
      toast.error(message);
    } finally {
      setSaving(false);
    }
  }

  async function remove(which: ClearKey) {
    setStatus(null);
    setError(null);
    try {
      const c = await client.updateMyCredentials({ clear: [which] });
      applyCredentials(c);
      setStatus("Credentials updated");
      toast.success("Credential removed");
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to update credentials";
      setError(message);
      toast.error(message);
    }
  }

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

  return (
    <SettingsSection
      icon={<KeyRound />}
      title="Credentials"
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
          <Share2 className="mr-1 h-3.5 w-3.5" />
          Share
        </Button>
      }
      description={
        <>
          Add your provider credentials once. They are stored privately in your namespace
          {namespace ? (
            <>
              {" "}
              (<code className="font-mono">{namespace}</code>)
            </>
          ) : null}{" "}
          and can be selected when you create a project.
        </>
      }
    >
      <ImportLocalCredentials onImported={() => void reload()} className="max-w-xl" />
      <AnthropicOAuthConnect
        className="max-w-xl"
        onSaved={(credentials) => {
          applyCredentials(credentials);
          setError(null);
          setStatus("Claude credentials saved");
          toast.success("Claude credentials saved");
        }}
      />
      <OpenAIOAuthConnect
        className="max-w-xl"
        onSaved={(credentials) => {
          applyCredentials(credentials);
          setError(null);
          setStatus("ChatGPT credentials saved");
          toast.success("ChatGPT credentials saved");
        }}
      />
      <CopilotOAuthConnect
        className="max-w-xl"
        onSaved={(credentials) => {
          applyCredentials(credentials);
          setError(null);
          setStatus("Copilot credentials saved");
          toast.success("Copilot credentials saved");
        }}
      />

      <div className="grid gap-x-4 gap-y-4 sm:grid-cols-2">
        <CredentialField
          label="Anthropic API key"
          present={presence.anthropicApiKey}
          onRemove={() => void remove("anthropic-api-key")}
        >
          <Input
            type="password"
            value={anthropicKey}
            onChange={(e) => setAnthropicKey(e.target.value)}
            placeholder={presence.anthropicApiKey ? "•••• (saved) — enter to replace" : "sk-ant-..."}
            autoComplete="off"
          />
        </CredentialField>

        <CredentialField
          label="OpenAI API key"
          present={presence.openaiApiKey}
          onRemove={() => void remove("openai-api-key")}
        >
          <Input
            type="password"
            value={openaiKey}
            onChange={(e) => setOpenaiKey(e.target.value)}
            placeholder={presence.openaiApiKey ? "•••• (saved) — enter to replace" : "sk-..."}
            autoComplete="off"
          />
        </CredentialField>

        <CredentialField
          label="OpenRouter API key"
          present={presence.openrouterApiKey}
          onRemove={() => void remove("openrouter-api-key")}
        >
          <Input
            type="password"
            value={openrouterKey}
            onChange={(e) => setOpenrouterKey(e.target.value)}
            placeholder={presence.openrouterApiKey ? "•••• (saved) — enter to replace" : "sk-or-v1-..."}
            autoComplete="off"
          />
        </CredentialField>

        <CredentialField
          label="xAI / Grok API key"
          present={presence.xaiApiKey}
          onRemove={() => void remove("xai-api-key")}
        >
          <Input
            type="password"
            value={xaiKey}
            onChange={(e) => setXaiKey(e.target.value)}
            placeholder={presence.xaiApiKey ? "•••• (saved) — enter to replace" : "xai-..."}
            autoComplete="off"
          />
        </CredentialField>

        <CredentialField
          label="Claude OAuth (.credentials.json)"
          present={presence.anthropicOauth}
          onRemove={() => void remove("anthropic-oauth")}
        >
          <Textarea
            value={anthropicOauth}
            onChange={(e) => setAnthropicOauth(e.target.value)}
            placeholder={presence.anthropicOauth ? "{…} (saved) — paste to replace" : "Paste ~/.claude/.credentials.json"}
            className="min-h-[72px] resize-none font-mono text-xs"
          />
        </CredentialField>

        <CredentialField
          label="OpenAI OAuth (auth.json)"
          present={presence.openaiOauth}
          onRemove={() => void remove("openai-oauth")}
        >
          <Textarea
            value={openaiOauth}
            onChange={(e) => setOpenaiOauth(e.target.value)}
            placeholder={presence.openaiOauth ? "{…} (saved) — paste to replace" : "Paste auth.json"}
            className="min-h-[72px] resize-none font-mono text-xs"
          />
          <Input
            value={openaiAccountId}
            onChange={(e) => setOpenaiAccountId(e.target.value)}
            placeholder="Account ID (optional)"
            className="mt-2"
            autoComplete="off"
          />
        </CredentialField>

        <CredentialField
          label="Copilot OAuth JSON"
          present={presence.copilotOauth}
          onRemove={() => void remove("copilot-oauth")}
        >
          <Textarea
            value={copilotOauth}
            onChange={(e) => setCopilotOauth(e.target.value)}
            placeholder={presence.copilotOauth ? "{…} (saved) — paste to replace" : "Paste Copilot auth JSON or apps.json"}
            className="min-h-[72px] resize-none font-mono text-xs"
          />
        </CredentialField>

        <CredentialField
          label="GitHub token"
          present={presence.githubToken}
          onRemove={() => void remove("github-token")}
        >
          <Input
            type="password"
            value={githubToken}
            onChange={(e) => setGithubToken(e.target.value)}
            placeholder={presence.githubToken ? "•••• (saved) — enter to replace" : "ghp_... / github_pat_..."}
            autoComplete="off"
          />
        </CredentialField>
      </div>

      <div className="flex items-center gap-3">
        <Button size="sm" onClick={() => void save()} disabled={saving || loading}>
          {saving ? "Saving…" : "Save credentials"}
        </Button>
        {status && <span className="text-[12px] text-muted-foreground">{status}</span>}
        {error && <span className="text-[12px] text-destructive" role="alert">{error}</span>}
      </div>

      <IntegrationCredentials
        integrations={integrations}
        onChanged={(c) => {
          applyCredentials(c);
          setError(null);
        }}
      />

      <ShareCredentialsDialog
        open={shareOpen}
        onOpenChange={setShareOpen}
        credentials={shareableCredentials}
      />
    </SettingsSection>
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
    <div className="max-w-xl border-t pt-4">
      <div className="mb-1 text-[13px] font-medium">Integration credentials</div>
      <p className="mb-3 text-[12px] text-muted-foreground">
        Free-form named secrets for tool integrations (e.g. <code className="font-mono">grafana</code> with{" "}
        <code className="font-mono">url</code> and <code className="font-mono">token</code>). Skill packages
        reference them to authenticate their tools. Values are write-only.
      </p>

      {integrations.length > 0 && (
        <ul className="mb-4 space-y-2">
          {integrations.map((integ) => (
            <li key={integ.name} className="flex flex-wrap items-center gap-2 text-[12.5px]">
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
                className="rounded-sm text-[11px] text-muted-foreground underline-offset-2 transition-colors hover:text-destructive hover:underline"
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

function CredentialField({
  label,
  present,
  onRemove,
  children,
}: {
  label: string;
  present: boolean;
  onRemove: () => void;
  children: React.ReactNode;
}) {
  return (
    <div>
      <div className="mb-1.5 flex h-5 items-center justify-between gap-2">
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
            <button
              type="button"
              onClick={onRemove}
              className="rounded-sm text-[11px] text-muted-foreground underline-offset-2 transition-colors hover:text-destructive hover:underline"
            >
              Remove
            </button>
          </span>
        )}
      </div>
      {children}
    </div>
  );
}
