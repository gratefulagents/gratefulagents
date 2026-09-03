import { useCallback, useEffect, useState } from "react";
import { Blocks, Bot, Cpu, FolderGit2, Loader2, RotateCcw, ShieldCheck, Sparkles } from "lucide-react";

import { FlowField } from "@/components/create-flow/create-flow";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { useAuth } from "@/contexts/AuthContext";
import { client } from "@/lib/client";
import { describeRpcError } from "@/lib/rpc-errors";
import { toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { Project } from "@/rpc/platform/service_pb";

import {
  resetSection,
  sectionChanged,
  updateRequestFromForm,
  type ProjectFormSection,
} from "./projectForm";
import {
  AgentFields,
  ModelFields,
  PrivilegedFields,
  RepositoryDetailsFields,
  RepositoryUrlField,
  RuntimeFields,
  ToolsFields,
} from "./ProjectFormSections";
import { useProjectForm } from "./useProjectForm";

/**
 * Project settings, edited in place on the project page.
 *
 * Every section is visible and directly editable — no modal, no nested
 * disclosures, no separate "view" and "edit" surfaces. Nothing is written
 * until the user presses Save in the bar that appears once something
 * changed; each section can be reverted on its own.
 */

type SectionMeta = {
  id: ProjectFormSection;
  title: string;
  description: string;
  icon: React.ComponentType<{ className?: string }>;
};

const SECTIONS: SectionMeta[] = [
  {
    id: "general",
    title: "General",
    description: "How the project appears and which repositories its runs work in.",
    icon: FolderGit2,
  },
  {
    id: "model",
    title: "Model & credentials",
    description: "Provider, default model, and how runs authenticate to it.",
    icon: Sparkles,
  },
  {
    id: "agent",
    title: "Agent behavior",
    description: "Defaults every new run inherits: mode, review loop, and instructions.",
    icon: Bot,
  },
  {
    id: "runtime",
    title: "Runtime",
    description: "Worker image, run timeout, and sandbox policy.",
    icon: Cpu,
  },
  {
    id: "tools",
    title: "Tools",
    description: "MCP servers available to runs and the policy that governs them.",
    icon: Blocks,
  },
  {
    id: "privileged",
    title: "Privileged access",
    description: "Cluster and container privileges for this project's run pods.",
    icon: ShieldCheck,
  },
];

export function ProjectSettingsPanel({
  project,
  onUpdated,
}: {
  project: Project;
  onUpdated?: (project: Project) => void;
}) {
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";
  const c = useProjectForm({ mode: "edit", project, enabled: true, idPrefix: "project-settings" });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<number | null>(null);

  const visibleSections = SECTIONS.filter((s) => s.id !== "privileged" || isAdmin);
  const changedSections = visibleSections.filter((s) => sectionChanged(s.id, c.form, c.initial));

  const save = useCallback(async () => {
    setError(null);
    const validationError = c.validate();
    if (validationError) {
      setError(validationError);
      return;
    }
    setSaving(true);
    try {
      const updated = await client.updateProject(updateRequestFromForm(c.form, project, { isAdmin }));
      // Snap to what the server persisted so one-shot flags (e.g. "create a
      // RuntimeProfile") clear and the bar reads Saved rather than dirty.
      c.acceptSaved(updated);
      setSavedAt(Date.now());
      onUpdated?.(updated);
    } catch (err) {
      setError(describeRpcError(err, "save project settings"));
    } finally {
      setSaving(false);
    }
  }, [c, project, isAdmin, onUpdated]);

  // ⌘S / Ctrl+S saves when there is something to save.
  useEffect(() => {
    if (!c.dirty) return;
    function onKey(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "s") {
        event.preventDefault();
        if (!saving) void save();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [c.dirty, saving, save]);

  useEffect(() => {
    if (savedAt == null) return;
    const t = window.setTimeout(() => setSavedAt(null), 2500);
    return () => window.clearTimeout(t);
  }, [savedAt]);

  function discard() {
    c.reset();
    setError(null);
  }

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        void save();
      }}
      className="relative"
      aria-label="Project settings"
    >
      <div className="grid gap-8 lg:grid-cols-[168px_minmax(0,1fr)]">
        <nav aria-label="Settings sections" className="hidden lg:block">
          <ol className="sticky top-4 space-y-0.5 text-[12.5px]">
            {visibleSections.map((s) => {
              const changed = changedSections.includes(s);
              return (
                <li key={s.id}>
                  <a
                    href={`#project-settings-section-${s.id}`}
                    className="flex items-center gap-2 rounded-md px-2 py-1.5 text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
                  >
                    <s.icon className="size-3.5 shrink-0" />
                    <span className="flex-1 truncate">{s.title}</span>
                    {changed ? (
                      <span
                        role="img"
                        aria-label="Unsaved changes"
                        className="size-1.5 rounded-full bg-[color:var(--color-primary)]/80"
                      />
                    ) : null}
                  </a>
                </li>
              );
            })}
          </ol>
        </nav>

        <div className={cn("min-w-0 divide-y divide-border/60", c.dirty && "pb-16")}>
          {visibleSections.map((s) => (
            <SettingsBlock
              key={s.id}
              meta={s}
              changed={changedSections.includes(s)}
              onReset={() => c.setForm((prev) => resetSection(s.id, prev, c.initial))}
            >
              {s.id === "general" ? (
                <>
                  <div className="grid gap-4 sm:grid-cols-2">
                    <FlowField id="project-settings-display" label="Display name" required>
                      <Input
                        id="project-settings-display"
                        value={c.form.displayName}
                        onChange={(event) => c.update("displayName", event.target.value)}
                        required
                      />
                    </FlowField>
                    <FlowField label="Name" hint="Fixed at creation.">
                      <Input value={project.name} readOnly disabled className="font-mono text-[13px]" />
                    </FlowField>
                  </div>
                  <RepositoryUrlField c={c} hint="Leave empty to run without a primary repository." />
                  <RepositoryDetailsFields c={c} />
                </>
              ) : s.id === "model" ? (
                <ModelFields c={c} />
              ) : s.id === "agent" ? (
                <AgentFields c={c} />
              ) : s.id === "runtime" ? (
                <RuntimeFields c={c} />
              ) : s.id === "tools" ? (
                <ToolsFields c={c} />
              ) : (
                <PrivilegedFields c={c} />
              )}
            </SettingsBlock>
          ))}
        </div>
      </div>

      <SaveBar
        visible={c.dirty || Boolean(error) || savedAt != null}
        dirty={c.dirty}
        saving={saving}
        error={error}
        saved={savedAt != null && !c.dirty}
        changedTitles={changedSections.map((s) => s.title)}
        onDiscard={discard}
      />
    </form>
  );
}

