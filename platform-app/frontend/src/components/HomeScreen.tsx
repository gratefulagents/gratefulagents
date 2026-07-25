import { useMemo } from "react";
import { Link } from "react-router-dom";
import { motion } from "framer-motion";
import { ChevronRight, FolderKanban, MessageSquarePlus, Plus } from "lucide-react";

import { NewChatComposer } from "@/components/NewChatComposer";
import { CreateProjectDialog } from "@/components/CreateProjectDialog";
import { SetupChecklist } from "@/components/onboarding/SetupChecklist";
import { Kbd, KbdGroup } from "@/components/ui/kbd";
import { useProjects } from "@/hooks/useWatchedList";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { useAuth } from "@/contexts/AuthContext";
import { formatAge } from "@/lib/format";
import { runSourceLabel } from "@/lib/runSource";
import { isRunComputing, runStatusLabel, runStatusTone } from "@/lib/runStatus";
import { toneColor } from "@/lib/status";
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
 * macOS "inset grouped" list section: a quiet header label above a hairline
 * card whose rows are separated by dividers (System Settings / Finder style).
 */
function Section({
  title,
  action,
  children,
}: {
  title: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section className="flex min-w-0 flex-col">
      <div className="mb-2 flex h-6 items-center justify-between gap-2 px-1">
        <h2 className="text-[11px] font-semibold uppercase tracking-[0.06em] text-muted-foreground/70">
          {title}
        </h2>
        {action}
      </div>
      <div className="overflow-hidden rounded-[10px] border border-border/70 bg-card/60 shadow-[var(--elevation-low)] backdrop-blur-[6px]">
        {children}
      </div>
    </section>
  );
}

/** A single grouped-list row: pressable, hairline-separated, chevron affordance. */
function Row({
  to,
  icon,
  title,
  subtitle,
  trailing,
}: {
  to: string;
  icon: React.ReactNode;
  title: string;
  subtitle?: string;
  trailing?: React.ReactNode;
}) {
  return (
    <Link
      to={to}
      className={cn(
        "group/row flex items-center gap-2.5 px-3 py-[9px] outline-none",
        "border-b border-border/50 last:border-b-0",
        "transition-colors duration-75 hover:bg-foreground/[0.045] active:bg-foreground/[0.075]",
        "focus-visible:bg-foreground/[0.045]",
      )}
    >
      <span className="grid size-[22px] shrink-0 place-items-center rounded-[6px] bg-foreground/[0.06] text-muted-foreground [&_svg]:size-[13px]">
        {icon}
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate text-[13px] font-medium leading-[1.35] tracking-[-0.006em]">
          {title}
        </span>
        {subtitle && (
          <span className="truncate text-[11.5px] leading-[1.35] text-muted-foreground/80">
            {subtitle}
          </span>
        )}
      </span>
      {trailing}
      <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/35 transition-colors group-hover/row:text-muted-foreground/70" />
    </Link>
  );
}

/**
 * Quiet status dot + label — reads calmer than a filled pill in a list.
 * The dot always renders (including narrow/iOS widths); only the text label
 * collapses below `sm`, and the accessible label is kept via `title`/sr-only
 * so status stays distinguishable everywhere.
 */
