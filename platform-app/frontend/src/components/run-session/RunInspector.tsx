import { type ComponentType, type KeyboardEvent, type ReactNode, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { AnimatePresence, motion } from "framer-motion";
import {
  Activity,
  CircleAlert,
  FileDiff,
  GitPullRequest,
  Info,
  PanelRight,
  SquareTerminal,
  Workflow,
  X,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { fade, transitions } from "@/lib/motion";
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
 * links keep resolving. Only the labels changed. "context" is new: it holds
 * what the header's run-context sheet used to.
 */
export type InspectorTab = "diff" | "pr" | "graph" | "logs" | "errors" | "trace" | "context";

const INSPECTOR_TABS: InspectorTab[] = ["diff", "pr", "graph", "logs", "errors", "trace", "context"];

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
  context: { label: "Context", icon: Info },
};

export type InspectorTabDef = {
  id: InspectorTab;
  /** Small trailing count, e.g. number of errors. Zero and undefined hide it. */
  count?: number;
  /** Quiet presence indicator, e.g. the run has uncommitted changes. */
  dot?: boolean;
};

export type InspectorShortcut = { type: "toggle" } | { type: "select"; tab: InspectorTab };

/**
 * Resolves the inspector's global keyboard shortcuts: `Mod+.` toggles the
 * panel and `Mod+Shift+1..9` jumps to the n-th tab. Digits are matched on
 * `code` because Shift changes `key` to punctuation on most layouts.
 */
export function inspectorShortcut(
  event: Pick<globalThis.KeyboardEvent, "key" | "code" | "metaKey" | "ctrlKey" | "shiftKey" | "altKey">,
  tabs: readonly InspectorTab[],
): InspectorShortcut | null {
  const mod = event.metaKey || event.ctrlKey;
  if (!mod || event.altKey) return null;
  if (event.key === "." && !event.shiftKey) return { type: "toggle" };
  if (!event.shiftKey) return null;
  const digit = /^Digit([1-9])$/.exec(event.code);
  if (!digit) return null;
  const tab = tabs[Number(digit[1]) - 1];
  return tab ? { type: "select", tab } : null;
}

/**
 * Shared keyboard handling for the tab strips (WAI-ARIA tabs pattern with
 * automatic activation): arrows wrap, Home/End jump, and focus follows the
 * selection so the roving tabIndex stays in sync.
 */
export function moveTabFocus<T extends string>(
  event: KeyboardEvent<HTMLElement>,
  ids: readonly T[],
  current: T,
  onChange: (id: T) => void,
) {
  const index = ids.indexOf(current);
  let next: number;
  switch (event.key) {
    case "ArrowRight":
      next = (index + 1) % ids.length;
      break;
    case "ArrowLeft":
      next = (index - 1 + ids.length) % ids.length;
      break;
    case "Home":
      next = 0;
      break;
    case "End":
      next = ids.length - 1;
      break;
    default:
      return;
  }
  event.preventDefault();
  const id = ids[next];
  if (id === undefined) return;
  onChange(id);
  const tabs = event.currentTarget.querySelectorAll<HTMLElement>('[role="tab"]');
  tabs[next]?.focus();
}

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
      size="icon-sm"
      onClick={onToggle}
      aria-pressed={open}
      aria-label={open ? "Hide inspector" : "Show inspector"}
      title={open ? "Hide inspector" : "Show inspector"}
      className={cn(
        "relative shrink-0 text-muted-foreground hover:text-foreground [@media(pointer:coarse)]:size-9",
        open && "bg-muted text-foreground",
      )}
    >
      <PanelRight />
      {attention && !open && (
        <span className="absolute right-1 top-1 size-1.5 rounded-full bg-tone-danger" />
      )}
    </Button>
  );
}

