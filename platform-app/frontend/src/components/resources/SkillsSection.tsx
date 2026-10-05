import { useCallback, useMemo, useState } from "react";
import { ExternalLink, GitBranch, GraduationCap, Package, PenLine, Plus, Store } from "lucide-react";

import { client } from "@/lib/client";
import type { StatusTone } from "@/lib/status";
import { resourceNameError } from "@/lib/resourceNames";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { filterByQuery } from "@/components/ui/list-search";
import { toast } from "@/components/ui/toaster";
import { FlowField, Segmented } from "@/components/create-flow/create-flow";
import { MCPServerPicker } from "@/components/MCPServerPicker";
import { ResourceFormDialog } from "@/components/resources/ResourceFormDialog";
import { Fact, Pill, ResourceRow, ResourceSection } from "@/components/resources/ResourceSection";
import { FieldGrid, FormSection, InlineNote, NameField } from "@/components/resources/form-kit";
import { SkillCatalogDialog } from "@/components/resources/SkillCatalogDialog";
import { resourceMeta } from "@/components/resources/resource-helpers";
import { useResourceList } from "@/components/resources/use-resource-list";

export interface Skill {
  name: string;
  version: string;
  description: string;
  instructions: string;
  gitUrl: string;
  gitRef: string;
  gitPath: string;
  mcpServerRefs: string[];
  phase: string;
  resolvedName: string;
  resolvedDescription: string;
  resolvedSha: string;
  statusMessage: string;
  catalogSource: string;
  catalogSkillId: string;
  catalogUrl: string;
  catalogHash: string;
}

const emptySkill: Skill = {
  name: "",
  version: "",
  description: "",
  instructions: "",
  gitUrl: "",
  gitRef: "",
  gitPath: "",
  mcpServerRefs: [],
  phase: "",
  resolvedName: "",
  resolvedDescription: "",
  resolvedSha: "",
  statusMessage: "",
  catalogSource: "",
  catalogSkillId: "",
  catalogUrl: "",
  catalogHash: "",
};

function normalizeSkill(skill: Partial<Skill>): Skill {
  return { ...emptySkill, ...skill, mcpServerRefs: skill.mcpServerRefs ?? [] };
}

function phaseTone(phase: string): StatusTone {
  switch (phase) {
    case "Ready":
      return "success";
    case "Error":
      return "danger";
    case "Invalid":
      return "warning";
    default:
      return "neutral";
  }
}

type Source = "inline" | "git";

function sourceOf(skill: Skill): "catalog" | "git" | "inline" {
  if (skill.catalogSource) return "catalog";
  if (skill.gitUrl) return "git";
  return "inline";
}

const sourceLabel = { catalog: "skills.sh", git: "Git", inline: "Inline" } as const;

const meta = resourceMeta.skills;

/**
 * Skills tab: reusable agent instructions written inline, pulled from a Git
 * folder with a SKILL.md, or installed from the skills.sh catalog.
 */
