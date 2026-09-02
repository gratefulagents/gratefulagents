import { type ReactNode, useId } from "react";
import { motion } from "framer-motion";

import { moveTabFocus } from "@/components/run-session/RunInspector";
import { transitions } from "@/lib/motion";
import { cn } from "@/lib/utils";

/**
 * Inspector panes are narrow and tall. When a pane holds two genuinely
 * different things (a timeline and a cost ledger; a checks list and a review
 * queue) stacking them in one scroller means neither gets height and the user
 * scrolls past the thing they came for. InspectorSubnav is the one-level-down
 * switch every pane uses for that, so the panes stay visually consistent with
 * each other and with the inspector's own tab strip.
 */
export type SubnavItem<T extends string> = {
  id: T;
  label: string;
  /** Small trailing count. Zero and undefined hide it. */
  count?: number;
  /** Renders the count in the danger tone (failing checks, unresolved threads). */
  alert?: boolean;
};

export function InspectorSubnav<T extends string>({
  items,
  value,
  onChange,
  trailing,
  className,
  panelId,
}: {
  items: SubnavItem<T>[];
  value: T;
  onChange: (value: T) => void;
  /** Right-aligned controls (refresh, filters) that belong to the whole pane. */
  trailing?: ReactNode;
  className?: string;
  /** `id` of the `role="tabpanel"` the pane renders below the strip, if it has one. */
  panelId?: string;
}) {
  const baseId = useId();
  const ids = items.map((item) => item.id);
  return (
    <div className={cn("flex h-9 shrink-0 items-center gap-2 border-b px-2", className)}>
      <div
        role="tablist"
        aria-label="Pane sections"
        onKeyDown={(event) => moveTabFocus(event, ids, value, onChange)}
        className={cn(
          "flex min-w-0 flex-1 items-center gap-0.5 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden",
          "[mask-image:linear-gradient(to_right,black_92%,transparent)]",
        )}
      >
        {items.map(({ id, label, count, alert }) => {
          const active = id === value;
          return (
            <button
              key={id}
              type="button"
              role="tab"
              id={`${baseId}-tab-${id}`}
              aria-selected={active}
              aria-controls={panelId}
              tabIndex={active ? 0 : -1}
              onClick={() => onChange(id)}
              className={cn(
                "relative isolate flex shrink-0 items-center gap-1.5 rounded-md px-2 py-1 text-xs whitespace-nowrap transition-colors",
                "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
                active ? "font-medium text-foreground" : "text-muted-foreground hover:text-foreground",
              )}
            >
              {active && (
                <motion.span
                  layoutId={`${baseId}-indicator`}
                  transition={transitions.subtle}
                  className="absolute inset-0 -z-10 rounded-md bg-muted"
                />
              )}
              {label}
              {count ? (
                <span
                  className={cn(
                    "rounded-full px-1.5 text-[10px] tabular-nums",
                    alert
                      ? "bg-[color:var(--tone-danger)]/12 text-[color:var(--tone-danger)]"
                      : "bg-muted-foreground/12 text-muted-foreground",
                  )}
                >
                  {count > 99 ? "99+" : count}
                </span>
              ) : null}
            </button>
          );
        })}
      </div>
      {trailing}
    </div>
  );
}
