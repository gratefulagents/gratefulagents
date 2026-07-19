import { Pencil, X } from "lucide-react";

import { toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { ChatMessage } from "@/rpc/platform/service_pb";

/**
 * PendingMessages renders user messages that were accepted (queued or
 * steering) but not yet delivered to the agent loop. They live above the
 * composer — outside the transcript — as quiet single-line rows, and each one
 * moves into the chat feed the moment the agent actually consumes it.
 *
 * Until the agent picks a message up it can still be withdrawn (onCancel) or
 * pulled back into the composer for editing (onEdit).
 */
export function PendingMessages({
  messages,
  onEdit,
  onCancel,
  busy = false,
  terminal = false,
}: {
  messages: ChatMessage[];
  /** True when the run ended before these messages could be delivered. */
  terminal?: boolean;
  /** Pull the message back into the composer for editing. Hidden when absent. */
  onEdit?: (message: ChatMessage) => void;
  /** Withdraw the message before the agent consumes it. Hidden when absent. */
  onCancel?: (message: ChatMessage) => void;
  /** Disables the row actions while a cancel/edit call is in flight. */
  busy?: boolean;
}) {
  if (messages.length === 0) {
    return null;
  }

  return (
    <div className="shrink-0 space-y-1 border-t px-4 py-2" aria-live="polite">
      {messages.map((message, index) => {
        const steering = message.queueMode === "immediate";
        const label = terminal ? "Delivery unconfirmed — run ended" : steering ? "Steering" : "Queued";
        const imageCount = message.imageDataUrls.length;
        const preview =
          message.content ||
          (imageCount > 0 ? `${imageCount} image attachment${imageCount === 1 ? "" : "s"}` : "");
        // Older snapshots may not carry durable message ids yet; without one
        // the message can't be targeted for cancellation.
        const actionable = message.id !== 0n;
        return (
          <div
            key={`${message.id.toString()}:${message.timestampUnix.toString()}:${index}`}
            className="group flex min-h-6 items-center gap-2 text-xs text-muted-foreground"
          >
            <span className="relative flex size-1.5 shrink-0" aria-hidden="true">
              <span
                className={cn(
                  "absolute inline-flex size-full rounded-full bg-current opacity-75",
                  !terminal && "animate-ping",
                  toneText[terminal ? "neutral" : steering ? "warning" : "info"],
                )}
              />
              <span
                className={cn(
                  "relative inline-flex size-1.5 rounded-full bg-current",
                  toneText[terminal ? "neutral" : steering ? "warning" : "info"],
                )}
              />
            </span>
            <span className="shrink-0 font-medium text-muted-foreground/70">{label}</span>
            <span className="min-w-0 flex-1 truncate">{preview}</span>
            {!terminal && actionable && (onEdit || onCancel) && (
              <span className="flex shrink-0 items-center gap-0.5">
                {onEdit && (
                  <button
                    type="button"
                    onClick={() => onEdit(message)}
                    disabled={busy}
                    aria-label="Edit message"
                    title="Edit — move this message back into the composer"
                    className="rounded p-1 text-muted-foreground/50 transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-40"
                  >
                    <Pencil className="size-3" />
                  </button>
                )}
                {onCancel && (
                  <button
                    type="button"
                    onClick={() => onCancel(message)}
                    disabled={busy}
                    aria-label="Cancel message"
                    title="Cancel — the agent will never see this message"
                    className="rounded p-1 text-muted-foreground/50 transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-40"
                  >
                    <X className="size-3" />
                  </button>
                )}
              </span>
            )}
          </div>
        );
      })}
    </div>
  );
}
