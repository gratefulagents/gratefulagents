import * as React from "react";
import { Link, useLocation } from "react-router-dom";
import { PanelLeftClose, PanelLeftOpen } from "lucide-react";

import { cn } from "@/lib/utils";
import { isTauri } from "@/lib/platform";
import { monogramInitials, monogramStyle } from "@/lib/monogram";
import { useAuth } from "@/contexts/AuthContext";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useSidebar } from "@/components/ui/sidebar";
import { WorkspaceSwitcher } from "@/components/shell/WorkspaceSwitcher";

export type RailDestination = {
  to: string;
  label: string;
  icon: React.ComponentType<{ className?: string; strokeWidth?: number }>;
  /** Route matcher; defaults to exact pathname equality. */
  match?: (pathname: string) => boolean;
  /** Small dot drawn on the icon (e.g. runs needing attention). */
  attention?: { label: string };
};

export type RailGroup = { id: string; items: RailDestination[] };

type RailTooltipProps = React.ComponentProps<typeof TooltipTrigger> & { label: React.ReactNode };

/** Tooltip trigger whose element is supplied via `render` (Link or button). */
function RailTooltip({ label, children, ...trigger }: RailTooltipProps) {
  return (
    <Tooltip>
      <TooltipTrigger {...trigger}>{children}</TooltipTrigger>
      <TooltipContent side="right" sideOffset={10} className="text-[11.5px]">
        {label}
      </TooltipContent>
    </Tooltip>
  );
}

const railButtonClass = cn(
  "group/rail-btn relative grid size-[34px] place-items-center rounded-[9px]",
  "text-foreground/65 transition-colors duration-[var(--dur-fast)]",
  "hover:bg-sidebar-accent hover:text-foreground",
  "outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring",
);

function RailItem({ item, active }: { item: RailDestination; active: boolean }) {
  const Icon = item.icon;
  return (
    <RailTooltip
      label={item.label}
      render={<Link to={item.to} />}
      aria-label={item.label}
      aria-current={active ? "page" : undefined}
      className={cn(
        railButtonClass,
        active && "bg-[color:var(--color-primary)]/12 text-[color:var(--color-primary)] hover:bg-[color:var(--color-primary)]/16 hover:text-[color:var(--color-primary)]",
        // Active indicator: a pill on the rail's outer edge.
        "before:absolute before:-left-[5px] before:top-1/2 before:h-4 before:w-[3px] before:-translate-y-1/2",
        "before:rounded-full before:bg-[color:var(--color-primary)] before:opacity-0 before:transition-opacity",
        active && "before:opacity-100",
      )}
    >
      <Icon className="size-[17px]" strokeWidth={active ? 2 : 1.75} />
      {item.attention && (
        <span
          role="img"
          aria-label={item.attention.label}
          title={item.attention.label}
          className="absolute right-[7px] top-[7px] inline-flex size-[7px] rounded-full bg-[color:var(--tone-warning)] ring-2 ring-[color:var(--color-sidebar)]"
        >
          <span className="absolute inset-0 rounded-full bg-[color:var(--tone-warning)] opacity-60 motion-safe:animate-ping" />
        </span>
      )}
    </RailTooltip>
  );
}

/**
 * The always-visible 48px navigation rail: workspace mark, top-level
 * destinations grouped by hairlines, panel toggle, and the user avatar.
 * Nothing project-specific lives here, so it stays legible at any density.
 */
export function NavRail({ groups }: { groups: RailGroup[] }) {
  const location = useLocation();
  const { user } = useAuth();
  const { open, toggleSidebar, isMobile } = useSidebar();
  const isSettings = location.pathname.startsWith("/settings");
  const displayName = user?.name || user?.username || "";

  return (
    <nav
      aria-label="Primary"
      className={cn(
        "flex h-full w-12 shrink-0 flex-col items-center gap-1 px-[7px] pb-[max(env(safe-area-inset-bottom),0.5rem)]",
        "border-r border-sidebar-border/70",
      )}
    >
      {/* Workspace mark */}
      <div className="mb-1 flex h-[42px] items-center">
        {isTauri ? (
          <WorkspaceSwitcher compact />
        ) : (
          <RailTooltip
            label="Grateful Agents"
            render={<Link to="/" />}
            aria-label="Grateful Agents home"
            className="grid size-[34px] place-items-center rounded-[10px] outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring"
          >
            <img src="/logo.png" alt="" className="size-[26px] rounded-[7px]" />
          </RailTooltip>
        )}
      </div>

      {groups.map((group, index) => (
        <React.Fragment key={group.id}>
          {index > 0 && <span aria-hidden className="my-1 h-px w-5 bg-border" />}
          {group.items.map((item) => {
            const active = item.match ? item.match(location.pathname) : location.pathname === item.to;
            return <RailItem key={item.to} item={item} active={active} />;
          })}
        </React.Fragment>
      ))}

      <div className="mt-auto flex flex-col items-center gap-1">
        {!isMobile && (
          <RailTooltip
            label={open ? "Hide projects panel" : "Show projects panel"}
            onClick={toggleSidebar}
            aria-label={open ? "Hide projects panel" : "Show projects panel"}
            aria-pressed={open}
            className={cn(railButtonClass, "size-[30px] rounded-[8px] text-muted-foreground/80")}
          >
            {open ? (
              <PanelLeftClose className="size-[15px]" strokeWidth={1.75} />
            ) : (
              <PanelLeftOpen className="size-[15px]" strokeWidth={1.75} />
            )}
          </RailTooltip>
        )}
        <RailTooltip
          label={
            <span className="flex flex-col">
              <span className="font-medium">{displayName || "Settings"}</span>
              {user?.email && <span className="font-mono text-[10.5px] text-muted-foreground">{user.email}</span>}
            </span>
          }
          render={<Link to="/settings" />}
          aria-label="Settings"
          aria-current={isSettings ? "page" : undefined}
          className={cn(
            "relative mt-0.5 grid size-[34px] place-items-center rounded-full",
            "outline-none focus-visible:ring-2 focus-visible:ring-sidebar-ring",
            "before:absolute before:-left-[5px] before:top-1/2 before:h-4 before:w-[3px] before:-translate-y-1/2",
            "before:rounded-full before:bg-[color:var(--color-primary)] before:opacity-0 before:transition-opacity",
            isSettings && "before:opacity-100",
          )}
        >
          <span
            className={cn(
              "grid size-[28px] place-items-center overflow-hidden rounded-full",
              "bg-[var(--mono-bg)] text-[11px] font-semibold text-[var(--mono)]",
              "ring-1 ring-inset ring-[var(--mono)]/30 transition-[box-shadow] duration-[var(--dur-fast)]",
              "hover:ring-[var(--mono)]/70",
              isSettings && "ring-2 ring-[color:var(--color-primary)]",
            )}
            style={monogramStyle(displayName || "user")}
          >
            {user?.picture ? (
              <img src={user.picture} alt="" className="size-full object-cover" />
            ) : (
              <span aria-hidden>{displayName ? monogramInitials(displayName).slice(0, 1) : "?"}</span>
            )}
          </span>
        </RailTooltip>
      </div>
    </nav>
  );
}
