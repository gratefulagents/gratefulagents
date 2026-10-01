import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { motion } from "framer-motion";
import {
  ArrowRight,
  Bug,
  FileSearch,
  FlaskConical,
  FolderKanban,
  GitPullRequest,
  MessageSquare,
  Plus,
  ShieldHalf,
} from "lucide-react";

import { NewChatComposer } from "@/components/NewChatComposer";
import { CreateProjectDialog } from "@/components/CreateProjectDialog";
import { FeatureTour } from "@/components/onboarding/FeatureTour";
import { SetupChecklist } from "@/components/onboarding/SetupChecklist";
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
  runStatusTone,
  visibleInputType,
} from "@/lib/runStatus";
import { isDonePhase, phaseTone, toneColor, type StatusTone } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { AgentRun } from "@/rpc/platform/service_pb";

function greeting(): string {
  const h = new Date().getHours();
  if (h < 12) return "Good morning";
  if (h < 18) return "Good afternoon";
  return "Good evening";
}

/** Shared entrance motion — gentle rise, staggered per section. */
const rise = (order: number) => ({
  initial: { opacity: 0, y: 8 },
  animate: { opacity: 1, y: 0 },
  transition: {
    duration: 0.3,
    ease: [0.25, 1, 0.5, 1] as const,
    delay: order * 0.05,
  },
});

/**
 * Starter prompts under the composer (ChatGPT / Claude style). Picking one
 * pre-fills the composer with an editable opener instead of starting a run,
 * so the user always finishes the sentence before anything happens.
 */
const STARTERS = [
  { icon: Bug, label: "Fix a bug", text: "Find and fix the bug where " },
  { icon: GitPullRequest, label: "Review a PR", text: "Review the pull request " },
  { icon: FlaskConical, label: "Write tests", text: "Add tests covering " },
  { icon: FileSearch, label: "Explain code", text: "Explain how this codebase handles " },
] as const;

/* ── Task list (Codex / Devin style) ─────────────────────────────── */

type TaskFilter = "all" | "active" | "attention" | "done";

const FILTERS: { id: TaskFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "active", label: "Active" },
  { id: "attention", label: "Needs you" },
  { id: "done", label: "Done" },
];

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

/**
 * Quiet status dot + label — reads calmer than a filled pill in a list.
 * The dot always renders (including narrow/iOS widths); only the text label
 * collapses below `sm`, and the accessible label is kept via `title`/sr-only
 * so status stays distinguishable everywhere.
 */
function StatusDot({ tone, live, label }: { tone: StatusTone; live: boolean; label: string }) {
  return (
    <span
      title={label}
      className="inline-flex shrink-0 items-center gap-1.5 text-[11.5px] text-muted-foreground"
    >
      <span
        className="relative inline-flex size-[6px] rounded-full"
        style={{ backgroundColor: toneColor[tone] }}
      >
        {live && (
          <span
            className="absolute inset-0 rounded-full opacity-60 motion-safe:animate-ping"
            style={{ backgroundColor: toneColor[tone] }}
          />
        )}
      </span>
      <span className="hidden sm:inline">{label}</span>
      <span className="sr-only sm:hidden">{label}</span>
    </span>
  );
}

function TaskRow({
  to,
  icon,
  title,
  meta,
  status,
}: {
  to: string;
  icon: React.ReactNode;
  title: string;
  meta: string[];
  status: React.ReactNode;
}) {
  return (
    <Link
      to={to}
      className={cn(
        "group/row flex items-center gap-3 px-3.5 py-2.5 outline-none",
        "border-b border-border/50 last:border-b-0",
        "transition-colors duration-75 hover:bg-foreground/[0.035] active:bg-foreground/[0.06]",
        "focus-visible:bg-foreground/[0.045]",
      )}
    >
      <span className="grid size-7 shrink-0 place-items-center rounded-[7px] border border-border/60 bg-background/60 text-muted-foreground [&_svg]:size-[13px]">
        {icon}
      </span>
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="truncate text-[13px] font-medium leading-tight tracking-[-0.006em]">
          {title}
        </span>
        {meta.length > 0 && (
          <span className="flex min-w-0 items-center gap-1.5 truncate text-[11.5px] leading-tight text-muted-foreground/80">
            {meta.map((m, i) => (
              <span key={i} className={cn("truncate", i > 0 && "before:mr-1.5 before:opacity-50 before:content-['·']")}>
                {m}
              </span>
            ))}
          </span>
        )}
      </span>
      {status}
    </Link>
  );
}

