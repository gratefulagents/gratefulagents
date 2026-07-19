import { Toaster as SonnerToaster } from "sonner";

import { useTheme } from "@/lib/theme";

/**
 * App-wide toaster. Single instance — mount once in App.tsx.
 * Visual language matches our surface tokens (graphite surface, hairline
 * ring, Geist body / JetBrains Mono numerics). Follows the in-app theme
 * toggle (not the OS theme, which `theme="system"` would track).
 */
export function Toaster() {
  const theme = useTheme();
  return (
    <SonnerToaster
      position="bottom-right"
      offset={16}
      theme={theme}
      duration={4000}
      closeButton
      toastOptions={{
        classNames: {
          toast:
            "!bg-[color:var(--color-popover)] !text-foreground !border !border-border/70 !shadow-[0_8px_24px_rgba(0,0,0,0.25)] !rounded-[8px] !font-sans",
          title: "!text-[13px] !font-medium !tracking-tight",
          description: "!text-[12px] !text-muted-foreground",
          actionButton:
            "!bg-primary !text-primary-foreground !text-[11.5px] !font-medium !rounded-[5px] !px-2 !py-1",
          cancelButton:
            "!bg-transparent !text-muted-foreground !text-[11.5px] !rounded-[5px] !px-2 !py-1 hover:!bg-muted/60",
          closeButton:
            "!bg-transparent !text-muted-foreground hover:!text-foreground !border-none",
          success: "!border-emerald-500/40",
          error: "!border-destructive/50",
          warning: "!border-amber-500/40",
          info: "!border-[color:var(--color-primary)]/40",
        },
      }}
    />
  );
}

export { toast } from "sonner";
