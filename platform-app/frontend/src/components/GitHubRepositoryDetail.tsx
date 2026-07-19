import { useState } from "react";
import { useParams } from "react-router-dom";
import { Share2 } from "lucide-react";

import { AgentRunTable } from "@/components/AgentRunTable";
import { GitHubRepositorySettingsDialog } from "@/components/GitHubRepositorySettingsDialog";
import { MaintainerPanel } from "@/components/MaintainerPanel";
import { ReadyBadge } from "@/components/ReadyBadge";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { ShareDialog } from "@/components/ShareDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import {
  DetailHeader,
  DetailSection,
  StatBar,
  Stat,
  FactList,
  Fact,
  FactLink,
  RunCountSummary,
  RunsSection,
} from "@/components/detail-page";
import { useGitHubRepositories } from "@/hooks/useWatchedList";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { formatAge, formatCount, formatSuccessRate } from "@/lib/format";
import type { GitHubRepository } from "@/rpc/platform/service_pb";

function githubTriggerPollInterval(repo: GitHubRepository): string {
  return repo.triggerSettings?.pollInterval || "60s";
}

function githubTriggerAuthSummary(repo: GitHubRepository): string {
  const allowed = repo.triggerSettings?.authAllowedUsers ?? [];
  const denied = repo.triggerSettings?.authDenyUsers ?? [];
  const parts = [
    allowed.length ? `allow ${allowed.join(", ")}` : "owners, members, collaborators",
  ];
  if (denied.length) parts.push(`deny ${denied.join(", ")}`);
  return parts.join(" · ");
}

function githubReviewLoopSummary(repo: GitHubRepository): string {
  const settings = repo.triggerSettings;
  if (settings?.reviewLoopDisabled ?? true) return "Disabled";
  const rounds =
    settings?.reviewLoopMaxRounds && settings.reviewLoopMaxRounds > 0
      ? settings.reviewLoopMaxRounds
      : 3;
  const modeParts = [settings?.reviewerModeRef || "review"];
  if (settings?.reviewerModeVersion) modeParts.push(settings.reviewerModeVersion);
  if (settings?.reviewerModeChannel) modeParts.push(settings.reviewerModeChannel);
  const runtime = repo.reviewerDefaults?.model || "inherits run defaults";
  return `${rounds} round${rounds === 1 ? "" : "s"} · ${modeParts.join(" · ")} · ${runtime}`;
}

export function GitHubRepositoryDetail() {
  const { namespace, name } = useParams<{ namespace: string; name: string }>();
  const { repositories, loading, error, refetch } = useGitHubRepositories();
  const { runs, loading: runsLoading } = useAgentRuns(namespace || "", name || "", "GitHubRepository");
  const [shareOpen, setShareOpen] = useState(false);

  const repo = repositories.find((r) => r.namespace === namespace && r.name === name);
  const metrics = repo?.metrics;

  return (
    <ListState
      loading={loading}
      error={error}
      empty={!repo}
      skeleton={<ListRowSkeleton rows={4} />}
      emptyTitle="Repository not found"
      emptyDescription="This GitHub repository may have been removed or you may not have access."
    >
      {repo && (
        <div className="space-y-7">
          <DetailHeader
            parentLabel="GitHub Repositories"
            parentTo="/github"
            title={repo.name}
            meta={
              <>
                {repo.resourceOwner && <OwnerAvatar owner={repo.resourceOwner} />}
                {repo.myPermission &&
                  repo.myPermission !== "owner" &&
                  repo.myPermission !== "admin" && (
                    <Badge variant="outline" className="text-xs">
                      {repo.myPermission}
                    </Badge>
                  )}
                <ReadyBadge status={repo.conditionReady} />
              </>
            }
            actions={
              (repo.myPermission === "owner" || repo.myPermission === "admin") && (
                <>
                  <Button variant="outline" size="sm" onClick={() => setShareOpen(true)}>
                    <Share2 data-icon="inline-start" />
                    Share
                  </Button>
                  <ShareDialog
                    resourceType="github_repository"
                    resourceId={repo.name}
                    resourceNamespace={repo.namespace}
                    open={shareOpen}
                    onOpenChange={setShareOpen}
                  />
                  <GitHubRepositorySettingsDialog repo={repo} onUpdated={() => void refetch()} />
                </>
              )
            }
          />

          <StatBar>
            <Stat
              label="Total Cost"
              value={`$${(metrics?.totalCostUsd ?? 0).toFixed(2)}`}
              sub={
                (metrics?.totalRuns ?? 0) > 0
                  ? `avg $${(metrics?.averageCostPerRun ?? 0).toFixed(3)}/run`
                  : undefined
              }
            />
            <Stat
              label="Tokens Used"
              value={formatCount(
                Number(metrics?.totalInputTokens ?? 0n) +
                  Number(metrics?.totalOutputTokens ?? 0n),
              )}
              sub={`${formatCount(Number(metrics?.totalInputTokens ?? 0n))} in · ${formatCount(Number(metrics?.totalOutputTokens ?? 0n))} out`}
            />
            <Stat
              label="Runs"
              mono={false}
              value={metrics?.totalRuns ?? 0}
              sub={
                <RunCountSummary
                  success={metrics?.successfulRuns ?? 0}
                  failed={metrics?.failedRuns ?? 0}
                  running={metrics?.runningRuns ?? 0}
                />
              }
            />
            <Stat
              label="Tool Calls"
              value={formatCount(metrics?.totalToolCalls ?? 0)}
              sub={
                (metrics?.lastRunAtUnix ?? 0n) !== 0n
                  ? `last run ${formatAge(metrics!.lastRunAtUnix)} ago`
                  : undefined
              }
            />
            <Stat
              label="Success Rate"
              mono={false}
              value={formatSuccessRate(metrics?.successfulRuns ?? 0, metrics?.failedRuns ?? 0)}
            />
          </StatBar>

          <DetailSection title="Configuration">
            <FactList>
              <Fact
                label="Repository"
                value={
                  <FactLink href={`https://github.com/${repo.owner}/${repo.repo}`}>
                    {repo.owner}/{repo.repo}
                  </FactLink>
                }
              />
              <Fact label="Provider" value={repo.provider || "openai"} mono />
              <Fact label="Model" value={repo.model} mono />
              <Fact label="Trigger Keyword" value={repo.triggerSettings?.triggerKeyword || repo.triggerKeyword || "@agent"} mono />
              <Fact label="Poll Interval" value={githubTriggerPollInterval(repo)} mono />
              <Fact label="Webhook Secret" value={repo.triggerSettings?.webhookSecret || "None"} mono={Boolean(repo.triggerSettings?.webhookSecret)} />
              <Fact
                label="Cancel on Issue Close"
                value={repo.triggerSettings?.cancelRunsOnIssueClose ? "Enabled" : "Disabled"}
              />
              <Fact label="Trigger Auth" value={githubTriggerAuthSummary(repo)} />
              <Fact label="Pull-based PR Reviews" value={githubReviewLoopSummary(repo)} />
              <Fact label="Issues Processed" value={String(repo.issuesProcessed)} />
            </FactList>
          </DetailSection>

          <DetailSection title="Maintainer">
            <MaintainerPanel repo={repo} />
          </DetailSection>

          <RunsSection count={runs.length} loading={runsLoading}>
            <AgentRunTable
              runs={runs}
              loading={runsLoading}
              emptyMessage="No runs yet."
              sourceFallbackLabel="Issue"
              sourceAriaLabel="Source issue (opens in new tab)"
              viewKey={`github:${namespace}/${name}`}
            />
          </RunsSection>
        </div>
      )}
    </ListState>
  );
}
