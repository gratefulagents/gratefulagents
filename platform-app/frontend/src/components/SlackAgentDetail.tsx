import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  ArrowDown,
  ArrowUp,
  Check,
  ChevronRight,
  Copy,
  ExternalLink,
  Inbox,
  RefreshCw,
} from "lucide-react";

import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneSoft, toneText, type StatusTone } from "@/lib/status";
import { slackAgentStatus } from "@/lib/slackStatus";
import { buildSlackManifest } from "@/lib/slackManifest";
import { formatAge } from "@/lib/format";
import { openExternal } from "@/lib/native";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { AgentRunTable } from "@/components/AgentRunTable";
import {
  DetailHeader,
  DetailSection,
  StatBar,
  Stat,
  FactList,
  Fact,
  RunsSection,
} from "@/components/detail-page";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import { ListSearchInput, filterByQuery } from "@/components/ui/list-search";
import { SegmentedControl } from "@/components/shell/SegmentedControl";
import { SlackAgentSettings } from "@/components/SlackAgentSettings";
import { toast } from "@/components/ui/toaster";
import type { SlackAgent } from "@/rpc/platform/service_pb";

interface DraftRow {
  id: string;
  channelId: string;
  targetUser: string;
  incomingText: string;
  draftText: string;
  status: string;
  createdAtUnix: bigint;
  decidedAtUnix: bigint;
}

const draftTones: Record<string, StatusTone> = {
  pending: "warning",
  sent: "success",
  dismissed: "neutral",
};

type DraftFilter = "" | "pending" | "sent" | "dismissed";

function DraftPill({ status }: { status: string }) {
  const tone = draftTones[status] ?? "neutral";
  return (
    <span
      className={cn(
        "inline-flex h-5 shrink-0 items-center gap-1.5 rounded-full pl-1.5 pr-2 text-[11px] font-medium capitalize select-none",
        toneSoft[tone],
      )}
    >
      <span className="size-1.5 rounded-full bg-current" aria-hidden />
      {status}
    </span>
  );
}

function draftTimestamp(d: DraftRow): string {
  const unix = d.decidedAtUnix || d.createdAtUnix;
  if (!Number(unix)) return "";
  return formatAge(unix);
}

function draftTimestampFull(d: DraftRow): string {
  const unix = Number(d.decidedAtUnix || d.createdAtUnix);
  if (!unix) return "";
  return new Date(unix * 1000).toLocaleString();
}

/** One conversation card: incoming message (if any) + the drafted reply. */
function DraftCard({ draft }: { draft: DraftRow }) {
  return (
    <li className="surface-card space-y-2.5 p-3.5">
      <div className="flex items-center justify-between gap-2">
        <span className="flex min-w-0 items-center gap-1.5 font-mono text-[12px] text-muted-foreground">
          {draft.targetUser ? (
            <>
              <ArrowUp className="size-3 shrink-0 text-muted-foreground/60" aria-hidden />
              <span className="truncate" title={`Reply to @${draft.targetUser}`}>
                to @{draft.targetUser}
              </span>
            </>
          ) : (
            <span className="truncate">{draft.channelId}</span>
          )}
        </span>
        <div className="flex shrink-0 items-center gap-2">
          <DraftPill status={draft.status} />
          <span
            className="font-mono text-[11px] tabular-nums text-muted-foreground/70"
            title={draftTimestampFull(draft)}
          >
            {draftTimestamp(draft)}
          </span>
        </div>
      </div>

      {draft.incomingText && (
        <div className="flex gap-2">
          <ArrowDown className="mt-0.5 size-3 shrink-0 text-muted-foreground/50" aria-hidden />
          <p className="min-w-0 flex-1 rounded-[8px] rounded-tl-[3px] bg-muted/50 px-2.5 py-1.5 text-[12.5px] leading-relaxed text-muted-foreground">
            {draft.incomingText}
          </p>
        </div>
      )}
      <p
        className={cn(
          "whitespace-pre-wrap rounded-[8px] rounded-tl-[3px] px-2.5 py-1.5 text-[13px] leading-6",
          "bg-[color:var(--color-primary)]/8 ring-1 ring-inset ring-[color:var(--color-primary)]/15",
          draft.status === "dismissed" && "opacity-60",
        )}
      >
        {draft.draftText}
      </p>
    </li>
  );
}

