import * as React from "react";
import { cn } from "@/lib/utils";
import { isTauri, platform } from "@/lib/platform";
import { Kbd } from "./Kbd";
import { NotificationBell } from "@/components/NotificationBell";
import { ThemeToggle } from "@/components/ThemeToggle";
import { Search } from "lucide-react";

export interface TitleBarProps {
  onOpenPalette: () => void;
  /** Optional breadcrumb / contextual trail shown centered on macOS. */
  trail?: React.ReactNode;
  /** Optional actions shown on the right. */
  right?: React.ReactNode;
}

/**
 * Modern titlebar. Renders edge-to-edge under the macOS traffic lights,
 * becomes a sticky top bar on iPad. Central "command bar" is the primary
 * command-palette entry point.
 */
export function TitleBar({ onOpenPalette, trail, right }: TitleBarProps) {
  const [mac, setMac] = React.useState(false);
  const [iPad, setIPad] = React.useState(false);
  React.useEffect(() => {
    if (!isTauri) return;
    platform().then((p) => {
      setMac(p === "macos");
      setIPad(p === "ios");
    });
  }, []);

  const modKey = mac ? "⌘" : "Ctrl";

  return (
    <header
      className={cn(
        "drag-region relative shrink-0 z-40",
        "min-h-[44px] pt-safe flex items-stretch",
        "bg-transparent",
        // Extra left inset on macOS so traffic lights don't collide with content
        mac ? "pl-[76px] pr-3" : iPad ? "pl-safe pr-[max(env(safe-area-inset-right),0.75rem)]" : "px-3",
        "select-none",
      )}
    >
      <div className="flex flex-1 items-center gap-2 min-w-0">
        {/* App mark — quiet, inline */}
        <div className="no-drag flex items-center gap-2 min-w-0">
          <img
            aria-hidden
            src="/logo.png"
            alt=""
            draggable={false}
            className="size-5 rounded-[5px] shadow-[inset_0_0_0_1px_oklch(1_0_0_/_0.2)]"
          />
          <span className="hidden md:inline text-[12.5px] font-medium tracking-tight text-foreground/90">
            gratefulagents
          </span>
        </div>

        {/* Breadcrumb */}
        {trail && (
          <>
            <span aria-hidden className="hidden md:inline px-1.5 text-xs text-muted-foreground/40">
              /
            </span>
            <div className="no-drag hidden md:block min-w-0 text-[12.5px] text-muted-foreground truncate">
              {trail}
            </div>
          </>
        )}
      </div>

      {/* Command bar — centered on wide screens, right-aligned on narrow */}
      <div className="no-drag flex min-w-0 items-center px-3">
        <button
          onClick={onOpenPalette}
          aria-label="Search or jump to…"
          className={cn(
            "md:hidden inline-flex size-10 items-center justify-center",
            "rounded-[8px] text-muted-foreground",
            "hover:bg-muted/60",
            "transition-colors duration-[var(--dur-fast)]",
          )}
        >
          <Search className="size-4 text-muted-foreground/80" />
        </button>
        <button
          onClick={onOpenPalette}
          className={cn(
            "group hidden md:inline-flex items-center gap-2",
            "h-[28px] px-2.5 w-[clamp(160px,calc(100vw-380px),440px)] min-w-0 max-w-[440px]",
            "rounded-[8px]",
            "bg-muted/40 hover:bg-muted/60",
            "ring-1 ring-inset ring-border/60 hover:ring-border",
            "text-[12px] text-muted-foreground",
            "transition-colors duration-[var(--dur-fast)]",
          )}
        >
          <Search className="size-3.5 text-muted-foreground/80" />
          <span className="flex-1 text-left tracking-tight">
            Search or jump to…
          </span>
          <span className="flex items-center gap-0.5">
            <Kbd>{modKey}</Kbd>
            <Kbd>K</Kbd>
          </span>
        </button>
      </div>

      <div className="flex flex-1 items-center justify-end gap-1.5 min-w-0">
        <div className="no-drag flex items-center gap-1">
          {right}
          <NotificationBell />
          <ThemeToggle />
        </div>
      </div>
    </header>
  );
}

/** Hairline divider under the titlebar. */
export function TitleBarDivider() {
  return <div className="hairline shrink-0" />;
}
