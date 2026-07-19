import { useState } from "react";
import { ChevronRight, FolderGit2, GitBranch, Loader2, Plus, RefreshCw } from "lucide-react";

import { CloneRepoDialog } from "@/components/run-session/CloneRepoDialog";
import { Button } from "@/components/ui/button";
import { useRepositories } from "@/hooks/useRepositories";
import { cn } from "@/lib/utils";

interface RepositoriesPanelProps {
  namespace: string;
  name: string;
  resourceType?: string;
  /** Collaborators can clone; viewers see the list only. */
  canClone: boolean;
  /** True once the sandbox pod can service repository RPCs. */
  sandboxReady?: boolean;
  /** Message shown while the sandbox is still provisioning. */
  startupMessage?: string;
  /** Initial disclosure state. Context drawers open the list immediately. */
  defaultExpanded?: boolean;
}

// RepositoriesPanel lists the git repositories cloned into a running run's
// sandbox and lets collaborators clone additional repositories on the fly.
export function RepositoriesPanel({
  namespace,
  name,
  resourceType = "AgentRun",
  canClone,
  sandboxReady = true,
  startupMessage = "Preparing sandbox… repositories will appear once the pod is ready.",
  defaultExpanded,
}: RepositoriesPanelProps) {
  const { repositories, loading, error, refresh } = useRepositories(namespace, name, resourceType, sandboxReady);
  const [cloneOpen, setCloneOpen] = useState(false);
  // Collapsed by default on phones to keep vertical space for the chat; open
  // on wider viewports where the list is cheap.
  const [expanded, setExpanded] = useState(
    () => defaultExpanded ?? (typeof window === "undefined" || window.innerWidth >= 768),
  );

  return (
    <div className="shrink-0 border-b px-3 py-2 md:px-4">
      <div className="flex items-center justify-between gap-2">
        <button
          type="button"
          onClick={() => setExpanded((value) => !value)}
          aria-expanded={expanded}
          className="flex min-w-0 items-center gap-1.5 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
        >
          <ChevronRight
            className={cn(
              "size-3 shrink-0 text-muted-foreground/60 transition-transform duration-150",
              expanded && "rotate-90",
            )}
          />
          <FolderGit2 className="size-3.5" />
          <span>Repositories</span>
          {repositories.length > 0 && (
            <span className="text-muted-foreground/70">· {repositories.length}</span>
          )}
          {!expanded && repositories.length > 0 && (
            <span className="min-w-0 truncate font-mono font-normal text-muted-foreground/50">
              {repositories.find((repo) => repo.isPrimary)?.name || repositories[0].name}
            </span>
          )}
        </button>
        <div className="flex shrink-0 items-center gap-1">
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="size-9 md:size-6"
            onClick={refresh}
            disabled={loading || !sandboxReady}
            aria-label="Refresh repositories"
          >
            {loading ? <Loader2 className="size-3.5 animate-spin" /> : <RefreshCw className="size-3.5" />}
          </Button>
          {canClone && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-9 gap-1 px-2 text-xs md:h-6"
              onClick={() => setCloneOpen(true)}
              disabled={!sandboxReady}
              title={!sandboxReady ? startupMessage : undefined}
            >
              <Plus className="size-3.5" />
              Clone repo
            </Button>
          )}
        </div>
      </div>

      {!expanded ? null : !sandboxReady ? (
        <p className="mt-1 text-[11px] text-muted-foreground" role="status" aria-live="polite">
          {startupMessage}
        </p>
      ) : error ? (
        <p className="mt-1 text-[11px] text-muted-foreground">Repositories unavailable: {error}</p>
      ) : repositories.length === 0 ? (
        !loading && (
          <p className="mt-1 text-[11px] text-muted-foreground">
            No repositories yet.{canClone ? " Use “Clone repo” to add one." : ""}
          </p>
        )
      ) : (
        <ul className="mt-1.5 max-h-32 space-y-1 overflow-y-auto">
          {repositories.map((repo) => (
            <li key={repo.path} className="flex items-center gap-2 text-xs">
              <span className="min-w-0 shrink truncate font-mono text-foreground">{repo.name}</span>
              {repo.isPrimary && (
                <span className="shrink-0 rounded bg-muted px-1 py-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
                  primary
                </span>
              )}
              {repo.branch && (
                <span className="flex min-w-0 shrink items-center gap-0.5 text-[11px] text-muted-foreground">
                  <GitBranch className="size-3 shrink-0" />
                  <span className="truncate">{repo.branch}</span>
                </span>
              )}
              {repo.remoteUrl && (
                <span
                  className={cn("hidden truncate text-[11px] text-muted-foreground/70 md:inline")}
                  title={repo.remoteUrl}
                >
                  {repo.remoteUrl}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}

      {canClone && sandboxReady && (
        <CloneRepoDialog
          namespace={namespace}
          name={name}
          resourceType={resourceType}
          open={cloneOpen}
          onOpenChange={setCloneOpen}
          onCloned={refresh}
        />
      )}
    </div>
  );
}
