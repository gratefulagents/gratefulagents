import * as React from "react";
import { Link, useLocation } from "react-router-dom";
import { ArrowRight, Check, ChevronRight, Eye, EyeOff, FolderKanban, MessageSquareDashed, MoreHorizontal, Plus } from "lucide-react";

import { cn } from "@/lib/utils";
import { phaseTone, toneColor, isLivePhase, isDonePhase } from "@/lib/status";
import { formatAge } from "@/lib/format";
import { writeLastProject } from "@/lib/lastProject";
import { projectRunKey } from "@/lib/runSource";
import {
  readHiddenSidebarProjects,
  sidebarProjectKey,
  writeHiddenSidebarProjects,
} from "@/lib/sidebarProjectVisibility";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarMenu,
  SidebarMenuItem,
  SidebarMenuButton,
  SidebarMenuSub,
  SidebarMenuSubItem,
  SidebarMenuSubButton,
} from "@/components/ui/sidebar";
import type { AgentRun, Project } from "@/rpc/platform/service_pb";

const MAX_RUNS = 5;
const SHOW_COMPLETED_KEY = "sidebar.showCompletedRuns";

function runLabel(r: AgentRun): string {
  return r.displayName || r.intentTitle || r.name;
}

/** Small pill showing how many runs are currently live for a project. */
function ActiveRunsBadge({ count }: { count: number }) {
  if (count === 0) return null;
  return (
    <span
      title={`${count} active run${count === 1 ? "" : "s"}`}
      className={cn(
        "ml-auto inline-flex h-[17px] min-w-[17px] shrink-0 items-center justify-center gap-1 rounded-full px-1.5 text-[10px] font-medium tabular-nums",
        "bg-[color-mix(in_oklch,var(--tone-running)_14%,transparent)] text-[color:var(--tone-running-fg)]",
        "ring-1 ring-inset ring-[color-mix(in_oklch,var(--tone-running)_30%,transparent)]",
        "transition-opacity duration-[var(--dur-fast)]",
        "group-hover/proj:opacity-0 group-focus-within/proj:opacity-0",
      )}
    >
      <span
        className="relative inline-flex size-1.5 rounded-full"
        style={{ backgroundColor: toneColor.running }}
      >
        <span
          className="absolute inset-0 rounded-full opacity-60 motion-safe:animate-ping"
          style={{ backgroundColor: toneColor.running }}
        />
      </span>
      {count}
    </span>
  );
}

function StatusDot({ phase }: { phase: string }) {
  return (
    <span
      className="relative ml-px inline-flex size-[6px] shrink-0 rounded-full"
      style={{ backgroundColor: toneColor[phaseTone(phase)] }}
      aria-hidden
    >
      {isLivePhase(phase) && (
        <span className="absolute inset-0 rounded-full opacity-60 motion-safe:animate-ping" style={{ backgroundColor: toneColor[phaseTone(phase)] }} />
      )}
    </span>
  );
}

function RunRow({ run, active }: { run: AgentRun; active: boolean }) {
  const to = `/runs/${run.namespace}/${run.name}`;
  const done = isDonePhase(run.phase);
  return (
    <SidebarMenuSubItem>
      <SidebarMenuSubButton
        render={<Link to={to} />}
        isActive={active}
        title={`${runLabel(run)} — ${run.phase || "Unknown"}`}
        className={cn(
          "group/run h-[26px] gap-2 rounded-[6px] px-1.5 text-[11.5px] tracking-tight",
          "transition-colors duration-[var(--dur-fast)]",
          done && !active && "text-muted-foreground/75 hover:text-foreground",
          active && "bg-[color:var(--color-primary)]/10 font-medium text-foreground data-active:bg-[color:var(--color-primary)]/10",
        )}
      >
        <StatusDot phase={run.phase} />
        <span className="truncate">{runLabel(run)}</span>
        {run.createdAtUnix > 0n && (
          <span
            className={cn(
              "ml-auto shrink-0 pl-1 font-mono text-[10px] tabular-nums tracking-tight",
              active ? "text-muted-foreground/80" : "text-muted-foreground/55 group-hover/run:text-muted-foreground/80",
            )}
          >
            {formatAge(run.createdAtUnix)}
          </span>
        )}
      </SidebarMenuSubButton>
    </SidebarMenuSubItem>
  );
}

