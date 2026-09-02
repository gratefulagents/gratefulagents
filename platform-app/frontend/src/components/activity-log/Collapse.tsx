import { useState, type ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Animated disclosure body: a CSS-grid row that transitions between 0fr and
 * 1fr so details slide open instead of popping in. Children mount lazily on
 * the first open (so collapsed rows never fetch payloads) and stay mounted
 * afterwards so the close animation has content to shrink. While closed the
 * body is `inert`, keeping hidden content out of tab order and the a11y tree.
 */
export function Collapse({
  open,
  id,
  className,
  children,
}: {
  open: boolean;
  id?: string;
  className?: string;
  children: ReactNode;
}) {
  const [everOpened, setEverOpened] = useState(open);
  if (open && !everOpened) setEverOpened(true);
  return (
    <div
      id={id}
      inert={!open}
      className={cn(
        "grid transition-[grid-template-rows] duration-[var(--dur-base)] motion-reduce:transition-none",
        open ? "grid-rows-[1fr]" : "grid-rows-[0fr]",
      )}
    >
      <div className="min-h-0 overflow-hidden">
        {everOpened && <div className={className}>{children}</div>}
      </div>
    </div>
  );
}
