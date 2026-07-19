import { Link, useLocation, useParams } from "react-router-dom";
import { ChevronRight } from "lucide-react";

import { cn } from "@/lib/utils";

const ROUTE_LABELS: Record<string, { label: string; to: string }> = {
  "/": { label: "Projects", to: "/projects" },
  "/projects": { label: "Projects", to: "/projects" },
  "/runs": { label: "Agent Ops", to: "/runs" },
  "/observability": { label: "Observability", to: "/observability" },
  "/linear": { label: "Linear", to: "/linear" },
  "/github": { label: "GitHub", to: "/github" },
  "/cron": { label: "Cron", to: "/cron" },
  "/shared": { label: "Shared", to: "/shared" },
  "/settings": { label: "Settings", to: "/settings" },
  "/resources": { label: "Resources", to: "/resources/skills" },
};

const SETTINGS_SECTIONS: Record<string, string> = {
  "/settings/connection": "Connection",
  "/settings/credentials": "Credentials",
  "/settings/skills": "Skill packages",
  "/settings/soul": "SOUL",
};

const DETAIL_PREFIX: Array<{
  prefix: string;
  root: { label: string; to: string };
}> = [
  { prefix: "/projects/", root: ROUTE_LABELS["/projects"] },
  { prefix: "/linear/", root: ROUTE_LABELS["/linear"] },
  { prefix: "/github/", root: ROUTE_LABELS["/github"] },
  { prefix: "/cron/", root: ROUTE_LABELS["/cron"] },
  { prefix: "/runs/", root: ROUTE_LABELS["/runs"] },
];

/**
 * Lightweight breadcrumbs rendered into TitleBar's `trail` slot. Uses
 * route pattern matching (not the router's matched routes) because routes
 * here are simple and predictable.
 */
export function Breadcrumbs() {
  const location = useLocation();
  const params = useParams();

  const path = location.pathname;

  // Root-only or unknown → no crumbs (TitleBar already shows "gratefulagents").
  const exact = ROUTE_LABELS[path];
  if (exact && path !== "/") {
    return (
      <span className="inline-flex items-center gap-1.5 text-muted-foreground">
        <Crumb label={exact.label} active />
      </span>
    );
  }

  if (path.startsWith("/resources/")) {
    const labels: Record<string, string> = { skills: "Skills", "mcp-servers": "MCP servers", "runtime-profiles": "Runtime profiles", "mcp-policies": "MCP policies", guardrails: "Guardrails", modes: "Modes", roles: "Roles" };
    return <span className="inline-flex items-center gap-1.5 text-muted-foreground"><Crumb label="Resources" to="/resources/skills" /><Sep /><Crumb label={labels[path.split("/").pop() ?? ""] ?? "Resources"} active /></span>;
  }

  const settingsSection = SETTINGS_SECTIONS[path];
  if (settingsSection) {
    return (
      <span className="inline-flex items-center gap-1.5 text-muted-foreground">
        <Crumb label="Settings" to="/settings" />
        <Sep />
        <Crumb label={settingsSection} active />
      </span>
    );
  }

  const match = DETAIL_PREFIX.find((d) => path.startsWith(d.prefix));
  if (!match) return null;

  const name = params.name ?? path.split("/").pop() ?? "";
  return (
    <span className="inline-flex items-center gap-1.5 text-muted-foreground">
      <Crumb label={match.root.label} to={match.root.to} />
      <Sep />
      <Crumb label={name} active mono />
    </span>
  );
}

function Sep() {
  return (
    <ChevronRight
      aria-hidden
      className="size-3 text-border shrink-0"
    />
  );
}

function Crumb({
  label,
  to,
  active,
  mono,
}: {
  label: string;
  to?: string;
  active?: boolean;
  mono?: boolean;
}) {
  const cls = cn(
    "max-w-[28ch] truncate",
    mono && "font-mono text-[11.5px]",
    active ? "text-foreground" : "transition-colors duration-[var(--dur-fast)] hover:text-foreground",
  );
  if (to && !active) {
    return (
      <Link to={to} className={cls}>
        {label}
      </Link>
    );
  }
  return <span className={cls} aria-current={active ? "page" : undefined}>{label}</span>;
}