function InspectorNav({
  tabs,
  activeTab,
  onTabChange,
  tabId,
  panelId,
}: {
  tabs: InspectorTabDef[];
  activeTab: InspectorTab;
  onTabChange: (tab: InspectorTab) => void;
  tabId: (tab: InspectorTab) => string;
  panelId: string;
}) {
  const indicatorId = useId();
  const ids = tabs.map((tab) => tab.id);
  const listRef = useRef<HTMLDivElement>(null);
  // Labels collapse to icons only when the labelled strip genuinely overflows
  // its container, and come back once the container is wide enough again.
  // Measuring beats a fixed breakpoint because the tab set varies per run.
  const [compact, setCompact] = useState(false);
  const labelledWidthRef = useRef(0);
  useLayoutEffect(() => {
    const el = listRef.current;
    if (!el) return;
    const measure = () => {
      if (!compact) {
        labelledWidthRef.current = el.scrollWidth;
        if (el.scrollWidth > el.clientWidth + 1) setCompact(true);
      } else if (el.clientWidth >= labelledWidthRef.current) {
        setCompact(false);
      }
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [compact, tabs.length]);
  return (
    <div
      ref={listRef}
      role="tablist"
      aria-label="Inspector sections"
      data-compact={compact || undefined}
      onKeyDown={(event) => moveTabFocus(event, ids, activeTab, onTabChange)}
      className={cn(
        "flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden",
        compact && "[mask-image:linear-gradient(to_right,black_92%,transparent)]",
      )}
    >
      {tabs.map(({ id, count, dot }) => {
        const { label, icon: Icon } = inspectorTabMeta[id];
        const active = id === activeTab;
        return (
          <button
            key={id}
            type="button"
            role="tab"
            id={tabId(id)}
            aria-selected={active}
            aria-controls={panelId}
            tabIndex={active ? 0 : -1}
            title={label}
            aria-label={label}
            onClick={() => onTabChange(id)}
            className={cn(
              "relative isolate flex shrink-0 items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium whitespace-nowrap transition-colors",
              "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
              active ? "text-foreground" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {active && (
              <motion.span
                layoutId={indicatorId}
                transition={transitions.subtle}
                className="absolute inset-0 -z-10 rounded-md bg-muted"
              />
            )}
            <Icon className="size-3.5 shrink-0" />
            {(!compact || active) && <span>{label}</span>}
            {count ? (
              <span className="rounded-full bg-tone-danger/12 px-1.5 text-3xs tabular-nums text-tone-danger-fg">
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
  persistent,
  children,
}: {
  tabs: InspectorTabDef[];
  activeTab: InspectorTab;
  onTabChange: (tab: InspectorTab) => void;
  onClose: () => void;
  persistent?: ReactNode;
  children: ReactNode;
}) {
  const baseId = useId();
  const tabId = (tab: InspectorTab) => `${baseId}-tab-${tab}`;
  const panelId = `${baseId}-panel`;
  return (
    <motion.div
      initial={{ opacity: 0, x: 8 }}
      animate={{ opacity: 1, x: 0 }}
      transition={transitions.panel}
      className="flex h-full min-h-0 min-w-0 flex-col bg-background"
    >
      <div className="@container flex h-12 shrink-0 items-center gap-2 border-b px-2">
        <InspectorNav
          tabs={tabs}
          activeTab={activeTab}
          onTabChange={onTabChange}
          tabId={tabId}
          panelId={panelId}
        />
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
      <div
        role="tabpanel"
        id={panelId}
        aria-labelledby={tabId(activeTab)}
        className="relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden"
      >
        {persistent}
        {/* Persistent panes keep their own mount across switches, so only the
            transient content crossfades; it overlays the panel while exiting. */}
        <AnimatePresence mode="wait" initial={false}>
          {children ? (
            <motion.div
              key={activeTab}
              {...fade}
              className="absolute inset-0 flex min-h-0 min-w-0 flex-col overflow-hidden"
            >
              {children}
            </motion.div>
          ) : null}
        </AnimatePresence>
      </div>
    </motion.div>
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
  persistent,
  children,
}: {
  split: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  tabs: InspectorTabDef[];
  activeTab: InspectorTab;
  onTabChange: (tab: InspectorTab) => void;
  /**
   * Panes that must survive tab switches (zoom, selection, scroll). They stay
   * mounted and toggle their own `hidden`; `children` is the pane that only
   * exists while its tab is active.
   */
  persistent?: ReactNode;
  children?: ReactNode;
}) {
  const body = (
    <InspectorBody
      tabs={tabs}
      activeTab={activeTab}
      onTabChange={onTabChange}
      onClose={() => onOpenChange(false)}
      persistent={persistent}
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
