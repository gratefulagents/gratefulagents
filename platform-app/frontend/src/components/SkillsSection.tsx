import { useCallback, useEffect, useRef, useState } from "react";
import { ExternalLink, GraduationCap, Plus, Search } from "lucide-react";

import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { resourceNameError } from "@/lib/resourceNames";
import { toneSoft } from "@/lib/status";
import { SettingsSection } from "@/components/settings-section";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { toast } from "@/components/ui/toaster";

interface Skill {
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

interface ServerOption {
  name: string;
  description: string;
}

interface CatalogSkill {
  source: string;
  skillId: string;
  name: string;
  installs: bigint;
  isOfficial: boolean;
  catalogUrl: string;
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

function phaseTone(phase: string): keyof typeof toneSoft {
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

// SkillsSection manages reusable agent skills (Skill CRs in the user's
// namespace): inline instructions written in the UI, or entries installed
// from the skills.sh catalog.
export function SkillsSection() {
  const [skills, setSkills] = useState<Skill[]>([]);
  const [servers, setServers] = useState<ServerOption[]>([]);
  const [editing, setEditing] = useState<Skill | null>(null);
  const [isNew, setIsNew] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [catalogSkills, setCatalogSkills] = useState<CatalogSkill[]>([]);
  const [catalogQuery, setCatalogQuery] = useState("");
  const [catalogPage, setCatalogPage] = useState(0);
  const [catalogHasMore, setCatalogHasMore] = useState(false);
  const [catalogLoading, setCatalogLoading] = useState(false);
  const [installingSkill, setInstallingSkill] = useState<string | null>(null);
  const catalogRequestGeneration = useRef(0);
  const [busy, setBusy] = useState(false);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);

  const reload = useCallback(async () => {
    try {
      const resp = await client.listSkills({});
      setSkills(
        ((resp.skills ?? []) as unknown as Skill[]).map((s) => ({
          ...emptySkill,
          ...s,
          mcpServerRefs: s.mcpServerRefs ?? [],
        })),
      );
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to load skills");
    }
  }, []);

  useEffect(() => {
    let active = true;
    void (async () => {
      await reload();
      try {
        const resp = await client.listMCPServers({});
        if (active) setServers(((resp.servers ?? []) as unknown as ServerOption[]));
      } catch {
        if (active) setServers([]);
      }
    })();
    return () => {
      active = false;
    };
  }, [reload]);

  useEffect(() => {
    if (!installing) return;
    let cancelled = false;
    const generation = ++catalogRequestGeneration.current;
    const query = catalogQuery.trim();
    const requestQuery = query.length >= 2 ? query : "";
    const timer = window.setTimeout(() => {
      setCatalogLoading(true);
      void client
        .listSkillCatalog({ query: requestQuery, page: 0 })
        .then((resp) => {
          if (cancelled || generation !== catalogRequestGeneration.current) return;
          setCatalogSkills((resp.skills ?? []) as unknown as CatalogSkill[]);
          setCatalogPage(0);
          setCatalogHasMore(resp.hasMore);
        })
        .catch((err: unknown) => {
          if (!cancelled && generation === catalogRequestGeneration.current) {
            toast.error(err instanceof Error ? err.message : "Failed to load skills.sh");
          }
        })
        .finally(() => {
          if (!cancelled && generation === catalogRequestGeneration.current) setCatalogLoading(false);
        });
    }, requestQuery ? 250 : 0);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [catalogQuery, installing]);

  async function loadMoreCatalog() {
    if (catalogLoading || !catalogHasMore || catalogQuery.trim().length >= 2) return;
    const generation = ++catalogRequestGeneration.current;
    setCatalogLoading(true);
    try {
      const nextPage = catalogPage + 1;
      const resp = await client.listSkillCatalog({ query: "", page: nextPage });
      if (generation !== catalogRequestGeneration.current) return;
      setCatalogSkills((current) => {
        const byCoordinate = new Map(current.map((skill) => [`${skill.source}/${skill.skillId}`, skill]));
        for (const skill of (resp.skills ?? []) as unknown as CatalogSkill[]) {
          byCoordinate.set(`${skill.source}/${skill.skillId}`, skill);
        }
        return [...byCoordinate.values()];
      });
      setCatalogPage(nextPage);
      setCatalogHasMore(resp.hasMore);
    } catch (err) {
      if (generation === catalogRequestGeneration.current) {
        toast.error(err instanceof Error ? err.message : "Failed to load more skills");
      }
    } finally {
      if (generation === catalogRequestGeneration.current) setCatalogLoading(false);
    }
  }

  async function save(skill: Skill) {
    setBusy(true);
    try {
      await client.upsertSkill({
        name: skill.name.trim(),
        version: skill.version.trim(),
        description: skill.description.trim(),
        instructions: skill.gitUrl.trim() ? "" : skill.instructions,
        gitUrl: skill.gitUrl.trim(),
        gitRef: skill.gitRef.trim(),
        gitPath: skill.gitPath.trim(),
        mcpServerRefs: skill.mcpServerRefs,
      });
      toast.success(`Skill ${skill.name.trim()} saved`);
      setEditing(null);
      setIsNew(false);
      await reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to save skill");
    } finally {
      setBusy(false);
    }
  }

  async function installCatalogSkill(skill: CatalogSkill) {
    setInstallingSkill(`${skill.source}/${skill.skillId}`);
    try {
      await client.installSkillFromCatalog({ source: skill.source, skillId: skill.skillId });
      toast.success(`Installed ${skill.name || skill.skillId}`);
      await reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : `Failed to install ${skill.name || skill.skillId}`);
    } finally {
      setInstallingSkill(null);
    }
  }

  async function remove(name: string) {
    setBusy(true);
    try {
      await client.deleteSkill({ name });
      toast.success(`Skill ${name} deleted`);
      if (editing?.name === name) setEditing(null);
      await reload();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Failed to delete skill");
    } finally {
      setBusy(false);
    }
  }

  return (
    <SettingsSection
      icon={<GraduationCap />}
      title="Skills"
      description="Reusable agent instructions: write them inline or install from the skills.sh catalog. Every skill is available automatically in all of your projects; required MCP servers are attached with it."
      aside={
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setInstalling(true);
              setEditing(null);
              setIsNew(false);
            }}
          >
            <Plus className="mr-1 size-3.5" /> Install from skills.sh
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setEditing({ ...emptySkill });
              setIsNew(true);
              setInstalling(false);
            }}
          >
            <Plus className="mr-1 size-3.5" /> New inline skill
          </Button>
        </div>
      }
    >
      {skills.length === 0 && !editing && !installing && (
        <p className="text-[12px] text-muted-foreground">
          No skills yet. Write one inline, or browse thousands of community skills from{" "}
          <a className="underline" href="https://skills.sh" target="_blank" rel="noreferrer">skills.sh</a>.
        </p>
      )}

      {skills.length > 0 && (
        <ul className="space-y-2">
          {skills.map((skill) => {
            const isGit = Boolean(skill.gitUrl);
            const isCatalog = Boolean(skill.catalogSource);
            return (
              <li key={skill.name} className="flex items-start justify-between gap-3 rounded-md border px-3 py-2">
                <div className="min-w-0">
                  <div className="text-[13px] font-medium">
                    {skill.name}
                    {isGit && skill.resolvedName && skill.resolvedName !== skill.name && (
                      <span className="ml-1.5 text-[11px] font-normal text-muted-foreground">
                        ({skill.resolvedName})
                      </span>
                    )}
                    {skill.version && (
                      <span className="ml-1.5 text-[11px] font-normal text-muted-foreground">v{skill.version}</span>
                    )}
                    {isGit && skill.phase && (
                      <span
                        className={cn(
                          "ml-2 inline-flex h-[18px] items-center rounded-full px-1.5 text-[10.5px] font-medium",
                          toneSoft[phaseTone(skill.phase)],
                        )}
                      >
                        {skill.phase}
                      </span>
                    )}
                  </div>
                  {(skill.description || skill.resolvedDescription) && (
                    <p className="text-[12px] text-muted-foreground">
                      {skill.description || skill.resolvedDescription}
                    </p>
                  )}
                  {isGit && (
                    <p className="mt-0.5 truncate font-mono text-[11px] text-muted-foreground">{skill.gitUrl}</p>
                  )}
                  {isCatalog && (
                    <a
                      className="mt-0.5 inline-flex items-center gap-1 truncate font-mono text-[11px] text-muted-foreground hover:underline"
                      href={skill.catalogUrl}
                      target="_blank"
                      rel="noreferrer"
                    >
                      skills.sh/{skill.catalogSource}/{skill.catalogSkillId} <ExternalLink className="size-3" />
                    </a>
                  )}
                  {skill.mcpServerRefs.length > 0 && (
                    <p className="mt-0.5 text-[11px] text-muted-foreground">
                      Requires servers: {skill.mcpServerRefs.join(", ")}
                    </p>
                  )}
                  {skill.statusMessage && (skill.phase === "Error" || skill.phase === "Invalid") && (
                    <p className="mt-0.5 text-[11.5px] text-destructive">{skill.statusMessage}</p>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setEditing({ ...skill });
                      setIsNew(false);
                      setInstalling(false);
                    }}
                  >
                    Edit
                  </Button>
                  <Button size="sm" variant="ghost" disabled={busy} onClick={() => setDeleteTarget(skill.name)}>
                    Delete
                  </Button>
                </div>
              </li>
            );
          })}
        </ul>
      )}

      <ConfirmDialog
        open={deleteTarget != null}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={`Delete ${deleteTarget ?? ""}?`}
        description="This permanently removes the skill. Projects and agents that reference it will no longer use its instructions."
        confirmLabel="Delete"
        destructive
        onConfirm={async () => {
          if (deleteTarget) await remove(deleteTarget);
        }}
      />

      {installing && (
        <div className="space-y-3 rounded-md border bg-muted/20 p-3.5">
          <div className="flex items-center justify-between gap-3">
            <div>
              <div className="text-[13px] font-medium">skills.sh catalog</div>
              <p className="text-[11.5px] text-muted-foreground">Choose a skill; Grateful Agents installs it as a Skill resource in your namespace.</p>
            </div>
            <Button size="sm" variant="ghost" onClick={() => setInstalling(false)}>Close</Button>
          </div>
          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={catalogQuery}
              onChange={(e) => setCatalogQuery(e.target.value)}
              placeholder="Search skills.sh"
              className="pl-8"
              autoFocus
            />
          </div>
          <div className="max-h-[420px] space-y-1.5 overflow-y-auto pr-1">
            {catalogSkills.map((skill) => {
              const key = `${skill.source}/${skill.skillId}`;
              const installed = skills.some(
                (current) => current.catalogSource === skill.source && current.catalogSkillId === skill.skillId,
              );
              return (
                <div key={key} className="flex items-center justify-between gap-3 rounded-md border bg-background px-3 py-2">
                  <div className="min-w-0">
                    <div className="flex items-center gap-1.5 text-[12.5px] font-medium">
                      <span className="truncate">{skill.name || skill.skillId}</span>
                      {skill.isOfficial && <span className="rounded bg-muted px-1 py-0.5 text-[9.5px] uppercase text-muted-foreground">Official</span>}
                    </div>
                    <div className="flex items-center gap-2 text-[11px] text-muted-foreground">
                      <span className="truncate font-mono">{skill.source}</span>
                      <span>{skill.installs.toLocaleString()} installs</span>
                      <a href={skill.catalogUrl} target="_blank" rel="noreferrer" aria-label={`Open ${skill.name} on skills.sh`}>
                        <ExternalLink className="size-3" />
                      </a>
                    </div>
                  </div>
                  <Button
                    size="sm"
                    variant={installed ? "ghost" : "outline"}
                    disabled={installed || installingSkill !== null}
                    onClick={() => void installCatalogSkill(skill)}
                  >
                    {installed ? "Installed" : installingSkill === key ? "Installing…" : "Install"}
                  </Button>
                </div>
              );
            })}
            {!catalogLoading && catalogSkills.length === 0 && (
              <p className="py-6 text-center text-[12px] text-muted-foreground">No matching skills found.</p>
            )}
            {catalogLoading && <p className="py-3 text-center text-[12px] text-muted-foreground">Loading skills…</p>}
          </div>
          {catalogHasMore && catalogQuery.trim().length < 2 && (
            <Button size="sm" variant="outline" disabled={catalogLoading} onClick={() => void loadMoreCatalog()}>
              Load more
            </Button>
          )}
        </div>
      )}

      {editing && (
        <SkillForm
          skill={editing}
          isNew={isNew}
          servers={servers}
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

function SkillForm({
  skill,
  isNew,
  servers,
  busy,
  onChange,
  onSave,
  onCancel,
}: {
  skill: Skill;
  isNew: boolean;
  servers: ServerOption[];
  busy: boolean;
  onChange: (s: Skill) => void;
  onSave: () => void;
  onCancel: () => void;
}) {
  const set = (patch: Partial<Skill>) => onChange({ ...skill, ...patch });
  const isGit = Boolean(skill.gitUrl) && !isNew;

  function toggleServer(name: string, on: boolean) {
    const without = skill.mcpServerRefs.filter((n) => n !== name);
    set({ mcpServerRefs: on ? [...without, name] : without });
  }

  const missingServers = skill.mcpServerRefs.filter((name) => !servers.some((s) => s.name === name));
  const nameError = isNew ? resourceNameError(skill.name.trim()) : null;

  return (
    <div className="space-y-3.5 rounded-md border bg-muted/20 p-3.5">
      <div className="grid gap-3 sm:grid-cols-2">
        <FieldBlock label="Name" hint="lowercase, digits, hyphens">
          <Input
            value={skill.name}
            onChange={(e) => set({ name: e.target.value })}
            placeholder="my-skill"
            className="font-mono"
            disabled={!isNew}
            autoComplete="off"
          />
          {nameError && <p className="mt-1 text-[11.5px] text-destructive">{nameError}</p>}
        </FieldBlock>
        <FieldBlock label="Version (optional)">
          <Input
            value={skill.version}
            onChange={(e) => set({ version: e.target.value })}
            placeholder="0.1.0"
            className="font-mono"
            autoComplete="off"
          />
        </FieldBlock>
      </div>

      <FieldBlock label="Description">
        <Input
          value={skill.description}
          onChange={(e) => set({ description: e.target.value })}
          placeholder="What the skill teaches the agent — shown in pickers"
        />
      </FieldBlock>

      {isGit ? (
        <FieldBlock label="GitHub link" hint="A repo folder containing SKILL.md — branch and path are parsed from the link.">
          <Input
            value={skill.gitUrl}
            onChange={(e) => set({ gitUrl: e.target.value })}
            placeholder="https://github.com/anthropics/skills/tree/main/document-skills/pdf"
            className="font-mono"
            autoComplete="off"
          />
        </FieldBlock>
      ) : (
        <FieldBlock
          label="Instructions"
          hint="Prompt guidance injected into runs using this skill — keep it short."
        >
          <Textarea
            value={skill.instructions}
            onChange={(e) => set({ instructions: e.target.value })}
            placeholder={"Query discipline, safety rules, runbooks…"}
            className="min-h-[100px] font-mono text-xs"
          />
        </FieldBlock>
      )}

      <FieldBlock
        label="Required MCP servers"
        hint="Attaching this skill to a run auto-attaches these servers."
      >
        {servers.length === 0 && missingServers.length === 0 ? (
          <p className="text-[12px] text-muted-foreground">
            No MCP servers in your namespace — create one under Resources → MCP servers.
          </p>
        ) : (
          <div className="space-y-2.5">
            {servers.map((server) => (
              <div key={server.name} className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="text-[12.5px] font-medium">{server.name}</div>
                  {server.description && (
                    <p className="text-[12px] text-muted-foreground">{server.description}</p>
                  )}
                </div>
                <Switch
                  aria-label={`Require ${server.name}`}
                  checked={skill.mcpServerRefs.includes(server.name)}
                  onCheckedChange={(on) => toggleServer(server.name, on)}
                />
              </div>
            ))}
            {missingServers.map((name) => (
              <div key={name} className="flex items-center justify-between gap-3">
                <div className="text-[12.5px] font-medium">
                  {name}
                  <span className="ml-1.5 text-[11px] font-normal text-amber-600">
                    not found in your namespace
                  </span>
                </div>
                <Switch aria-label={`Remove ${name}`} checked onCheckedChange={(on) => toggleServer(name, on)} />
              </div>
            ))}
          </div>
        )}
      </FieldBlock>

      <div className="flex items-center gap-3 border-t pt-3">
        <Button
          size="sm"
          onClick={onSave}
          disabled={
            busy ||
            !skill.name.trim() ||
            nameError != null ||
            (isGit ? !skill.gitUrl.trim() : !skill.instructions.trim())
          }
        >
          {busy ? "Saving…" : isNew ? "Create skill" : "Save changes"}
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
