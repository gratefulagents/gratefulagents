import { RotateCw, WifiOff } from "lucide-react";
import { AnimatePresence, motion } from "framer-motion";

import { useConnectionStatus } from "@/hooks/useConnectionStatus";
import { cn } from "@/lib/utils";

/**
 * Slim banner that appears when the browser reports we're offline or when
 * the app's backend fetch probe fails. Animates height so the content
 * below doesn't jump, and offers a manual retry.
 */
export function OfflineBanner() {
  const status = useConnectionStatus();

  return (
    <AnimatePresence initial={false}>
      {status !== "online" && (
        <motion.div
          role="alert"
          initial={{ height: 0, opacity: 0 }}
          animate={{ height: 26, opacity: 1 }}
          exit={{ height: 0, opacity: 0 }}
          transition={{ duration: 0.16, ease: [0.25, 1, 0.5, 1] }}
          className={cn(
            "flex items-center justify-center gap-2 overflow-hidden px-3",
            "border-b border-[color-mix(in_oklch,var(--tone-warning)_30%,transparent)]",
            "bg-[color-mix(in_oklch,var(--tone-warning)_10%,transparent)]",
            "text-[11.5px] font-medium text-[color:var(--tone-warning-fg)]",
          )}
        >
          <WifiOff className="size-3 shrink-0" />
          {status === "offline"
            ? "Offline — showing cached data."
            : "Reconnecting to gratefulagents…"}
          <button
            type="button"
            onClick={() => window.location.reload()}
            className="inline-flex items-center gap-1 rounded-[4px] px-1.5 py-px hover:bg-[color-mix(in_oklch,var(--tone-warning)_18%,transparent)] transition-colors"
          >
            <RotateCw className="size-2.5" />
            Retry
          </button>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
