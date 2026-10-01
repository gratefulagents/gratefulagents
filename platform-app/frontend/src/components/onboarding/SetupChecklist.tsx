import { useState } from "react";
import { Link } from "react-router-dom";
import { motion } from "framer-motion";
import { ArrowUpRight, Check, FolderGit2, GitBranch, KeyRound, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { ProgressPips } from "@/components/onboarding/ProgressPips";
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

  const done = setupStepsDone(progress);

  return (
    <motion.section
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.35, ease: [0.25, 1, 0.5, 1], delay: 0.12 }}
      aria-label="Finish setting up"
      className={cn("flex flex-col gap-2.5", className)}
    >
      <div className="flex items-center justify-between gap-3 px-1">
        <div className="flex min-w-0 items-center gap-2.5">
          <h2 className="text-[12.5px] font-medium">Finish setting up</h2>
          <ProgressPips done={done} total={items.length} />
          <span className="font-mono text-[11px] text-muted-foreground">{done}/3</span>
        </div>
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
      <div className="grid gap-2 sm:grid-cols-3">
        {items.map((item) =>
          item.done ? (
            <div
              key={item.title}
              className="flex items-center gap-2.5 rounded-xl border border-border/50 px-3 py-2.5 opacity-60"
            >
              <span
                className={cn("grid size-7 shrink-0 place-items-center rounded-[7px]", toneSoft.success)}
              >
                <Check className="size-3.5" />
              </span>
              <span className="truncate text-[13px] font-medium line-through decoration-muted-foreground/50">
                {item.title}
              </span>
            </div>
          ) : (
            <Link
              key={item.title}
              to={item.to}
              className="group/tile flex flex-col gap-2 rounded-xl border border-border/70 bg-card/50 p-3 outline-none transition-colors hover:border-border hover:bg-foreground/[0.035] focus-visible:border-ring"
            >
              <span className="flex items-center justify-between">
                <span className="grid size-7 place-items-center rounded-[7px] border border-border/60 bg-background/60 text-muted-foreground">
                  <item.icon className="size-[13px]" />
                </span>
                <ArrowUpRight className="size-3.5 text-muted-foreground/50 transition-colors group-hover/tile:text-foreground" />
              </span>
              <span className="flex flex-col gap-0.5">
                <span className="text-[13px] font-medium leading-tight">{item.title}</span>
                <span className="text-[11.5px] leading-snug text-muted-foreground/80">
                  {item.description}
                </span>
              </span>
            </Link>
          ),
        )}
      </div>
    </motion.section>
  );
}
