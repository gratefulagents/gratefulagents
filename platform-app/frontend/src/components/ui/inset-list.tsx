import * as React from "react";
import { Link } from "react-router-dom";
import { ChevronRight } from "lucide-react";

import { cn } from "@/lib/utils";

/**
 * macOS "inset grouped" list primitives (System Settings, Finder, Mail
 * preferences): a bold section title, a rounded hairline card, rows with a
 * colored icon tile and separators inset to the text column, and an optional
 * footnote below. Rows use the default arrow cursor and a pressed state rather
 * than web-style hover lifts.
 */

export function InsetSection({
  title,
  accessory,
  action,
  footer,
  label,
  className,
  children,
}: {
  title?: React.ReactNode;
  /** Secondary text beside the title (e.g. "2/4"). */
  accessory?: React.ReactNode;
  /** Right-aligned controls in the header row. */
  action?: React.ReactNode;
  footer?: React.ReactNode;
  /** Accessible name for the section landmark. */
  label?: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <section aria-label={label} className={cn("flex min-w-0 flex-col", className)}>
      {(title || action) && (
        <div className="mb-1.5 flex min-h-6 items-center justify-between gap-3 px-2.5">
          <div className="flex min-w-0 items-baseline gap-2">
            {title && (
              <h2 className="truncate text-[13px] font-semibold tracking-[-0.003em] text-foreground/90">
                {title}
              </h2>
            )}
            {accessory && (
              <span className="shrink-0 text-[12px] tabular-nums text-muted-foreground">
                {accessory}
              </span>
            )}
          </div>
          {action && <div className="flex shrink-0 items-center gap-1">{action}</div>}
        </div>
      )}
      <div
        className={cn(
          "overflow-hidden rounded-[10px] bg-card",
          "ring-1 ring-foreground/[0.08] shadow-[0_0.5px_1.5px_oklch(0_0_0_/_0.18)]",
        )}
      >
        {children}
      </div>
      {footer && (
        <div className="mt-1.5 px-2.5 text-[11.5px] leading-snug text-muted-foreground">{footer}</div>
      )}
    </section>
  );
}

const rowClass = cn(
  "relative flex min-h-[44px] cursor-default select-none items-center gap-2.5 py-1.5 pl-2.5 pr-3 outline-none",
  // Hairline separator inset to the text column (icon tile 26px + gap 10px + padding 10px).
  "after:pointer-events-none after:absolute after:bottom-0 after:left-[46px] after:right-0 after:h-px after:bg-foreground/[0.07] last:after:hidden",
  "focus-visible:bg-foreground/[0.05]",
);

const pressableClass = "active:bg-foreground/[0.07] [@media(hover:hover)]:hover:bg-foreground/[0.03]";

export interface InsetRowProps {
  icon?: React.ReactNode;
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  /** Right-side content before the chevron (secondary value, buttons). */
  trailing?: React.ReactNode;
  /** Navigates on click and shows a disclosure chevron. */
  to?: string;
  dimmed?: boolean;
  className?: string;
}

export function InsetRow({ icon, title, subtitle, trailing, to, dimmed, className }: InsetRowProps) {
  const body = (
    <>
      {icon}
      <span className={cn("flex min-w-0 flex-1 flex-col", dimmed && "opacity-60")}>
        <span className="truncate text-[13px] leading-[1.3] tracking-[-0.003em]">{title}</span>
        {subtitle && (
          <span className="truncate text-[11.5px] leading-[1.3] text-muted-foreground">{subtitle}</span>
        )}
      </span>
      {trailing && <span className="flex shrink-0 items-center gap-1.5">{trailing}</span>}
      {to && <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/45" strokeWidth={2.25} />}
    </>
  );
  if (to) {
    return (
      <Link to={to} className={cn(rowClass, pressableClass, className)}>
        {body}
      </Link>
    );
  }
  return <div className={cn(rowClass, className)}>{body}</div>;
}

/**
 * System Settings-style icon tile: a small rounded square filled with a
 * gradient of `color` and a white glyph.
 */
export function IconTile({ color, children }: { color: string; children: React.ReactNode }) {
  return (
    <span
      aria-hidden
      className="grid size-[26px] shrink-0 place-items-center rounded-[7px] text-white shadow-[inset_0_0_0_0.5px_oklch(0_0_0_/_0.12),0_0.5px_1px_oklch(0_0_0_/_0.2)] [&_svg]:size-[14px] [&_svg]:stroke-[2.25]"
      style={{
        background: `linear-gradient(180deg, color-mix(in oklch, ${color} 82%, white), ${color})`,
      }}
    >
      {children}
    </span>
  );
}
