import { create } from "@bufbuild/protobuf";
import { useCallback, useMemo, useState } from "react";
import { Blocks, Gauge, Lock, Plus, Workflow } from "lucide-react";

import { client } from "@/lib/client";
import { resourceNameError } from "@/lib/resourceNames";
import { useAuth } from "@/contexts/AuthContext";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { filterByQuery } from "@/components/ui/list-search";
import { toast } from "@/components/ui/toaster";
import { FlowField, OptionRow, OptionRows, Segmented } from "@/components/create-flow/create-flow";
import { MCPServerPicker } from "@/components/MCPServerPicker";
import { SkillPicker } from "@/components/SkillPicker";
import { ModeConstraintsSchema, ModeTemplateSchema, type ModeTemplate } from "@/rpc/platform/service_pb";
import { ResourceFormDialog } from "@/components/resources/ResourceFormDialog";
import { Fact, Pill, ResourceRow, ResourceSection } from "@/components/resources/ResourceSection";
import { ChoiceCards, FieldError, FieldGrid, FormSection, NameField, SwitchRow, TokenInput } from "@/components/resources/form-kit";
import {
  canCreateResource,
  canDeleteResource,
  canMutateResource,
  countError,
  joinFacts,
  resourceMeta,
} from "@/components/resources/resource-helpers";
import { useResourceList } from "@/components/resources/use-resource-list";

type Draft = {
  name: string;
  displayName: string;
  version: string;
  description: string;
  category: string;
  executionStrategy: string;
  autonomous: boolean;
  instructions: string;
  permissionMode: string;
  allowedMutatingTools: string[];
  defaultMcpServerRefs: string[];
  defaultSkillRefs: string[];
  manageConstraints: boolean;
  maxTurns: string;
  subagentMaxTurns: string;
  maxRuntimeMinutes: string;
  maxRetries: string;
  maxConcurrentSubagents: string;
};

const strategyHints: Record<string, string> = {
  serial: "Steps run one after another.",
  parallel: "Independent steps run at the same time.",
  pipeline: "Each step feeds the next.",
};

const limitFields: Array<{ key: keyof Draft & string; label: string }> = [
  { key: "maxTurns", label: "Max turns" },
  { key: "subagentMaxTurns", label: "Sub-agent max turns" },
  { key: "maxRuntimeMinutes", label: "Max runtime (minutes)" },
  { key: "maxRetries", label: "Max retries" },
  { key: "maxConcurrentSubagents", label: "Max concurrent sub-agents" },
];

function draftFromTemplate(template: ModeTemplate | null): Draft {
  if (!template) {
    return {
      name: "", displayName: "", version: "v1", description: "", category: "direct", executionStrategy: "serial",
      autonomous: false, instructions: "", permissionMode: "", allowedMutatingTools: [], defaultMcpServerRefs: [],
      defaultSkillRefs: [], manageConstraints: false, maxTurns: "", subagentMaxTurns: "", maxRuntimeMinutes: "",
      maxRetries: "", maxConcurrentSubagents: "",
    };
  }
  const constraints = template.constraints;
  return {
    name: template.name,
    displayName: template.displayName ?? "",
    version: template.version ?? "",
    description: template.description ?? "",
    category: template.category || "direct",
    executionStrategy: template.executionStrategy || "serial",
    autonomous: Boolean(template.autonomous),
    instructions: template.instructions ?? "",
    permissionMode: template.permissionMode ?? "",
    allowedMutatingTools: [...(template.allowedMutatingTools ?? [])],
    defaultMcpServerRefs: [...(template.defaultMcpServerRefs ?? [])],
    defaultSkillRefs: [...(template.defaultSkillRefs ?? [])],
    manageConstraints: Boolean(constraints),
    maxTurns: constraints ? String(constraints.maxTurns ?? 0) : "",
    subagentMaxTurns: constraints ? String(constraints.subagentMaxTurns ?? 0) : "",
    maxRuntimeMinutes: constraints ? String(constraints.maxRuntimeMinutes ?? 0) : "",
    maxRetries: constraints ? String(constraints.maxRetries ?? 0) : "",
    maxConcurrentSubagents: constraints ? String(constraints.maxConcurrentSubagents ?? 0) : "",
  };
}

function limitsSummary(values: { maxTurns?: number | string; maxRuntimeMinutes?: number | string; maxRetries?: number | string }): string {
  const turns = Number(values.maxTurns) || 0;
  const minutes = Number(values.maxRuntimeMinutes) || 0;
  const retries = Number(values.maxRetries) || 0;
  return joinFacts([turns > 0 && `${turns} turns`, minutes > 0 && `${minutes} min`, retries > 0 && `${retries} retries`]);
}