/** Quiet trailing row under a project's runs ("View all", "N completed hidden"). */
const subFooterClass = cn(
  "flex h-[24px] w-full items-center gap-1.5 rounded-[6px] px-1.5 text-left text-[10.5px] tracking-tight",
  "text-muted-foreground/65 hover:bg-sidebar-accent hover:text-foreground",
  "transition-colors duration-[var(--dur-fast)]",
  "outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring",
);

function SubEmptyRow({ children }: { children: React.ReactNode }) {
  return (
    <li className="flex h-[26px] items-center gap-2 px-1.5 text-[11px] text-muted-foreground/60">
      <MessageSquareDashed className="size-3.5 shrink-0" strokeWidth={1.75} />
      <span className="truncate">{children}</span>
    </li>
  );
}

/** Tiny checkbox row that fits the sidebar's muted visual language. */
function ShowCompletedCheckbox({
  checked,
  onChange,
  count,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  count: number;
}) {
  if (!checked && count === 0) return null;
  return (
    <li className="list-none pt-1">
      <button
        type="button"
        role="checkbox"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={cn(
          "flex h-[26px] w-full items-center gap-2 rounded-[6px] px-2",
          "text-[11px] text-muted-foreground/65 hover:text-foreground hover:bg-sidebar-accent",
          "transition-colors duration-[var(--dur-fast)]",
          "outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring",
          "group-data-[collapsible=icon]:hidden",
        )}
      >
        <span
          className={cn(
            "ml-px grid size-[13px] shrink-0 place-items-center rounded-[3.5px] ring-1 ring-inset transition-colors",
            checked
              ? "bg-[color:var(--color-primary)] ring-[color:var(--color-primary)] text-[color:var(--color-primary-foreground)]"
              : "bg-transparent ring-border/80",
          )}
        >
          {checked && <Check className="size-2.5" strokeWidth={3} />}
        </span>
        <span className="truncate tracking-tight">
          Show completed
        </span>
        {count > 0 && (
          <span className="ml-auto font-mono text-[10px] tabular-nums text-muted-foreground/50">{count}</span>
        )}
      </button>
    </li>
  );
}

