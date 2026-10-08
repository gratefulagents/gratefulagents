import { create } from "@bufbuild/protobuf";
import { useCallback, useMemo, useState } from "react";
import { Cpu, Plus, UserCog } from "lucide-react";

import { client } from "@/lib/client";
import type { StatusTone } from "@/lib/status";
import { resourceNameError } from "@/lib/resourceNames";
import { useAuth } from "@/contexts/AuthContext";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { filterByQuery } from "@/components/ui/list-search";
import { toast } from "@/components/ui/toaster";
import { FlowField, OptionRow, OptionRows } from "@/components/create-flow/create-flow";
import { RoleInstructionSchema, type RoleInstruction } from "@/rpc/platform/service_pb";
import { ResourceFormDialog } from "@/components/resources/ResourceFormDialog";
import { Fact, Pill, ResourceRow, ResourceSection } from "@/components/resources/ResourceSection";
import { ChoiceCards, FormSection, KeyValueRows, NameField } from "@/components/resources/form-kit";
import {
  canCreateResource,
  canDeleteResource,
  canMutateResource,
  joinFacts,
  keyValueRowsError,
  recordToRows,
  resourceMeta,
  rowsToRecord,
  type KeyValueRow,
} from "@/components/resources/resource-helpers";
import { useResourceList } from "@/components/resources/use-resource-list";

type Draft = {
  name: string;
  description: string;
  instructions: string;
  toolAccess: string;
  model: string;
  providerModels: KeyValueRow[];
  /** "inherit" stands in for the empty API value; Select items cannot be "". */
  reasoningLevel: string;
};

const reasoningLevels = ["none", "minimal", "low", "medium", "high", "xhigh", "max"] as const;

const toolAccessTone: Record<string, StatusTone> = {
  "read-only": "info",
  analysis: "purple",
  execution: "warning",
  full: "neutral",
};

function draftFromRole(role: RoleInstruction | null): Draft {
  if (!role) return { name: "", description: "", instructions: "", toolAccess: "full", model: "", providerModels: [], reasoningLevel: "inherit" };
  return {
    name: role.name,
    description: role.description ?? "",
    instructions: role.instructions ?? "",
    toolAccess: role.toolAccess || "full",
    model: role.model ?? "",
    providerModels: recordToRows(role.modelsByProvider),
    reasoningLevel: role.reasoningLevel || "inherit",
  };
}

const meta = resourceMeta.roles;

/** The runtime always offers this role; a RoleInstruction of the same name replaces it. */
const GENERAL_ROLE = "general";

function deleteRoleDescription(name: string | undefined) {
  return name === GENERAL_ROLE
    ? "Runs go back to the built-in general sub-agent. This cannot be undone."
    : "Runs stop offering this sub-agent. This cannot be undone.";
}

