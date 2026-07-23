import { memo } from "react";
import { MessageCircleQuestion } from "lucide-react";

import { InlineActivityLog } from "@/components/FullActivityLog";
import { MarkdownViewer } from "@/components/MarkdownViewer";
import type { ActivityEntry } from "@/rpc/platform/service_pb";
import { thinkingLabel, type TimelineItem } from "./helpers";

function sameRefs<T>(a: readonly T[], b: readonly T[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

// Timeline items are rebuilt on every snapshot, but the entry/message
// references inside them are reused when unchanged (snapshot merge in the
// hooks), so element-wise reference comparison detects real changes cheaply.
function sameTimelineItem(a: TimelineItem, b: TimelineItem): boolean {
  if (a === b) return true;
  if (a.kind !== b.kind || a.key !== b.key) return false;
  switch (a.kind) {
    case "user-request":
      return a.content === (b as typeof a).content;
    case "message": {
      const other = b as typeof a;
      return (
        a.role === other.role &&
        a.timestamp === other.timestamp &&
        a.content === other.content &&
        sameRefs(a.imageDataUrls ?? [], other.imageDataUrls ?? [])
      );
    }
    case "activity": {
      const other = b as typeof a;
      return (
        a.isLive === other.isLive &&
        a.planContent === other.planContent &&
        sameRefs(a.entries as ActivityEntry[], other.entries)
      );
    }
    case "pending": {
      const other = b as typeof a;
      const aActions = a.actions ?? [];
      const bActions = other.actions ?? [];
      return (
        a.content === other.content &&
        aActions.length === bActions.length &&
        aActions.every((act, i) => act.id === bActions[i].id && act.label === bActions[i].label)
      );
    }
    case "thinking":
      return a.phase === (b as typeof a).phase;
  }
}

export const TimelineRow = memo(
  function TimelineRow({ item, thinkingStep }: { item: TimelineItem; thinkingStep: string }) {
    switch (item.kind) {
      case "user-request":
        return (
          <div className="flex justify-end">
            <div className="max-w-[85%] rounded-lg bg-primary px-3.5 py-2 text-primary-foreground md:max-w-[75%]">
              <p className="text-sm leading-relaxed whitespace-pre-wrap">{item.content}</p>
            </div>
          </div>
        );
      case "message":
        if (item.role === "system") {
          return (
            <div className="flex justify-center py-1">
              <span className="text-xs italic text-muted-foreground">
                {item.content.replace(/^\[SYSTEM\]\s*/i, "")}
              </span>
            </div>
          );
        }

        if (item.role === "user") {
          return (
            <div className="flex justify-end">
              <div className="max-w-[85%] space-y-2 rounded-lg bg-primary px-3.5 py-2 text-primary-foreground md:max-w-[75%]">
                {item.imageDataUrls && item.imageDataUrls.length > 0 && (
                  <div className="flex flex-wrap justify-end gap-2">
                    {item.imageDataUrls.map((url, i) => (
                      <a
                        key={i}
                        href={url}
                        target="_blank"
                        rel="noreferrer"
                        className="block overflow-hidden rounded-md border border-primary-foreground/20"
                      >
                        <img
                          src={url}
                          alt={`attachment ${i + 1}`}
                          loading="lazy"
                          className="max-h-48 max-w-[12rem] object-contain"
                        />
                      </a>
                    ))}
                  </div>
                )}
                {item.content && (
                  <p className="text-sm leading-relaxed whitespace-pre-wrap">{item.content}</p>
                )}
              </div>
            </div>
          );
        }

        return (
          <div className="text-sm leading-relaxed text-foreground">
            <MarkdownViewer content={item.content} />
          </div>
        );
      case "activity":
        return (
          <InlineActivityLog
            entries={item.entries}
            isLive={item.isLive}
            planContent={item.planContent}
          />
        );
      case "pending":
        return (
          <div className="flex items-center gap-2 py-1 text-xs text-amber-600 dark:text-amber-400">
            <MessageCircleQuestion className="size-3.5" />
            <span>Paused</span>
          </div>
        );
      case "thinking":
        return (
          <div className="flex flex-col gap-1.5 py-1">
            <div className="flex items-center gap-2">
              <span className="relative flex size-2">
                <span className="absolute inline-flex size-full animate-ping rounded-full bg-primary opacity-75" />
                <span className="relative inline-flex size-2 rounded-full bg-primary" />
              </span>
              <p className="text-xs text-muted-foreground">{thinkingLabel(item.phase, thinkingStep)}</p>
            </div>
          </div>
        );
    }
  },
  (prev, next) =>
    prev.thinkingStep === next.thinkingStep && sameTimelineItem(prev.item, next.item),
);