function EntryRow({ entry }: { entry: OpsRowEntry }) {
  if (entry.kind === "run") {
    const { run } = entry;
    return (
      <TaskRow
        to={`/runs/${run.namespace}/${run.name}`}
        icon={<MessageSquare />}
        title={run.displayName || run.intentTitle || run.name}
        meta={[runSourceLabel(run), formatAge(run.createdAtUnix)].filter(Boolean)}
        status={
          <StatusDot tone={runStatusTone(run)} live={isRunComputing(run)} label={runStatusLabel(run)} />
        }
      />
    );
  }
  const { group } = entry;
  const phase = scanGroupPhase(group.runs);
  return (
    <TaskRow
      to="/security/runs"
      icon={<ShieldHalf />}
      title={`Security scan ${group.scanName}`}
      meta={[`${group.runs.length} task runs`, formatAge(group.runs[0].createdAtUnix)]}
      status={<StatusDot tone={phaseTone(phase)} live={phase === "Running"} label={phase} />}
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

  return (
    <section aria-label="Tasks" className="flex min-w-0 flex-col">
      <div className="mb-2 flex items-center justify-between gap-3 px-1">
        <div role="tablist" aria-label="Filter tasks" className="flex min-w-0 items-center gap-0.5 overflow-x-auto [scrollbar-width:none]">
          {FILTERS.map((f) => {
            const selected = filter === f.id;
            const count = counts[f.id];
            return (
              <button
                key={f.id}
                type="button"
                role="tab"
                aria-selected={selected}
                onClick={() => setFilter(f.id)}
                className={cn(
                  "inline-flex h-7 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-[7px] px-2.5 text-[12.5px] font-medium transition-colors",
                  selected
                    ? "bg-foreground/[0.07] text-foreground"
                    : "text-muted-foreground hover:bg-foreground/[0.04] hover:text-foreground",
                )}
              >
                {f.label}
                {f.id !== "all" && count > 0 && (
                  <span
                    className={cn("font-mono text-[10.5px] tabular-nums", f.id !== "attention" && "opacity-60")}
                    style={f.id === "attention" ? { color: toneColor.purple } : undefined}
                  >
                    {count}
                  </span>
                )}
              </button>
            );
          })}
        </div>
        <Link
          to="/runs"
          aria-label="View all tasks"
          className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-[12px] text-muted-foreground transition-colors hover:text-foreground"
        >
          <span className="hidden sm:inline">View all</span>
          <ArrowRight className="size-3" />
        </Link>
      </div>
      <div className="overflow-hidden rounded-xl border border-border/70 bg-card/50">
        {visible.length === 0 ? (
          <p className="px-4 py-8 text-center text-[12.5px] text-muted-foreground/80">
            {entries.length === 0
              ? "No tasks yet — describe one above to get started."
              : "Nothing here right now."}
          </p>
        ) : (
          visible.map((entry) => (
            <EntryRow
              key={entry.kind === "run" ? `${entry.run.namespace}/${entry.run.name}` : entry.group.key}
              entry={entry}
            />
          ))
        )}
      </div>
    </section>
  );
}

/* ── Projects strip ──────────────────────────────────────────────── */

function ProjectsStrip() {
  const { projects, loading } = useProjects();
  const shown = projects.slice(0, 6);

  return (
    <section aria-label="Projects" className="flex min-w-0 flex-col">
      <div className="mb-2 flex h-7 items-center justify-between gap-2 px-1">
        <h2 className="text-[12.5px] font-medium text-muted-foreground">Projects</h2>
        <span className="flex items-center gap-1">
          {projects.length > shown.length && (
            <Link
              to="/projects"
              className="rounded-[6px] px-1.5 py-0.5 text-[12px] text-muted-foreground transition-colors hover:text-foreground"
            >
              All {projects.length}
            </Link>
          )}
          <CreateProjectDialog
            trigger={
              <button
                type="button"
                className="inline-flex items-center gap-1 rounded-[6px] px-1.5 py-0.5 text-[12px] font-medium text-muted-foreground transition-colors hover:bg-foreground/[0.06] hover:text-foreground"
              >
                <Plus className="size-3" />
                New
              </button>
            }
          />
        </span>
      </div>
      {shown.length === 0 ? (
        <p className="rounded-xl border border-dashed border-border/70 px-4 py-6 text-center text-[12.5px] text-muted-foreground/80">
          {loading ? "Loading projects…" : "Projects keep chats, files, and instructions together."}
        </p>
      ) : (
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-3">
          {shown.map((p) => {
            const total = p.metrics?.totalRuns ?? 0;
            return (
              <Link
                key={`${p.namespace}/${p.name}`}
                to={`/projects/${p.namespace}/${p.name}`}
                className="group/proj flex min-w-0 items-center gap-2.5 rounded-xl border border-border/70 bg-card/50 px-3 py-2.5 outline-none transition-colors hover:border-border hover:bg-foreground/[0.035] focus-visible:border-ring"
              >
                <span className="grid size-7 shrink-0 place-items-center rounded-[7px] bg-primary/10 text-primary">
                  <FolderKanban className="size-[13px]" />
                </span>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate text-[13px] font-medium leading-tight">
                    {p.displayName || p.name}
                  </span>
                  <span className="truncate text-[11.5px] leading-tight text-muted-foreground/80 tabular-nums">
                    {total > 0 ? `${total} ${total === 1 ? "run" : "runs"}` : "No runs yet"}
                  </span>
                </span>
              </Link>
            );
          })}
        </div>
      )}
    </section>
  );
}

/* ── Screen ──────────────────────────────────────────────────────── */

/**
 * Home: a prompt-first launcher modelled on chat-first agent tools (ChatGPT,
 * Claude, Codex, Devin). One compact greeting, the composer with starter
 * prompts, then the work itself — a filterable task list — with onboarding
 * kept to slim cards and projects as a quick-jump grid.
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

  return (
    <div className="mx-auto flex min-h-full w-full max-w-[720px] flex-col px-5 pt-[min(12vh,7rem)] pb-[max(2.5rem,env(safe-area-inset-bottom))]">
      <motion.div {...rise(0)} className="mb-6 flex items-center justify-center gap-3">
        <img
          src="/logo.png"
          alt=""
          draggable={false}
          className="size-9 rounded-[9px] shadow-[0_1px_2px_oklch(0_0_0_/_0.25),inset_0_0_0_1px_oklch(1_0_0_/_0.14)]"
        />
        <h1 className="text-[26px] font-semibold leading-tight tracking-[-0.025em]">
          {greeting()}
          {firstName ? `, ${firstName}` : ""}
        </h1>
      </motion.div>

      <motion.div {...rise(1)}>
        <NewChatComposer
          variant="hero"
          autoFocus
          placeholder="What should the agent work on?"
          prefill={prefill}
          className="rounded-2xl shadow-[var(--elevation-mid)]"
        />
        <div className="mt-3 flex flex-wrap items-center justify-center gap-2">
          {STARTERS.map((s) => (
            <button
              key={s.label}
              type="button"
              onClick={() => setPrefill((prev) => ({ text: s.text, nonce: (prev?.nonce ?? 0) + 1 }))}
              className="inline-flex h-8 items-center gap-1.5 rounded-full border border-border/70 bg-card/40 px-3 text-[12.5px] text-muted-foreground transition-colors hover:border-border hover:bg-foreground/[0.05] hover:text-foreground"
            >
              <s.icon className="size-3.5" />
              {s.label}
            </button>
          ))}
        </div>
      </motion.div>

      <SetupChecklist className="mt-8" />
      <FeatureTour className="mt-8" />

      <motion.div {...rise(2)} className="mt-10">
        <TaskList entries={entries} />
      </motion.div>

      <motion.div {...rise(3)} className="mt-8">
        <ProjectsStrip />
      </motion.div>
    </div>
  );
}
