import * as React from "react";
import { useNavigate } from "react-router-dom";
import { Plus, Search, X } from "lucide-react";

import { cn } from "@/lib/utils";
import { APP_VERSION } from "@/lib/build-info";
import { writeLastProject } from "@/lib/lastProject";
import { CreateProjectDialog } from "@/components/CreateProjectDialog";
import { ProjectTree } from "@/components/shell/ProjectTree";
import type { AgentRun, Project } from "@/rpc/platform/service_pb";

/**
 * The contextual panel beside the navigation rail: a searchable list of
 * projects and their recent chats. Hidden entirely when the sidebar is
 * collapsed, so the rail never has to squeeze project rows into icons.
 */
export function ProjectsPanel({
  projects,
  runs,
  workspaceId,
}: {
  projects: Project[];
  runs: AgentRun[];
  workspaceId: string;
}) {
  const navigate = useNavigate();
  const [query, setQuery] = React.useState("");
  const inputRef = React.useRef<HTMLInputElement>(null);

  return (
    <div className="flex h-full min-w-0 flex-1 flex-col group-data-[collapsible=icon]:hidden">
      {/* Header */}
      <div className="flex h-[42px] shrink-0 items-center gap-1 pl-3 pr-1.5">
        <h2 className="min-w-0 flex-1 truncate text-[13px] font-semibold tracking-tight">
          Projects
          {projects.length > 0 && (
            <span className="ml-1.5 font-mono text-[10.5px] font-normal tabular-nums text-muted-foreground/60">
              {projects.length}
            </span>
          )}
        </h2>
        <CreateProjectDialog
          trigger={
            <button
              type="button"
              title="New project"
              aria-label="New project"
              className={cn(
                "grid size-[26px] place-items-center rounded-[7px] text-muted-foreground",
                "transition-colors duration-[var(--dur-fast)] hover:bg-sidebar-accent hover:text-foreground",
                "outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring",
              )}
            >
              <Plus className="size-[15px]" strokeWidth={2} />
            </button>
          }
        />
      </div>

      {/* Search */}
      <div className="shrink-0 px-2 pb-1.5">
        <label
          className={cn(
            "group/search flex h-[28px] items-center gap-1.5 rounded-[7px] px-2",
            "bg-muted/35 ring-1 ring-inset ring-border/50",
            "transition-[background-color,box-shadow] duration-[var(--dur-fast)]",
            "focus-within:bg-muted/60 focus-within:ring-[color:var(--color-primary)]/45",
          )}
        >
          <Search className="size-[13px] shrink-0 text-muted-foreground/60" strokeWidth={1.75} />
          <input
            ref={inputRef}
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape" && query) {
                e.preventDefault();
                setQuery("");
              }
            }}
            placeholder="Filter projects & chats"
            aria-label="Filter projects and chats"
            className={cn(
              "min-w-0 flex-1 bg-transparent text-[12px] tracking-tight outline-none",
              "placeholder:text-muted-foreground/65",
              "[&::-webkit-search-cancel-button]:hidden",
            )}
          />
          {query && (
            <button
              type="button"
              onClick={() => {
                setQuery("");
                inputRef.current?.focus();
              }}
              aria-label="Clear filter"
              className="grid size-4 place-items-center rounded text-muted-foreground/60 hover:text-foreground"
            >
              <X className="size-3" strokeWidth={2} />
            </button>
          )}
        </label>
      </div>

      {/* List */}
      <div className="sidebar-scroll min-h-0 flex-1 overflow-y-auto px-1.5 pb-2">
        <ProjectTree
          key={workspaceId}
          projects={projects}
          runs={runs}
          workspaceId={workspaceId}
          filter={query}
          onNewChat={(p) => {
            writeLastProject(p);
            navigate("/");
          }}
        />
      </div>

      {/* Footer */}
      <div
        className="flex h-[26px] shrink-0 items-center justify-end border-t border-sidebar-border/70 px-3 font-mono text-[10px] tracking-tight text-muted-foreground/60"
        title={`App version ${APP_VERSION}`}
      >
        build v{APP_VERSION}
      </div>
    </div>
  );
}
