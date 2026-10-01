import { useState } from "react";
import { Link } from "react-router-dom";
import { motion } from "framer-motion";
import {
  ArrowUpRight,
  BookOpen,
  CalendarClock,
  Check,
  GitBranch,
  Layers,
  MessageSquare,
  X,
} from "lucide-react";

import {
  triggerSource,
  type ProjectWithTriggers,
} from "@/components/project-triggers/types";
import { Button } from "@/components/ui/button";
import { ProgressPips } from "@/components/onboarding/ProgressPips";
import { useAuth } from "@/contexts/AuthContext";
import { useMyCredentials } from "@/hooks/useMyCredentials";
import { useProjects } from "@/hooks/useWatchedList";
import { readLastProject } from "@/lib/lastProject";
import {
  FEATURE_TOUR_SOURCES,
  dismissFeatureTour,
  featureTourDismissed,
  featureTourProgress,
  featureTourStepsDone,
  setupProgress,
  shouldShowFeatureTour,
  type FeatureTourSource,
} from "@/lib/onboarding";
import { toneSoft } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { Project } from "@/rpc/platform/service_pb";

const DOCS_BASE = "https://gratefulagents.dev/docs";

const TOUR_ITEMS: Record<
  FeatureTourSource,
  { icon: typeof GitBranch; title: string; description: string; docsPath: string }
> = {
  github: {
    icon: GitBranch,
    title: "Set up a GitHub trigger",
    description: "Agents pick up new issues and pull requests automatically.",
    docsPath: "/integrations/github/",
  },
  slack: {
    icon: MessageSquare,
    title: "Set up a Slack trigger",
    description: "@mention the bot in Slack to start and steer runs.",
    docsPath: "/integrations/slack/",
  },
  cron: {
    icon: CalendarClock,
    title: "Schedule recurring runs",
    description: "Cron entry points run agent work on a schedule.",
    docsPath: "/projects/cron/",
  },
  linear: {
    icon: Layers,
    title: "Connect Linear",
    description: "Turn Linear issues into agent runs.",
    docsPath: "/integrations/linear/",
  },
};

/** The project whose Entry points tab the tutorial deep-links into. */
function tourProject(projects: Project[]): Project | undefined {
  const last = readLastProject();
  if (last) {
    const match = projects.find((p) => p.namespace === last.namespace && p.name === last.name);
    if (match) return match;
  }
  return projects[0];
}

function triggerSourcesOf(projects: Project[]): string[] {
  return projects.flatMap((project) =>
    ((project as unknown as ProjectWithTriggers).triggers ?? []).map(triggerSource),
  );
}

/**
 * FeatureTour is the Home-screen tutorial that succeeds the setup checklist:
 * once the account can run work (provider + project), it teaches the
 * automation entry points — GitHub triggers, Slack triggers, Cron schedules,
 * and Linear — with live completion state per feature. Rows deep-link into
 * the project's Entry points tab; each also links to the step-by-step guide.
 * It hides itself when every feature is in use or when dismissed.
 */
export function FeatureTour({ className }: { className?: string }) {
  const { user } = useAuth();
  const { projects, loading: projectsLoading } = useProjects();
  const { presence } = useMyCredentials();
  const [dismissed, setDismissed] = useState(() => featureTourDismissed(user?.id));

  if (dismissed || projectsLoading || !presence) return null;

  const setup = setupProgress(presence, projects.length);
  const tour = featureTourProgress(triggerSourcesOf(projects));
  if (!shouldShowFeatureTour({ setup, tour, role: user?.role, dismissed })) return null;

  const project = tourProject(projects);
  if (!project) return null;
  const entryPointsTo = `/projects/${project.namespace}/${project.name}?tab=entry-points`;

  const done = featureTourStepsDone(tour);
  const pending = FEATURE_TOUR_SOURCES.filter((source) => !tour[source]);
  const total = FEATURE_TOUR_SOURCES.length;

  return (
    <motion.section
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.35, ease: [0.25, 1, 0.5, 1], delay: 0.12 }}
      aria-label="Do more with your agents"
      className={cn("flex flex-col gap-2.5", className)}
    >
      <div className="flex items-center justify-between gap-3 px-1">
        <div className="flex min-w-0 items-center gap-2.5">
          <h2 className="text-[12.5px] font-medium">Do more with your agents</h2>
          <ProgressPips done={done} total={total} />
          <span className="font-mono text-[11px] text-muted-foreground">
            {done}/{total}
          </span>
        </div>
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label="Dismiss feature tour"
          className="text-muted-foreground"
          onClick={() => {
            dismissFeatureTour(user?.id);
            setDismissed(true);
          }}
        >
          <X />
        </Button>
      </div>
      <div className={cn("grid gap-2", pending.length > 1 && "sm:grid-cols-2")}>
        {pending.map((source) => {
          const item = TOUR_ITEMS[source];
          return (
            <div
              key={source}
              className="group/tile flex items-center gap-2.5 rounded-xl border border-border/70 bg-card/50 px-3 py-2.5 transition-colors hover:border-border"
            >
              <span className="grid size-7 shrink-0 place-items-center rounded-[7px] border border-border/60 bg-background/60 text-muted-foreground">
                <item.icon className="size-[13px]" />
              </span>
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className="truncate text-[13px] font-medium leading-tight">{item.title}</span>
                <span className="truncate text-[11.5px] leading-tight text-muted-foreground/80">
                  {item.description}
                </span>
              </span>
              <span className="flex shrink-0 items-center gap-0.5">
                <Button
                  variant="ghost"
                  size="icon-xs"
                  nativeButton={false}
                  aria-label={`Open the ${item.title.toLowerCase()} guide`}
                  title="Guide"
                  className="text-muted-foreground"
                  render={
                    <a
                      href={`${DOCS_BASE}${item.docsPath}`}
                      target="_blank"
                      rel="noopener noreferrer"
                    />
                  }
                >
                  <BookOpen />
                </Button>
                <Button
                  variant="outline"
                  size="xs"
                  nativeButton={false}
                  render={<Link to={entryPointsTo} />}
                >
                  Set up
                  <ArrowUpRight data-icon="inline-end" />
                </Button>
              </span>
            </div>
          );
        })}
      </div>
      {done > 0 && (
        <ul aria-label="Already set up" className="flex flex-wrap items-center gap-1.5 px-1">
          {FEATURE_TOUR_SOURCES.filter((source) => tour[source]).map((source) => (
            <li
              key={source}
              className="inline-flex items-center gap-1 text-[11.5px] text-muted-foreground/80"
            >
              <span className={cn("grid size-3.5 place-items-center rounded-full", toneSoft.success)}>
                <Check className="size-2.5" />
              </span>
              <span className="line-through decoration-muted-foreground/40">
                {TOUR_ITEMS[source].title}
              </span>
            </li>
          ))}
        </ul>
      )}
      <p className="px-1 text-[11.5px] text-muted-foreground/80">
        Entry points run agents without the dashboard — find them in{" "}
        <Link to={entryPointsTo} className="underline underline-offset-2 hover:text-foreground">
          {project.displayName || project.name} → Entry points
        </Link>
        .
      </p>
    </motion.section>
  );
}
