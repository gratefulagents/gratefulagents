import { useEffect, useRef } from "react";

import { cn } from "@/lib/utils";
import { optionId } from "@/components/run-session/composer";
import type { SlashCommand } from "@/components/run-session/slashCommands";

interface SlashCommandMenuProps {
  /** Listbox id the composer's `aria-controls` points at. */
  id: string;
  commands: SlashCommand[];
  activeIndex: number;
  onHover: (index: number) => void;
  onSelect: (command: SlashCommand) => void;
}

// SlashCommandMenu renders the reactive command palette anchored above the
// composer (the parent positions it). Keyboard navigation is driven by the
// parent (the textarea keeps focus); this component only renders the list and
// reports hover/selection.
export function SlashCommandMenu({
  id,
  commands,
  activeIndex,
  onHover,
  onSelect,
}: SlashCommandMenuProps) {
  const activeRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    activeRef.current?.scrollIntoView?.({ block: "nearest" });
  }, [activeIndex]);

  if (commands.length === 0) {
    return null;
  }

  return (
    <div
      role="listbox"
      id={id}
      aria-label="Slash commands"
      className="w-full max-w-md overflow-hidden rounded-lg border bg-popover text-popover-foreground shadow-lg"
    >
      <div className="border-b px-3 py-1.5 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
        Commands
      </div>
      <ul className="max-h-64 overflow-y-auto py-1">
        {commands.map((command, index) => {
          const active = index === activeIndex;
          return (
            <li key={command.id}>
              <button
                ref={active ? activeRef : undefined}
                id={optionId(id, index)}
                type="button"
                role="option"
                aria-selected={active}
                tabIndex={-1}
                onMouseEnter={() => onHover(index)}
                // mousedown (not click) so the textarea does not blur first.
                onMouseDown={(event) => {
                  event.preventDefault();
                  onSelect(command);
                }}
                className={cn(
                  "flex w-full items-start gap-3 px-3 py-1.5 text-left",
                  active ? "bg-accent" : "hover:bg-accent/50",
                )}
              >
                <code className="mt-0.5 shrink-0 rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-foreground">
                  {command.trigger}
                </code>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate text-sm text-foreground">{command.title}</span>
                  <span className="truncate text-2xs text-muted-foreground">
                    {command.description}
                  </span>
                </span>
              </button>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
