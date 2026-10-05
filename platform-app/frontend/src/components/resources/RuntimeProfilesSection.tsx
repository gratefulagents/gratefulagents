import { create } from "@bufbuild/protobuf";
import { useCallback, useMemo, useState } from "react";
import { Box, Container, Gauge, HardDrive, Layers, Plus, Terminal } from "lucide-react";

import { client } from "@/lib/client";
import type { StatusTone } from "@/lib/status";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { filterByQuery } from "@/components/ui/list-search";
import { toast } from "@/components/ui/toaster";
import { Chip, FlowField, OptionRow, OptionRows } from "@/components/create-flow/create-flow";
import { RuntimeProfileSchema, type RuntimeProfile } from "@/rpc/platform/service_pb";
import { ResourceFormDialog } from "@/components/resources/ResourceFormDialog";
import { Fact, Pill, ResourceRow, ResourceSection } from "@/components/resources/ResourceSection";
import {
  ChoiceCards,
  FieldError,
  FieldGrid,
  FormSection,
  InlineNote,
  KeyValueRows,
  NameField,
  SwitchRow,
} from "@/components/resources/form-kit";
import { joinFacts, resourceMeta } from "@/components/resources/resource-helpers";
import {
  draftFromProfile,
  emptyRuntimeProfileDraft,
  parseStringList,
  profileFromDraft,
  runtimeProfileDraftErrors,
  type RuntimeProfileDraft,
} from "@/components/resources/runtime-profile-form";
import { useResourceList } from "@/components/resources/use-resource-list";

const meta = resourceMeta["runtime-profiles"];

const permissionTone: Record<string, StatusTone> = {
  "read-only": "info",
  "workspace-write": "neutral",
  "danger-full-access": "danger",
};
const egressTone: Record<string, StatusTone> = {
  restricted: "neutral",
  unrestricted: "warning",
  disabled: "warning",
};


function profileDescription(profile: RuntimeProfile): string {
  return joinFacts([
    profile.defaultTimeout && `timeout ${profile.defaultTimeout}`,
    profile.persistWorkspace ? `persistent workspace (${profile.workspaceSize || "default size"})` : "ephemeral workspace",
    profile.enablePrivateProcfs && "private procfs",
    profile.runtimeClassName && `runtime class ${profile.runtimeClassName}`,
    profile.warmPoolRef && `warm pool ${profile.warmPoolRef}`,
    profile.sandboxTemplateRef && `sandbox template ${profile.sandboxTemplateRef}`,
  ]);
}

