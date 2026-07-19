import * as React from "react";
import { Link, useLocation } from "react-router-dom";
import { Check, ChevronRight, FolderKanban, Plus } from "lucide-react";

import { cn } from "@/lib/utils";
import { phaseTone, toneColor, isLivePhase, isDonePhase } from "@/lib/status";
import { formatAge } from "@/lib/format";
import { writeLastProject } from "@/lib/lastProject";
import { projectRunKey } from "@/lib/runSource";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
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
        "ml-auto inline-flex h-[16px] min-w-[16px] shrink-0 items-center justify-center gap-1 rounded-full px-1 text-[10px] font-medium tabular-nums",
        "bg-[color-mix(in_oklch,var(--tone-running)_14%,transparent)] text-[color:var(--tone-running-fg)]",
        "ring-1 ring-inset ring-[color-mix(in_oklch,var(--tone-running)_30%,transparent)]",
        "group-hover/proj:hidden",
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
    <span className="relative inline-flex size-1.5 shrink-0 rounded-full" style={{ backgroundColor: toneColor[phaseTone(phase)] }}>
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
          "group/run text-[11.5px]",
          done && !active && "text-muted-foreground/70",
          active && "bg-sidebar-accent text-sidebar-accent-foreground",
        )}
      >
        <StatusDot phase={run.phase} />
        <span className="truncate">{runLabel(run)}</span>
        {run.createdAtUnix > 0n && (
          <span className="ml-auto shrink-0 text-[10px] tabular-nums text-muted-foreground/50">
            {formatAge(run.createdAtUnix)}
          </span>
        )}
      </SidebarMenuSubButton>
    </SidebarMenuSubItem>
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
    <li className="list-none">
      <button
        type="button"
        role="checkbox"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={cn(
          "mt-0.5 flex w-full items-center gap-2 rounded-[6px] px-2 py-1",
          "text-[11px] text-muted-foreground/70 hover:text-foreground hover:bg-sidebar-accent",
          "transition-colors duration-[var(--dur-fast)]",
        )}
      >
        <span
          className={cn(
            "grid size-[13px] shrink-0 place-items-center rounded-[3.5px] ring-1 ring-inset transition-colors",
            checked
              ? "bg-[color:var(--color-primary)]/80 ring-[color:var(--color-primary)]/60 text-background"
              : "bg-transparent ring-border",
          )}
        >
          {checked && <Check className="size-2.5" strokeWidth={3} />}
        </span>
        <span className="truncate tracking-tight">
          Show completed{count > 0 ? ` (${count})` : ""}
        </span>
      </button>
    </li>
  );
}

/** Collapsible per project → recent chats as leaves. Project tree for the sidebar. */
export function ProjectTree({
  projects,
  runs,
  onNewChat,
}: {
  projects: Project[];
  runs: AgentRun[];
  onNewChat: (p: Project) => void;
}) {
  const location = useLocation();
  const [expanded, setExpanded] = React.useState<Record<string, boolean>>({});
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

  const totalCompleted = React.useMemo(() => {
    let n = 0;
    for (const list of runsByProject.values()) n += list.filter((r) => isDonePhase(r.phase)).length;
    return n;
  }, [runsByProject]);

  return (
    <SidebarMenu>
      {projects.map((p) => {
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
            className="group/proj"
          >
            <SidebarMenuItem>
              <CollapsibleTrigger
                render={
                  <button className="absolute left-0.5 top-1/2 z-10 grid size-5 -translate-y-1/2 place-items-center rounded text-muted-foreground/60 hover:text-foreground" />
                }
                title="Expand"
              >
                <ChevronRight className={cn("size-3 transition-transform", open && "rotate-90")} />
              </CollapsibleTrigger>
              <SidebarMenuButton
                render={<Link to={detail} onClick={() => writeLastProject(p)} />}
                isActive={active}
                tooltip={p.displayName || p.name}
                className="h-[30px] pl-6 pr-2 text-[12.5px] rounded-[6px] gap-2 data-[active=true]:bg-[color:var(--color-primary)]/12 data-[active=true]:text-foreground hover:bg-sidebar-accent"
              >
                <FolderKanban className="size-[14px] text-muted-foreground" />
                <span className="truncate tracking-tight">{p.displayName || p.name}</span>
                <ActiveRunsBadge count={projRuns.filter((r) => isLivePhase(r.phase)).length} />
                <button
                  onClick={(e) => { e.preventDefault(); e.stopPropagation(); onNewChat(p); }}
                  title="New chat"
                  className="ml-auto grid size-[18px] shrink-0 place-items-center rounded text-muted-foreground/70 opacity-0 hover:bg-muted/70 hover:text-foreground group-hover/proj:opacity-100"
                >
                  <Plus className="size-3.5" />
                </button>
              </SidebarMenuButton>
            </SidebarMenuItem>
            <CollapsibleContent>
              <SidebarMenuSub>
                {projRuns.length === 0 ? (
                  <li className="px-2 py-1 text-[11px] text-muted-foreground/60">No chats yet</li>
                ) : (
                  <>
                    {visible.length === 0 && (
                      <li className="px-2 py-1 text-[11px] text-muted-foreground/60">No active chats</li>
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
                          className="w-full rounded px-2 py-0.5 text-left text-[10.5px] text-muted-foreground/50 hover:text-foreground hover:bg-sidebar-accent transition-colors duration-[var(--dur-fast)]"
                          title="Show completed runs"
                        >
                          {hiddenDone} completed hidden
                        </button>
                      </li>
                    )}
                    {overflow > 0 && (
                      <li>
                        <Link
                          to={detail}
                          onClick={() => writeLastProject(p)}
                          className="block rounded px-2 py-0.5 text-[10.5px] text-muted-foreground/50 hover:text-foreground hover:bg-sidebar-accent transition-colors duration-[var(--dur-fast)]"
                        >
                          View all {projRuns.length} →
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
      <ShowCompletedCheckbox
        checked={showCompleted}
        onChange={toggleShowCompleted}
        count={totalCompleted}
      />
    </SidebarMenu>
  );
}
