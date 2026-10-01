import { useState } from "react";
import { motion } from "framer-motion";
import { Check, FolderGit2, GitBranch, KeyRound, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { IconTile, InsetRow, InsetSection } from "@/components/ui/inset-list";
import { tileColor } from "@/components/ui/inset-list-styles";
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
      color: tileColor.orange,
      title: "Connect a model provider",
      description: "Claude, OpenAI, or Copilot — OAuth or API key.",
      done: progress.provider,
      to: "/welcome?step=1",
    },
    {
      icon: GitBranch,
      color: tileColor.gray,
      title: "Add a GitHub token",
      description: "Clone private repos, push branches, open PRs.",
      done: progress.github,
      to: "/welcome?step=2",
    },
    {
      icon: FolderGit2,
      color: tileColor.blue,
      title: "Create your first project",
      description: "Point the agents at a repository.",
      done: progress.project,
      to: "/welcome?step=3",
    },
  ];

  const done = setupStepsDone(progress);

  return (
    <motion.div
      initial={{ opacity: 0, y: 4 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.25, ease: [0.25, 1, 0.5, 1], delay: 0.08 }}
      className={className}
    >
      <InsetSection
        label="Finish setting up"
        title="Finish setting up"
        accessory={`${done}/3`}
        action={
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
        }
      >
        {items.map((item) =>
          item.done ? (
            <InsetRow
              key={item.title}
              dimmed
              icon={
                <IconTile color={tileColor.green}>
                  <Check />
                </IconTile>
              }
              title={item.title}
              trailing={<span className="text-[12px] text-muted-foreground">Done</span>}
            />
          ) : (
            <InsetRow
              key={item.title}
              to={item.to}
              icon={
                <IconTile color={item.color}>
                  <item.icon />
                </IconTile>
              }
              title={item.title}
              subtitle={item.description}
            />
          ),
        )}
      </InsetSection>
    </motion.div>
  );
}
