import { useEffect, useRef } from "react";
import { File as FileIcon } from "lucide-react";

import type { FileMatch } from "@/lib/fileMentions";
import { cn } from "@/lib/utils";

interface FileMentionMenuProps {
  matches: FileMatch[];
  activeIndex: number;
  loading: boolean;
  hasQuery: boolean;
  onHover: (index: number) => void;
  onSelect: (match: FileMatch) => void;
}

// Splits a path into directory + filename, highlighting the fuzzy-matched
// characters so the user can see why each result matched.
function renderPath(path: string, positions: number[]) {
  const matched = new Set(positions);
  const slash = path.lastIndexOf("/");
  const dirEnd = slash === -1 ? 0 : slash + 1;
  return path.split("").map((char, i) => (
    <span
      key={i}
      className={cn(
        matched.has(i) ? "font-semibold text-foreground" : i < dirEnd ? "text-muted-foreground" : "text-foreground",
      )}
    >
      {char}
    </span>
  ));
}

// FileMentionMenu renders the "@" file picker anchored above the composer.
// Keyboard navigation is driven by the parent (the textarea keeps focus); this
// component only renders the list and reports hover/selection.
export function FileMentionMenu({
  matches,
  activeIndex,
  loading,
  hasQuery,
  onHover,
  onSelect,
}: FileMentionMenuProps) {
  const activeRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    activeRef.current?.scrollIntoView?.({ block: "nearest" });
  }, [activeIndex]);

  const empty = !loading && matches.length === 0;

  return (
    <div
      role="listbox"
      id="file-mention-menu"
      aria-label="Workspace files"
      className="absolute bottom-full left-0 z-50 mb-2 w-full max-w-md overflow-hidden rounded-lg border bg-popover text-popover-foreground shadow-lg"
    >
      <div className="border-b px-3 py-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
        Files
      </div>
      {loading && matches.length === 0 ? (
        <div className="px-3 py-2 text-xs text-muted-foreground">Loading files…</div>
      ) : empty ? (
        <div className="px-3 py-2 text-xs text-muted-foreground">
          {hasQuery ? "No files match" : "No files found"}
        </div>
      ) : (
        <ul className="max-h-64 overflow-y-auto py-1">
          {matches.map((match, index) => {
            const active = index === activeIndex;
            return (
              <li key={match.path}>
                <button
                  ref={active ? activeRef : undefined}
                  type="button"
                  role="option"
                  aria-selected={active}
                  tabIndex={-1}
                  onMouseEnter={() => onHover(index)}
                  // mousedown (not click) so the textarea does not blur first.
                  onMouseDown={(event) => {
                    event.preventDefault();
                    onSelect(match);
                  }}
                  className={cn(
                    "flex w-full items-center gap-2 px-3 py-1.5 text-left",
                    active ? "bg-accent" : "hover:bg-accent/50",
                  )}
                >
                  <FileIcon className="size-3.5 shrink-0 text-muted-foreground" />
                  <span className="truncate font-mono text-xs">{renderPath(match.path, match.positions)}</span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
