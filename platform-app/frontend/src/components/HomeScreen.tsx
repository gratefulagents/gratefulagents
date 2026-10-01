import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { motion } from "framer-motion";
import {
  Bug,
  Check,
  FileSearch,
  FlaskConical,
  Folder,
  GitPullRequest,
  MessageCircleQuestion,
  MessageSquare,
  Plus,
  ShieldHalf,
  X,
} from "lucide-react";

import { NewChatComposer } from "@/components/NewChatComposer";
import { CreateProjectDialog } from "@/components/CreateProjectDialog";
import { FeatureTour } from "@/components/onboarding/FeatureTour";
import { SetupChecklist } from "@/components/onboarding/SetupChecklist";
import { SegmentedControl } from "@/components/shell/SegmentedControl";
import { IconTile, InsetRow, InsetSection } from "@/components/ui/inset-list";
import { pushButtonClass, tileColor } from "@/components/ui/inset-list-styles";
import { Spinner } from "@/components/ui/spinner";
import { useProjects } from "@/hooks/useWatchedList";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { useAuth } from "@/contexts/AuthContext";
import { formatAge } from "@/lib/format";
import { collapseSecurityScanRuns, scanGroupPhase, type OpsRowEntry } from "@/lib/agentOps";
import { runSourceLabel } from "@/lib/runSource";
import {
  isActionableInputType,
  isRunComputing,
  runStatusLabel,
  visibleInputType,
} from "@/lib/runStatus";
import { isDonePhase, phaseTone } from "@/lib/status";
import type { AgentRun } from "@/rpc/platform/service_pb";

function greeting(): string {
  const h = new Date().getHours();
  if (h < 12) return "Good morning";
  if (h < 18) return "Good afternoon";
  return "Good evening";
}

/** Shared entrance motion — gentle fade, staggered per section. */
const rise = (order: number) => ({
  initial: { opacity: 0, y: 4 },
  animate: { opacity: 1, y: 0 },
  transition: { duration: 0.25, ease: [0.25, 1, 0.5, 1] as const, delay: order * 0.04 },
});

/**
 * Starter prompts under the composer. Picking one pre-fills the composer with
 * an editable opener instead of starting a run, so the user always finishes
 * the sentence before anything happens.
 */
const STARTERS = [
  { icon: Bug, label: "Fix a bug", text: "Find and fix the bug where " },
  { icon: GitPullRequest, label: "Review a PR", text: "Review the pull request " },
  { icon: FlaskConical, label: "Write tests", text: "Add tests covering " },
  { icon: FileSearch, label: "Explain code", text: "Explain how this codebase handles " },
] as const;

/* ── Tasks ───────────────────────────────────────────────────────── */

type TaskFilter = "all" | "active" | "attention" | "done";

function runBucket(run: AgentRun): Exclude<TaskFilter, "all"> {
  if (isDonePhase(run.phase)) return "done";
  const input = visibleInputType(run);
  if (isActionableInputType(input) || input === "circuit_breaker") return "attention";
  return "active";
}

function entryBucket(entry: OpsRowEntry): Exclude<TaskFilter, "all"> {
  if (entry.kind === "run") return runBucket(entry.run);
  const phase = scanGroupPhase(entry.group.runs);
  if (phase === "Running") return "active";
  if (phaseTone(phase) === "danger") return "attention";
  return "done";
}

/** Mail/Reminders-style status glyph: the tile color carries the state. */
function statusTile(bucket: Exclude<TaskFilter, "all">, failed: boolean, security: boolean) {
  if (failed) return <IconTile color={tileColor.red}><X /></IconTile>;
  if (bucket === "attention") {
    return <IconTile color={tileColor.purple}><MessageCircleQuestion /></IconTile>;
  }
  if (bucket === "done") return <IconTile color={tileColor.green}><Check /></IconTile>;
  return (
    <IconTile color={security ? tileColor.indigo : tileColor.blue}>
      {security ? <ShieldHalf /> : <MessageSquare />}
    </IconTile>
  );
}

