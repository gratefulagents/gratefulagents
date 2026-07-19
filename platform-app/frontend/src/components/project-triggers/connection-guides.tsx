import type { ComponentType, ReactNode } from "react";
import { ExternalLink } from "lucide-react";
import { openExternal } from "@/lib/native";
import { cn } from "@/lib/utils";

/** Validates a Kubernetes / DNS label: lowercase alphanumeric and hyphens, ≤ 63 chars. */
export function isDnsLabel(name: string): boolean {
  return /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/.test(name) && name.length <= 63;
}

/**
 * Returns a plain-language description for common cron expressions.
 * Hand-rolled for the most frequent patterns — no external dependency.
 */
export function describeCron(expr: string): string {
  const parts = expr.trim().split(/\s+/);
  if (parts.length !== 5) return "";
  const [min, hour, dom, mon, dow] = parts;
  // Hourly shorthand: 0 * * * *
  if (min === "0" && hour === "*" && dom === "*" && mon === "*" && dow === "*") return "Every hour";
  const hasTime = min !== "*" && hour !== "*";
  const timeStr = hasTime ? ` at ${hour.padStart(2, "0")}:${min.padStart(2, "0")}` : "";
  if (dom === "*" && mon === "*") {
    if (dow === "*") return `Daily${timeStr}`;
    if (dow === "1-5") return `Weekdays${timeStr}`;
    const dayLabels = [
      "Sundays",
      "Mondays",
      "Tuesdays",
      "Wednesdays",
      "Thursdays",
      "Fridays",
      "Saturdays",
    ];
    const dayNum = Number.parseInt(dow, 10);
    if (!Number.isNaN(dayNum) && dayNum >= 0 && dayNum <= 6) {
      return `Weekly on ${dayLabels[dayNum]}${timeStr}`;
    }
  }
  return "";
}

export const CRON_PRESETS = [
  { label: "Every hour", value: "0 * * * *" },
  { label: "Weekdays 9 am", value: "0 9 * * 1-5" },
  { label: "Daily 9 am", value: "0 9 * * *" },
  { label: "Weekly Monday", value: "0 9 * * 1" },
] as const;

/** Numbered how-to step rendered inline in the guided form. */
export function HowToStep({ n, children }: { n: number; children: ReactNode }) {
  return (
    <div className="flex gap-2.5">
      <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-muted text-[10.5px] font-semibold text-muted-foreground">
        {n}
      </span>
      <p className="text-[12.5px] leading-relaxed text-muted-foreground">{children}</p>
    </div>
  );
}

/** A button that opens a URL externally — styled as an inline link with an icon. */
export function ExternalLinkButton({
  href,
  children,
  className,
}: {
  href: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <button
      type="button"
      onClick={() => void openExternal(href)}
      className={cn(
        "inline-flex items-center gap-1 text-primary underline underline-offset-2 hover:opacity-80",
        className,
      )}
    >
      {children}
      <ExternalLink className="size-3 shrink-0" />
    </button>
  );
}

/** Section header for the how-to panel. */
export function HowToHeader({ children }: { children: ReactNode }) {
  return (
    <p className="mb-2 text-[11.5px] font-semibold uppercase tracking-wide text-muted-foreground/70">
      {children}
    </p>
  );
}

/** Password-like stored-value placeholder. */
export const STORED_PLACEHOLDER = "●●●●●● stored — paste to replace";

/** Provider card for the provider-selection step. */
export function ProviderCard({
  icon: Icon,
  label,
  description,
  selected,
  onClick,
}: {
  icon: ComponentType<{ className?: string }>;
  label: string;
  description: string;
  selected?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cn(
        "flex w-full items-start gap-3 rounded-lg border px-4 py-3.5 text-left transition-colors",
        selected
          ? "border-primary/40 bg-primary/5 ring-1 ring-primary/30"
          : "border-border/70 hover:bg-muted/40",
      )}
    >
      <span className="mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-foreground">
        <Icon className="size-4" />
      </span>
      <div>
        <p className="text-[13px] font-medium leading-snug">{label}</p>
        <p className="mt-0.5 text-[12px] text-muted-foreground">{description}</p>
      </div>
    </button>
  );
}

/** Sub-label / hint beneath an input. */
export function FieldHint({ children }: { children: ReactNode }) {
  return (
    <p className="mt-1 text-[11.5px] leading-relaxed text-muted-foreground/80">{children}</p>
  );
}
