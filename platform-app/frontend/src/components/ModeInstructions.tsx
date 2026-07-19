import { useId, useState } from "react";

import { ChevronRight, ScrollText } from "lucide-react";

export function ModeInstructions({
  instructions,
  defaultOpen = false,
}: {
  instructions?: string;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const contentId = useId();

  if (!instructions) {
    return null;
  }

  return (
    <div className="py-0.5">
      <button
        type="button"
        className="group flex w-full items-center gap-2 rounded-md px-1.5 py-1 text-left text-xs text-muted-foreground transition-colors hover:text-foreground"
        aria-expanded={open}
        aria-controls={contentId}
        onClick={() => setOpen((value) => !value)}
      >
        <ChevronRight
          className={`size-3.5 shrink-0 text-muted-foreground/60 transition-transform duration-150 ${open ? "rotate-90" : ""}`}
        />
        <ScrollText className="size-3.5 shrink-0 text-muted-foreground/70" />
        <span className="shrink-0 whitespace-nowrap font-medium tracking-tight">Mode instructions</span>
        {!open && (
          <span className="min-w-0 truncate font-normal text-muted-foreground/50">
            {instructions.replace(/\s+/g, " ").slice(0, 90)}
          </span>
        )}
      </button>
      {open && (
        <div id={contentId} className="mt-1 pl-1.5">
          <div className="max-h-56 overflow-y-auto rounded-md border border-border/60 bg-muted/20">
            <pre className="whitespace-pre-wrap break-words p-3 font-mono text-xs leading-relaxed text-muted-foreground">
              {instructions}
            </pre>
          </div>
        </div>
      )}
    </div>
  );
}
