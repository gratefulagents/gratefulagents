import { useState } from "react";
import { Link } from "react-router-dom";
import { motion } from "framer-motion";
import { ArrowUpRight, Check, FolderGit2, GitBranch, KeyRound, X } from "lucide-react";

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
import {
  checklistDismissed,
  dismissChecklist,
  setupProgress,
  setupStepsDone,
  shouldShowChecklist,
} from "@/lib/onboarding";
import { toneSoft } from "@/lib/status";
import { cn } from "@/lib/utils";

/**
 * SetupChecklist is the Home-screen "finish setting up" card: the three
 * onboarding essentials with live completion state, each deep-linking into the
 * /welcome wizard. It hides itself once the account can actually run work
 * (provider + project) or when dismissed.
 */
export function SetupChecklist({ className }: { className?: string }) {
  const { user } = useAuth();
  const { projects, loading: projectsLoading } = useProjects();
  const { presence } = useMyCredentials();
  const [dismissed, setDismissed] = useState(() => checklistDismissed(user?.id));

  if (dismissed || projectsLoading || !presence) return null;

  const progress = setupProgress(presence, projects.length);
  if (!shouldShowChecklist({ progress, role: user?.role, dismissed })) return null;

  const items = [
    {
      icon: KeyRound,
      title: "Connect a model provider",
      description: "Claude, OpenAI, or Copilot — OAuth or API key.",
      done: progress.provider,
      to: "/welcome?step=1",
    },
    {
      icon: GitBranch,
      title: "Add a GitHub token",
      description: "Clone private repos, push branches, open PRs.",
      done: progress.github,
      to: "/welcome?step=2",
    },
    {
      icon: FolderGit2,
      title: "Create your first project",
      description: "Point the agents at a repository.",
      done: progress.project,
      to: "/welcome?step=3",
    },
  ];

  return (
    <motion.section
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.35, ease: [0.25, 1, 0.5, 1], delay: 0.12 }}
      aria-label="Finish setting up"
      className={cn("rounded-xl border bg-card p-4 shadow-[var(--elevation-low)]", className)}
    >
      <div className="mb-2 flex items-center justify-between gap-2">
        <h2 className="text-[13px] font-medium">
          Finish setting up
          <span className="ml-2 font-mono text-[11px] font-normal text-muted-foreground">
            {setupStepsDone(progress)}/3
          </span>
        </h2>
        <Button
          variant="ghost"
          size="icon-xs"
          aria-label="Dismiss setup checklist"
          className="text-muted-foreground"
          onClick={() => {
            dismissChecklist(user?.id);
            setDismissed(true);
          }}
        >
          <X />
        </Button>
      </div>
      <ItemGroup className="gap-1">
        {items.map((item) =>
          item.done ? (
            <Item key={item.title} size="xs" className="opacity-70">
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
            <Item key={item.title} size="xs" render={<Link to={item.to} />}>
              <ItemMedia variant="icon">
                <item.icon className="text-muted-foreground" />
              </ItemMedia>
              <ItemContent className="min-w-0">
                <ItemTitle>{item.title}</ItemTitle>
                <ItemDescription>{item.description}</ItemDescription>
              </ItemContent>
              <ItemActions>
                <ArrowUpRight className="size-3.5 text-muted-foreground" />
              </ItemActions>
            </Item>
          ),
        )}
      </ItemGroup>
    </motion.section>
  );
}