function StatusText({ label, live }: { label: string; live: boolean }) {
  return (
    <span className="flex items-center gap-1.5 text-[12px] text-muted-foreground">
      {live && <Spinner aria-hidden className="size-3" />}
      <span className="hidden sm:inline">{label}</span>
      <span className="sr-only sm:hidden">{label}</span>
    </span>
  );
}

function EntryRow({ entry }: { entry: OpsRowEntry }) {
  const bucket = entryBucket(entry);
  if (entry.kind === "run") {
    const { run } = entry;
    const failed = phaseTone(run.phase) === "danger" || visibleInputType(run) === "circuit_breaker";
    return (
      <InsetRow
        to={`/runs/${run.namespace}/${run.name}`}
        icon={statusTile(bucket, failed, false)}
        title={run.displayName || run.intentTitle || run.name}
        subtitle={[runSourceLabel(run), formatAge(run.createdAtUnix)].filter(Boolean).join(" · ")}
        trailing={<StatusText label={runStatusLabel(run)} live={isRunComputing(run)} />}
      />
    );
  }
  const { group } = entry;
  const phase = scanGroupPhase(group.runs);
  return (
    <InsetRow
      to="/security/runs"
      icon={statusTile(bucket, phaseTone(phase) === "danger", true)}
      title={`Security scan ${group.scanName}`}
      subtitle={`${group.runs.length} task runs · ${formatAge(group.runs[0].createdAtUnix)}`}
      trailing={<StatusText label={phase} live={phase === "Running"} />}
    />
  );
}

function TaskList({ entries }: { entries: OpsRowEntry[] }) {
  const [filter, setFilter] = useState<TaskFilter>("all");
  const counts = useMemo(() => {
    const c: Record<TaskFilter, number> = { all: entries.length, active: 0, attention: 0, done: 0 };
    for (const e of entries) c[entryBucket(e)] += 1;
    return c;
  }, [entries]);
  const visible = useMemo(
    () => (filter === "all" ? entries : entries.filter((e) => entryBucket(e) === filter)).slice(0, 8),
    [entries, filter],
  );
  const withCount = (label: string, id: TaskFilter) =>
    counts[id] > 0 ? (
      <>
        {label}
        <span className="ml-1 tabular-nums opacity-55">{counts[id]}</span>
      </>
    ) : (
      label
    );

  return (
    <section aria-label="Tasks" className="flex min-w-0 flex-col">
      <div className="mb-2 flex items-center justify-between gap-3 px-0.5">
        <SegmentedControl
          size="sm"
          value={filter}
          onChange={setFilter}
          className="min-w-0 overflow-x-auto [scrollbar-width:none]"
          options={[
            { value: "all", label: "All" },
            { value: "active", label: withCount("Active", "active") },
            { value: "attention", label: withCount("Needs you", "attention") },
            { value: "done", label: withCount("Done", "done") },
          ]}
        />
        <Link to="/runs" className="shrink-0 cursor-default text-[12px] text-primary hover:underline">
          Show All
        </Link>
      </div>
      <InsetSection>
        {visible.length === 0 ? (
          <p className="px-4 py-7 text-center text-[12.5px] text-muted-foreground">
            {entries.length === 0 ? "No tasks yet — describe one above to get started." : "No tasks"}
          </p>
        ) : (
          visible.map((entry) => (
            <EntryRow
              key={entry.kind === "run" ? `${entry.run.namespace}/${entry.run.name}` : entry.group.key}
              entry={entry}
            />
          ))
        )}
      </InsetSection>
    </section>
  );
}

/* ── Projects ────────────────────────────────────────────────────── */

