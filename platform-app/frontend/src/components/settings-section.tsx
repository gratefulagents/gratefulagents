import * as React from "react";

import { cn } from "@/lib/utils";

/**
 * Shared card shell for settings sections: icon chip + title + description
 * header, then content. Keeps every section on the settings page (and the
 * standalone Credentials / SOUL sections) visually identical.
 */
export function SettingsSection({
  icon,
  title,
  description,
  aside,
  children,
  className,
}: {
  icon?: React.ReactNode;
  title: string;
  description?: React.ReactNode;
  aside?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("surface-card space-y-4 p-4 sm:p-5", className)}>
      {/* Stack the aside under the header on phones: a wide aside (e.g. the
          Skills install buttons) otherwise squeezes the title/description
          into a sliver and the two overlap. */}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex min-w-0 items-start gap-2.5">
          {icon && (
            <div className="grid size-7 shrink-0 place-items-center rounded-[7px] bg-muted/60 text-muted-foreground ring-1 ring-inset ring-border/60 [&_svg]:size-3.5">
              {icon}
            </div>
          )}
          <div className="min-w-0">
            <h2 className="text-[14px] font-semibold tracking-[-0.01em] leading-7">{title}</h2>
            {description && (
              <p className="mt-0.5 max-w-[62ch] text-[12px] leading-relaxed text-muted-foreground">
                {description}
              </p>
            )}
          </div>
        </div>
        {aside && <div className="shrink-0">{aside}</div>}
      </div>
      {children}
    </section>
  );
}
