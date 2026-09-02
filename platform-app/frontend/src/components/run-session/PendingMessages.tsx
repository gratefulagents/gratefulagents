import { Pencil, X } from "lucide-react";

import { LiveDot } from "@/components/ui/live-dot";
import type { ChatMessage } from "@/rpc/platform/service_pb";

/**
 * A message the composer has handed to the server but that no snapshot has
 * echoed back yet. `messageId` is filled in from the send response; the row
 * disappears once a conversation message with that id shows up.
 */
export type OutboundMessage = {
  clientMessageId: string;
  content: string;
  imageCount: number;
  sentAt: number;
  messageId?: bigint;
};

/** Outbound rows older than this without a matching echo are dropped so a
 * lost snapshot cannot leave a "Sending…" chip behind forever. */
export const OUTBOUND_MESSAGE_TTL_MS = 30_000;

/**
 * Drops outbound rows that the conversation now contains (matched by server
 * message id) and, when `now` is given, rows that have outlived their TTL.
 * Returns the same array when nothing changed so callers can skip a re-render.
 */
export function settleOutboundMessages(
  outbound: OutboundMessage[],
  conversation: ChatMessage[],
  now?: number,
): OutboundMessage[] {
  if (outbound.length === 0) {
    return outbound;
  }
  const ids = new Set(conversation.map((m) => m.id));
  const next = outbound.filter(
    (m) =>
      !(m.messageId !== undefined && ids.has(m.messageId)) &&
      (now === undefined || now - m.sentAt < OUTBOUND_MESSAGE_TTL_MS),
  );
  return next.length === outbound.length ? outbound : next;
}

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
  outbound = [],
  onEdit,
  onCancel,
  busy = false,
  terminal = false,
}: {
  messages: ChatMessage[];
  /** Locally sent messages awaiting the server echo; rendered as "Sending…". */
  outbound?: OutboundMessage[];
  /** True when the run ended before these messages could be delivered. */
  terminal?: boolean;
  /** Pull the message back into the composer for editing. Hidden when absent. */
  onEdit?: (message: ChatMessage) => void;
  /** Withdraw the message before the agent consumes it. Hidden when absent. */
  onCancel?: (message: ChatMessage) => void;
  /** Disables the row actions while a cancel/edit call is in flight. */
  busy?: boolean;
}) {
  if (messages.length === 0 && outbound.length === 0) {
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
            <LiveDot
              tone={terminal ? "idle" : steering ? "waiting" : "info"}
              pulse={!terminal}
              size="xs"
            />
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
                    className="inline-flex size-6 items-center justify-center rounded-sm text-muted-foreground/50 transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2 disabled:pointer-events-none disabled:opacity-40"
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
                    className="inline-flex size-6 items-center justify-center rounded-sm text-muted-foreground/50 transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2 disabled:pointer-events-none disabled:opacity-40"
                  >
                    <X className="size-3" />
                  </button>
                )}
              </span>
            )}
          </div>
        );
      })}
      {outbound.map((message) => (
        <div
          key={`outbound:${message.clientMessageId}`}
          className="flex min-h-6 items-center gap-2 text-xs text-muted-foreground"
        >
          <LiveDot tone="idle" pulse size="xs" />
          <span className="shrink-0 font-medium text-muted-foreground/70">Sending…</span>
          <span className="min-w-0 flex-1 truncate">
            {message.content ||
              (message.imageCount > 0
                ? `${message.imageCount} image attachment${message.imageCount === 1 ? "" : "s"}`
                : "")}
          </span>
        </div>
      ))}
    </div>
  );
}