function RunStatus({ run }: { run: AgentRun }) {
  const tone = runStatusTone(run);
  const live = isRunComputing(run);
  const label = runStatusLabel(run);
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

function EmptyRow({ children }: { children: React.ReactNode }) {
  return (
    <p className="px-3 py-5 text-center text-[12.5px] text-muted-foreground/80">{children}</p>
  );
}

export function HomeScreen() {
  const { user } = useAuth();
  const { projects, loading: projectsLoading } = useProjects();
  const { runs } = useAgentRuns();

  const recent = useMemo(
    () => [...runs].sort((a, b) => Number(b.createdAtUnix - a.createdAtUnix)).slice(0, 6),
    [runs],
  );
  const firstName = (user?.name || user?.username || "").split(" ")[0];

  return (
    <div className="mx-auto flex min-h-full max-w-[760px] flex-col px-6 pt-[7vh] pb-[max(2.5rem,env(safe-area-inset-bottom))]">
      <motion.div {...rise(0)} className="mb-5 flex flex-col items-center text-center">
        <img
          src="/logo.png"
          alt="Grateful Agents"
          draggable={false}
          className="mb-3 size-12 rounded-[11px] shadow-[0_1px_2px_oklch(0_0_0_/_0.25),inset_0_0_0_1px_oklch(1_0_0_/_0.14)]"
        />
        <h1 className="text-[21px] font-semibold leading-tight tracking-[-0.02em]">
          {greeting()}
          {firstName ? `, ${firstName}` : ""}
        </h1>
        <p className="mt-0.5 text-[12.5px] text-muted-foreground">
          What should the agent work on?
        </p>
      </motion.div>

      <motion.div {...rise(1)}>
        <NewChatComposer variant="hero" autoFocus className="shadow-[var(--elevation-mid)]" />
        <p className="mt-2 flex items-center justify-center gap-1.5 text-[11px] text-muted-foreground/70">
          <Kbd>⏎</Kbd>
          to start
          <span aria-hidden className="opacity-50">
            ·
          </span>
          <KbdGroup>
            <Kbd>⇧</Kbd>
            <Kbd>⏎</Kbd>
          </KbdGroup>
          for a new line
        </p>
      </motion.div>

      <SetupChecklist className="mt-6" />

      <div className="mt-8 grid gap-x-6 gap-y-7 sm:grid-cols-2">
        <motion.div {...rise(2)} className="min-w-0">
          <Section title="Recent">
            {recent.length === 0 ? (
              <EmptyRow>Describe a task above to start your first chat.</EmptyRow>
            ) : (
              recent.map((r) => (
                <Row
                  key={`${r.namespace}/${r.name}`}
                  to={`/runs/${r.namespace}/${r.name}`}
                  icon={<MessageSquarePlus />}
                  title={r.displayName || r.intentTitle || r.name}
                  subtitle={
                    [runSourceLabel(r), formatAge(r.createdAtUnix)]
                      .filter(Boolean)
                      .join(" · ") || undefined
                  }
                  trailing={<RunStatus run={r} />}
                />
              ))
            )}
          </Section>
        </motion.div>

        <motion.div {...rise(3)} className="min-w-0">
          <Section
            title="Projects"
            action={
              <span className="flex items-center gap-3">
                {projects.length > 6 && (
                  <Link
                    to="/projects"
                    className="text-[11.5px] text-muted-foreground hover:text-foreground"
                  >
                    All
                  </Link>
                )}
                <CreateProjectDialog
                  trigger={
                    <button
                      type="button"
                      className="inline-flex items-center gap-1 rounded-[6px] px-1.5 py-0.5 text-[11.5px] font-medium text-muted-foreground transition-colors hover:bg-foreground/[0.06] hover:text-foreground"
                    >
                      <Plus className="size-3" />
                      New
                    </button>
                  }
                />
              </span>
            }
          >
            {projects.length === 0 ? (
              <EmptyRow>
                {projectsLoading
                  ? "Loading projects…"
                  : "Projects keep chats, files, and instructions together."}
              </EmptyRow>
            ) : (
              projects.slice(0, 6).map((p) => {
                const total = p.metrics?.totalRuns ?? 0;
                return (
                  <Row
                    key={`${p.namespace}/${p.name}`}
                    to={`/projects/${p.namespace}/${p.name}`}
                    icon={<FolderKanban className="text-primary" />}
                    title={p.displayName || p.name}
                    trailing={
                      total > 0 ? (
                        <span className="shrink-0 text-[11.5px] tabular-nums text-muted-foreground/80">
                          {total} {total === 1 ? "run" : "runs"}
                        </span>
                      ) : undefined
                    }
                  />
                );
              })
            )}
          </Section>
        </motion.div>
      </div>
    </div>
  );
}
