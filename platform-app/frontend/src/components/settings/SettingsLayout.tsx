import * as React from "react";
import { NavLink, Outlet } from "react-router-dom";
import {
  Bot,
  Cable,
  ChartNoAxesCombined,
  DownloadCloud,
  GitCommitHorizontal,
  KeyRound,
  ScrollText,
  Search,
  Server,
  SlidersHorizontal,
  Sparkles,
  UserRound,
} from "lucide-react";

import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Input } from "@/components/ui/input";
import { useAuth } from "@/contexts/AuthContext";
import { isTauri } from "@/lib/platform";
import { cn } from "@/lib/utils";

interface SettingsNavItem {
  to: string;
  /** Exact-match highlighting for the index route. */
  end?: boolean;
  label: string;
  icon: React.ReactNode;
  /** Extra search terms beyond the label (lowercase). */
  keywords: string;
}

interface SettingsNavGroup {
  heading?: string;
  items: SettingsNavItem[];
}

/**
 * /settings layout: persistent sidebar (identity, search, section nav) with
 * the active section rendered in the right pane via nested routes. Modeled
 * on desktop-app settings (one surface, every section a click away) instead
 * of a hub page with per-section round trips.
 *
 * Policy reminder: settings is for per-user preferences and personal
 * resources only. New resource-management surfaces get their own top-level
 * routes; the Resources group below merely links out to /resources.
 */
export default function SettingsLayout() {
  const [query, setQuery] = React.useState("");

  const groups: SettingsNavGroup[] = [
    {
      items: [
        {
          to: "/settings",
          end: true,
          label: "General",
          icon: <SlidersHorizontal />,
          keywords: "appearance theme dark light account sign out logout diagnostics logs bug report",
        },
        ...(isTauri
          ? [
              {
                to: "/settings/connection",
                label: "Connection",
                icon: <Server />,
                keywords: "backend endpoint server cloudflare access workspace device",
              },
              {
                to: "/settings/updates",
                label: "Desktop updates",
                icon: <DownloadCloud />,
                keywords: "version upgrade github token check release",
              },
            ]
          : []),
      ],
    },
    {
      heading: "Personal",
      items: [
        {
          to: "/settings/credentials",
          label: "Credentials",
          icon: <KeyRound />,
          keywords: "api key oauth token provider anthropic openai sign in",
        },
        {
          to: "/settings/usage",
          label: "Usage",
          icon: <ChartNoAxesCombined />,
          keywords: "openai chatgpt oauth tokens quota limits cost spend monthly",
        },
        {
          to: "/settings/soul",
          label: "SOUL",
          icon: <Sparkles />,
          keywords: "persona agent personality perspective teammate",
        },
        {
          to: "/settings/role-models",
          label: "Role models",
          icon: <Bot />,
          keywords: "model override provider role planner executor reviewer",
        },
        {
          to: "/settings/git",
          label: "Git identity",
          icon: <GitCommitHorizontal />,
          keywords: "commit author name email",
        },
      ],
    },
    {
      heading: "Resources",
      items: [
        {
          to: "/resources/skills",
          label: "Skills",
          icon: <ScrollText />,
          keywords: "instructions skills.sh install agent",
        },
        {
          to: "/resources/mcp-servers",
          label: "MCP servers",
          icon: <Cable />,
          keywords: "mcp tools integrations servers",
        },
      ],
    },
  ];

  const q = query.trim().toLowerCase();
  const filtered = groups
    .map((group) => ({
      ...group,
      items: q
        ? group.items.filter(
            (item) =>
              item.label.toLowerCase().includes(q) || item.keywords.includes(q),
          )
        : group.items,
    }))
    .filter((group) => group.items.length > 0);

  return (
    <div className="mx-auto flex max-w-[1000px] flex-col gap-5 pb-10 md:flex-row md:gap-10">
      <aside
        aria-label="Settings sections"
        className="shrink-0 md:sticky md:top-0 md:w-56 md:self-start md:pt-1"
      >
        <SettingsIdentity />

        <div className="relative mt-3">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search settings…"
            aria-label="Search settings"
            className="h-8 pl-8 text-[13px]"
          />
        </div>

        <nav className="mt-3 space-y-4">
          {filtered.length === 0 && (
            <p className="px-2.5 py-1.5 text-[12px] text-muted-foreground">
              No settings match “{query.trim()}”.
            </p>
          )}
          {filtered.map((group, index) => (
            <div key={group.heading ?? index}>
              {group.heading && (
                <div className="px-2.5 pb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/70">
                  {group.heading}
                </div>
              )}
              <ul className="flex gap-1 overflow-x-auto md:flex-col md:overflow-visible [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
                {group.items.map((item) => (
                  <li key={item.to} className="shrink-0 md:shrink">
                    <NavLink
                      to={item.to}
                      end={item.end}
                      className={({ isActive }) =>
                        cn(
                          "flex items-center gap-2.5 whitespace-nowrap rounded-md px-2.5 py-1.5 text-[13px] transition-colors duration-[var(--dur-fast)] [&_svg]:size-4 [&_svg]:shrink-0",
                          isActive
                            ? "bg-muted font-medium text-foreground [&_svg]:text-foreground"
                            : "text-muted-foreground hover:bg-muted/60 hover:text-foreground",
                        )
                      }
                    >
                      {item.icon}
                      {item.label}
                    </NavLink>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </nav>
      </aside>

      <div className="min-w-0 flex-1">
        {/* Local boundary: lazy sections load without unmounting the sidebar. */}
        <React.Suspense fallback={null}>
          <Outlet />
        </React.Suspense>
      </div>
    </div>
  );
}

/** Who these settings belong to — mirrors the desktop-app identity header. */
function SettingsIdentity() {
  const { user } = useAuth();
  const displayName = user?.name || user?.username || "";
  return (
    <div className="flex items-center gap-2.5 px-0.5">
      <Avatar className="size-9">
        {user?.picture && <AvatarImage src={user.picture} alt="" />}
        <AvatarFallback>
          {displayName ? displayName.slice(0, 2).toUpperCase() : <UserRound />}
        </AvatarFallback>
      </Avatar>
      <div className="min-w-0">
        <div className="truncate text-[13px] font-medium tracking-tight">
          {displayName || "Settings"}
        </div>
        <div className="truncate text-[11.5px] text-muted-foreground">
          {user?.username ? `@${user.username}` : user?.email || "offline"}
        </div>
      </div>
    </div>
  );
}
