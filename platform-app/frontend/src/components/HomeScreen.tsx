import { useMemo } from "react";
import { Link } from "react-router-dom";
import { motion } from "framer-motion";
import {
  ArrowUpRight,
  FolderKanban,
  MessageSquarePlus,
  Sparkles,
} from "lucide-react";

import { NewChatComposer } from "@/components/NewChatComposer";
import { StatusBadge } from "@/components/StatusBadge";
import { CreateProjectDialog } from "@/components/CreateProjectDialog";
import { SetupChecklist } from "@/components/onboarding/SetupChecklist";
import { Button } from "@/components/ui/button";
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "@/components/ui/empty";
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item";
import { Kbd, KbdGroup } from "@/components/ui/kbd";
import { useProjects } from "@/hooks/useWatchedList";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { useAuth } from "@/contexts/AuthContext";
import { formatAge } from "@/lib/format";
import { runSourceLabel } from "@/lib/runSource";

function greeting(): string {
  const h = new Date().getHours();
  if (h < 12) return "Good morning";
  if (h < 18) return "Good afternoon";
  return "Good evening";
}

/** Shared entrance motion — gentle rise, staggered per section. */
const rise = (order: number) => ({
  initial: { opacity: 0, y: 10 },
  animate: { opacity: 1, y: 0 },
  transition: {
    duration: 0.35,
    ease: [0.25, 1, 0.5, 1] as const,
    delay: order * 0.06,
  },
});

function EmptyHint({
  icon: Icon,
  title,
  children,
}: {
  icon: typeof Sparkles;
  title: string;
  children: React.ReactNode;
}) {
  return (
    <Empty className="border border-dashed p-6">
      <EmptyHeader>
        <EmptyMedia variant="icon">
          <Icon />
        </EmptyMedia>
        <EmptyTitle>{title}</EmptyTitle>
        <EmptyDescription>{children}</EmptyDescription>
      </EmptyHeader>
    </Empty>
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
    <div className="mx-auto flex min-h-full max-w-[860px] flex-col px-6 pt-[8vh] pb-[max(2.5rem,env(safe-area-inset-bottom))]">
      <motion.div {...rise(0)} className="mb-5 flex items-center gap-2.5">
        <div className="grid size-8 place-items-center rounded-lg bg-primary text-primary-foreground">
          <Sparkles className="size-4" />
        </div>
        <div>
          <h1 className="text-[24px] font-semibold tracking-[-0.02em] leading-none">
            {greeting()}{firstName ? `, ${firstName}` : ""}
          </h1>
          <p className="mt-1 text-[13px] text-muted-foreground">What should the agent work on?</p>
        </div>
      </motion.div>

      <motion.div {...rise(1)}>
        <NewChatComposer variant="hero" autoFocus className="shadow-[var(--elevation-mid)]" />
        <p className="mt-2 flex items-center gap-1.5 px-1 text-xs text-muted-foreground">
          <Kbd>⏎</Kbd>
          to start
          <span aria-hidden>·</span>
          <KbdGroup>
            <Kbd>⇧</Kbd>
            <Kbd>⏎</Kbd>
          </KbdGroup>
          for a new line
        </p>
      </motion.div>

      <SetupChecklist className="mt-6" />

      <div className="mt-8 grid gap-x-10 gap-y-8 sm:grid-cols-[3fr_2fr]">
        <motion.section {...rise(2)} className="flex min-w-0 flex-col gap-3">
          <div className="flex h-7 items-center">
            <h2 className="text-[13px] font-medium text-muted-foreground">Recent chats</h2>
          </div>
          {recent.length === 0 ? (
            <EmptyHint icon={MessageSquarePlus} title="No chats yet">
              Describe a task above to start your first one.
            </EmptyHint>
          ) : (
            <ItemGroup className="gap-1">
              {recent.map((r) => {
                const source = runSourceLabel(r);
                return (
                  <Item
                    key={`${r.namespace}/${r.name}`}
                    size="xs"
                    render={<Link to={`/runs/${r.namespace}/${r.name}`} />}
                  >
                    <ItemMedia variant="icon">
                      <MessageSquarePlus className="text-muted-foreground" />
                    </ItemMedia>
                    <ItemContent className="min-w-0">
                      <ItemTitle>{r.displayName || r.intentTitle || r.name}</ItemTitle>
                      {source && <ItemDescription>{source}</ItemDescription>}
                    </ItemContent>
                    <ItemActions>
                      <StatusBadge phase={r.phase} run={r} />
                      <span className="w-9 text-right font-mono text-xs tabular-nums text-muted-foreground group-hover/item:hidden">
                        {formatAge(r.createdAtUnix)}
                      </span>
                      <span className="hidden w-9 justify-end group-hover/item:flex">
                        <ArrowUpRight className="size-3.5 text-muted-foreground" />
                      </span>
                    </ItemActions>
                  </Item>
                );
              })}
            </ItemGroup>
          )}
        </motion.section>

        <motion.section {...rise(3)} className="flex min-w-0 flex-col gap-3">
          <div className="flex h-7 items-center justify-between">
            <h2 className="text-[13px] font-medium text-muted-foreground">Projects</h2>
            <CreateProjectDialog />
          </div>
          {projects.length === 0 ? (
            <EmptyHint
              icon={FolderKanban}
              title={projectsLoading ? "Loading projects…" : "No projects yet"}
            >
              {projectsLoading
                ? "Hang tight while we fetch your workspaces."
                : "Projects keep chats, files, instructions, and shared work together."}
            </EmptyHint>
          ) : (
            <>
              <ItemGroup className="gap-1">
                {projects.slice(0, 6).map((p) => {
                  const total = p.metrics?.totalRuns ?? 0;
                  return (
                    <Item
                      key={`${p.namespace}/${p.name}`}
                      size="xs"
                      render={<Link to={`/projects/${p.namespace}/${p.name}`} />}
                    >
                      <ItemMedia variant="icon">
                        <FolderKanban className="text-primary" />
                      </ItemMedia>
                      <ItemContent className="min-w-0">
                        <ItemTitle>{p.displayName || p.name}</ItemTitle>
                      </ItemContent>
                      <ItemActions>
                        {total > 0 && (
                          <span className="font-mono text-xs tabular-nums text-muted-foreground group-hover/item:hidden">
                            {total} {total === 1 ? "run" : "runs"}
                          </span>
                        )}
                        <ArrowUpRight className="hidden size-3.5 text-muted-foreground group-hover/item:block" />
                      </ItemActions>
                    </Item>
                  );
                })}
              </ItemGroup>
              <Button
                variant="ghost"
                size="xs"
                className="self-start text-muted-foreground"
                nativeButton={false}
                render={<Link to="/projects" />}
              >
                All projects
                <ArrowUpRight data-icon="inline-end" />
              </Button>
            </>
          )}
        </motion.section>
      </div>
    </div>
  );
}
