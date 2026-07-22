import * as React from "react";
import { Command } from "cmdk";
import { AnimatePresence, motion } from "framer-motion";
import { useNavigate, useLocation } from "react-router-dom";
import {
  FolderKanban,
  GitBranch,
  Clock3,
  Layers,
  Radio,
  Activity,
  Users,
  Home as HomeIcon,
  Settings as SettingsIcon,
  LogOut,
  Sun,
  Moon,
  Search,
  Server,
  KeyRound,
  Blocks,
  Sparkles,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { palette as paletteMotion, fade } from "@/lib/motion";
import { toggleTheme, useTheme } from "@/lib/theme";
import { Kbd } from "./Kbd";
import { useAuth } from "@/contexts/AuthContext";
import { useRecents, type Recent } from "@/hooks/useRecents";
import { isTauri } from "@/lib/platform";

export interface PaletteItem {
  id: string;
  group: string;
  label: string;
  hint?: string;
  icon?: React.ReactNode;
  shortcut?: string[];
  keywords?: string[];
  action: () => void | Promise<void>;
}

export interface CommandPaletteProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Extra contextual items injected by the active screen. */
  extraItems?: PaletteItem[];
  /** Map of run path ("/runs/:ns/:name") to display name, for the Recent group. */
  runLabels?: Map<string, string>;
}

/**
 * Command palette. Raycast/Linear-style spotlight. Keyboard-first,
 * groups, sharp selection, subtle springy entry, hairline dividers.
 */
