import * as React from "react";
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
  "/security": { label: "Security", to: "/security" },
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
  { prefix: "/security/", root: ROUTE_LABELS["/security"] },
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

  // Security is the only section with three levels (page → scan → finding), so
  // it gets an explicit trail instead of the generic "root / last segment"
  // fallback, which rendered a bare finding UUID with no way back to its scan.
  if (path.startsWith("/security/")) {
    return <SecurityCrumbs path={path} />;
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

/**
 * Security trail: page → scan run → finding, so a deep-linked finding always
 * shows (and can click back to) the scan and the section it belongs to.
 * Filters live in the query string, so the "up" links carry it along.
 */
function SecurityCrumbs({ path }: { path: string }) {
  const location = useLocation();
  const segments = path.split("/").filter(Boolean).slice(1); // drop "security"
  const [first, second, ...rest] = segments;

  const SECTION_LABELS: Record<string, string> = {
    runs: "Scan runs",
    configs: "Configurations",
    library: "Library",
  };

  const wrap = (children: React.ReactNode) => (
    <span className="inline-flex items-center gap-1.5 text-muted-foreground">
      <Crumb label="Security" to="/security" />
      {children}
    </span>
  );

  if (first in SECTION_LABELS) {
    // /security/configs/:namespace/:name → Security / Configurations / name
    const name = first === "configs" ? rest[0] : undefined;
    return wrap(
      <>
        <Sep />
        <Crumb label={SECTION_LABELS[first]} to={`/security/${first}`} active={!name} />
        {name && (
          <>
            <Sep />
            <Crumb label={name} active mono />
          </>
        )}
      </>,
    );
  }

  // /security/:namespace/:runName[/findings/:id]
  if (!first || !second) return wrap(null);
  const scanPath = `/security/${first}/${second}${location.search}`;
  const isFinding = rest[0] === "findings" && Boolean(rest[1]);
  return wrap(
    <>
      <Sep />
      <Crumb label="Scan runs" to="/security/runs" />
      <Sep />
      <Crumb label={second} to={isFinding ? scanPath : undefined} active={!isFinding} mono />
      {isFinding && (
        <>
          <Sep />
          <Crumb label="Finding" active />
        </>
      )}
    </>,
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
