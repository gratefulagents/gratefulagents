/* eslint-disable react-refresh/only-export-components, react-hooks/set-state-in-effect */
import { useEffect, useState } from "react";
import { Search, X } from "lucide-react";

import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

/**
 * Compact search input used above every list. Controlled; exposes a clear
 * button; respects reduced motion by using CSS transitions under the
 * global `prefers-reduced-motion` override.
 */
export function ListSearchInput({
  value,
  onChange,
  placeholder = "Search…",
  className,
  ariaLabel,
}: {
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  className?: string;
  ariaLabel?: string;
}) {
  const [localValue, setLocalValue] = useState(value);

  useEffect(() => {
    setLocalValue(value);
  }, [value]);

  useEffect(() => {
    if (localValue === value) return;
    const timer = setTimeout(() => onChange(localValue), 120);
    return () => clearTimeout(timer);
  }, [localValue, onChange, value]);

  return (
    <div
      className={cn(
        "relative flex items-center h-8 w-[280px] max-w-full",
        className,
      )}
    >
      <Search
        className="pointer-events-none absolute left-2.5 size-[14px] text-muted-foreground/70"
        aria-hidden
      />
      <Input
        type="search"
        value={localValue}
        onChange={(e) => setLocalValue(e.currentTarget.value)}
        placeholder={placeholder}
        aria-label={ariaLabel ?? placeholder}
        className="h-8 pl-8 pr-8 text-[13px] placeholder:text-muted-foreground/60"
      />
      {localValue && (
        <button
          type="button"
          onClick={() => {
            setLocalValue("");
            onChange("");
          }}
          aria-label="Clear search"
          className="absolute right-1.5 flex size-[18px] items-center justify-center rounded text-muted-foreground/80 transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        >
          <X className="size-3" />
        </button>
      )}
    </div>
  );
}

/**
 * Lowercase-contains filter across the given string extractors.
 * Empty query returns the input unchanged.
 */
export function filterByQuery<T>(
  items: T[],
  query: string,
  fields: (item: T) => Array<string | undefined | null>,
): T[] {
  const q = query.trim().toLowerCase();
  if (!q) return items;
  return items.filter((it) =>
    fields(it).some((f) => (f ?? "").toLowerCase().includes(q)),
  );
}