const meta = resourceMeta.modes;

export function ModesSection() {
  const { user } = useAuth();
  const creatable = canCreateResource("modes", user?.role);
  const mutable = canMutateResource("modes", user?.role);
  const deletable = canDeleteResource("modes", user?.role);
  const load = useCallback(async () => (await client.listModeTemplates({})).templates, []);
  const { rows, loading, error, reload } = useResourceList(load);
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<ModeTemplate | null | undefined>();
  const [deleting, setDeleting] = useState<ModeTemplate | null>(null);

  const visible = useMemo(
    () => filterByQuery(rows, query, (template) => [template.name, template.displayName, template.description, template.category, template.executionStrategy]),
    [rows, query],
  );

  async function remove(template: ModeTemplate) {
    await client.deleteModeTemplate({ name: template.name });
    toast.success(`Deleted ${template.name}`);
    await reload();
  }

  const createButton = creatable ? (
    <Button size="sm" onClick={() => setEditing(null)}>
      <Plus data-icon="inline-start" />
      New mode
    </Button>
  ) : undefined;

  return (
    <>
      <ResourceSection
        description={meta.description}
        hint={!deletable ? "You can create and edit modes. Only administrators can delete them." : undefined}
        count={rows.length}
        query={query}
        onQuery={setQuery}
        searchPlaceholder="Search modes"
        actions={createButton}
        loading={loading}
        error={error}
        onRetry={() => void reload()}
        empty={!loading && rows.length === 0}
        noMatches={visible.length === 0 && rows.length > 0}
        emptyIcon={<Workflow />}
        emptyTitle="No modes yet"
        emptyDescription="Give runs a reusable behavior template: system instructions, autonomy, an execution strategy, default tools, and limits."
        emptyAction={createButton}
      >
        {visible.map((template) => {
          const title = template.displayName || template.name;
          const servers = template.defaultMcpServerRefs ?? [];
          const skills = template.defaultSkillRefs ?? [];
          const limits = template.constraints ? limitsSummary(template.constraints) : "";
          return (
            <ResourceRow
              key={template.name}
              name={template.name}
              icon={<Workflow />}
              title={title}
              badges={
                <>
                  <Pill tone={template.category === "orchestrated" ? "info" : "neutral"}>
                    {template.category === "orchestrated" ? "Orchestrated" : "Direct"}
                  </Pill>
                  {template.executionStrategy && <Pill tone="neutral">{template.executionStrategy}</Pill>}
                  {template.autonomous && <Pill tone="purple">Autonomous</Pill>}
                  {template.permissionMode && (
                    <Pill tone={template.permissionMode === "read-only" ? "warning" : "neutral"}>clamps to {template.permissionMode}</Pill>
                  )}
                  {template.version && <span className="font-mono text-[11px] text-muted-foreground">{/^v/i.test(template.version) ? template.version : `v${template.version}`}</span>}
                </>
              }
              description={template.description || "No description"}
              facts={
                (title !== template.name || servers.length > 0 || skills.length > 0 || limits) && (
                  <>
                    {title !== template.name && <Fact>{template.name}</Fact>}
                    {servers.length > 0 && <Fact title={servers.join(", ")}>{servers.length} default {servers.length === 1 ? "server" : "servers"}</Fact>}
                    {skills.length > 0 && <Fact title={skills.join(", ")}>{skills.length} default {skills.length === 1 ? "skill" : "skills"}</Fact>}
                    {limits && <Fact>{limits}</Fact>}
                  </>
                )
              }
              onOpen={mutable ? () => setEditing(template) : undefined}
              onEdit={mutable ? () => setEditing(template) : undefined}
              onDelete={deletable ? () => setDeleting(template) : undefined}
            />
          );
        })}
      </ResourceSection>

      {editing !== undefined && (
        <ModeDialog
          template={editing}
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
        description="Runs already using this mode keep their instructions; new runs can no longer select it. This cannot be undone."
        confirmLabel="Delete mode"
        destructive
        onConfirm={async () => {
          if (deleting) await remove(deleting);
        }}
      />
    </>
  );
}


function ModeDialog({ template, onClose, onSaved }: { template: ModeTemplate | null; onClose: () => void; onSaved: () => Promise<void> }) {
  const isNew = template === null;
  const [initial] = useState(() => draftFromTemplate(template));
  const [draft, setDraft] = useState<Draft>(initial);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const patch = (changes: Partial<Draft>) => setDraft((current) => ({ ...current, ...changes }));

  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  const limitErrors = Object.fromEntries(
    limitFields.map(({ key, label }) => [key, draft.manageConstraints ? countError(String(draft[key]), label) : null]),
  ) as Record<string, string | null>;
  const valid =
    Boolean(draft.name.trim()) &&
    (!isNew || resourceNameError(draft.name.trim()) === null) &&
    Boolean(draft.version.trim()) &&
    Object.values(limitErrors).every((problem) => !problem);

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const constraints = draft.manageConstraints
        ? create(ModeConstraintsSchema, {
            maxTurns: Number(draft.maxTurns) || 0,
            subagentMaxTurns: Number(draft.subagentMaxTurns) || 0,
            maxRuntimeMinutes: Number(draft.maxRuntimeMinutes) || 0,
            maxRetries: Number(draft.maxRetries) || 0,
            maxConcurrentSubagents: Number(draft.maxConcurrentSubagents) || 0,
          })
        : undefined;
      const value = create(ModeTemplateSchema, {
        name: draft.name.trim(),
        version: draft.version.trim(),
        displayName: draft.displayName.trim(),
        description: draft.description.trim(),
        category: draft.category,
        executionStrategy: draft.executionStrategy,
        instructions: draft.instructions,
        autonomous: draft.autonomous,
        permissionMode: draft.permissionMode,
        allowedMutatingTools: draft.allowedMutatingTools,
        defaultMcpServerRefs: draft.defaultMcpServerRefs,
        defaultSkillRefs: draft.defaultSkillRefs,
        constraints,
      });
      await (isNew ? client.createModeTemplate({ template: value }) : client.updateModeTemplate({ template: value }));
      toast.success(`${isNew ? "Created" : "Saved"} ${value.name}`);
      await onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  const permissionSummary = joinFacts([
    draft.permissionMode ? `Clamps to ${draft.permissionMode}` : "No clamp",
    draft.allowedMutatingTools.length > 0 && `${draft.allowedMutatingTools.length} ${draft.allowedMutatingTools.length === 1 ? "tool" : "tools"} allowed`,
  ]);
  const defaultsSummary =
    draft.defaultMcpServerRefs.length + draft.defaultSkillRefs.length > 0
      ? `${draft.defaultMcpServerRefs.length} ${draft.defaultMcpServerRefs.length === 1 ? "server" : "servers"} · ${draft.defaultSkillRefs.length} ${draft.defaultSkillRefs.length === 1 ? "skill" : "skills"}`
      : "None";
  const limitsText = draft.manageConstraints ? limitsSummary(draft) || "Platform defaults" : "Managed elsewhere";

  return (
    <ResourceFormDialog
      open
      onClose={onClose}
      icon={<Workflow />}
      title={isNew ? "New mode" : `Edit ${template.name}`}
      description="The template a run starts from: instructions, autonomy, strategy, defaults, and limits."
      dirty={dirty}
      saving={saving}
      canSave={valid && (isNew || dirty)}
      saveLabel={isNew ? "Create mode" : "Save changes"}
      error={error}
      onSave={() => void save()}
    >
      <FormSection title="Mode">
        <FieldGrid>
          <NameField id="mode-name" value={draft.name} onChange={(name) => patch({ name })} isNew={isNew} placeholder="my-mode" autoFocus={isNew} />
          <FlowField id="mode-display-name" label="Display name" hint="Shown in pickers instead of the name.">
            <Input id="mode-display-name" value={draft.displayName} onChange={(event) => patch({ displayName: event.target.value })} placeholder="Autopilot" />
          </FlowField>
        </FieldGrid>
        <FieldGrid>
          <FlowField id="mode-version" label="Version">
            <Input id="mode-version" value={draft.version} onChange={(event) => patch({ version: event.target.value })} placeholder="v1" className="font-mono" autoComplete="off" />
          </FlowField>
          <FlowField id="mode-description" label="Description">
            <Input id="mode-description" value={draft.description} onChange={(event) => patch({ description: event.target.value })} placeholder="What this mode is for" />
          </FlowField>
        </FieldGrid>
      </FormSection>

      <FormSection title="Behavior">
        <div className="space-y-1.5">
          <span id="mode-category-label" className="text-[12.5px] font-medium">Category</span>
          <ChoiceCards
            aria-labelledby="mode-category-label"
            value={draft.category}
            onChange={(category) => patch({ category })}
            columns={2}
            options={[
              { value: "direct", label: "Direct", description: "One agent does the work end to end" },
              { value: "orchestrated", label: "Orchestrated", description: "A lead agent plans and delegates to specialist sub-agents" },
            ]}
          />
        </div>
        <FlowField label="Execution strategy" hint={strategyHints[draft.executionStrategy] ?? ""}>
          <Segmented
            aria-label="Execution strategy"
            value={draft.executionStrategy}
            onChange={(executionStrategy) => patch({ executionStrategy })}
            options={[
              { value: "serial", label: "Serial" },
              { value: "parallel", label: "Parallel" },
              { value: "pipeline", label: "Pipeline" },
            ]}
          />
        </FlowField>
        <SwitchRow
          id="mode-autonomous"
          label="Autonomous"
          hint="Never asks clarifying questions; completes work end to end."
          checked={draft.autonomous}
          onCheckedChange={(autonomous) => patch({ autonomous })}
        />
        <FlowField id="mode-instructions" label="Instructions" hint="System prompt loaded into the run: philosophy, workflow, constraints, and rules.">
          <Textarea
            id="mode-instructions"
            value={draft.instructions}
            onChange={(event) => patch({ instructions: event.target.value })}
            placeholder="You are operating in autopilot mode…"
            className="min-h-[240px] font-mono text-xs"
            spellCheck={false}
          />
        </FlowField>
      </FormSection>

      <OptionRows label="Options">
        <OptionRow icon={Lock} title="Permissions" summary={permissionSummary} modified={Boolean(draft.permissionMode) || draft.allowedMutatingTools.length > 0}>
          <FlowField label="Permission clamp" hint="The effective mode is the most restrictive of this and the runtime profile: a mode can restrict but never grant.">
            <ChoiceCards
              aria-label="Permission clamp"
              value={draft.permissionMode}
              onChange={(permissionMode) => patch({ permissionMode })}
              columns={4}
              options={[
                { value: "", label: "Inherit", description: "No clamp; the runtime profile decides" },
                { value: "read-only", label: "Read-only", description: "Inspect only" },
                { value: "workspace-write", label: "Workspace write", description: "Edit inside /workspace" },
                { value: "danger-full-access", label: "Full access", description: "No sandbox restrictions", tone: "danger" },
              ]}
            />
          </FlowField>
          <FlowField id="mode-allowed-tools" label="Allowed mutating tools" hint="Mutating tools that stay registered even when the effective mode is read-only.">
            <TokenInput
              id="mode-allowed-tools"
              aria-label="Allowed mutating tools"
              value={draft.allowedMutatingTools}
              onChange={(allowedMutatingTools) => patch({ allowedMutatingTools })}
              placeholder="github_create_review"
            />
          </FlowField>
        </OptionRow>

        <OptionRow icon={Blocks} title="Defaults" summary={defaultsSummary} modified={draft.defaultMcpServerRefs.length + draft.defaultSkillRefs.length > 0}>
          <FlowField label="MCP servers" hint="Attached to every run that uses this mode.">
            <MCPServerPicker selected={draft.defaultMcpServerRefs} onChange={(defaultMcpServerRefs) => patch({ defaultMcpServerRefs })} />
          </FlowField>
          <FlowField label="Skills" hint="Enabled on every run that uses this mode; their required servers come along.">
            <SkillPicker selected={draft.defaultSkillRefs} onChange={(defaultSkillRefs) => patch({ defaultSkillRefs })} />
          </FlowField>
        </OptionRow>

        <OptionRow icon={Gauge} title="Limits" summary={limitsText} modified={draft.manageConstraints}>
          <SwitchRow
            id="mode-manage-limits"
            label="Set limits in this mode"
            hint="Off leaves limits managed outside the dashboard untouched."
            checked={draft.manageConstraints}
            onCheckedChange={(manageConstraints) => patch({ manageConstraints })}
          />
          {draft.manageConstraints && (
            <FieldGrid>
              {limitFields.map(({ key, label }) => (
                <FlowField key={key} id={`mode-${key}`} label={label} hint="0 keeps the platform default">
                  <Input
                    id={`mode-${key}`}
                    type="number"
                    min={0}
                    value={String(draft[key])}
                    onChange={(event) => patch({ [key]: event.target.value } as Partial<Draft>)}
                    placeholder="0"
                    aria-invalid={limitErrors[key] ? true : undefined}
                  />
                  <FieldError>{limitErrors[key]}</FieldError>
                </FlowField>
              ))}
            </FieldGrid>
          )}
        </OptionRow>
      </OptionRows>
    </ResourceFormDialog>
  );
}
