import { Link, Navigate, useParams } from "react-router-dom";

import { cn } from "@/lib/utils";
import { useScrollEdgeFade } from "@/hooks/useScrollEdgeFade";
import { resourceTabs, type ResourceKind } from "@/components/resources/resource-helpers";
import { SkillsSection } from "@/components/resources/SkillsSection";
import { MCPServersSection } from "@/components/resources/MCPServersSection";
import { RuntimeProfilesSection } from "@/components/resources/RuntimeProfilesSection";
import { GuardrailsSection } from "@/components/resources/GuardrailsSection";
import { ModesSection } from "@/components/resources/ModesSection";
import { RolesSection } from "@/components/resources/RolesSection";

const sections: Record<ResourceKind, () => React.JSX.Element> = {
  skills: SkillsSection,
  "mcp-servers": MCPServersSection,
  "runtime-profiles": RuntimeProfilesSection,
  guardrails: GuardrailsSection,
  modes: ModesSection,
  roles: RolesSection,
};

/**
 * /resources/:kind — one page, six tabs. The header and tab strip are the
 * only shared chrome; each tab owns its list, dialogs, and data.
 */
export function ResourcePage() {
  const rawKind = useParams().kind;
  // Resource tabs overflow on phones, so fade the edges to reveal more tabs.
  const [tabsRef, tabsFadeStyle] = useScrollEdgeFade<HTMLElement>();
  if (!resourceTabs.some(([id]) => id === rawKind)) {
    return <Navigate to="/resources/skills" replace />;
  }
  const kind = rawKind as ResourceKind;
  const Section = sections[kind];
  return (
    <div className="mx-auto max-w-[1080px] space-y-5">
      <header className="space-y-0.5">
        <h1>Resources</h1>
        <p className="text-[13px] text-muted-foreground">Reusable building blocks for projects, triggers, and runs.</p>
      </header>
      <nav
        aria-label="Resource types"
        ref={tabsRef}
        style={tabsFadeStyle}
        className="flex gap-5 overflow-x-auto border-b border-border/60"
      >
        {resourceTabs.map(([id, label]) => {
          const active = kind === id;
          return (
            <Link
              key={id}
              to={`/resources/${id}`}
              aria-current={active ? "page" : undefined}
              className={cn(
                "relative shrink-0 whitespace-nowrap px-0.5 py-2 text-[13px] font-medium transition-colors",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 rounded-sm",
                "after:absolute after:inset-x-0 after:-bottom-px after:h-0.5 after:rounded-full after:bg-foreground after:opacity-0 after:transition-opacity",
                active ? "text-foreground after:opacity-100" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {label}
            </Link>
          );
        })}
      </nav>
      <Section key={kind} />
    </div>
  );
}