export function SlackAgentDetail() {
  const { namespace, name } = useParams<{ namespace: string; name: string }>();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const tab = searchParams.get("tab") === "settings" ? "settings" : "overview";
  const { runs, loading: runsLoading } = useAgentRuns(namespace || "", name || "", "SlackAgent");
  const [agent, setAgent] = useState<SlackAgent | null>(null);
  const [drafts, setDrafts] = useState<DraftRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<DraftFilter>("");
  const [query, setQuery] = useState("");
  const [copied, setCopied] = useState(false);
  const [manifestOpen, setManifestOpen] = useState(false);

  const epochRef = useRef(0);
  const load = useCallback(
    async (mode: "initial" | "refresh" = "initial") => {
      const epoch = ++epochRef.current;
      if (mode === "refresh") setRefreshing(true);
      else setLoading(true);
      try {
        const [agentsResp, draftsResp] = await Promise.all([
          client.listSlackAgents({}),
          client.listSlackDrafts({ name: name ?? "", limit: 200 }),
        ]);
        if (epochRef.current !== epoch) return;
        const match = (agentsResp.agents ?? []).find((a) => a.name === name) ?? null;
        setAgent(match);
        setDrafts((draftsResp.drafts ?? []) as unknown as DraftRow[]);
        setError(null);
      } catch (err) {
        if (epochRef.current !== epoch) return;
        setError(err instanceof Error ? err.message : "Failed to load Slack agent");
      } finally {
        if (epochRef.current === epoch) {
          setLoading(false);
          if (mode === "refresh") setRefreshing(false);
        }
      }
    },
    [name],
  );

  useEffect(() => {
    // Async loader flips `loading` synchronously before awaiting; epoch guard
    // prevents cascading renders from stale responses.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  const counts = useMemo(() => {
    const c = { pending: 0, sent: 0, dismissed: 0, total: drafts.length };
    for (const d of drafts) {
      if (d.status === "pending") c.pending += 1;
      else if (d.status === "sent") c.sent += 1;
      else if (d.status === "dismissed") c.dismissed += 1;
    }
    return c;
  }, [drafts]);

  const visible = useMemo(() => {
    const byStatus = filter ? drafts.filter((d) => d.status === filter) : drafts;
    const byQuery = filterByQuery(byStatus, query, (d) => [
      d.targetUser,
      d.channelId,
      d.incomingText,
      d.draftText,
    ]);
    return [...byQuery].sort(
      (a, b) =>
        Number(b.decidedAtUnix || b.createdAtUnix) - Number(a.decidedAtUnix || a.createdAtUnix),
    );
  }, [drafts, filter, query]);

  const draftFilters = useMemo(
    () =>
      [
        { value: "" as const, label: `All${counts.total ? ` ${counts.total}` : ""}` },
        {
          value: "pending" as const,
          label: `Pending${counts.pending ? ` ${counts.pending}` : ""}`,
        },
        { value: "sent" as const, label: `Sent${counts.sent ? ` ${counts.sent}` : ""}` },
        {
          value: "dismissed" as const,
          label: `Dismissed${counts.dismissed ? ` ${counts.dismissed}` : ""}`,
        },
      ],
    [counts],
  );

  const manifest = useMemo(
    () => buildSlackManifest(agent?.name ?? name ?? "My Agent"),
    [agent, name],
  );

  const copyManifest = useCallback(() => {
    void navigator.clipboard?.writeText(manifest).then(() => {
      setCopied(true);
      toast.success("Manifest copied to clipboard");
      window.setTimeout(() => setCopied(false), 1500);
    });
  }, [manifest]);

  const status = agent ? slackAgentStatus(agent) : null;

  return (
    <ListState
      loading={loading}
      error={error}
      empty={!agent}
      skeleton={<ListRowSkeleton rows={4} />}
      emptyTitle="Slack agent not found"
      emptyDescription="This Slack agent may have been removed or you may not have access."
    >
      {agent && (
        <div className="space-y-7">
          <DetailHeader
            parentLabel="Slack"
            parentTo="/slack"
            title={agent.name}
            meta={
              status && (
                <span
                  className={cn(
                    "inline-flex h-5 w-fit items-center gap-1.5 rounded-full pl-1.5 pr-2 text-[11px] font-medium select-none",
                    toneSoft[status.tone],
                  )}
                >
                  <span
                    className={cn(
                      "relative inline-flex size-1.5 rounded-full bg-current",
                    )}
                  >
                    {agent.connected && !agent.suspended && (
                      <span className="absolute inset-0 rounded-full bg-current opacity-60 motion-safe:animate-ping" />
                    )}
                  </span>
                  {status.label}
                </span>
              )
            }
            actions={
              <>
                {tab === "overview" && (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => void load("refresh")}
                    disabled={refreshing}
                    aria-label="Refresh drafts"
                  >
                    <RefreshCw
                      data-icon="inline-start"
                      className={cn(refreshing && "motion-safe:animate-spin")}
                    />
                    Refresh
                  </Button>
                )}
                <SegmentedControl
                  size="sm"
                  value={tab}
                  onChange={(next) =>
                    setSearchParams(next === "settings" ? { tab: "settings" } : {}, {
                      replace: true,
                    })
                  }
                  options={[
                    { value: "overview", label: "Overview" },
                    { value: "settings", label: "Settings" },
                  ]}
                />
              </>
            }
          />

          {tab === "settings" ? (
            <SlackAgentSettings
              agent={agent}
              onSaved={() => void load("refresh")}
              onDeleted={() => navigate("/slack")}
            />
          ) : (
          <>
          <StatBar>
            <Stat
              label="Pending"
              mono={false}
              value={
                <span className={counts.pending > 0 ? toneText.warning : undefined}>
                  {counts.pending}
                </span>
              }
              sub={counts.pending > 0 ? "awaiting your approval in Slack" : "nothing waiting"}
            />
            <Stat label="Sent" mono={false} value={counts.sent} />
            <Stat label="Dismissed" mono={false} value={counts.dismissed} />
            <Stat
              label="Model"
              mono
              value={<span className="text-[13px]">{agent.model || "—"}</span>}
              sub={agent.provider}
            />
          </StatBar>

          {agent.lastError && (
            <div
              className="rounded-md border border-[color-mix(in_oklch,var(--tone-danger)_30%,transparent)] bg-[color-mix(in_oklch,var(--tone-danger)_10%,transparent)] px-3 py-2.5 text-[12.5px] text-[color:var(--tone-danger-fg)]"
              role="alert"
            >
              {agent.lastError}
            </div>
          )}

          <DetailSection
            title="Held replies"
            description="Replies the agent proposed for channels and group DMs, held for your approval. Approve or dismiss them from Slack — this is a read-only audit trail."
            aside={
              <SegmentedControl size="sm" value={filter} onChange={setFilter} options={draftFilters} />
            }
          >
            {drafts.length > 0 && (
              <ListSearchInput
                value={query}
                onChange={setQuery}
                placeholder="Search drafts…"
                className="w-[240px]"
                ariaLabel="Search drafts"
              />
            )}
            {visible.length === 0 ? (
              <div className="surface-card flex flex-col items-center justify-center gap-2.5 rounded-xl border border-border/60 bg-muted/20 px-6 py-12 text-center">
                <div className="flex size-11 items-center justify-center rounded-full bg-muted/60 text-muted-foreground/70 ring-1 ring-inset ring-border/60">
                  <Inbox className="size-5" />
                </div>
                <p className="text-[14px] font-medium text-foreground">
                  {drafts.length === 0 ? "No held replies yet" : "No matching drafts"}
                </p>
                <p className="max-w-[48ch] text-[12.5px] leading-relaxed text-muted-foreground">
                  {drafts.length === 0
                    ? "When a reply in a channel or group DM needs your approval, it shows up here."
                    : "Try a different filter or clear the search."}
                </p>
              </div>
            ) : (
              <ul className="space-y-2.5">
                {visible.map((d) => (
                  <DraftCard key={d.id} draft={d} />
                ))}
              </ul>
            )}
          </DetailSection>

          <RunsSection count={runs.length} loading={runsLoading}>
            <AgentRunTable
              runs={runs}
              loading={runsLoading}
              emptyMessage="No runs yet. Conversations with this agent in Slack will show up here."
              sourceFallbackLabel="Slack"
              sourceAriaLabel="Slack conversation source"
              viewKey={`slack:${namespace}/${name}`}
            />
          </RunsSection>

          <DetailSection title="Configuration">
            <FactList>
              <Fact
                label="Connection"
                value={
                  agent.workspaceRefName
                    ? `Shared workspace app · ${agent.workspaceRefName}`
                    : "Dedicated app"
                }
              />
              <Fact label="Team" value={agent.teamId} mono />
              <Fact label="Bot user" value={agent.botUserId} mono />
              <Fact label="Owner user" value={agent.slackUserId} mono />
              <Fact label="Model" value={agent.model} mono />
              <Fact label="Provider" value={agent.provider} />
              <Fact
                label="Channel replies"
                value={agent.channelReplyMode === "auto" ? "Post directly" : "Require approval"}
              />
              <Fact
                label="Memory window"
                value={agent.sessionIdleMinutes > 0 ? `${agent.sessionIdleMinutes} min` : "12h (default)"}
              />
            </FactList>
          </DetailSection>

          {!agent.workspaceRefName && (
          <DetailSection
            title="Slack app manifest"
            description="Everything the connector needs — Socket Mode, the assistant pane, the App Home tab, and Block Kit interactivity. Create the app from this manifest, then paste its tokens into the agent configuration."
            aside={
              <div className="flex items-center gap-1.5">
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => void openExternal("https://api.slack.com/apps?new_app=1")}
                >
                  <ExternalLink data-icon="inline-start" />
                  api.slack.com/apps
                </Button>
                <Button variant="outline" size="xs" onClick={copyManifest}>
                  {copied ? (
                    <Check data-icon="inline-start" className={toneText.success} />
                  ) : (
                    <Copy data-icon="inline-start" />
                  )}
                  {copied ? "Copied" : "Copy manifest"}
                </Button>
              </div>
            }
          >
            <ol className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[11.5px] text-muted-foreground">
              <li className="flex items-center gap-1.5">
                <span className="grid size-4 place-items-center rounded-full bg-muted/70 font-mono text-[9.5px] text-muted-foreground ring-1 ring-inset ring-border/60">1</span>
                Copy the manifest
              </li>
              <ChevronRight className="size-3 text-muted-foreground/40" aria-hidden />
              <li className="flex items-center gap-1.5">
                <span className="grid size-4 place-items-center rounded-full bg-muted/70 font-mono text-[9.5px] text-muted-foreground ring-1 ring-inset ring-border/60">2</span>
                Create New App → From a manifest
              </li>
              <ChevronRight className="size-3 text-muted-foreground/40" aria-hidden />
              <li className="flex items-center gap-1.5">
                <span className="grid size-4 place-items-center rounded-full bg-muted/70 font-mono text-[9.5px] text-muted-foreground ring-1 ring-inset ring-border/60">3</span>
                Install to workspace &amp; paste the tokens
              </li>
            </ol>

            <Collapsible open={manifestOpen} onOpenChange={setManifestOpen}>
              <div className="overflow-hidden rounded-md border">
                <CollapsibleTrigger
                  render={
                    <button
                      type="button"
                      className="flex w-full items-center gap-2 bg-muted/40 px-3 py-2 text-left text-[12px] text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
                    />
                  }
                >
                  <ChevronRight
                    className={cn(
                      "size-3.5 shrink-0 transition-transform duration-[var(--dur-fast)]",
                      manifestOpen && "rotate-90",
                    )}
                  />
                  <span className="font-medium">manifest.yaml</span>
                  <span className="ml-auto font-mono text-[10.5px] text-muted-foreground/60">
                    bot scopes only
                  </span>
                </CollapsibleTrigger>
                <CollapsibleContent>
                  <pre className="max-h-96 overflow-auto border-t bg-muted/20 p-3 text-[11.5px] leading-relaxed">
                    <code>{manifest}</code>
                  </pre>
                </CollapsibleContent>
              </div>
            </Collapsible>
          </DetailSection>
          )}
          </>
          )}
        </div>
      )}
    </ListState>
  );
}