export function RuntimeProfilesSection() {
  const load = useCallback(async () => (await client.listRuntimeProfiles({})).profiles, []);
  const { rows, loading, error, reload } = useResourceList(load);
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<RuntimeProfile | null | undefined>();
  const [deleting, setDeleting] = useState<RuntimeProfile | null>(null);

  const visible = useMemo(
    () => filterByQuery(rows, query, (profile) => [profile.name, profile.permissionMode, profile.egressMode, profile.runtimeClassName]),
    [rows, query],
  );

  async function remove(profile: RuntimeProfile) {
    await client.deleteRuntimeProfile({ name: profile.name });
    toast.success(`Deleted ${profile.name}`);
    await reload();
  }

  const createButton = (
    <Button size="sm" onClick={() => setEditing(null)}>
      <Plus data-icon="inline-start" />
      New profile
    </Button>
  );

  return (
    <>
      <ResourceSection
        description={meta.description}
        count={rows.length}
        query={query}
        onQuery={setQuery}
        searchPlaceholder="Search profiles"
        actions={createButton}
        loading={loading}
        error={error}
        onRetry={() => void reload()}
        empty={!loading && rows.length === 0}
        noMatches={visible.length === 0 && rows.length > 0}
        emptyIcon={<Container />}
        emptyTitle="No runtime profiles yet"
        emptyDescription="Decide what run pods may do: file permissions, network egress, workspace persistence, and concurrency. Projects and triggers pick a profile by name."
        emptyAction={createButton}
      >
        {visible.map((profile) => {
          const requests = Object.entries(profile.resourceRequests ?? {}).filter(([key]) => key === "cpu" || key === "memory");
          const envCount = Object.keys(profile.commandEnv ?? {}).length;
          return (
            <ResourceRow
              key={profile.name}
              name={profile.name}
              icon={<Container />}
              badges={
                <>
                  <Pill tone={permissionTone[profile.permissionMode] ?? "neutral"}>{profile.permissionMode || "workspace-write"}</Pill>
                  <Pill tone={egressTone[profile.egressMode] ?? "neutral"}>{profile.egressMode || "restricted"} egress</Pill>
                  {profile.gitRemoteWrites === "disabled" && <Pill tone="neutral">git pushes off</Pill>}
                </>
              }
              description={profileDescription(profile)}
              facts={
                (profile.maxConcurrentRuns > 0 || profile.perNamespaceMaxConcurrentRuns > 0 || profile.staleRunTimeout || envCount > 0 || requests.length > 0) && (
                  <>
                    {profile.maxConcurrentRuns > 0 && <Fact>{profile.maxConcurrentRuns} per profile</Fact>}
                    {profile.perNamespaceMaxConcurrentRuns > 0 && <Fact>{profile.perNamespaceMaxConcurrentRuns} per namespace</Fact>}
                    {profile.staleRunTimeout && <Fact>stale after {profile.staleRunTimeout}</Fact>}
                    {envCount > 0 && <Fact>{envCount} env</Fact>}
                    {requests.map(([key, value]) => (
                      <Fact key={key}>
                        {key} {value}
                      </Fact>
                    ))}
                  </>
                )
              }
              onOpen={() => setEditing(profile)}
              onEdit={() => setEditing(profile)}
              onDelete={() => setDeleting(profile)}
            />
          );
        })}
      </ResourceSection>

      {editing !== undefined && (
        <RuntimeProfileDialog
          profile={editing}
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
        description="Projects and triggers that reference this profile fall back to platform defaults. This cannot be undone."
        confirmLabel="Delete profile"
        destructive
        onConfirm={async () => {
          if (deleting) await remove(deleting);
        }}
      />
    </>
  );
}

const TIMEOUT_PRESETS = ["30m", "1h", "2h", "4h"];

function RuntimeProfileDialog({ profile, onClose, onSaved }: { profile: RuntimeProfile | null; onClose: () => void; onSaved: () => Promise<void> }) {
  const isNew = profile === null;
  const [initial] = useState<RuntimeProfileDraft>(() => (profile ? draftFromProfile(profile) : emptyRuntimeProfileDraft()));
  const [draft, setDraft] = useState<RuntimeProfileDraft>(initial);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = <K extends keyof RuntimeProfileDraft>(key: K, value: RuntimeProfileDraft[K]) =>
    setDraft((current) => ({ ...current, [key]: value }));

  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  const errors = runtimeProfileDraftErrors(draft, isNew);
  const valid = Object.keys(errors).length === 0;
  const hasClaims = (profile?.resourceClaims?.length ?? 0) > 0;

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const value = create(RuntimeProfileSchema, profileFromDraft(draft, isNew));
      await (isNew ? client.createRuntimeProfile({ profile: value }) : client.updateRuntimeProfile({ profile: value }));
      toast.success(`${isNew ? "Created" : "Saved"} ${value.name}`);
      await onSaved();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  }

  const pathCount = (["commandPath", "commandPathPrepend", "commandPathAppend", "extraReadOnlyPaths", "extraWritablePaths"] as const).reduce(
    (total, field) => total + parseStringList(draft[field]).length,
    0,
  );
  const envCount = draft.commandEnv.filter((row) => row.key.trim()).length;
  const resourceSummary = joinFacts(
    draft.resourceRequests.filter((row) => row.key.trim()).map((row) => `${row.key.trim()} ${row.value.trim()}`),
  );
  const sandboxSummary = joinFacts([
    draft.sandboxTemplateRef.trim() && `template ${draft.sandboxTemplateRef.trim()}`,
    draft.runtimeClassName.trim() && `class ${draft.runtimeClassName.trim()}`,
    draft.warmPoolRef.trim() && `pool ${draft.warmPoolRef.trim()}`,
  ]);
  const admissionSummary = joinFacts([
    Number(draft.maxConcurrentRuns) > 0 && `${draft.maxConcurrentRuns} per profile`,
    Number(draft.perNamespaceMaxConcurrentRuns) > 0 && `${draft.perNamespaceMaxConcurrentRuns} per namespace`,
    draft.staleRunTimeout.trim() && `stale ${draft.staleRunTimeout.trim()}`,
  ]);
  const workspaceSummary = joinFacts([
    draft.persistWorkspace ? `Persistent · ${draft.workspaceSize.trim() || "10Gi"}` : "Ephemeral",
    draft.enablePrivateProcfs && "private procfs",
  ]);

  return (
    <ResourceFormDialog
      open
      onClose={onClose}
      icon={<Container />}
      title={isNew ? "New runtime profile" : `Edit ${profile.name}`}
      description="Defaults for every run pod that references this profile. Saving replaces the whole spec."
      dirty={dirty}
      saving={saving}
      canSave={valid && (isNew || dirty)}
      saveLabel={isNew ? "Create profile" : "Save changes"}
      error={error}
      onSave={() => void save()}
    >
      <FormSection title="Profile">
        <FieldGrid>
          <NameField id="profile-name" value={draft.name} onChange={(name) => set("name", name)} isNew={isNew} placeholder="build-default" autoFocus={isNew} />
        </FieldGrid>
        {hasClaims && <InlineNote>Resource claims are managed outside the dashboard and are preserved on save.</InlineNote>}
      </FormSection>

      <FormSection title="Permissions" description="The effective mode of a run is the most restrictive of this profile and the mode it uses.">
        <div className="space-y-2">
          <span id="profile-permission-label" className="text-[12.5px] font-medium">
            Permission mode
          </span>
          <ChoiceCards
            aria-labelledby="profile-permission-label"
            value={draft.permissionMode}
            onChange={(permissionMode) => set("permissionMode", permissionMode)}
            columns={3}
            options={[
              { value: "read-only", label: "Read-only", description: "Inspect the workspace; no edits or writes" },
              { value: "workspace-write", label: "Workspace write", description: "Edit files and run commands inside /workspace" },
              { value: "danger-full-access", label: "Full access", tone: "danger", description: "No sandbox restrictions on commands; only for trusted automation" },
            ]}
          />
          {draft.permissionMode === "danger-full-access" && (
            <InlineNote tone="danger">
              Commands run without the subprocess sandbox. Every run using this profile can reach anything the pod can; pair it with disabled egress and trusted prompts only.
            </InlineNote>
          )}
        </div>
        <SwitchRow
          id="profile-git-remote-writes"
          label="Allow Git remote writes"
          hint="Off removes push and pull-request creation while keeping workspace edits, local commits, fetches, and pulls."
          checked={draft.gitRemoteWrites !== "disabled"}
          onCheckedChange={(checked) => set("gitRemoteWrites", checked ? "enabled" : "disabled")}
        />
        <div className="space-y-2">
          <span id="profile-egress-label" className="text-[12.5px] font-medium">
            Network egress
          </span>
          <ChoiceCards
            aria-labelledby="profile-egress-label"
            value={draft.egressMode}
            onChange={(egressMode) => set("egressMode", egressMode)}
            columns={3}
            options={[
              { value: "restricted", label: "Restricted", description: "Only allow-listed hosts: package registries, Git, model APIs" },
              { value: "unrestricted", label: "Unrestricted", tone: "warning", description: "Any outbound host" },
              { value: "disabled", label: "Disabled", description: "No outbound network at all" },
            ]}
          />
        </div>
        <FieldGrid>
          <FlowField id="profile-default-timeout" label="Default timeout" hint="Go duration. Blank keeps the platform default.">
            <Input
              id="profile-default-timeout"
              value={draft.defaultTimeout}
              onChange={(event) => set("defaultTimeout", event.target.value)}
              placeholder="1h"
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={errors.defaultTimeout ? true : undefined}
            />
            <div className="flex flex-wrap gap-1.5 pt-1.5">
              {TIMEOUT_PRESETS.map((preset) => (
                <Chip key={preset} mono selected={draft.defaultTimeout.trim() === preset} onSelect={() => set("defaultTimeout", preset)}>
                  {preset}
                </Chip>
              ))}
            </div>
            <FieldError>{errors.defaultTimeout}</FieldError>
          </FlowField>
        </FieldGrid>
      </FormSection>

      <OptionRows label="Options">
        <OptionRow
          icon={HardDrive}
          title="Workspace"
          summary={workspaceSummary}
          modified={draft.persistWorkspace || draft.enablePrivateProcfs}
        >
          <SwitchRow
            id="profile-persist-workspace"
            label="Persist the workspace"
            hint="Backs /workspace with a volume so files survive pod restarts, for example pause and resume."
            checked={draft.persistWorkspace}
            onCheckedChange={(checked) => set("persistWorkspace", checked)}
          />
          {draft.persistWorkspace && (
            <FieldGrid>
              <FlowField id="profile-workspace-size" label="Workspace size" hint="Kubernetes quantity; blank uses 10Gi.">
                <Input
                  id="profile-workspace-size"
                  value={draft.workspaceSize}
                  onChange={(event) => set("workspaceSize", event.target.value)}
                  placeholder="10Gi"
                  className="font-mono"
                  autoComplete="off"
                  spellCheck={false}
                  aria-invalid={errors.workspaceSize ? true : undefined}
                />
                <FieldError>{errors.workspaceSize}</FieldError>
              </FlowField>
            </FieldGrid>
          )}
          <SwitchRow
            id="profile-private-procfs"
            label="Private /proc"
            hint="Required by Chromium and toolchains that inspect /proc. The cluster must support pod user namespaces and unmasked proc mounts."
            checked={draft.enablePrivateProcfs}
            onCheckedChange={(checked) => set("enablePrivateProcfs", checked)}
          />
        </OptionRow>

        <OptionRow icon={Box} title="Sandbox" summary={sandboxSummary || "Cluster defaults"} modified={Boolean(sandboxSummary)}>
          <FieldGrid className="sm:grid-cols-3">
            <RefField id="profile-sandbox-template" label="Sandbox template" value={draft.sandboxTemplateRef} onChange={(value) => set("sandboxTemplateRef", value)} error={errors.sandboxTemplateRef} hint="SandboxTemplate name; blank uses the cluster default." />
            <RefField id="profile-runtime-class" label="Runtime class" value={draft.runtimeClassName} onChange={(value) => set("runtimeClassName", value)} error={errors.runtimeClassName} hint="Pod RuntimeClass, e.g. gvisor; blank uses the cluster default." />
            <RefField id="profile-warm-pool" label="Warm pool" value={draft.warmPoolRef} onChange={(value) => set("warmPoolRef", value)} error={errors.warmPoolRef} hint="WarmPool name to take pre-provisioned pods from; blank starts cold." />
          </FieldGrid>
        </OptionRow>

        <OptionRow
          icon={Terminal}
          title="Command sandbox"
          summary={pathCount + envCount > 0 ? joinFacts([pathCount > 0 && `${pathCount} ${pathCount === 1 ? "path" : "paths"}`, envCount > 0 && `${envCount} env`]) : "Defaults"}
          modified={pathCount + envCount > 0}
        >
          <PathsField id="profile-command-path" label="PATH override" value={draft.commandPath} onChange={(value) => set("commandPath", value)} error={errors.commandPath} hint="Replaces the subprocess PATH. One absolute path per line; when set, prepend and append are ignored." />
          <FieldGrid>
            <PathsField id="profile-command-path-prepend" label="PATH prepend" value={draft.commandPathPrepend} onChange={(value) => set("commandPathPrepend", value)} error={errors.commandPathPrepend} hint="Added before the default PATH. One absolute path per line." />
            <PathsField id="profile-command-path-append" label="PATH append" value={draft.commandPathAppend} onChange={(value) => set("commandPathAppend", value)} error={errors.commandPathAppend} hint="Added after the default PATH, so project tools resolve after system tools." />
          </FieldGrid>
          <FieldGrid>
            <PathsField id="profile-extra-read-only" label="Extra read-only paths" value={draft.extraReadOnlyPaths} onChange={(value) => set("extraReadOnlyPaths", value)} error={errors.extraReadOnlyPaths} hint="Mounted read-only inside model-controlled subprocesses. One absolute path per line." />
            <PathsField id="profile-extra-writable" label="Extra writable paths" value={draft.extraWritablePaths} onChange={(value) => set("extraWritablePaths", value)} error={errors.extraWritablePaths} hint="Admin-owned scratch or cache paths. Workspace, home, and system paths are rejected." />
          </FieldGrid>
          <FlowField label="Environment" hint="Non-secret variables for sandboxed subprocesses; values may reference $PATH. Secret-like keys are ignored.">
            <KeyValueRows
              rows={draft.commandEnv}
              onChange={(commandEnv) => set("commandEnv", commandEnv)}
              keyLabel="Variable"
              keyPlaceholder="GOFLAGS"
              valuePlaceholder="-mod=mod"
              addLabel="Add variable"
            />
          </FlowField>
        </OptionRow>

        <OptionRow icon={Gauge} title="Resources" summary={resourceSummary || "Cluster defaults"} modified={draft.resourceRequests.length + draft.resourceLimits.length > 0}>
          <FlowField label="Requests" hint="Scheduling reservations for the run pod.">
            <KeyValueRows
              rows={draft.resourceRequests}
              onChange={(resourceRequests) => set("resourceRequests", resourceRequests)}
              keyLabel="Resource"
              keyPlaceholder="cpu"
              valuePlaceholder="500m / 2Gi"
              addLabel="Add request"
              keySuggestions={["cpu", "memory", "ephemeral-storage"]}
            />
            <QuantityError rows={draft.resourceRequests} message={errors.resourceRequests} />
          </FlowField>
          <FlowField label="Limits" hint="Hard ceilings; a request may not exceed its limit.">
            <KeyValueRows
              rows={draft.resourceLimits}
              onChange={(resourceLimits) => set("resourceLimits", resourceLimits)}
              keyLabel="Resource"
              keyPlaceholder="memory"
              valuePlaceholder="500m / 2Gi"
              addLabel="Add limit"
              keySuggestions={["cpu", "memory", "ephemeral-storage"]}
            />
            <QuantityError rows={draft.resourceLimits} message={errors.resourceLimits} />
          </FlowField>
        </OptionRow>

        <OptionRow icon={Layers} title="Admission" summary={admissionSummary || "No limits"} modified={Boolean(admissionSummary)}>
          <FieldGrid>
            <FlowField id="profile-max-concurrent" label="Max concurrent runs" hint="0 = unlimited">
              <Input
                id="profile-max-concurrent"
                type="number"
                min={0}
                value={draft.maxConcurrentRuns}
                onChange={(event) => set("maxConcurrentRuns", event.target.value)}
                aria-invalid={errors.maxConcurrentRuns ? true : undefined}
              />
              <FieldError>{errors.maxConcurrentRuns}</FieldError>
            </FlowField>
            <FlowField id="profile-max-concurrent-namespace" label="Per-namespace max concurrent runs" hint="0 = unlimited">
              <Input
                id="profile-max-concurrent-namespace"
                type="number"
                min={0}
                value={draft.perNamespaceMaxConcurrentRuns}
                onChange={(event) => set("perNamespaceMaxConcurrentRuns", event.target.value)}
                aria-invalid={errors.perNamespaceMaxConcurrentRuns ? true : undefined}
              />
              <FieldError>{errors.perNamespaceMaxConcurrentRuns}</FieldError>
            </FlowField>
            <FlowField id="profile-stale-timeout" label="Stale run timeout" hint="Runs idle longer than this are reclaimed. Blank disables.">
              <Input
                id="profile-stale-timeout"
                value={draft.staleRunTimeout}
                onChange={(event) => set("staleRunTimeout", event.target.value)}
                placeholder="30m"
                className="font-mono"
                autoComplete="off"
                spellCheck={false}
                aria-invalid={errors.staleRunTimeout ? true : undefined}
              />
              <FieldError>{errors.staleRunTimeout}</FieldError>
            </FlowField>
          </FieldGrid>
        </OptionRow>
      </OptionRows>
    </ResourceFormDialog>
  );
}

function RefField({ id, label, value, onChange, error, hint }: { id: string; label: string; value: string; onChange: (value: string) => void; error?: string; hint: string }) {
  return (
    <FlowField id={id} label={label} hint={hint}>
      <Input id={id} value={value} onChange={(event) => onChange(event.target.value)} className="font-mono" autoComplete="off" spellCheck={false} aria-invalid={error ? true : undefined} />
      <FieldError>{error}</FieldError>
    </FlowField>
  );
}

function PathsField({ id, label, value, onChange, error, hint }: { id: string; label: string; value: string; onChange: (value: string) => void; error?: string; hint: string }) {
  return (
    <FlowField id={id} label={label} hint={hint}>
      <Textarea
        id={id}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder="/usr/local/bin"
        className="min-h-[72px] font-mono text-xs"
        spellCheck={false}
        aria-invalid={error ? true : undefined}
      />
      <FieldError>{error}</FieldError>
    </FlowField>
  );
}

/** KeyValueRows already reports empty/duplicate keys; only surface quantity problems here. */
function QuantityError({ rows, message }: { rows: { key: string; value: string }[]; message?: string }) {
  if (!message) return null;
  const keyProblem = rows.some((row) => (row.value.trim() && !row.key.trim())) || new Set(rows.map((row) => row.key.trim()).filter(Boolean)).size !== rows.filter((row) => row.key.trim()).length;
  if (keyProblem) return null;
  return <FieldError>{message}</FieldError>;
}
