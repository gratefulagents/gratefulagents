import * as React from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { X, Plus } from "lucide-react";

import { cn } from "@/lib/utils";
import { phaseTone, isLivePhase, toneText, type StatusTone } from "@/lib/status";
import { getRunAttention } from "@/lib/agentOps";
import {
  useRunTabs,
  closeRunTab,
  closeOtherRunTabs,
  closeAllRunTabs,
  moveRunTab,
  type RunTab,
} from "@/hooks/useRunTabs";
import type { AgentRun } from "@/rpc/platform/service_pb";

/**
 * Browser-style tab strip for open AgentRuns. Rendered under the titlebar on
 * every route once at least one run tab is open, so switching between live
 * runs is one click (or ⌥⌘[/]) from anywhere in the app.
 *
 * The active tab is derived from the URL; the strip never owns navigation
 * state. Tabs show a live status dot (attention tone wins over phase tone),
 * close on middle-click or via the × button, and drag to reorder.
 */
export function RunTabs({ runs, scope }: { runs: AgentRun[]; scope: string }) {
  const tabs = useRunTabs(scope);
  const navigate = useNavigate();
  const location = useLocation();
  const listRef = React.useRef<HTMLDivElement | null>(null);
  const dragFrom = React.useRef<number | null>(null);
  const [dropIndex, setDropIndex] = React.useState<number | null>(null);

  const activePath = location.pathname;
  const activeIndex = tabs.findIndex((t) => t.path === activePath);

  const runByPath = React.useMemo(() => {
    const m = new Map<string, AgentRun>();
    for (const r of runs) m.set(`/runs/${r.namespace}/${r.name}`, r);
    return m;
  }, [runs]);

  const close = React.useCallback(
    (tab: RunTab) => {
      const nextPath = closeRunTab(scope, tab.path);
      if (tab.path === activePath) {
        navigate(nextPath ?? "/runs");
      }
    },
    [scope, activePath, navigate],
  );

  // ⌥⌘] / ⌥⌘[ cycle tabs, ⌥⌘W closes the active tab. Uses e.code so macOS
  // Option dead-keys don't get in the way; deliberately not ⌘W/⌘digit which
  // the browser (web build) and window manager (Tauri) already own.
  React.useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (!(e.metaKey || e.ctrlKey) || !e.altKey || e.shiftKey) return;
      // Windows layouts report AltGr as Ctrl+Alt; AltGr+letter is normal text
      // entry (e.g. Polish ż via AltGr+Z), never a tab command.
      if (e.getModifierState?.("AltGraph")) return;
      // Never steal keystrokes from editable targets — closing a tab while
      // typing in the run composer would discard draft state.
      const target = e.target as HTMLElement | null;
      if (
        target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.isContentEditable)
      ) {
        return;
      }
      if (tabs.length === 0) return;
      if (e.code === "BracketRight" || e.code === "BracketLeft") {
        e.preventDefault();
        const dir = e.code === "BracketRight" ? 1 : -1;
        // From a non-run page, jump (back) into the most recent position.
        const from = activeIndex === -1 ? (dir === 1 ? -1 : 0) : activeIndex;
        const next = (from + dir + tabs.length) % tabs.length;
        navigate(tabs[next].path);
        return;
      }
      if (e.code === "KeyW" && activeIndex !== -1) {
        e.preventDefault();
        close(tabs[activeIndex]);
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [tabs, activeIndex, navigate, close]);

  // Keep the active tab visible when the strip overflows.
  React.useEffect(() => {
    if (activeIndex === -1) return;
    const el = listRef.current?.querySelector<HTMLElement>('[aria-selected="true"]');
    el?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [activeIndex, tabs.length]);

  if (tabs.length === 0) return null;

  return (
    <>
    <div className="shrink-0 flex items-stretch min-w-0 bg-muted/20">
      <div
        ref={listRef}
        role="tablist"
        aria-label="Open runs"
        className="flex items-stretch min-w-0 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      >
        {tabs.map((tab, i) => (
          <RunTabItem
            key={tab.path}
            tab={tab}
            run={runByPath.get(tab.path)}
            active={tab.path === activePath}
            dropTarget={dropIndex === i}
            onActivate={() => navigate(tab.path)}
            onClose={() => close(tab)}
            onCloseOthers={() => {
              closeOtherRunTabs(scope, tab.path);
              // Close-others can remove the tab for the current route; move
              // to the surviving tab so the strip and URL stay coherent.
              if (activeIndex !== -1 && tab.path !== activePath) {
                navigate(tab.path);
              }
            }}
            onCloseAll={() => {
              closeAllRunTabs(scope);
              if (activeIndex !== -1) navigate("/runs");
            }}
            onDragStart={() => {
              dragFrom.current = i;
            }}
            onDragOver={(e) => {
              if (dragFrom.current === null) return;
              e.preventDefault();
              setDropIndex(i);
            }}
            onDrop={() => {
              if (dragFrom.current !== null && dragFrom.current !== i) {
                moveRunTab(scope, dragFrom.current, i);
              }
              dragFrom.current = null;
              setDropIndex(null);
            }}
            onDragEnd={() => {
              dragFrom.current = null;
              setDropIndex(null);
            }}
          />
        ))}
      </div>
      <button
        onClick={() => navigate("/")}
        aria-label="New run"
        title="New run"
        className={cn(
          "shrink-0 self-center mx-1 inline-flex size-[24px] items-center justify-center rounded-[6px]",
          "text-muted-foreground hover:text-foreground hover:bg-muted/60",
          "transition-colors duration-[var(--dur-fast)]",
        )}
      >
        <Plus className="size-[14px]" />
      </button>
      <div className="flex-1" />
    </div>
    <div className="hairline shrink-0" />
    </>
  );
}