export function CommandPalette({ open, onOpenChange, extraItems = [], runLabels }: CommandPaletteProps) {
  const navigate = useNavigate();
  const location = useLocation();
  const { logout } = useAuth();
  const theme = useTheme();
  const recents = useRecents();
  const focusRestoreRef = React.useRef<HTMLElement | null>(null);

  React.useEffect(() => {
    if (open) {
      focusRestoreRef.current = document.activeElement as HTMLElement | null;
      return;
    }
    const el = focusRestoreRef.current;
    if (!el) return;
    requestAnimationFrame(() => {
      el.focus();
      if (focusRestoreRef.current === el) focusRestoreRef.current = null;
    });
  }, [open]);

  const navItems: PaletteItem[] = React.useMemo(
    () => [
      {
        id: "nav.home",
        group: "Go to",
        label: "Home",
        icon: <HomeIcon className="size-4" />,
        keywords: ["start", "landing"],
        action: () => navigate("/"),
      },
      {
        id: "nav.projects",
        group: "Go to",
        label: "Projects",
        hint: "All projects",
        icon: <FolderKanban className="size-4" />,
        keywords: ["dashboard"],
        action: () => navigate("/projects"),
      },
      {
        id: "nav.agent-ops",
        group: "Go to",
        label: "Agent Ops",
        hint: "Monitor and manage runs",
        icon: <Radio className="size-4" />,
        keywords: ["runs", "fleet", "operations", "status", "bulk", "history"],
        action: () => navigate("/runs"),
      },
      {
        id: "nav.observability",
        group: "Go to",
        label: "Observability",
        hint: "Usage, cost, and reliability",
        icon: <Activity className="size-4" />,
        keywords: ["metrics", "usage", "cost", "tokens", "tools", "errors", "history"],
        action: () => navigate("/observability"),
      },
      {
        id: "action.new-cron",
        group: "Go to",
        label: "New cron trigger",
        hint: "Schedule recurring runs",
        icon: <Clock3 className="size-4" />,
        keywords: ["new", "cron", "schedule", "trigger", "create"],
        action: () => navigate("/cron?new=1"),
      },
      {
        id: "nav.shared",
        group: "Go to",
        label: "Shared with me",
        icon: <Users className="size-4" />,
        action: () => navigate("/shared"),
      },
      ...(isTauri
        ? [
            {
              id: "nav.settings-connection",
              group: "Go to",
              label: "Settings: Connection",
              icon: <Server className="size-4" />,
              keywords: ["endpoint", "backend", "workspace", "settings"],
              action: () => navigate("/settings/connection"),
            },
          ]
        : []),
      {
        id: "nav.settings-credentials",
        group: "Go to",
        label: "Settings: Credentials",
        icon: <KeyRound className="size-4" />,
        keywords: ["credentials", "api key", "oauth", "token", "settings"],
        action: () => navigate("/settings/credentials"),
      },
      {
        id: "nav.resources",
        group: "Go to",
        label: "Resources",
        hint: "Skills, MCP, runtime profiles, policies, guardrails, modes, and roles",
        icon: <Blocks className="size-4" />,
        keywords: ["skills", "mcp", "runtime profiles", "policies", "guardrails", "modes", "roles"],
        action: () => navigate("/resources/skills"),
      },
      {
        id: "nav.settings-soul",
        group: "Go to",
        label: "Settings: SOUL",
        icon: <Sparkles className="size-4" />,
        keywords: ["soul", "persona", "agent", "settings"],
        action: () => navigate("/settings/soul"),
      },
    ],
    [navigate],
  );

  const recentItems: PaletteItem[] = React.useMemo(
    () => recents.slice(0, 5).map((recent) => {
      const label = recent.kind === "run" ? runLabels?.get(recent.path) ?? recent.label : recent.label;
      return {
        id: `recent.${recent.path}`,
        group: "Recent",
        label,
        hint: recent.path,
        icon: recentIcon(recent.kind),
        keywords: [recent.kind, recent.path, recent.label],
        action: () => navigate(recent.path),
      };
    }),
    [navigate, recents, runLabels],
  );

  const actions: PaletteItem[] = React.useMemo(
    () => [
      {
        id: "action.theme",
        group: "Actions",
        label: "Toggle theme",
        icon: theme === "dark" ? (
          <Sun className="size-4" />
        ) : (
          <Moon className="size-4" />
        ),
        shortcut: ["⌘", "⇧", "L"],
        keywords: ["dark", "light", "mode"],
        action: () => {
          toggleTheme();
        },
      },
      {
        id: "action.settings",
        group: "Actions",
        label: "Open Settings",
        icon: <SettingsIcon className="size-4" />,
        shortcut: ["⌘", ","],
        action: () => navigate("/settings"),
      },
      {
        id: "action.logout",
        group: "Actions",
        label: "Sign out",
        icon: <LogOut className="size-4" />,
        action: () => void logout(),
      },
    ],
    [navigate, logout, theme],
  );

  const all = React.useMemo(
    () => [...recentItems, ...extraItems, ...navItems, ...actions].filter((i) => !isActiveRoute(i, location.pathname)),
    [recentItems, extraItems, navItems, actions, location.pathname],
  );

  const runAndClose = (item: PaletteItem) => {
    onOpenChange(false);
    // Defer so close animation starts first
    requestAnimationFrame(() => void item.action());
  };

  return (
    <AnimatePresence>
      {open && (
        <>
          <motion.div
            className="fixed inset-0 z-50 bg-[oklch(0_0_0_/_0.4)] backdrop-blur-[2px]"
            onClick={() => onOpenChange(false)}
            {...fade}
          />
          <motion.div
            className={cn(
              "fixed z-[60] left-1/2 top-[max(env(safe-area-inset-top),12px)] md:top-[18vh] -translate-x-1/2",
              "w-[min(640px,92vw)]",
            )}
            {...paletteMotion}
          >
            <Command
              label="Command palette"
              className={cn(
                "surface-elevated overflow-hidden",
                "ring-1 ring-inset ring-border",
              )}
              shouldFilter
            >
              <div className="flex items-center gap-2.5 px-4 h-[52px] border-b border-border/60">
                <Search className="size-4 text-muted-foreground/80" />
                <Command.Input
                  autoFocus
                  placeholder="Search or run a command…"
                  className={cn(
                    "flex-1 bg-transparent outline-none border-none",
                    "text-[14px] placeholder:text-muted-foreground/60",
                    "tracking-tight",
                  )}
                />
                <Kbd>esc</Kbd>
              </div>
              <Command.List
                className={cn(
                  "max-h-[min(420px,60vh)] overflow-y-auto",
                  "px-1.5 py-1.5",
                )}
              >
                <Command.Empty className="px-4 py-10 text-[12.5px] text-muted-foreground">
                  <div className="flex flex-col items-center gap-2 text-center">
                    <Search className="size-5 opacity-25" aria-hidden />
                    <p>No results — try “runs”, “settings”, or a project name.</p>
                  </div>
                </Command.Empty>

                {groupBy(all).map(([group, items]) => (
                  <Command.Group
                    key={group}
                    heading={group}
                    className={cn(
                      "[&_[cmdk-group-heading]]:px-2.5",
                      "[&_[cmdk-group-heading]]:py-1.5",
                      "[&_[cmdk-group-heading]]:text-[10.5px]",
                      "[&_[cmdk-group-heading]]:uppercase",
                      "[&_[cmdk-group-heading]]:tracking-[0.08em]",
                      "[&_[cmdk-group-heading]]:text-muted-foreground/70",
                      "[&_[cmdk-group-heading]]:font-medium",
                    )}
                  >
                    {items.map((item) => (
                      <Command.Item
                        key={item.id}
                        value={[item.label, ...(item.keywords ?? [])].join(" ")}
                        onSelect={() => runAndClose(item)}
                        className={cn(
                          "group flex items-center gap-2.5",
                          "px-2.5 h-[34px] rounded-[6px]",
                          "text-[13px] text-foreground/90",
                          "cursor-default select-none",
                          "transition-colors duration-[var(--dur-fast)]",
                          "data-[selected=true]:bg-[color:var(--color-primary)]/12",
                          "data-[selected=true]:text-foreground",
                          "data-[selected=true]:ring-1",
                          "data-[selected=true]:ring-inset",
                          "data-[selected=true]:ring-[color:var(--color-primary)]/20",
                        )}
                      >
                        <span className="text-muted-foreground group-data-[selected=true]:text-[color:var(--color-primary)]">
                          {item.icon}
                        </span>
                        <span className="flex-1 truncate">{item.label}</span>
                        {item.hint && (
                          <span className="text-[11px] text-muted-foreground/70 truncate">
                            {item.hint}
                          </span>
                        )}
                        {item.shortcut && (
                          <span className="flex items-center gap-0.5">
                            {item.shortcut.map((k, i) => (
                              <Kbd key={i}>{k}</Kbd>
                            ))}
                          </span>
                        )}
                      </Command.Item>
                    ))}
                  </Command.Group>
                ))}
              </Command.List>
            </Command>
          </motion.div>
        </>
      )}
    </AnimatePresence>
  );
}

