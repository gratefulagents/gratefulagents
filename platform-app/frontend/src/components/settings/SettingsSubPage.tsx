import * as React from "react";
import { Link } from "react-router-dom";
import { ArrowLeft } from "lucide-react";

/**
 * Shared chrome for /settings/* sub-pages: back link to the settings hub plus
 * the page header. Sub-pages are lazy routes, so each section's data is
 * fetched only when its route mounts.
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
    <div className="mx-auto max-w-[760px] space-y-5 pb-10">
      <header className="pt-1">
        <Link
          to="/settings"
          className="mb-1.5 inline-flex items-center gap-1 text-[12px] text-muted-foreground transition-colors duration-[var(--dur-fast)] hover:text-foreground"
        >
          <ArrowLeft className="size-3" />
          Settings
        </Link>
        <h1>{title}</h1>
        {description && <p className="text-[12.5px] text-muted-foreground">{description}</p>}
      </header>
      {children}
    </div>
  );
}