function RunTabItem({
  tab,
  run,
  active,
  dropTarget,
  onActivate,
  onClose,
  onCloseOthers,
  onCloseAll,
  onDragStart,
  onDragOver,
  onDrop,
  onDragEnd,
}: {
  tab: RunTab;
  run?: AgentRun;
  active: boolean;
  dropTarget: boolean;
  onActivate: () => void;
  onClose: () => void;
  onCloseOthers: () => void;
  onCloseAll: () => void;
  onDragStart: () => void;
  onDragOver: (e: React.DragEvent) => void;
  onDrop: () => void;
  onDragEnd: () => void;
}) {
  const label = run?.displayName || tab.name;
  const attention = run ? getRunAttention(run) : null;
  const tone: StatusTone =
    attention && attention.kind !== "none" ? attention.tone : phaseTone(run?.phase ?? "");
  const live = run ? isLivePhase(run.phase) : false;
  const title = run
    ? `${label} — ${run.phase || "…"}${attention && attention.kind !== "none" ? ` · ${attention.label}` : ""}`
    : `${tab.namespace}/${tab.name}`;

  return (
    <div
      role="tab"
      tabIndex={0}
      aria-selected={active}
      aria-label={label}
      title={title}
      draggable
      onClick={onActivate}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onActivate();
        }
        if (e.key === "Delete" || e.key === "Backspace") {
          e.preventDefault();
          onClose();
        }
      }}
      onAuxClick={(e) => {
        // Middle-click closes, like a browser tab.
        if (e.button === 1) {
          e.preventDefault();
          onClose();
        }
      }}
      onContextMenu={(e) => {
        // Minimal power-user affordances without a menu component:
        // ⌥-right-click closes others, ⇧-right-click closes all.
        if (e.altKey) {
          e.preventDefault();
          onCloseOthers();
        } else if (e.shiftKey) {
          e.preventDefault();
          onCloseAll();
        }
      }}
      onDragStart={onDragStart}
      onDragOver={onDragOver}
      onDrop={onDrop}
      onDragEnd={onDragEnd}
      className={cn(
        "group relative flex items-center gap-1.5 cursor-pointer select-none",
        "h-[32px] pl-2.5 pr-1.5 min-w-[110px] max-w-[200px] flex-shrink basis-[160px]",
        "text-[12px] tracking-tight border-r border-border/40",
        "transition-colors duration-[var(--dur-fast)]",
        active
          ? "bg-background text-foreground"
          : "text-muted-foreground hover:text-foreground hover:bg-muted/40",
        dropTarget && "ring-1 ring-inset ring-primary/50",
      )}
    >
      {/* Active indicator — a hairline on top, browser-style. */}
      <span
        aria-hidden
        className={cn(
          "absolute inset-x-0 top-0 h-[2px] rounded-b-full",
          active ? "bg-primary/70" : "bg-transparent",
        )}
      />
      <span
        aria-hidden
        className={cn("relative inline-flex size-[7px] shrink-0 rounded-full bg-current", toneText[tone])}
      >
        {live && (
          <span className="absolute inset-0 rounded-full bg-current opacity-60 motion-safe:animate-ping" />
        )}
      </span>
      <span className="flex-1 min-w-0 truncate">{label}</span>
      <button
        onClick={(e) => {
          e.stopPropagation();
          onClose();
        }}
        aria-label={`Close ${label}`}
        tabIndex={-1}
        className={cn(
          "inline-flex size-[18px] shrink-0 items-center justify-center rounded-[4px]",
          "text-muted-foreground/70 hover:text-foreground hover:bg-muted",
          "opacity-0 group-hover:opacity-100 focus-visible:opacity-100",
          active && "opacity-100",
          "transition-opacity duration-[var(--dur-fast)]",
        )}
      >
        <X className="size-[12px]" />
      </button>
    </div>
  );
}
