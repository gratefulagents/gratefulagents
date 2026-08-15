import * as React from "react";
import { Link } from "react-router-dom";
import { FileQuestion, Lock, ServerCrash, Ban } from "lucide-react";

import { Button, buttonVariants } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export type DetailErrorKind = "not-found" | "forbidden" | "unsupported" | "error";

const PRESETS: Record<DetailErrorKind, { icon: React.ReactNode; title: string; hint: string }> = {
  "not-found": {
    icon: <FileQuestion aria-hidden />,
    title: "Not found",
    hint: "It may have been deleted, or the link may point at a different namespace.",
  },
  forbidden: {
    icon: <Lock aria-hidden />,
    title: "You don't have access",
    hint: "Ask the owner to share it with you, or switch to a resource you own.",
  },
  unsupported: {
    icon: <Ban aria-hidden />,
    title: "Not available on this server",
    hint: "This deployment doesn't expose the API this page needs.",
  },
  error: {
    icon: <ServerCrash aria-hidden />,
    title: "Something went wrong",
    hint: "The request failed. Retrying often clears a transient error.",
  },
};

export type RecoveryLink = { to: string; label: string };

/**
 * Typed dead-end state for detail pages.
 *
 * A bad deep link (deleted scan, wrong namespace, another user's finding) used
 * to leave the security detail pages on a permanent skeleton or a bare error
 * string with nowhere to go. This renders the reason plainly and always offers
 * a way back — retry plus one or more recovery destinations — so the user is
 * never stranded on a URL that can no longer resolve.
 */
export function DetailErrorState({
  kind,
  title,
  description,
  detail,
  onRetry,
  links = [],
  className,
}: {
  kind: DetailErrorKind;
  title?: string;
  description?: string;
  /** Raw server message, shown small so the headline stays human. */
  detail?: string;
  onRetry?: () => void;
  links?: RecoveryLink[];
  className?: string;
}) {
  const preset = PRESETS[kind];
  return (
    <div
      role="alert"
      className={cn(
        "surface-card mx-auto flex max-w-xl flex-col items-center gap-3 rounded-xl border border-border/60 bg-muted/20 px-6 py-14 text-center",
        className,
      )}
    >
      <div className="flex size-11 items-center justify-center rounded-full bg-muted/60 text-muted-foreground/80 ring-1 ring-inset ring-border/60 [&_svg]:size-5">
        {preset.icon}
      </div>
      <p className="text-[15px] font-medium text-foreground">{title ?? preset.title}</p>
      <p className="max-w-[52ch] text-[12.5px] leading-relaxed text-muted-foreground">
        {description ?? preset.hint}
      </p>
      {detail && (
        <p className="max-w-[52ch] break-words font-mono text-[11px] text-muted-foreground/70">
          {detail}
        </p>
      )}
      <div className="flex flex-wrap items-center justify-center gap-2 pt-1">
        {onRetry && (
          <Button variant="outline" size="sm" onClick={onRetry}>
            Try again
          </Button>
        )}
        {links.map((link, index) => (
          // Real anchors, not buttons-that-navigate: recovery destinations must
          // be middle-clickable, copyable, and announced as links.
          <Link
            key={link.to}
            to={link.to}
            className={cn(
              buttonVariants({
                variant: index === 0 && !onRetry ? "default" : "outline",
                size: "sm",
              }),
            )}
          >
            {link.label}
          </Link>
        ))}
      </div>
    </div>
  );
}

/** Map a ConnectRPC error message to a `DetailErrorKind`. */
export function classifyDetailError(message: string): DetailErrorKind {
  const text = message.toLowerCase();
  if (text.includes("not_found") || text.includes("notfound") || text.includes("not found")) {
    return "not-found";
  }
  if (
    text.includes("permission_denied")
    || text.includes("permissiondenied")
    || text.includes("forbidden")
    || text.includes("unauthenticated")
  ) {
    return "forbidden";
  }
  if (text.includes("unimplemented") || text.includes("not implemented")) {
    return "unsupported";
  }
  return "error";
}
