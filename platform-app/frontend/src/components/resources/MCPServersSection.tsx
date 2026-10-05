import { useCallback, useMemo, useState } from "react";
import { Blocks, KeyRound, Network, Plus, X } from "lucide-react";

import { client } from "@/lib/client";
import { resourceNameError } from "@/lib/resourceNames";
import { useMySecretInventory } from "@/hooks/useMySecretInventory";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { filterByQuery } from "@/components/ui/list-search";
import { toast } from "@/components/ui/toaster";
import { FlowField, OptionRow, OptionRows } from "@/components/create-flow/create-flow";
import { ResourceFormDialog } from "@/components/resources/ResourceFormDialog";
import { Fact, Pill, ResourceRow, ResourceSection } from "@/components/resources/ResourceSection";
import { FieldGrid, FormSection, KeyValueRows, NameField, SwitchRow, TokenInput } from "@/components/resources/form-kit";
import {
  joinFacts,
  keyValueRowsError,
  recordToRows,
  resourceMeta,
  rowsToRecord,
  type KeyValueRow,
} from "@/components/resources/resource-helpers";
import { useResourceList } from "@/components/resources/use-resource-list";

interface SecretEnvRow {
  name: string;
  secretName: string;
  secretKey: string;
  required: boolean;
}

export interface MCPServer {
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

function normalizeServer(server: Partial<MCPServer>): MCPServer {
  return {
    ...emptyServer,
    ...server,
    args: server.args ?? [],
    env: server.env ?? {},
    allowEnv: server.allowEnv ?? [],
    secretEnv: (server.secretEnv ?? []).map((row) => ({ ...row, required: Boolean(row.required) })),
  };
}

type Draft = Omit<MCPServer, "env" | "args"> & { argsText: string; envRows: KeyValueRow[] };

function draftFromServer(server: MCPServer | null): Draft {
  const base = server ?? emptyServer;
  return {
    name: base.name,
    version: base.version,
    description: base.description,
    command: base.command,
    argsText: base.args.join(" "),
    envRows: recordToRows(base.env),
    allowEnv: [...base.allowEnv],
    secretEnv: base.secretEnv.map((row) => ({ ...row })),
    trustReadOnlyHint: base.trustReadOnlyHint,
    allowNetwork: base.allowNetwork,
  };
}

const secretRowBlank = (row: SecretEnvRow) => !row.name.trim() && !row.secretName && !row.secretKey;
const secretRowComplete = (row: SecretEnvRow) => Boolean(row.name.trim() && row.secretName && row.secretKey);

const meta = resourceMeta["mcp-servers"];

/**
 * MCP servers tab: stdio tool servers launched inside run pods, with
 * credentials wired from the caller's saved integrations.
 */
export function MCPServersSection() {
  const load = useCallback(
    async () => ((await client.listMCPServers({})).servers ?? []).map((server) => normalizeServer(server as unknown as Partial<MCPServer>)),
    [],
  );
  const { rows, loading, error, reload } = useResourceList(load);
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<MCPServer | null | undefined>();
  const [deleting, setDeleting] = useState<MCPServer | null>(null);

  const visible = useMemo(
    () => filterByQuery(rows, query, (server) => [server.name, server.description, server.command, ...server.args]),
    [rows, query],
  );

  async function remove(server: MCPServer) {
    await client.deleteMCPServer({ name: server.name });
    toast.success(`Deleted ${server.name}`);
    await reload();
  }

  const createButton = (
    <Button size="sm" onClick={() => setEditing(null)}>
      <Plus data-icon="inline-start" />
      New server
    </Button>
  );

  return (
    <>
      <ResourceSection
        description={meta.description}
        count={rows.length}
        query={query}
        onQuery={setQuery}
        searchPlaceholder="Search servers"
        actions={createButton}
        loading={loading}
        error={error}
        onRetry={() => void reload()}
        empty={!loading && rows.length === 0}
        noMatches={visible.length === 0 && rows.length > 0}
        emptyIcon={<Blocks />}
        emptyTitle="No MCP servers yet"
        emptyDescription="Wrap any stdio MCP server, for example uvx mcp-grafana==0.17.2, and attach it to projects, triggers, or modes."
        emptyAction={createButton}
      >
        {visible.map((server) => {
          const commandLine = [server.command, ...server.args].join(" ");
          const envCount = Object.keys(server.env).length;
          return (
            <ResourceRow
              key={server.name}
              name={server.name}
              icon={<Blocks />}
              badges={
                <>
                  {server.version && <span className="font-mono text-[11px] text-muted-foreground">v{server.version}</span>}
                  {server.allowNetwork && (
                    <Pill tone="info">network</Pill>
                  )}
                  {server.secretEnv.length > 0 && (
                    <Pill tone="neutral" className="tabular-nums">
                      {server.secretEnv.length} {server.secretEnv.length === 1 ? "secret" : "secrets"}
                    </Pill>
                  )}
                </>
              }
              description={server.description || undefined}
              facts={
                <>
                  <Fact title={commandLine}>{commandLine}</Fact>
                  {envCount > 0 && <Fact>{envCount} env</Fact>}
                </>
              }
              onOpen={() => setEditing(server)}
              onEdit={() => setEditing(server)}
              onDelete={() => setDeleting(server)}
            />
          );
        })}
      </ResourceSection>

      {editing !== undefined && (
        <MCPServerDialog
          server={editing}
          onClose={() => setEditing(undefined)}
          onSaved={async () => {
            setEditing(undefined);
            await reload();
          }}
        />
      )}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Delete ${deleting?.name ?? ""}?`}
        description="This permanently removes the MCP server config. Agents that reference it will no longer load its tools."
        confirmLabel="Delete server"
        destructive
        onConfirm={async () => {
          if (deleting) await remove(deleting);
        }}
      />
    </>
  );
}

function MCPServerDialog({ server, onClose, onSaved }: { server: MCPServer | null; onClose: () => void; onSaved: () => Promise<void> }) {
  const isNew = server === null;
  const secretInventory = useMySecretInventory();
  const integrations = secretInventory.integrations as unknown as Integration[];
  const [initial] = useState(() => draftFromServer(server));
  const [draft, setDraft] = useState<Draft>(initial);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = (patch: Partial<Draft>) => setDraft((current) => ({ ...current, ...patch }));
  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  const envError = keyValueRowsError(draft.envRows, "Variable");
  const secretsValid = draft.secretEnv.every((row) => secretRowBlank(row) || secretRowComplete(row));
  const valid =
    Boolean(draft.name.trim()) &&
    (!isNew || resourceNameError(draft.name.trim()) === null) &&
    Boolean(draft.command.trim()) &&
    envError === null &&
    secretsValid;

  const activeSecrets = draft.secretEnv.filter((row) => !secretRowBlank(row)).length;
  const activeEnv = draft.envRows.filter((row) => row.key.trim() || row.value.trim()).length;

  const updateSecret = (index: number, patch: Partial<SecretEnvRow>) =>
    set({ secretEnv: draft.secretEnv.map((row, at) => (at === index ? { ...row, ...patch } : row)) });

  async function save() {
    setSaving(true);
    setError(null);
    try {
      await client.upsertMCPServer({
        name: draft.name.trim(),
        version: draft.version.trim(),
        description: draft.description.trim(),
        command: draft.command.trim(),
        args: draft.argsText.split(/\s+/).filter(Boolean),
        env: rowsToRecord(draft.envRows, "Variable"),
        allowEnv: draft.allowEnv,
        secretEnv: draft.secretEnv.filter(secretRowComplete).map((row) => ({ ...row, name: row.name.trim() })),
        trustReadOnlyHint: draft.trustReadOnlyHint,
        allowNetwork: draft.allowNetwork,
      });
      toast.success(`${isNew ? "Created" : "Saved"} ${draft.name.trim()}`);
      await onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  return (
    <ResourceFormDialog
      open
      onClose={onClose}
      icon={<Blocks />}
      title={isNew ? "New MCP server" : `Edit ${server.name}`}
      description="A stdio MCP server launched inside the run pod. Saving replaces the server's config."
      dirty={dirty}
      saving={saving}
      canSave={valid && (isNew || dirty)}
      saveLabel={isNew ? "Create server" : "Save changes"}
      error={error}
      onSave={() => void save()}
    >
      <FormSection title="Server">
        <FieldGrid>
          <NameField id="mcp-name" value={draft.name} onChange={(name) => set({ name })} isNew={isNew} placeholder="my-tool" autoFocus={isNew} />
          <FlowField id="mcp-version" label="Version" hint="Optional; shown next to the name.">
            <Input id="mcp-version" value={draft.version} onChange={(event) => set({ version: event.target.value })} placeholder="0.1.0" className="font-mono" autoComplete="off" />
          </FlowField>
        </FieldGrid>
        <FlowField id="mcp-description" label="Description">
          <Input id="mcp-description" value={draft.description} onChange={(event) => set({ description: event.target.value })} placeholder="What the tools do — shown in agent settings" />
        </FlowField>
      </FormSection>

      <FormSection title="Command" description="Runs inside the pod's command sandbox; toolchains are mounted read-only.">
        <div className="grid gap-4 sm:grid-cols-[160px_1fr]">
          <FlowField id="mcp-command" label="Command" required>
            <Input id="mcp-command" value={draft.command} onChange={(event) => set({ command: event.target.value })} placeholder="uvx" className="font-mono" autoComplete="off" spellCheck={false} />
          </FlowField>
          <FlowField id="mcp-args" label="Arguments" hint="Space-separated.">
            <Input id="mcp-args" value={draft.argsText} onChange={(event) => set({ argsText: event.target.value })} placeholder="mcp-grafana==0.17.2 --disable-oncall" className="font-mono" autoComplete="off" spellCheck={false} />
          </FlowField>
        </div>
      </FormSection>

      <OptionRows label="Options">
        <OptionRow
          icon={KeyRound}
          title="Credentials"
          defaultOpen={isNew}
          modified={activeSecrets > 0 || activeEnv > 0 || draft.allowEnv.length > 0}
          summary={
            joinFacts([
              activeSecrets > 0 && `${activeSecrets} ${activeSecrets === 1 ? "secret" : "secrets"}`,
              activeEnv > 0 && `${activeEnv} ${activeEnv === 1 ? "variable" : "variables"}`,
              draft.allowEnv.length > 0 && `${draft.allowEnv.length} allowed`,
            ]) || "None"
          }
        >
          <FlowField
            label="Secret credentials"
            hint="Env vars sourced from your saved integration credentials — never stored in the server config. Required secrets block the run pod when missing."
          >
            <div className="space-y-2">
              {draft.secretEnv.map((row, index) => {
                const integration = integrations.find((candidate) => `usercred-${candidate.name}` === row.secretName);
                const keys = integration?.keys ?? (row.secretKey ? [row.secretKey] : []);
                return (
                  <div key={index} className="flex flex-wrap items-center gap-2">
                    <Input
                      aria-label={`Secret env name ${index + 1}`}
                      value={row.name}
                      onChange={(event) => updateSecret(index, { name: event.target.value })}
                      placeholder="ENV_VAR_NAME"
                      className="w-[200px] font-mono"
                      autoComplete="off"
                      spellCheck={false}
                    />
                    <Select
                      value={integration?.name ?? ""}
                      onOpenChange={(open) => {
                        if (open) void secretInventory.reload();
                      }}
                      onValueChange={(value) => updateSecret(index, { secretName: value ? `usercred-${value}` : "", secretKey: "" })}
                    >
                      <SelectTrigger className="w-[160px]" aria-label={`Integration ${index + 1}`}>
                        <SelectValue placeholder="integration" />
                      </SelectTrigger>
                      <SelectContent>
                        {integrations.map((candidate) => (
                          <SelectItem key={candidate.name} value={candidate.name}>
                            {candidate.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <Select value={row.secretKey || ""} onValueChange={(value) => updateSecret(index, { secretKey: value ?? "" })}>
                      <SelectTrigger className="w-[130px]" aria-label={`Secret key ${index + 1}`}>
                        <SelectValue placeholder="key" />
                      </SelectTrigger>
                      <SelectContent>
                        {keys.map((key) => (
                          <SelectItem key={key} value={key}>
                            {key}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <label className="inline-flex items-center gap-1.5 text-[11.5px] text-muted-foreground">
                      <Switch
                        size="sm"
                        aria-label={`Require secret ${index + 1}`}
                        checked={row.required}
                        onCheckedChange={(required) => updateSecret(index, { required })}
                      />
                      Required
                    </label>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-sm"
                      aria-label={`Remove secret ${row.name.trim() || index + 1}`}
                      className="text-muted-foreground hover:text-destructive"
                      onClick={() => set({ secretEnv: draft.secretEnv.filter((_, at) => at !== index) })}
                    >
                      <X />
                    </Button>
                  </div>
                );
              })}
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => set({ secretEnv: [...draft.secretEnv, { name: "", secretName: "", secretKey: "", required: false }] })}
              >
                <Plus data-icon="inline-start" />
                Add credential
              </Button>
              {integrations.length === 0 && draft.secretEnv.length > 0 && (
                <p className="text-[11.5px] text-[color:var(--tone-warning-fg)]">
                  No saved integrations — add one under Credentials → Integration credentials first.
                </p>
              )}
            </div>
          </FlowField>

          <FlowField label="Plain environment" hint="Non-secret values only.">
            <KeyValueRows
              rows={draft.envRows}
              onChange={(envRows) => set({ envRows })}
              keyLabel="Variable"
              keyPlaceholder="KEY"
              valuePlaceholder="value"
              addLabel="Add variable"
            />
          </FlowField>

          <FlowField
            id="mcp-allow-env"
            label="Allowed credential env names"
            hint="Names passed through to the server even though they look like secrets (e.g. GRAFANA_SERVICE_ACCOUNT_TOKEN)."
          >
            <TokenInput id="mcp-allow-env" aria-label="Allowed credential env names" value={draft.allowEnv} onChange={(allowEnv) => set({ allowEnv })} placeholder="MY_TOOL_TOKEN" />
          </FlowField>
        </OptionRow>

        <OptionRow
          icon={Network}
          title="Access"
          modified={draft.allowNetwork || !draft.trustReadOnlyHint}
          summary={joinFacts([
            draft.allowNetwork ? "Network on" : "Network off",
            draft.trustReadOnlyHint ? "trusts read-only hints" : "ignores read-only hints",
          ])}
        >
          <SwitchRow
            id="mcp-allow-network"
            label="Allow network access"
            hint="Uses the run pod's network; the runtime profile's egress policy still applies."
            checked={draft.allowNetwork}
            onCheckedChange={(allowNetwork) => set({ allowNetwork })}
          />
          <SwitchRow
            id="mcp-trust-read-only"
            label="Trust read-only tool hints"
            hint="Keeps query tools available in read-only runs."
            checked={draft.trustReadOnlyHint}
            onCheckedChange={(trustReadOnlyHint) => set({ trustReadOnlyHint })}
          />
        </OptionRow>
      </OptionRows>
    </ResourceFormDialog>
  );
}
