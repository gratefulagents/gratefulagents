import { memo, useMemo, useState } from "react";
import { Loader2 } from "lucide-react";

import { useActivityLog } from "@/hooks/useActivityLog";
import { useActivityEntryDetail } from "@/hooks/useActivityEntryDetail";
import { MarkdownViewer } from "@/components/MarkdownViewer";
import { groupActivityEntries } from "@/lib/activityGrouping";
import type { ActivityEntry } from "@/rpc/platform/service_pb";
import { buildFeed, keyedFeedItems } from "./feedModel";
import { ActivityDetailProvider } from "./detailContext";
import type { FeedItem } from "./types";
import { WorkCard } from "./WorkRows";
import { InlineSubagentCard, SubagentCard } from "./SubagentCards";
import { SubagentDagCard } from "./SubagentDagCard";
import { MetaLine, PhaseDivider, PlanCard, QuestionCard, ReasoningCard } from "./InteractionCards";

function sameEntryRefs(a: ActivityEntry[], b: ActivityEntry[]): boolean {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return false;
  }
  return true;
}

// Feed items are rebuilt from scratch on every snapshot, but the underlying
// ActivityEntry references are reused for unchanged entries (snapshot merge in
// the hooks). Comparing element references therefore detects real changes
// while letting untouched rows skip re-rendering entirely.
function sameFeedItem(a: FeedItem, b: FeedItem): boolean {
  if (a === b) return true;
  if (a.kind !== b.kind) return false;
  switch (a.kind) {
    case "prose":
    case "phase":
    case "meta":
      return a.entry === (b as typeof a).entry;
    case "reasoning":
    case "work":
      return sameEntryRefs(a.entries, (b as typeof a).entries);
    case "subagent":
      return sameEntryRefs(a.group.entries, (b as typeof a).group.entries);
    case "subagent-dag": {
      const other = b as typeof a;
      if (a.groups.length !== other.groups.length) return false;
      for (let i = 0; i < a.groups.length; i++) {
        if (!sameEntryRefs(a.groups[i].entries, other.groups[i].entries)) return false;
      }
      return true;
    }
    case "inline-subagent": {
      const other = b as typeof a;
      return (
        a.group.parentEntry === other.group.parentEntry &&
        a.group.resultEntry === other.group.resultEntry &&
        sameEntryRefs(a.group.children, other.group.children)
      );
    }
    case "question":
    case "plan": {
      const other = b as typeof a;
      return a.use === other.use && a.result === other.result;
    }
  }
}

export const FeedItemView = memo(
  function FeedItemView({ item, live }: { item: FeedItem; live: boolean }) {
    switch (item.kind) {
      case "prose":
        return (
          <div className="text-sm leading-relaxed text-foreground">
            <MarkdownViewer content={item.entry.message} />
          </div>
        );
      case "reasoning":
        return <ReasoningCard entries={item.entries} />;
      case "work":
        return <WorkCard item={item} live={live} />;
      case "subagent":
        return <SubagentCard group={item.group} />;
      case "subagent-dag":
        return <SubagentDagCard groups={item.groups} waves={item.waves} />;
      case "inline-subagent":
        return <InlineSubagentCard group={item.group} />;
      case "question":
        return <QuestionCard use={item.use} result={item.result} />;
      case "plan":
        return <PlanCard use={item.use} />;
      case "phase":
        return <PhaseDivider entry={item.entry} />;
      case "meta":
        return <MetaLine entry={item.entry} />;
    }
  },
  (prev, next) => prev.live === next.live && sameFeedItem(prev.item, next.item),
);

export const ActivityFeed = memo(function ActivityFeed({
  entries,
  isLive,
}: {
  entries: ActivityEntry[];
  isLive: boolean;
}) {
  const feed = useMemo(
    () => buildFeed(groupActivityEntries(entries)),
    [entries],
  );
  const keyedFeed = useMemo(() => keyedFeedItems(feed), [feed]);
  const [showAll, setShowAll] = useState(false);
  const hiddenCount = keyedFeed.length > 150 && !showAll ? keyedFeed.length - 150 : 0;
  const visibleFeed = hiddenCount > 0 ? keyedFeed.slice(hiddenCount) : keyedFeed;
  const liveIndex = isLive ? keyedFeed.length - 1 : -1;

  return (
    <ul className="list-none space-y-2.5 m-0 p-0">
      {hiddenCount > 0 && (
        <li>
          <button
            type="button"
            onClick={() => setShowAll(true)}
            className="w-full rounded-md py-1.5 text-[12px] text-muted-foreground transition-colors hover:bg-muted/40"
          >
            Show {hiddenCount} earlier items
          </button>
        </li>
      )}
      {visibleFeed.map(({ item, key }, i) => (
        <li key={key}>
          <FeedItemView item={item} live={i + hiddenCount === liveIndex && item.kind === "work"} />
        </li>
      ))}
    </ul>
  );
});

// ─── Exported components ────────────────────────────────────────────────────

export function ActivityLogTable({
  entries,
  loading,
  error,
}: {
  entries: ActivityEntry[];
  loading: boolean;
  error: string | null;
}) {
  if (loading) {
    return (
      <div className="flex items-center justify-center gap-2 py-8">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
        <span className="text-sm text-muted-foreground">Loading activity log…</span>
      </div>
    );
  }
  if (error) return <p className="py-4 text-sm text-destructive">{error}</p>;
  if (!entries.length)
    return <p className="py-4 text-sm text-muted-foreground/60">No log entries.</p>;

  return <ActivityFeed entries={entries} isLive={false} />;
}

export function FullActivityLog({
  namespace,
  name,
  phase,
}: {
  namespace: string;
  name: string;
  phase?: string;
}) {
  const { entries, loading, error, isComplete } = useActivityLog(
    namespace,
    name,
    phase,
  );
  const fetchDetail = useActivityEntryDetail(namespace, name);
  if (loading) {
    return (
      <div className="flex items-center justify-center gap-2 py-8">
        <Loader2 className="size-4 animate-spin text-muted-foreground" />
        <span className="text-sm text-muted-foreground">Loading activity log…</span>
      </div>
    );
  }
  if (error) return <p className="py-4 text-sm text-destructive">{error}</p>;
  if (!entries.length)
    return <p className="py-4 text-sm text-muted-foreground/60">No log entries.</p>;

  const live = !isComplete;
  return (
    <ActivityDetailProvider value={fetchDetail}>
      <div>
        <ActivityFeed entries={entries} isLive={live} />
        {live && (
          <div className="mt-3 flex items-center gap-2 py-2 text-xs text-muted-foreground/60">
            <span className="size-1.5 animate-pulse rounded-full bg-primary" />
            <span>Live — streaming updates</span>
          </div>
        )}
      </div>
    </ActivityDetailProvider>
  );
}

export function InlineActivityLog({
  entries,
  isLive,
}: {
  entries: ActivityEntry[];
  isLive?: boolean;
}) {
  if (!entries.length) return null;
  return <ActivityFeed entries={entries} isLive={Boolean(isLive)} />;
}
