import { NavLink } from "react-router-dom";
import { Gauge, History, LibraryBig, Settings2 } from "lucide-react";

import { cn } from "@/lib/utils";

const SECURITY_PAGES = [
  { to: "/security", end: true, label: "Overview", icon: <Gauge /> },
  { to: "/security/runs", end: false, label: "Scan runs", icon: <History /> },
  { to: "/security/configs", end: false, label: "Configurations", icon: <Settings2 /> },
  { to: "/security/library", end: false, label: "Library", icon: <LibraryBig /> },
] as const;

/**
 * SecurityNav is the shared sub-navigation rendered on every top-level
 * security page (overview, scan runs, configurations, library) so the whole
 * tab reads as one surface. Segmented links, never a dropdown: every page
 * stays one click away.
 */
export function SecurityNav() {
  return (
    <nav aria-label="Security pages">
      <ul className="flex gap-1 overflow-x-auto rounded-lg border border-border/70 bg-muted/30 p-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {SECURITY_PAGES.map((page) => (
          <li key={page.to} className="shrink-0">
            <NavLink
              to={page.to}
              end={page.end}
              className={({ isActive }) =>
                cn(
                  "flex items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-[12.5px] transition-colors duration-[var(--dur-fast)] [&_svg]:size-3.5 [&_svg]:shrink-0",
                  isActive
                    ? "bg-background font-medium text-foreground shadow-sm ring-1 ring-border/70"
                    : "text-muted-foreground hover:text-foreground",
                )
              }
            >
              {page.icon}
              {page.label}
            </NavLink>
          </li>
        ))}
      </ul>
    </nav>
  );
}
