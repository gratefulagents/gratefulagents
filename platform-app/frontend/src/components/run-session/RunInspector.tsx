import { type ComponentType, type ReactNode, useEffect, useState } from "react";
import { Activity, CircleAlert, FileDiff, GitPullRequest, PanelRight, SquareTerminal, Workflow, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { cn } from "@/lib/utils";

/**
 * Everything that is not the conversation lives in the inspector: the run's
 * artifacts (changes, pull requests), its structure (sub-agents), and its
 * diagnostics (logs, errors, trace). The chat is the page; this panel is the
 * one place you go to look at anything else, and it is closed by default.
 */
/**
 * Tab ids intentionally reuse the existing `MainView` vocabulary (minus
 * "chat", which is now the page itself) so persisted preferences and deep
 * links keep resolving. Only the labels changed.
 */
export type InspectorTab = "diff" | "pr" | "graph" | "logs" | "errors" | "trace";

const INSPECTOR_TABS: InspectorTab[] = ["diff", "pr", "graph", "logs", "errors", "trace"];

export function isInspectorTab(value: string | null): value is InspectorTab {
  return INSPECTOR_TABS.includes(value as InspectorTab);
}

export const inspectorTabMeta: Record<InspectorTab, { label: string; icon: ComponentType<{ className?: string }> }> = {
  diff: { label: "Changes", icon: FileDiff },
  pr: { label: "Pull request", icon: GitPullRequest },
  graph: { label: "Agents", icon: Workflow },
  logs: { label: "Logs", icon: SquareTerminal },
  errors: { label: "Errors", icon: CircleAlert },
  trace: { label: "Trace", icon: Activity },
};

export type InspectorTabDef = {
  id: InspectorTab;
  /** Small trailing count, e.g. number of errors. Zero and undefined hide it. */
  count?: number;
  /** Quiet presence indicator, e.g. the run has uncommitted changes. */
  dot?: boolean;
};

/** Viewports wide enough to host the chat and the inspector side by side. */
export function useSplitViewport(): boolean {
  const [split, setSplit] = useState(
    () => typeof window !== "undefined" && window.matchMedia("(min-width: 1024px)").matches,
  );
  useEffect(() => {
    const mql = window.matchMedia("(min-width: 1024px)");
    const onChange = (event: MediaQueryListEvent) => setSplit(event.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);
  return split;
}

/**
 * InspectorToggle is the single control that reveals the panel. It sits in the
 * run header next to the primary action and reports unread-ish state through
 * the same dot/count vocabulary the panel's own nav uses.
 */
export function InspectorToggle({
  open,
  onToggle,
  attention,
}: {
  open: boolean;
  onToggle: () => void;
  attention?: boolean;
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      onClick={onToggle}
      aria-pressed={open}
      aria-label={open ? "Hide inspector" : "Show inspector"}
      title={open ? "Hide inspector" : "Show inspector"}
      className={cn(
        "relative size-8 shrink-0 p-0 text-muted-foreground hover:text-foreground",
        open && "bg-muted text-foreground",
      )}
    >
      <PanelRight className="size-4" />
      {attention && !open && (
        <span className="absolute right-1.5 top-1.5 size-1.5 rounded-full bg-[color:var(--tone-danger)]" />
      )}
    </Button>
  );
}

function InspectorNav({
  tabs,
  activeTab,
  onTabChange,
}: {
  tabs: InspectorTabDef[];
  activeTab: InspectorTab;
  onTabChange: (tab: InspectorTab) => void;
}) {
  return (
    <div
      role="tablist"
      aria-label="Inspector sections"
      className="flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
    >
      {tabs.map(({ id, count, dot }) => {
        const { label, icon: Icon } = inspectorTabMeta[id];
        const active = id === activeTab;
        return (
          <button
            key={id}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onTabChange(id)}
            className={cn(
              "flex shrink-0 items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium whitespace-nowrap transition-colors",
              "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
              active ? "bg-muted text-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            <Icon className="size-3.5 shrink-0" />
            {label}
            {count ? (
              <span className="rounded-full bg-[color:var(--tone-danger)]/12 px-1.5 text-[10px] tabular-nums text-[color:var(--tone-danger)]">
                {count > 99 ? "99+" : count}
              </span>
            ) : dot ? (
              <span className="size-1.5 rounded-full bg-current opacity-50" />
            ) : null}
          </button>
        );
      })}
    </div>
  );
}

function InspectorBody({
  tabs,
  activeTab,
  onTabChange,
  onClose,
  children,
}: {
  tabs: InspectorTabDef[];
  activeTab: InspectorTab;
  onTabChange: (tab: InspectorTab) => void;
  onClose: () => void;
  children: ReactNode;
}) {
  return (
    <div className="flex h-full min-h-0 min-w-0 flex-col bg-background">
      <div className="flex h-11 shrink-0 items-center gap-2 border-b px-2">
        <InspectorNav tabs={tabs} activeTab={activeTab} onTabChange={onTabChange} />
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={onClose}
          aria-label="Close inspector"
          className="size-7 shrink-0 p-0 text-muted-foreground hover:text-foreground"
        >
          <X className="size-4" />
        </Button>
      </div>
      <div role="tabpanel" className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
        {children}
      </div>
    </div>
  );
}

/**
 * RunInspector renders as a docked side panel on wide viewports (the parent
 * supplies the resizable panel) and as a full-height sheet on narrow ones, so
 * the conversation is never squeezed on a phone.
 */
export function RunInspector({
  split,
  open,
  onOpenChange,
  tabs,
  activeTab,
  onTabChange,
  children,
}: {
  split: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tabs: InspectorTabDef[];
  activeTab: InspectorTab;
  onTabChange: (tab: InspectorTab) => void;
  children: ReactNode;
}) {
  const body = (
    <InspectorBody
      tabs={tabs}
      activeTab={activeTab}
      onTabChange={onTabChange}
      onClose={() => onOpenChange(false)}
    >
      {children}
    </InspectorBody>
  );

  if (split) {
    return body;
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="w-full gap-0 p-0 sm:max-w-lg [&>button]:hidden">
        <SheetHeader className="sr-only">
          <SheetTitle>{inspectorTabMeta[activeTab].label}</SheetTitle>
        </SheetHeader>
        {body}
      </SheetContent>
    </Sheet>
  );
}
