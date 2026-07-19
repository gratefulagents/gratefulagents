import { useCallback, useEffect, useState } from "react";
import { Blocks, Plus } from "lucide-react";

import { client } from "@/lib/client";
import { resourceNameError } from "@/lib/resourceNames";
import { SettingsSection } from "@/components/settings-section";
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
import { toast } from "@/components/ui/toaster";
import { useMySecretInventory } from "@/hooks/useMySecretInventory";

interface SecretEnvRow {
  name: string;
  secretName: string;
  secretKey: string;
  required: boolean;
}

interface MCPServer {
  name: string;
  version: string;
  description: string;
  command: string;
  args: string[];
  env: Record<string, string>;
  allowEnv: string[];
  secretEnv: SecretEnvRow[];
  trustReadOnlyHint: boolean;
  allowNetwork: boolean;
}

interface Integration {
  name: string;
  keys: string[];
}

const emptyServer: MCPServer = {
  name: "",
  version: "",
  description: "",
  command: "",
  args: [],
  env: {},
  allowEnv: [],
  secretEnv: [],
  trustReadOnlyHint: true,
  allowNetwork: false,
};

// MCPServersSection lets users create and manage their own MCP server configs
// (MCPServer CRs in their namespace) entirely from the UI: server command,
// env, and secret-backed credentials (wired to saved integration credentials).
export function MCPServersSection() {
  const [servers, setServers] = useState<MCPServer[]>([]);
  const secretInventory = useMySecretInventory();
  const [editing, setEditing] = useState<MCPServer | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      const resp = await client.listMCPServers({});
      setServers(
        ((resp.servers ?? []) as unknown as (MCPServer & { secretEnv?: SecretEnvRow[] })[]).map(
          (s) => ({
            ...emptyServer,
            ...s,
            args: s.args ?? [],
            env: s.env ?? {},
            allowEnv: s.allowEnv ?? [],
            secretEnv: (s.secretEnv ?? []).map((row) => ({ ...row, required: !!row.required })),
          }),
        ),
      );
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to load MCP servers");
    }
  }, []);

  useEffect(() => {
    void (async () => {
      await reload();
    })();
  }, [reload]);

  async function save(server: MCPServer) {
    setBusy(true);
    try {
      await client.upsertMCPServer({
        name: server.name.trim(),
        version: server.version.trim(),
        description: server.description.trim(),
        command: server.command.trim(),
        args: server.args,
        env: server.env,
        allowEnv: server.allowEnv,
        secretEnv: server.secretEnv,
        trustReadOnlyHint: server.trustReadOnlyHint,
        allowNetwork: server.allowNetwork,
      });
      toast.success(`MCP server ${server.name.trim()} saved`);
      setEditing(null);
      setIsNew(false);
      await reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save MCP server");
    } finally {
      setBusy(false);
    }
  }

  async function remove(name: string) {
    setBusy(true);
    try {
      await client.deleteMCPServer({ name });
      toast.success(`MCP server ${name} deleted`);
      if (editing?.name === name) setEditing(null);
      await reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to delete MCP server");
    } finally {
      setBusy(false);
    }
  }

  return (
    <SettingsSection
      icon={<Blocks />}
      title="MCP servers"
      description="Your own MCP server configs: a server command plus credentials from your saved integrations. Attach them to agents (e.g. the Slack agent's Tool servers) to give runs the tools."
      aside={
        <Button
          size="sm"
          variant="outline"
          onClick={() => {
            setEditing({ ...emptyServer });
            setIsNew(true);
          }}
        >
          <Plus className="mr-1 size-3.5" /> New server
        </Button>
      }
    >
      {servers.length === 0 && !editing && (
        <p className="text-[12px] text-muted-foreground">
          No MCP servers yet. Create one to wrap any stdio MCP server (e.g.{" "}
          <code className="font-mono">uvx mcp-grafana==0.17.2</code>).
        </p>
      )}

      {servers.length > 0 && (
        <ul className="space-y-2">
          {servers.map((server) => (
            <li key={server.name} className="flex items-start justify-between gap-3 rounded-md border px-3 py-2">
              <div className="min-w-0">
                <div className="text-[13px] font-medium">
                  {server.name}
                  {server.version && (
                    <span className="ml-1.5 text-[11px] font-normal text-muted-foreground">v{server.version}</span>
                  )}
                </div>
                {server.description && <p className="text-[12px] text-muted-foreground">{server.description}</p>}
                <p className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">
                  {server.command} {server.args.join(" ")}
                </p>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => {
                    setEditing({ ...server });
                    setIsNew(false);
                  }}
                >
                  Edit
                </Button>
                <Button size="sm" variant="ghost" disabled={busy} onClick={() => setDeleteTarget(server.name)}>
                  Delete
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}

      <ConfirmDialog
        open={deleteTarget != null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={`Delete ${deleteTarget ?? ""}?`}
        description="This permanently removes the MCP server config. Agents that reference it will no longer load its tools."
        confirmLabel="Delete"
        destructive
        onConfirm={async () => {
          if (deleteTarget) await remove(deleteTarget);
        }}
      />

      {editing && (
        <MCPServerForm
          key={isNew ? "__new__" : editing.name}
          server={editing}
          isNew={isNew}
          integrations={secretInventory.integrations}
          onRefreshIntegrations={() => void secretInventory.reload()}
          busy={busy}
          onChange={setEditing}
          onSave={() => void save(editing)}
          onCancel={() => {
            setEditing(null);
            setIsNew(false);
          }}
        />
      )}
    </SettingsSection>
  );
}

function MCPServerForm({
  server,
  isNew,
  integrations,
  onRefreshIntegrations,
  busy,
  onChange,
  onSave,
  onCancel,
}: {
  server: MCPServer;
  isNew: boolean;
  integrations: Integration[];
  onRefreshIntegrations: () => void;
  busy: boolean;
  onChange: (s: MCPServer) => void;
  onSave: () => void;
  onCancel: () => void;
}) {
  const set = (patch: Partial<MCPServer>) => onChange({ ...server, ...patch });

  const [envRows, setEnvRows] = useState<{ key: string; value: string }[]>(() =>
    Object.entries(server.env).map(([key, value]) => ({ key, value })),
  );

  const trimmedEnvKeys = envRows.map((row) => row.key.trim());
  const hasEmptyEnvKey = trimmedEnvKeys.some((key) => !key);
  const duplicateEnvKeys = [
    ...new Set(trimmedEnvKeys.filter((key, i) => key && trimmedEnvKeys.indexOf(key) !== i)),
  ];
  const envInvalid = hasEmptyEnvKey || duplicateEnvKeys.length > 0;

  const setEnv = (rows: { key: string; value: string }[]) => {
    setEnvRows(rows);
    const keys = rows.map((row) => row.key.trim());
    const valid = keys.every(Boolean) && new Set(keys).size === keys.length;
    if (valid) {
      onChange({ ...server, env: Object.fromEntries(rows.map((row) => [row.key.trim(), row.value])) });
    }
  };

  const nameError = isNew ? resourceNameError(server.name.trim()) : null;

  return (
    <div className="space-y-3.5 rounded-md border bg-muted/20 p-3.5">
      <div className="grid gap-3 sm:grid-cols-2">
        <FieldBlock label="Name" hint="lowercase, digits, hyphens">
          <Input
            value={server.name}
            onChange={(e) => set({ name: e.target.value })}
            placeholder="my-tool"
            className="font-mono"
            disabled={!isNew}
            autoComplete="off"
          />
          {nameError && <p className="mt-1 text-[11.5px] text-destructive">{nameError}</p>}
        </FieldBlock>
        <FieldBlock label="Version (optional)">
          <Input
            value={server.version}
            onChange={(e) => set({ version: e.target.value })}
            placeholder="0.1.0"
            className="font-mono"
            autoComplete="off"
          />
        </FieldBlock>
      </div>

      <FieldBlock label="Description">
        <Input
          value={server.description}
          onChange={(e) => set({ description: e.target.value })}
          placeholder="What the tools do — shown in agent settings"
        />
      </FieldBlock>

      <div className="grid gap-3 sm:grid-cols-[160px_1fr]">
        <FieldBlock label="Command">
          <Input
            value={server.command}
            onChange={(e) => set({ command: e.target.value })}
            placeholder="uvx"
            className="font-mono"
            autoComplete="off"
          />
        </FieldBlock>
        <FieldBlock label="Arguments" hint="space-separated">
          <Input
            value={server.args.join(" ")}
            onChange={(e) => set({ args: e.target.value.split(/\s+/).filter(Boolean) })}
            placeholder="mcp-grafana==0.17.2 --disable-oncall"
            className="font-mono"
            autoComplete="off"
          />
        </FieldBlock>
      </div>

      <FieldBlock
        label="Secret credentials"
        hint="Env vars sourced from your saved integration credentials — never stored in the server config."
      >
        <div className="space-y-2">
          {server.secretEnv.map((row, i) => {
            const integ = integrations.find((x) => `usercred-${x.name}` === row.secretName);
            return (
              <div key={i} className="flex flex-wrap items-center gap-2">
                <Input
                  value={row.name}
                  onChange={(e) =>
                    set({
                      secretEnv: server.secretEnv.map((r, j) => (j === i ? { ...r, name: e.target.value } : r)),
                    })
                  }
                  placeholder="ENV_VAR_NAME"
                  className="w-[220px] font-mono"
                  autoComplete="off"
                />
                <Select
                  value={integ?.name ?? ""}
                  onOpenChange={(open) => {
                    if (open) onRefreshIntegrations();
                  }}
                  onValueChange={(v) =>
                    set({
                      secretEnv: server.secretEnv.map((r, j) =>
                        j === i ? { ...r, secretName: v ? `usercred-${v}` : "", secretKey: "" } : r,
                      ),
                    })
                  }
                >
                  <SelectTrigger className="w-[160px]">
                    <SelectValue placeholder="integration" />
                  </SelectTrigger>
                  <SelectContent>
                    {integrations.map((x) => (
                      <SelectItem key={x.name} value={x.name}>
                        {x.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Select
                  value={row.secretKey || ""}
                  onValueChange={(v) =>
                    set({
                      secretEnv: server.secretEnv.map((r, j) => (j === i ? { ...r, secretKey: v ?? "" } : r)),
                    })
                  }
                >
                  <SelectTrigger className="w-[130px]">
                    <SelectValue placeholder="key" />
                  </SelectTrigger>
                  <SelectContent>
                    {(integ?.keys ?? (row.secretKey ? [row.secretKey] : [])).map((k) => (
                      <SelectItem key={k} value={k}>
                        {k}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <button
                  type="button"
                  onClick={() => set({ secretEnv: server.secretEnv.filter((_, j) => j !== i) })}
                  className="rounded-sm text-[11px] text-muted-foreground underline-offset-2 hover:text-destructive hover:underline"
                >
                  Remove
                </button>
              </div>
            );
          })}
          <Button
            size="sm"
            variant="outline"
            onClick={() =>
              set({
                secretEnv: [...server.secretEnv, { name: "", secretName: "", secretKey: "", required: false }],
              })
            }
          >
            Add credential
          </Button>
          {integrations.length === 0 && server.secretEnv.length > 0 && (
            <p className="text-[12px] text-amber-600">
              No saved integrations — add one under Credentials → Integration credentials first.
            </p>
          )}
        </div>
      </FieldBlock>

      <FieldBlock label="Plain environment" hint="non-secret values only">
        <div className="space-y-2">
          {envRows.map((row, i) => (
            <div key={i} className="flex items-center gap-2">
              <Input
                value={row.key}
                onChange={(e) =>
                  setEnv(envRows.map((r, j) => (j === i ? { ...r, key: e.target.value } : r)))
                }
                placeholder="KEY"
                className="w-[220px] font-mono"
                autoComplete="off"
              />
              <Input
                value={row.value}
                onChange={(e) =>
                  setEnv(envRows.map((r, j) => (j === i ? { ...r, value: e.target.value } : r)))
                }
                placeholder="value"
                className="font-mono"
                autoComplete="off"
              />
              <button
                type="button"
                onClick={() => setEnv(envRows.filter((_, j) => j !== i))}
                className="rounded-sm text-[11px] text-muted-foreground underline-offset-2 hover:text-destructive hover:underline"
              >
                Remove
              </button>
            </div>
          ))}
          <Button size="sm" variant="outline" onClick={() => setEnv([...envRows, { key: "", value: "" }])}>
            Add variable
          </Button>
          {hasEmptyEnvKey && (
            <p className="text-[11.5px] text-destructive">Environment variable names cannot be empty.</p>
          )}
          {duplicateEnvKeys.length > 0 && (
            <p className="text-[11.5px] text-destructive">
              Duplicate variable name{duplicateEnvKeys.length === 1 ? "" : "s"}: {duplicateEnvKeys.join(", ")}
            </p>
          )}
        </div>
      </FieldBlock>

      <FieldBlock
        label="Allowed credential env names"
        hint="Comma-separated names passed through to the server even though they look like secrets (e.g. GRAFANA_SERVICE_ACCOUNT_TOKEN)."
      >
        <Input
          value={server.allowEnv.join(", ")}
          onChange={(e) =>
            set({ allowEnv: e.target.value.split(",").map((s) => s.trim()).filter(Boolean) })
          }
          placeholder="MY_TOOL_TOKEN"
          className="font-mono"
          autoComplete="off"
        />
      </FieldBlock>

      <div className="flex items-center gap-2">
        <Switch
          aria-label="Allow network access"
          checked={server.allowNetwork}
          onCheckedChange={(v) => set({ allowNetwork: v })}
        />
        <Label className="text-[12.5px]">
          Allow this server to use the run pod's network (the RuntimeProfile egress policy still applies)
        </Label>
      </div>

      <div className="flex items-center gap-2">
        <Switch
          aria-label="Trust read-only hints"
          checked={server.trustReadOnlyHint}
          onCheckedChange={(v) => set({ trustReadOnlyHint: v })}
        />
        <Label className="text-[12.5px]">
          Trust the server's read-only tool hints (keeps query tools available in read-only runs)
        </Label>
      </div>

      <div className="flex items-center gap-3 border-t pt-3">
        <Button
          size="sm"
          onClick={onSave}
          disabled={busy || !server.name.trim() || !server.command.trim() || nameError != null || envInvalid}
        >
          {busy ? "Saving…" : isNew ? "Create server" : "Save changes"}
        </Button>
        <Button size="sm" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

function FieldBlock({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <Label className="text-[12.5px]">{label}</Label>
      {hint && <p className="mb-1 text-[11.5px] text-muted-foreground">{hint}</p>}
      <div className={hint ? "" : "mt-1.5"}>{children}</div>
    </div>
  );
}