/** Collapsible per project → recent chats as leaves. Project tree for the sidebar. */
export function ProjectTree({
  projects,
  runs,
  workspaceId,
  onNewChat,
}: {
  projects: Project[];
  runs: AgentRun[];
  workspaceId: string;
  onNewChat: (p: Project) => void;
}) {
  const location = useLocation();
  const [expanded, setExpanded] = React.useState<Record<string, boolean>>({});
  const [hiddenProjectKeys, setHiddenProjectKeys] = React.useState<Set<string>>(
    () => readHiddenSidebarProjects(workspaceId),
  );
  const [showCompleted, setShowCompleted] = React.useState<boolean>(
    () => localStorage.getItem(SHOW_COMPLETED_KEY) === "1",
  );
  const toggleShowCompleted = React.useCallback((v: boolean) => {
    setShowCompleted(v);
    try {
      localStorage.setItem(SHOW_COMPLETED_KEY, v ? "1" : "0");
    } catch {
      // Persistence is best-effort.
    }
  }, []);

  const runsByProject = React.useMemo(() => {
    const m = new Map<string, AgentRun[]>();
    for (const r of runs) {
      const key = projectRunKey(r);
      if (!key) continue;
      const list = m.get(key) ?? [];
      list.push(r);
      m.set(key, list);
    }
    for (const list of m.values()) list.sort((a, b) => Number(b.createdAtUnix - a.createdAtUnix));
    return m;
  }, [runs]);

  const hiddenProjects = React.useMemo(
    () => projects.filter((project) => hiddenProjectKeys.has(sidebarProjectKey(project))),
    [hiddenProjectKeys, projects],
  );
  const visibleProjects = React.useMemo(
    () => projects.filter((project) => !hiddenProjectKeys.has(sidebarProjectKey(project))),
    [hiddenProjectKeys, projects],
  );
  const totalCompleted = React.useMemo(() => {
    let count = 0;
    for (const project of visibleProjects) {
      const projectRuns = runsByProject.get(sidebarProjectKey(project)) ?? [];
      count += projectRuns.filter((run) => isDonePhase(run.phase)).length;
    }
    return count;
  }, [runsByProject, visibleProjects]);

  const projectLinkRefs = React.useRef(new Map<string, HTMLAnchorElement>());
  const hiddenProjectsTriggerRef = React.useRef<HTMLButtonElement>(null);

  const setProjectHidden = React.useCallback((project: Project, hidden: boolean) => {
    setHiddenProjectKeys((previous) => {
      const next = new Set(previous);
      const key = sidebarProjectKey(project);
      if (hidden) next.add(key);
      else next.delete(key);
      writeHiddenSidebarProjects(workspaceId, next);
      return next;
    });
  }, [workspaceId]);

  const restoreAllProjects = React.useCallback(() => {
    const next = new Set<string>();
    writeHiddenSidebarProjects(workspaceId, next);
    setHiddenProjectKeys(next);
    const firstProject = projects[0];
    if (firstProject) {
      window.setTimeout(() => projectLinkRefs.current.get(sidebarProjectKey(firstProject))?.focus());
    }
  }, [projects, workspaceId]);

  const hideProject = React.useCallback((project: Project) => {
    const index = visibleProjects.findIndex((candidate) => sidebarProjectKey(candidate) === sidebarProjectKey(project));
    const focusTarget = visibleProjects[index + 1] ?? visibleProjects[index - 1];
    setProjectHidden(project, true);
    window.setTimeout(() => {
      if (focusTarget) projectLinkRefs.current.get(sidebarProjectKey(focusTarget))?.focus();
      else hiddenProjectsTriggerRef.current?.focus();
    });
  }, [setProjectHidden, visibleProjects]);

  const restoreProject = React.useCallback((project: Project) => {
    setProjectHidden(project, false);
    window.setTimeout(() => projectLinkRefs.current.get(sidebarProjectKey(project))?.focus());
  }, [setProjectHidden]);

  return (
    <SidebarMenu className="gap-px">
      {visibleProjects.map((p) => {
        const key = `${p.namespace}/${p.name}`;
        const projRuns = runsByProject.get(key) ?? [];
        const detail = `/projects/${p.namespace}/${p.name}`;
        const active = location.pathname === detail;
        const hasActiveChild = projRuns.some((r) => location.pathname === `/runs/${r.namespace}/${r.name}`);
        const open = expanded[key] ?? hasActiveChild;

        const activeRuns = projRuns.filter((r) => !isDonePhase(r.phase));
        const doneRuns = projRuns.filter((r) => isDonePhase(r.phase));
        // Keep the run the user is currently viewing visible even when
        // completed runs are hidden.
        const visible = showCompleted
          ? [...activeRuns.slice(0, MAX_RUNS), ...doneRuns.slice(0, MAX_RUNS)]
          : [
              ...activeRuns.slice(0, MAX_RUNS),
              ...doneRuns.filter((r) => location.pathname === `/runs/${r.namespace}/${r.name}`),
            ];
        const hiddenDone = showCompleted ? 0 : doneRuns.length - (visible.length - Math.min(activeRuns.length, MAX_RUNS));
        const overflow = projRuns.length - visible.length - hiddenDone;

        return (
          <Collapsible
            key={key}
            open={open}
            onOpenChange={(o) => setExpanded((prev) => ({ ...prev, [key]: o }))}
            className="group/proj relative"
          >
            <SidebarMenuItem>
              <CollapsibleTrigger
                render={
                  <button
                    className={cn(
                      "absolute left-0.5 top-1/2 z-10 grid size-5 -translate-y-1/2 place-items-center rounded-[5px]",
                      "text-muted-foreground/60 hover:bg-sidebar-accent hover:text-foreground",
                      "transition-[opacity,color,background-color] duration-[var(--dur-fast)]",
                      "group-data-[collapsible=icon]:hidden",
                      "outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:opacity-100",
                      // Collapsed chevrons sit back until the row is hovered.
                      !open && "opacity-70 group-hover/proj:opacity-100 group-focus-within/proj:opacity-100",
                    )}
                  />
                }
                title={open ? "Collapse" : "Expand"}
              >
                <ChevronRight
                  strokeWidth={2}
                  className={cn("size-3 transition-transform duration-[var(--dur-base)]", open && "rotate-90")}
                />
              </CollapsibleTrigger>
              <SidebarMenuButton
                render={
                  <Link
                    to={detail}
                    ref={(node) => {
                      if (node) projectLinkRefs.current.set(key, node);
                      else projectLinkRefs.current.delete(key);
                    }}
                    onClick={() => writeLastProject(p)}
                  />
                }
                isActive={active}
                tooltip={p.displayName || p.name}
                className={cn(
                  "h-[30px] gap-2 rounded-[7px] pl-6 pr-8 text-[12.5px]",
                  "text-sidebar-foreground/85 transition-colors duration-[var(--dur-fast)]",
                  "hover:bg-sidebar-accent hover:text-foreground",
                  "data-active:bg-[color:var(--color-primary)]/10 data-active:text-foreground",
                  "group-data-[collapsible=icon]:pl-2! group-data-[collapsible=icon]:pr-2!",
                )}
              >
                <FolderKanban
                  strokeWidth={1.75}
                  className={cn(
                    "size-[14px] shrink-0 transition-colors duration-[var(--dur-fast)]",
                    active || open ? "text-[color:var(--color-primary)]" : "text-muted-foreground",
                  )}
                />
                <span className="truncate tracking-tight">{p.displayName || p.name}</span>
                <ActiveRunsBadge count={projRuns.filter((r) => isLivePhase(r.phase)).length} />
              </SidebarMenuButton>
              <DropdownMenu>
                <DropdownMenuTrigger
                  aria-label={`Actions for ${p.displayName || p.name} (${key})`}
                  className={cn(
                    "absolute right-1 top-1/2 z-10 grid size-6 -translate-y-1/2 place-items-center rounded-[5px]",
                    "text-muted-foreground/70 hover:bg-muted/70 hover:text-foreground",
                    "transition-[opacity,color,background-color] duration-[var(--dur-fast)]",
                    // Reveal on hover/focus/open; always visible on touch devices.
                    "md:opacity-0 group-hover/proj:opacity-100 group-focus-within/proj:opacity-100 data-[popup-open]:opacity-100",
                    "group-data-[collapsible=icon]:hidden",
                    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring focus-visible:opacity-100",
                  )}
                >
                  <MoreHorizontal className="size-3.5" strokeWidth={2} />
                </DropdownMenuTrigger>
                <DropdownMenuContent side="right" align="start" className="min-w-[190px]">
                  <DropdownMenuItem onClick={() => onNewChat(p)}>
                    <Plus className="size-3.5" />
                    New chat
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    onClick={() => hideProject(p)}
                    aria-label={`Hide ${p.displayName || p.name} (${key}) from sidebar`}
                  >
                    <EyeOff className="size-3.5" />
                    Hide from sidebar
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </SidebarMenuItem>
            <CollapsibleContent>
              <SidebarMenuSub className="mx-[13px] my-0.5 gap-px border-sidebar-border/90 px-1.5 py-0">
                {projRuns.length === 0 ? (
                  <SubEmptyRow>No chats yet</SubEmptyRow>
                ) : (
                  <>
                    {visible.length === 0 && (
                      <SubEmptyRow>No active chats</SubEmptyRow>
                    )}
                    {visible.map((r) => (
                      <RunRow
                        key={`${r.namespace}/${r.name}`}
                        run={r}
                        active={location.pathname === `/runs/${r.namespace}/${r.name}`}
                      />
                    ))}
                    {hiddenDone > 0 && (
                      <li>
                        <button
                          type="button"
                          onClick={() => toggleShowCompleted(true)}
                          className={subFooterClass}
                          title="Show completed runs"
                        >
                          <Eye className="size-3 shrink-0" strokeWidth={1.75} />
                          <span className="truncate">{hiddenDone} completed hidden</span>
                        </button>
                      </li>
                    )}
                    {overflow > 0 && (
                      <li>
                        <Link
                          to={detail}
                          onClick={() => writeLastProject(p)}
                          className={cn(subFooterClass, "group/more")}
                        >
                          <span className="truncate">View all {projRuns.length}</span>
                          <ArrowRight
                            className="size-3 shrink-0 transition-transform duration-[var(--dur-fast)] group-hover/more:translate-x-0.5"
                            strokeWidth={1.75}
                          />
                        </Link>
                      </li>
                    )}
                  </>
                )}
              </SidebarMenuSub>
            </CollapsibleContent>
          </Collapsible>
        );
      })}
      {hiddenProjects.length > 0 && (
        <SidebarMenuItem className="pt-1">
          <DropdownMenu>
            <DropdownMenuTrigger
              ref={hiddenProjectsTriggerRef}
              aria-label={`${hiddenProjects.length} hidden ${hiddenProjects.length === 1 ? "project" : "projects"}`}
              className={cn(
                "flex h-[26px] w-full items-center gap-2 rounded-[6px] px-2",
                "text-[11px] text-muted-foreground/65 hover:bg-sidebar-accent hover:text-foreground",
                "transition-colors duration-[var(--dur-fast)]",
                "group-data-[collapsible=icon]:hidden",
                "outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring",
              )}
            >
              <EyeOff className="size-3.5 shrink-0" strokeWidth={1.75} />
              <span className="truncate tracking-tight">
                {hiddenProjects.length} hidden {hiddenProjects.length === 1 ? "project" : "projects"}
              </span>
              <ChevronRight className="ml-auto size-3 shrink-0 text-muted-foreground/50" />
            </DropdownMenuTrigger>
            <DropdownMenuContent side="right" align="end" className="min-w-[210px]">
              <DropdownMenuLabel>Hidden projects</DropdownMenuLabel>
              {hiddenProjects.map((project) => (
                <DropdownMenuItem
                  key={sidebarProjectKey(project)}
                  onClick={() => restoreProject(project)}
                  aria-label={`Show ${project.displayName || project.name} (${sidebarProjectKey(project)}) in sidebar`}
                  className="gap-2"
                >
                  <Eye className="size-3.5 text-muted-foreground" />
                  <span className="min-w-0">
                    <span className="block truncate">{project.displayName || project.name}</span>
                    <span className="block truncate font-mono text-[10px] text-muted-foreground">
                      {sidebarProjectKey(project)}
                    </span>
                  </span>
                </DropdownMenuItem>
              ))}
              {hiddenProjects.length > 1 && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem onClick={restoreAllProjects}>
                    Show all projects
                  </DropdownMenuItem>
                </>
              )}
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarMenuItem>
      )}
      <ShowCompletedCheckbox
        checked={showCompleted}
        onChange={toggleShowCompleted}
        count={totalCompleted}
      />
    </SidebarMenu>
  );
}
