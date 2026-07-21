import * as React from "react";

/**
 * Shared chrome for /settings/* sub-pages: the page header. Sub-pages render
 * inside SettingsLayout (which owns navigation) and are lazy routes, so each
 * section's data is fetched only when its route mounts.
 */
export function SettingsSubPage({
  title,
  description,
  children,
}: {
  title: string;
  description?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="max-w-[760px] space-y-5">
      <header className="pt-1">
        <h1>{title}</h1>
        {description && <p className="text-[12.5px] text-muted-foreground">{description}</p>}
      </header>
      {children}
    </div>
  );
}
