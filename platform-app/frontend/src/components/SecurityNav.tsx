import { Link, useLocation } from "react-router-dom";
import { Gauge, History, Inbox, LibraryBig, Settings2 } from "lucide-react";

import { cn } from "@/lib/utils";

type SecurityPage = {
  to: string;
  /** Path prefix that marks this page (and its detail pages) as current. */
  match: (pathname: string) => boolean;
  label: string;
  icon: React.ReactNode;
};

const SECURITY_PAGES: SecurityPage[] = [
  {
    to: "/security",
    match: (p) => p === "/security",
    label: "Overview",
    icon: <Gauge />,
  },
  {
    // Scan detail lives at /security/:namespace/:runName, so "Scan runs" must
    // stay lit while the user drills into a run or one of its findings —
    // otherwise the whole nav goes dark two clicks into the section.
    to: "/security/runs",
    match: (p) =>
      p.startsWith("/security/runs")
      || (/^\/security\/[^/]+\/[^/]+/.test(p)
        && !p.startsWith("/security/configs")
        && !p.startsWith("/security/library")
        && !p.startsWith("/security/queue")),
    label: "Scan runs",
    icon: <History />,
  },
  {
    to: "/security/queue",
    match: (p) => p.startsWith("/security/queue"),
    label: "Submission queue",
    icon: <Inbox />,
  },
  {
    to: "/security/configs",
    match: (p) => p.startsWith("/security/configs"),
    label: "Configurations",
    icon: <Settings2 />,
  },
  {
    to: "/security/library",
    match: (p) => p.startsWith("/security/library"),
    label: "Library",
    icon: <LibraryBig />,
  },
];

/**
 * SecurityNav is the shared sub-navigation rendered on every security page so
 * the whole tab reads as one surface. Segmented links, never a dropdown: every
 * page stays one click away, and detail pages keep their parent tab lit.
 *
 * Counts are optional: pages that already know how many rows they hold pass
 * them in so the nav doubles as a cheap inventory.
 */
export function SecurityNav({ counts }: { counts?: Partial<Record<string, number>> } = {}) {
  const location = useLocation();
  return (
    <nav aria-label="Security pages">
      <ul className="flex gap-1 overflow-x-auto rounded-lg border border-border/70 bg-muted/30 p-1 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {SECURITY_PAGES.map((page) => {
          const current = page.match(location.pathname);
          const count = counts?.[page.to];
          return (
            <li key={page.to} className="shrink-0">
              <Link
                to={page.to}
                aria-current={current ? "page" : undefined}
                className={cn(
                  "flex items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-[12.5px] transition-colors duration-[var(--dur-fast)] [&_svg]:size-3.5 [&_svg]:shrink-0",
                  current
                    ? "bg-background font-medium text-foreground shadow-sm ring-1 ring-border/70"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {page.icon}
                {page.label}
                {count !== undefined && (
                  <span className="rounded-full bg-muted px-1.5 text-[10.5px] tabular-nums text-muted-foreground">
                    {count}
                  </span>
                )}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