export function RolesSection() {
  const { user } = useAuth();
  const creatable = canCreateResource("roles", user?.role);
  const mutable = canMutateResource("roles", user?.role);
  const deletable = canDeleteResource("roles", user?.role);
  const load = useCallback(async () => (await client.listRoleInstructions({})).instructions, []);
  const { rows, loading, error, reload } = useResourceList(load);
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<RoleInstruction | null | undefined>();
  const [deleting, setDeleting] = useState<RoleInstruction | null>(null);

  const visible = useMemo(
    () => filterByQuery(rows, query, (role) => [role.name, role.description, role.toolAccess]),
    [rows, query],
  );

  async function remove(role: RoleInstruction) {
    await client.deleteRoleInstruction({ name: role.name });
    toast.success(`Deleted ${role.name}`);
    await reload();
  }

  const createButton = creatable ? (
    <Button size="sm" onClick={() => setEditing(null)}>
      <Plus data-icon="inline-start" />
      New role
    </Button>
  ) : undefined;

  return (
    <>
      <ResourceSection
        description={meta.description}
        hint={!mutable ? "Only administrators can change roles. You can still read every role's instructions." : undefined}
        count={rows.length}
        query={query}
        onQuery={setQuery}
        searchPlaceholder="Search roles"
        actions={createButton}
        loading={loading}
        error={error}
        onRetry={() => void reload()}
        empty={!loading && rows.length === 0}
        noMatches={visible.length === 0 && rows.length > 0}
        emptyIcon={<UserCog />}
        emptyTitle="No roles yet"
        emptyDescription="Runs still offer the built-in general sub-agent. Add a role to give a sub-agent its own prompt, tool boundary, or model."
        emptyAction={createButton}
      >
        {visible.map((role) => {
          const providerEntries = Object.entries(role.modelsByProvider ?? {}).sort(([left], [right]) => left.localeCompare(right));
          return (
            <ResourceRow
              key={role.name}
              name={role.name}
              icon={<UserCog />}
              badges={
                <>
                  {role.toolAccess && (
                    <Pill tone={toolAccessTone[role.toolAccess] ?? "neutral"}>
                      {role.toolAccess} tools
                    </Pill>
                  )}
                  {role.reasoningLevel && (
                    <Pill tone="neutral">
                      {role.reasoningLevel} reasoning
                    </Pill>
                  )}
                  {providerEntries.length > 0 && (
                    <Pill tone="neutral" className="tabular-nums">
                      {providerEntries.length} provider {providerEntries.length === 1 ? "model" : "models"}
                    </Pill>
                  )}
                </>
              }
              description={role.description || "No description"}
              facts={
                (providerEntries.length > 0 || role.model) && (
                  <>
                    {providerEntries.map(([provider, model]) => (
                      <Fact key={provider} title={`${provider}=${model}`}>
                        {provider}={model}
                      </Fact>
                    ))}
                    {role.model && <Fact>legacy: {role.model}</Fact>}
                  </>
                )
              }
              onOpen={() => setEditing(role)}
              openLabel={mutable ? "Edit" : "View"}
              onEdit={() => setEditing(role)}
              onDelete={deletable ? () => setDeleting(role) : undefined}
            />
          );
        })}
      </ResourceSection>

      {editing !== undefined && (
        <RoleDialog
          role={editing}
          readOnly={!mutable}
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
        description={deleteRoleDescription(deleting?.name)}
        confirmLabel="Delete role"
        destructive
        onConfirm={async () => {
          if (deleting) await remove(deleting);
        }}
      />
    </>
  );
}

function RoleDialog({
  role,
  readOnly,
  onClose,
  onSaved,
}: {
  role: RoleInstruction | null;
  readOnly: boolean;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) {
  const isNew = role === null;
  const [initial] = useState(() => draftFromRole(role));
  const [draft, setDraft] = useState<Draft>(initial);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const patch = (changes: Partial<Draft>) => setDraft((current) => ({ ...current, ...changes }));

  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  const valid =
    Boolean(draft.name.trim()) &&
    (!isNew || resourceNameError(draft.name.trim()) === null) &&
    Boolean(draft.instructions.trim()) &&
    keyValueRowsError(draft.providerModels, "Provider") === null;

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const modelsByProvider = Object.fromEntries(
        Object.entries(rowsToRecord(draft.providerModels, "Provider")).map(([provider, model]) => [provider.toLowerCase(), model.trim()]),
      );
      const value = create(RoleInstructionSchema, {
        name: draft.name.trim(),
        description: draft.description.trim(),
        instructions: draft.instructions,
        toolAccess: draft.toolAccess,
        model: draft.model.trim(),
        modelsByProvider,
        reasoningLevel: draft.reasoningLevel === "inherit" ? "" : draft.reasoningLevel,
      });
      await (isNew ? client.createRoleInstruction({ instruction: value }) : client.updateRoleInstruction({ instruction: value }));
      toast.success(`${isNew ? "Created" : "Saved"} ${value.name}`);
      await onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  const activeProviders = draft.providerModels.filter((row) => row.key.trim() || row.value.trim()).length;
  const routingSummary =
    activeProviders > 0 || draft.reasoningLevel !== "inherit"
      ? joinFacts([
          activeProviders > 0 && `${activeProviders} provider ${activeProviders === 1 ? "model" : "models"}`,
          draft.reasoningLevel !== "inherit" && `${draft.reasoningLevel} reasoning`,
        ])
      : "Inherits the run's model";

  return (
    <ResourceFormDialog
      open
      onClose={onClose}
      icon={<UserCog />}
      title={isNew ? "New role" : readOnly ? role.name : `Edit ${role.name}`}
      description="The prompt and tool boundary for one sub-agent the parent agent can delegate to."
      dirty={dirty}
      saving={saving}
      canSave={valid && (isNew || dirty)}
      saveLabel={isNew ? "Create role" : "Save changes"}
      readOnly={readOnly}
      error={error}
      onSave={() => void save()}
    >
      <FormSection title="Role">
        <NameField
          id="role-name"
          value={draft.name}
          onChange={(name) => patch({ name })}
          isNew={isNew}
          placeholder="reviewer"
          hint={`The parent agent delegates to the role by this name. Name a role ${GENERAL_ROLE} to replace the built-in general sub-agent.`}
          autoFocus={isNew}
        />
        <FlowField id="role-description" label="Description" hint="Shown to the parent agent when it picks a sub-agent. Say what the role can do and when to use it.">
          <Input id="role-description" value={draft.description} onChange={(event) => patch({ description: event.target.value })} placeholder="Read-only. Use for a second look at a plan or diff." />
        </FlowField>
      </FormSection>

      <FormSection title="Tool access">
        <ChoiceCards
          aria-label="Tool access"
          value={draft.toolAccess}
          onChange={(toolAccess) => patch({ toolAccess })}
          columns={4}
          options={[
            { value: "full", label: "Full", description: "Every tool the run has" },
            { value: "read-only", label: "Read-only", description: "Inspect only; no edits or mutations" },
            { value: "analysis", label: "Analysis", description: "Read and reason; no execution tools" },
            { value: "execution", label: "Execution", description: "Build, test, and edit tools" },
          ]}
        />
      </FormSection>

      <FormSection title="Instructions">
        <FlowField id="role-instructions" label="Instructions" required hint="Added after the shared sub-agent base prompt. Keep it short: the parent's task message carries the task-specific instructions.">
          <Textarea
            id="role-instructions"
            value={draft.instructions}
            onChange={(event) => patch({ instructions: event.target.value })}
            placeholder="Review the change in the task and report problems with file:line evidence."
            className="min-h-[280px] font-mono text-xs"
            spellCheck={false}
            required
          />
        </FlowField>
      </FormSection>

      <OptionRows label="Options">
        <OptionRow icon={Cpu} title="Model routing" summary={routingSummary} modified={activeProviders > 0 || draft.reasoningLevel !== "inherit" || Boolean(draft.model)}>
          <FlowField label="Models by provider" hint="Leave empty to use the parent run's model. A pinned model stays put when the parent moves to a newer one.">
            <KeyValueRows
              rows={draft.providerModels}
              onChange={(providerModels) => patch({ providerModels: providerModels.map((row) => ({ ...row, key: row.key.toLowerCase() })) })}
              keyLabel="Provider"
              valueLabel="Model"
              keyPlaceholder="anthropic"
              valuePlaceholder="model id"
              addLabel="Add provider"
              keySuggestions={["anthropic", "openai", "copilot"]}
            />
          </FlowField>
          <FlowField id="role-reasoning" label="Reasoning level" hint="Inherit uses the parent run's reasoning level.">
            <Select value={draft.reasoningLevel} onValueChange={(value) => patch({ reasoningLevel: value ? String(value) : "inherit" })}>
              <SelectTrigger id="role-reasoning" className="w-full sm:w-[220px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="inherit">Inherit</SelectItem>
                {reasoningLevels.map((level) => (
                  <SelectItem key={level} value={level}>
                    {level}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </FlowField>
          <FlowField id="role-model" label="Legacy model" hint="Compatibility value only. Runtime routing uses provider models.">
            <Input id="role-model" value={draft.model} onChange={(event) => patch({ model: event.target.value })} placeholder="claude-sonnet-4-6" className="font-mono" autoComplete="off" />
          </FlowField>
        </OptionRow>
      </OptionRows>
    </ResourceFormDialog>
  );
}