function ProjectsSection() {
  const { projects, loading } = useProjects();
  const shown = projects.slice(0, 6);

  return (
    <InsetSection
      label="Projects"
      title="Projects"
      action={
        <>
          {projects.length > shown.length && (
            <Link to="/projects" className="cursor-default text-[12px] text-primary hover:underline">
              Show All
            </Link>
          )}
          <CreateProjectDialog
            trigger={
              <button type="button" aria-label="New project" title="New project" className={pushButtonClass("w-[22px] justify-center px-0")}>
                <Plus />
              </button>
            }
          />
        </>
      }
    >
      {shown.length === 0 ? (
        <p className="px-4 py-6 text-center text-[12.5px] text-muted-foreground">
          {loading ? "Loading projects…" : "Projects keep chats, files, and instructions together."}
        </p>
      ) : (
        shown.map((p) => {
          const total = p.metrics?.totalRuns ?? 0;
          return (
            <InsetRow
              key={`${p.namespace}/${p.name}`}
              to={`/projects/${p.namespace}/${p.name}`}
              icon={
                <IconTile color={tileColor.blue}>
                  <Folder />
                </IconTile>
              }
              title={p.displayName || p.name}
              trailing={
                <span className="text-[12px] tabular-nums text-muted-foreground">
                  {total > 0 ? `${total} ${total === 1 ? "run" : "runs"}` : "No runs"}
                </span>
              }
            />
          );
        })
      )}
    </InsetSection>
  );
}

/* ── Screen ──────────────────────────────────────────────────────── */

/**
 * Home: a prompt-first launcher styled like a native macOS window — a quiet
 * title, the composer with bezel-button starter prompts, then System
 * Settings-style inset grouped lists for tasks, onboarding, and projects.
 */
export function HomeScreen() {
  const { user } = useAuth();
  const { runs } = useAgentRuns();
  const [prefill, setPrefill] = useState<{ text: string; nonce: number }>();

  const entries = useMemo(
    () => collapseSecurityScanRuns([...runs].sort((a, b) => Number(b.createdAtUnix - a.createdAtUnix))),
    [runs],
  );
  const firstName = (user?.name || user?.username || "").split(" ")[0];
  const needsYou = entries.filter((e) => entryBucket(e) === "attention").length;

  return (
    <div className="mx-auto flex min-h-full w-full max-w-[680px] flex-col px-5 pt-[min(10vh,5.5rem)] pb-[max(2.5rem,env(safe-area-inset-bottom))]">
      <motion.div {...rise(0)} className="mb-5 flex flex-col items-center text-center">
        <img
          src="/logo.png"
          alt=""
          draggable={false}
          className="mb-2.5 size-14 rounded-[13px] shadow-[0_2px_6px_oklch(0_0_0_/_0.3),inset_0_0_0_0.5px_oklch(1_0_0_/_0.18)]"
        />
        <h1 className="text-[22px] font-semibold tracking-[-0.015em]">
          {greeting()}
          {firstName ? `, ${firstName}` : ""}
        </h1>
        <p className="mt-0.5 text-[12.5px] text-muted-foreground">
          {needsYou > 0
            ? `${needsYou} ${needsYou === 1 ? "task needs" : "tasks need"} your attention`
            : "What should the agent work on?"}
        </p>
      </motion.div>

      <motion.div {...rise(1)}>
        <NewChatComposer
          variant="hero"
          autoFocus
          placeholder="Describe a task, or ask anything…"
          prefill={prefill}
          className="rounded-[12px] bg-card shadow-[0_0.5px_1.5px_oklch(0_0_0_/_0.18),0_8px_24px_-12px_oklch(0_0_0_/_0.35)]"
        />
        <div className="mt-2.5 flex flex-wrap items-center justify-center gap-1.5">
          {STARTERS.map((s) => (
            <button
              key={s.label}
              type="button"
              onClick={() => setPrefill((prev) => ({ text: s.text, nonce: (prev?.nonce ?? 0) + 1 }))}
              className={pushButtonClass("h-6 px-2.5 font-normal text-muted-foreground hover:text-foreground")}
            >
              <s.icon />
              {s.label}
            </button>
          ))}
        </div>
      </motion.div>

      <div className="mt-8 flex flex-col gap-7">
        <SetupChecklist />
        <FeatureTour />

        <motion.div {...rise(2)}>
          <TaskList entries={entries} />
        </motion.div>

        <motion.div {...rise(3)}>
          <ProjectsSection />
        </motion.div>
      </div>
    </div>
  );
}
