import { ArrowDownToLine, ArrowUpToLine } from "lucide-react";

interface ChatScrollControlsProps {
  show: boolean;
  isPinnedToTop: boolean;
  isPinnedToBottom: boolean;
  onScrollTo: (where: "top" | "bottom") => void;
}

export function ChatScrollControls({
  show,
  isPinnedToTop,
  isPinnedToBottom,
  onScrollTo,
}: ChatScrollControlsProps) {
  if (!show) return null;

  return (
    <div className="pointer-events-none absolute right-3 bottom-3 z-10 flex flex-col gap-1.5">
      {!isPinnedToTop && (
        <button
          type="button"
          onClick={() => onScrollTo("top")}
          aria-label="Scroll to top"
          title="Scroll to top"
          className="pointer-events-auto flex size-10 items-center justify-center rounded-full border border-border/60 bg-background/90 text-muted-foreground shadow-sm backdrop-blur transition-colors hover:bg-muted hover:text-foreground md:size-8"
        >
          <ArrowUpToLine className="size-3.5" />
        </button>
      )}
      {!isPinnedToBottom && (
        <button
          type="button"
          onClick={() => onScrollTo("bottom")}
          aria-label="Scroll to bottom"
          title="Scroll to bottom"
          className="pointer-events-auto flex size-10 items-center justify-center rounded-full border border-border/60 bg-background/90 text-muted-foreground shadow-sm backdrop-blur transition-colors hover:bg-muted hover:text-foreground md:size-8"
        >
          <ArrowDownToLine className="size-3.5" />
        </button>
      )}
    </div>
  );
}
