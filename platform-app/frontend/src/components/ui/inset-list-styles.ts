import { cn } from "@/lib/utils";

/** Small macOS push button (bezel style) for row and header actions. */
export function pushButtonClass(extra?: string) {
  return cn(
    "inline-flex h-[22px] cursor-default select-none items-center gap-1 whitespace-nowrap rounded-[6px] px-2 text-[12px] font-medium text-foreground/90",
    "bg-foreground/[0.08] shadow-[inset_0_0.5px_0_oklch(1_0_0_/_0.08),0_0.5px_1px_oklch(0_0_0_/_0.25)]",
    "transition-colors duration-75 hover:bg-foreground/[0.11] active:bg-foreground/[0.16]",
    "outline-none focus-visible:ring-2 focus-visible:ring-ring/60 [&_svg]:size-3",
    extra,
  );
}

/** System Settings palette for icon tiles. */
export const tileColor = {
  blue: "oklch(0.62 0.17 255)",
  green: "oklch(0.66 0.16 150)",
  orange: "oklch(0.72 0.16 60)",
  red: "oklch(0.63 0.2 25)",
  purple: "oklch(0.6 0.19 300)",
  teal: "oklch(0.68 0.11 210)",
  indigo: "oklch(0.55 0.18 275)",
  gray: "oklch(0.6 0.01 265)",
} as const;