function recentIcon(kind: Recent["kind"]): React.ReactNode {
  switch (kind) {
    case "run":
      return <Radio className="size-4" />;
    case "project":
      return <FolderKanban className="size-4" />;
    case "repo":
      return <GitBranch className="size-4" />;
    case "cron":
      return <Clock3 className="size-4" />;
    default:
      return <Layers className="size-4" />;
  }
}

function groupBy(items: PaletteItem[]): [string, PaletteItem[]][] {
  const map = new Map<string, PaletteItem[]>();
  for (const i of items) {
    const list = map.get(i.group) ?? [];
    list.push(i);
    map.set(i.group, list);
  }
  return [...map.entries()];
}

function isActiveRoute(item: PaletteItem, pathname: string): boolean {
  // Hide the "go to" entry for the page you're already on.
  if (!item.id.startsWith("nav.")) return false;
  const map: Record<string, string> = {
    "nav.newchat": "/",
    "nav.projects": "/projects",
    "nav.agent-ops": "/runs",
    "nav.observability": "/observability",
    "nav.shared": "/shared",
    "nav.settings-connection": "/settings/connection",
    "nav.settings-credentials": "/settings/credentials",
    "nav.resources": "/resources/skills",
    "nav.settings-soul": "/settings/soul",
  };
  const expected = map[item.id];
  return expected === pathname;
}
