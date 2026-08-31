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
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item";
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

  return (
    <motion.section
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.35, ease: [0.25, 1, 0.5, 1], delay: 0.12 }}
      aria-label="Do more with your agents"
      className={cn("rounded-xl border bg-card p-4 shadow-[var(--elevation-low)]", className)}
    >
      <div className="mb-1 flex items-center justify-between gap-2">
        <h2 className="text-[13px] font-medium">
          Do more with your agents
          <span className="ml-2 font-mono text-[11px] font-normal text-muted-foreground">
            {featureTourStepsDone(tour)}/{FEATURE_TOUR_SOURCES.length}
          </span>
        </h2>
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
      <p className="mb-2 text-[11.5px] leading-relaxed text-muted-foreground">
        Entry points run agents without the dashboard. Each one lives in{" "}
        <Link to={entryPointsTo} className="underline underline-offset-2 hover:text-foreground">
          {project.displayName || project.name} → Entry points
        </Link>
        .
      </p>
      <ItemGroup className="gap-1">
        {FEATURE_TOUR_SOURCES.map((source) => {
          const item = TOUR_ITEMS[source];
          return tour[source] ? (
            <Item key={source} size="xs" className="opacity-70">
              <ItemMedia variant="icon">
                <span
                  className={cn(
                    "grid size-full place-items-center rounded-[inherit]",
                    toneSoft.success,
                  )}
                >
                  <Check className="size-3.5" />
                </span>
              </ItemMedia>
              <ItemContent className="min-w-0">
                <ItemTitle className="line-through decoration-muted-foreground/50">
                  {item.title}
                </ItemTitle>
              </ItemContent>
            </Item>
          ) : (
            <Item key={source} size="xs">
              <ItemMedia variant="icon">
                <item.icon className="text-muted-foreground" />
              </ItemMedia>
              <ItemContent className="min-w-0">
                <ItemTitle>{item.title}</ItemTitle>
                <ItemDescription>{item.description}</ItemDescription>
              </ItemContent>
              <ItemActions>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  nativeButton={false}
                  aria-label={`Open the ${item.title.toLowerCase()} guide`}
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
              </ItemActions>
            </Item>
          );
        })}
      </ItemGroup>
    </motion.section>
  );
}
