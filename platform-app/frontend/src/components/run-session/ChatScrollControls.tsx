import { ArrowDownToLine, ArrowUpToLine } from "lucide-react";

import { cn } from "@/lib/utils";

interface ChatScrollControlsProps {
  show: boolean;
  isPinnedToTop: boolean;
  isPinnedToBottom: boolean;
  onScrollTo: (where: "top" | "bottom") => void;
  /** Items that arrived while scrolled away from the bottom. */
  newCount?: number;
}

export function ChatScrollControls({
  show,
  isPinnedToTop,
  isPinnedToBottom,
  onScrollTo,
  newCount = 0,
}: ChatScrollControlsProps) {
  if (!show) return null;
  const showNew = !isPinnedToBottom && newCount > 0;
  const bottomLabel = showNew
    ? `Scroll to bottom, ${newCount} new message${newCount === 1 ? "" : "s"}`
    : "Scroll to bottom";

  return (
    <div className="pointer-events-none absolute right-3 bottom-3 z-10 flex flex-col items-end gap-1.5">
      {!isPinnedToTop && (
        <button
          type="button"
          onClick={() => onScrollTo("top")}
          aria-label="Scroll to top"
          title="Scroll to top"
          className="pointer-events-auto flex size-10 items-center justify-center rounded-full border border-border/60 bg-background/90 text-muted-foreground shadow-sm backdrop-blur transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2 md:size-8"
        >
          <ArrowUpToLine className="size-3.5" />
        </button>
      )}
      {!isPinnedToBottom && (
        <button
          type="button"
          onClick={() => onScrollTo("bottom")}
          aria-label={bottomLabel}
          title={bottomLabel}
          className={cn(
            "pointer-events-auto flex h-10 items-center justify-center gap-1.5 rounded-full border border-border/60 bg-background/90 text-muted-foreground shadow-sm backdrop-blur transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2 md:h-8",
            showNew ? "self-end px-3 text-xs font-medium text-foreground" : "w-10 md:w-8",
          )}
        >
          {showNew && <span>{newCount} new</span>}
          <ArrowDownToLine className="size-3.5" />
        </button>
      )}
    </div>
  );
}