export function SkillsSection() {
  const load = useCallback(async () => ((await client.listSkills({})).skills ?? []).map((skill) => normalizeSkill(skill as unknown as Partial<Skill>)), []);
  const { rows, setRows, loading, error, reload } = useResourceList(load);
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<Skill | null | undefined>();
  const [browsing, setBrowsing] = useState(false);
  const [deleting, setDeleting] = useState<Skill | null>(null);

  const visible = useMemo(
    () =>
      filterByQuery(rows, query, (skill) => [
        skill.name, skill.description, skill.resolvedDescription, skill.resolvedName, skill.catalogSource, skill.catalogSkillId, skill.gitUrl,
      ]),
    [rows, query],
  );

  async function remove(skill: Skill) {
    await client.deleteSkill({ name: skill.name });
    toast.success(`Deleted ${skill.name}`);
    setRows((current) => current.filter((item) => item.name !== skill.name));
  }

  const createButton = (
    <Button size="sm" onClick={() => setEditing(null)}>
      <Plus data-icon="inline-start" />
      New skill
    </Button>
  );

  return (
    <>
      <ResourceSection
        description={meta.description}
        count={rows.length}
        query={query}
        onQuery={setQuery}
        searchPlaceholder="Search skills"
        actions={
          <>
            <Button size="sm" variant="outline" onClick={() => setBrowsing(true)}>
              <Store data-icon="inline-start" />
              Browse skills.sh
            </Button>
            {createButton}
          </>
        }
        loading={loading}
        error={error}
        onRetry={() => void reload()}
        empty={!loading && rows.length === 0}
        noMatches={visible.length === 0 && rows.length > 0}
        emptyIcon={<GraduationCap />}
        emptyTitle="No skills yet"
        emptyDescription="Write instructions inline, point at a Git folder with a SKILL.md, or browse thousands of community skills on skills.sh."
        emptyAction={
          <div className="flex flex-wrap items-center justify-center gap-2">
            <Button size="sm" variant="outline" onClick={() => setBrowsing(true)}>
              <Store data-icon="inline-start" />
              Browse skills.sh
            </Button>
            {createButton}
          </div>
        }
      >
        {visible.map((skill) => {
          const source = sourceOf(skill);
          const Icon = source === "catalog" ? Package : source === "git" ? GitBranch : PenLine;
          const failed = skill.phase === "Error" || skill.phase === "Invalid";
          return (
            <ResourceRow
              key={skill.name}
              name={skill.name}
              icon={<Icon />}
              badges={
                <>
                  {skill.version && <span className="font-mono text-[11px] text-muted-foreground">v{skill.version}</span>}
                  {source !== "inline" && skill.phase && (
                    <Pill tone={phaseTone(skill.phase)}>
                      {skill.phase}
                    </Pill>
                  )}
                  <Pill tone="neutral">
                    {sourceLabel[source]}
                  </Pill>
                </>
              }
              description={skill.description || skill.resolvedDescription || undefined}
              facts={
                (source !== "inline" || skill.mcpServerRefs.length > 0) && (
                  <>
                    {source === "catalog" && (
                      <Fact title={skill.catalogUrl}>
                        <a href={skill.catalogUrl} target="_blank" rel="noreferrer" className="inline-flex min-w-0 items-center gap-1 truncate hover:text-foreground hover:underline">
                          <span className="truncate">skills.sh/{skill.catalogSource}/{skill.catalogSkillId}</span>
                          <ExternalLink className="size-3 shrink-0" />
                        </a>
                      </Fact>
                    )}
                    {source === "git" && <Fact title={skill.gitUrl}>{skill.gitUrl}</Fact>}
                    {skill.mcpServerRefs.length > 0 && <Fact title={skill.mcpServerRefs.join(", ")}>needs: {skill.mcpServerRefs.join(", ")}</Fact>}
                  </>
                )
              }
              status={failed && skill.statusMessage ? skill.statusMessage : undefined}
              onOpen={() => setEditing(skill)}
              onEdit={() => setEditing(skill)}
              onDelete={() => setDeleting(skill)}
            />
          );
        })}
      </ResourceSection>

      {editing !== undefined && (
        <SkillDialog
          skill={editing}
          onClose={() => setEditing(undefined)}
          onSaved={(saved) => {
            setEditing(undefined);
            setRows((current) =>
              [...current.filter((item) => item.name !== saved.name), saved].sort((left, right) => left.name.localeCompare(right.name)),
            );
          }}
        />
      )}

      {browsing && (
        <SkillCatalogDialog
          installed={rows}
          onClose={() => setBrowsing(false)}
          onInstalled={() => void reload()}
        />
      )}

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Delete ${deleting?.name ?? ""}?`}
        description="This permanently removes the skill. Projects and agents that reference it will no longer use its instructions."
        confirmLabel="Delete skill"
        destructive
        onConfirm={async () => {
          if (deleting) await remove(deleting);
        }}
      />
    </>
  );
}

function SkillDialog({ skill, onClose, onSaved }: { skill: Skill | null; onClose: () => void; onSaved: (saved: Skill) => void }) {
  const isNew = skill === null;
  const [initial] = useState<Skill>(() => (skill ? { ...skill } : { ...emptySkill }));
  const [draft, setDraft] = useState<Skill>(initial);
  const [source, setSource] = useState<Source>(() => (skill?.gitUrl ? "git" : "inline"));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const set = (patch: Partial<Skill>) => setDraft((current) => ({ ...current, ...patch }));
  const dirty = JSON.stringify(draft) !== JSON.stringify(initial);
  const isCatalog = Boolean(skill?.catalogSource);
  const valid =
    Boolean(draft.name.trim()) &&
    (!isNew || resourceNameError(draft.name.trim()) === null) &&
    (source === "git" ? Boolean(draft.gitUrl.trim()) : Boolean(draft.instructions.trim()));

  async function save() {
    setSaving(true);
    setError(null);
    try {
      const gitUrl = source === "git" ? draft.gitUrl.trim() : "";
      const saved = await client.upsertSkill({
        name: draft.name.trim(),
        version: draft.version.trim(),
        description: draft.description.trim(),
        instructions: gitUrl ? "" : draft.instructions,
        gitUrl,
        gitRef: source === "git" ? draft.gitRef.trim() : "",
        gitPath: source === "git" ? draft.gitPath.trim() : "",
        mcpServerRefs: draft.mcpServerRefs,
      });
      toast.success(`${isNew ? "Created" : "Saved"} ${draft.name.trim()}`);
      onSaved(normalizeSkill(saved as unknown as Partial<Skill>));
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
      icon={<GraduationCap />}
      title={isNew ? "New skill" : `Edit ${skill.name}`}
      description={
        isNew
          ? "Skills load on demand once a project or mode enables them, and bring their required MCP servers along."
          : "Saving replaces the skill's instructions, source, and required servers."
      }
      dirty={dirty}
      saving={saving}
      canSave={valid && (isNew || dirty)}
      saveLabel={isNew ? "Create skill" : "Save changes"}
      error={error}
      onSave={() => void save()}
    >
      <FormSection title="Skill">
        <FieldGrid>
          <NameField id="skill-name" value={draft.name} onChange={(name) => set({ name })} isNew={isNew} placeholder="my-skill" autoFocus={isNew} />
          <FlowField id="skill-version" label="Version" hint="Optional; shown next to the name in pickers.">
            <Input id="skill-version" value={draft.version} onChange={(event) => set({ version: event.target.value })} placeholder="0.1.0" className="font-mono" autoComplete="off" />
          </FlowField>
        </FieldGrid>
        <FlowField id="skill-description" label="Description">
          <Input id="skill-description" value={draft.description} onChange={(event) => set({ description: event.target.value })} placeholder="What the skill teaches the agent — shown in pickers" />
        </FlowField>
      </FormSection>

      <FormSection
        title="Source"
        description={isNew ? "Write the instructions here, or point at a repository folder that contains a SKILL.md." : undefined}
        aside={
          isNew ? (
            <Segmented
              aria-label="Skill source"
              value={source}
              onChange={setSource}
              options={[
                { value: "inline", label: "Write instructions" },
                { value: "git", label: "From a Git repository" },
              ]}
            />
          ) : undefined
        }
      >
        {isCatalog && skill && (
          <InlineNote>
            Installed from{" "}
            <a href={skill.catalogUrl} target="_blank" rel="noreferrer" className="font-mono text-foreground underline underline-offset-2">
              skills.sh/{skill.catalogSource}/{skill.catalogSkillId}
            </a>
            . The catalog is not re-checked after install; editing the instructions here detaches the skill from
            its catalog entry.
          </InlineNote>
        )}
        {source === "git" ? (
          <FlowField id="skill-git-url" label="Git link" required hint="A repo folder containing SKILL.md; branch and path are parsed from the link.">
            <Input
              id="skill-git-url"
              value={draft.gitUrl}
              onChange={(event) => set({ gitUrl: event.target.value })}
              placeholder="https://github.com/anthropics/skills/tree/main/document-skills/pdf"
              className="font-mono"
              autoComplete="off"
              spellCheck={false}
            />
          </FlowField>
        ) : (
          <FlowField id="skill-instructions" label="Instructions" required hint="Prompt guidance injected into runs that use this skill — keep it short.">
            <Textarea
              id="skill-instructions"
              value={draft.instructions}
              onChange={(event) => set({ instructions: event.target.value })}
              placeholder="Query discipline, safety rules, runbooks…"
              className="min-h-[220px] font-mono text-xs"
              spellCheck={false}
            />
          </FlowField>
        )}
      </FormSection>

      <FormSection title="Required MCP servers" description="Attaching this skill to a run auto-attaches these servers.">
        <MCPServerPicker selected={draft.mcpServerRefs} onChange={(mcpServerRefs) => set({ mcpServerRefs })} />
      </FormSection>
    </ResourceFormDialog>
  );
}