function SettingsBlock({
  meta,
  changed,
  onReset,
  children,
}: {
  meta: SectionMeta;
  changed: boolean;
  onReset: () => void;
  children: React.ReactNode;
}) {
  return (
    <section
      id={`project-settings-section-${meta.id}`}
      aria-labelledby={`project-settings-section-${meta.id}-title`}
      className="scroll-mt-4 py-7 first:pt-0 last:pb-0"
    >
      <div className="mb-4 flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-2.5">
          <meta.icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          <div className="min-w-0">
            <h3
              id={`project-settings-section-${meta.id}-title`}
              className="text-[14px] font-semibold tracking-[-0.01em] text-foreground"
            >
              {meta.title}
            </h3>
            <p className="mt-0.5 max-w-[62ch] text-[12px] leading-relaxed text-muted-foreground">
              {meta.description}
            </p>
          </div>
        </div>
        {changed ? (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={onReset}
            className="shrink-0 text-muted-foreground"
            aria-label={`Reset ${meta.title}`}
          >
            <RotateCcw className="size-3.5" />
            Reset
          </Button>
        ) : null}
      </div>
      <div className="space-y-4 lg:pl-6.5">{children}</div>
    </section>
  );
}

function SaveBar({
  visible,
  dirty,
  saving,
  error,
  saved,
  changedTitles,
  onDiscard,
}: {
  visible: boolean;
  dirty: boolean;
  saving: boolean;
  error: string | null;
  saved: boolean;
  changedTitles: string[];
  onDiscard: () => void;
}) {
  return (
    <div
      className={cn(
        "pointer-events-none sticky bottom-3 z-10 mt-6 flex justify-center transition-[opacity,transform] duration-[var(--dur-fast)]",
        visible ? "opacity-100 translate-y-0" : "opacity-0 translate-y-2",
      )}
      aria-hidden={!visible}
    >
      <div
        role="status"
        className={cn(
          "pointer-events-auto flex w-full max-w-2xl items-center gap-3 rounded-xl border bg-popover/95 px-4 py-2.5 shadow-lg backdrop-blur",
          error ? "border-[color:var(--tone-danger-fg)]/40" : "border-border/70",
        )}
      >
        <p className={cn("min-w-0 flex-1 truncate text-[12.5px]", error ? toneText.danger : "text-muted-foreground")}>
          {error
            ? error
            : saved
              ? "Saved."
              : changedTitles.length
                ? `Unsaved changes in ${changedTitles.join(", ")}.`
                : "Unsaved changes."}
        </p>
        {dirty ? (
          <>
            <Button type="button" variant="ghost" size="sm" onClick={onDiscard} disabled={saving}>
              Discard
            </Button>
            <Button type="submit" size="sm" disabled={saving}>
              {saving ? <Loader2 className="size-4 animate-spin" /> : null}
              {saving ? "Saving…" : "Save changes"}
            </Button>
          </>
        ) : null}
      </div>
    </div>
  );
}
