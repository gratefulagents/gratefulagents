import { useState } from "react";
import { Link } from "react-router-dom";
import { motion } from "framer-motion";
import {
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
import { IconTile, InsetRow, InsetSection } from "@/components/ui/inset-list";
import { pushButtonClass, tileColor } from "@/components/ui/inset-list-styles";
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
import type { Project } from "@/rpc/platform/service_pb";

const DOCS_BASE = "https://gratefulagents.dev/docs";

const TOUR_ITEMS: Record<
  FeatureTourSource,
  { icon: typeof GitBranch; color: string; title: string; short: string; description: string; docsPath: string }
> = {
  github: {
    color: tileColor.gray,
    short: "GitHub",
    icon: GitBranch,
    title: "Set up a GitHub trigger",
    description: "Agents pick up new issues and pull requests automatically.",
    docsPath: "/integrations/github/",
  },
  slack: {
    color: tileColor.purple,
    short: "Slack",
    icon: MessageSquare,
    title: "Set up a Slack trigger",
    description: "@mention the bot in Slack to start and steer runs.",
    docsPath: "/integrations/slack/",
  },
  cron: {
    color: tileColor.orange,
    short: "Cron",
    icon: CalendarClock,
    title: "Schedule recurring runs",
    description: "Cron entry points run agent work on a schedule.",
    docsPath: "/projects/cron/",
  },
  linear: {
    color: tileColor.indigo,
    short: "Linear",
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
  const finished = FEATURE_TOUR_SOURCES.filter((source) => tour[source]);

  return (
    <motion.div
      initial={{ opacity: 0, y: 4 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.25, ease: [0.25, 1, 0.5, 1], delay: 0.08 }}
      className={className}
    >
      <InsetSection
        label="Do more with your agents"
        title="Do more with your agents"
        accessory={`${done}/${FEATURE_TOUR_SOURCES.length}`}
        action={
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
        }
        footer={
          <div className="flex flex-col gap-0.5">
            {finished.length > 0 && (
              <span className="flex items-center gap-1">
                <Check className="size-3 shrink-0 text-[color:var(--tone-success)]" />
                Already set up: {finished.map((source) => TOUR_ITEMS[source].short).join(", ")}
              </span>
            )}
            <span>
              Entry points run agents without the dashboard — find them in{" "}
              <Link to={entryPointsTo} className="cursor-default text-primary hover:underline">
                {project.displayName || project.name} → Entry points
              </Link>
              .
            </span>
          </div>
        }
      >
        {pending.map((source) => {
          const item = TOUR_ITEMS[source];
          return (
            <InsetRow
              key={source}
              icon={
                <IconTile color={item.color}>
                  <item.icon />
                </IconTile>
              }
              title={item.title}
              subtitle={item.description}
              trailing={
                <>
                  <a
                    href={`${DOCS_BASE}${item.docsPath}`}
                    target="_blank"
                    rel="noopener noreferrer"
                    role="button"
                    aria-label={`Open the ${item.title.toLowerCase()} guide`}
                    title="Guide"
                    className="grid size-[22px] cursor-default place-items-center rounded-[6px] text-muted-foreground outline-none hover:bg-foreground/[0.07] hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/60"
                  >
                    <BookOpen className="size-3.5" />
                  </a>
                  <Link to={entryPointsTo} role="button" className={pushButtonClass()}>
                    Set up
                  </Link>
                </>
              }
            />
          );
        })}
      </InsetSection>
    </motion.div>
  );
}
