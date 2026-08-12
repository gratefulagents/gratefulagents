import { Toaster as SonnerToaster } from "sonner";

import { useTheme } from "@/lib/theme";

/**
 * App-wide toaster. Single instance — mount once in App.tsx.
 * Rendered top-center so it never covers the chat composer's Send/Stop
 * controls in the bottom-right corner. Visual language matches our surface
 * tokens (graphite surface, hairline ring, Geist body). Follows the in-app
 * theme toggle (not the OS theme, which `theme="system"` would track).
 */
export function Toaster() {
  const theme = useTheme();
  return (
    <SonnerToaster
      position="top-center"
      offset={20}
      mobileOffset={12}
      gap={10}
      theme={theme}
      duration={3500}
      visibleToasts={3}
      closeButton
      toastOptions={{
        classNames: {
          toast:
            "!bg-[color:var(--color-popover)]/95 !backdrop-blur-md !text-foreground !border !border-border/60 !shadow-[0_1px_2px_rgba(0,0,0,0.08),0_8px_28px_rgba(0,0,0,0.18)] !rounded-[10px] !font-sans !px-3.5 !py-3 !gap-2.5 !items-center",
          content: "!gap-0.5",
          title: "!text-[13px] !font-medium !leading-snug !tracking-tight",
          description: "!text-[12px] !leading-snug !text-muted-foreground",
          icon: "!m-0 !self-center",
          actionButton:
            "!bg-primary !text-primary-foreground !text-[11.5px] !font-medium !rounded-[6px] !px-2.5 !py-1 hover:!opacity-90",
          cancelButton:
            "!bg-transparent !text-muted-foreground !text-[11.5px] !rounded-[6px] !px-2.5 !py-1 hover:!bg-muted/60 hover:!text-foreground",
          closeButton:
            "!static !order-last !ml-auto !translate-x-0 !translate-y-0 !bg-transparent !text-muted-foreground/70 hover:!text-foreground hover:!bg-muted/60 !border-none !rounded-[6px] !size-5 [&>svg]:!size-3.5",
          success: "[&_[data-icon]]:!text-emerald-500",
          error: "[&_[data-icon]]:!text-destructive",
          warning: "[&_[data-icon]]:!text-amber-500",
          info: "[&_[data-icon]]:!text-[color:var(--color-primary)]",
        },
      }}
    />
  );
}

export { toast } from "sonner";
