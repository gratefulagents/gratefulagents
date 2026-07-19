import { useMemo, useState } from "react";
import { useParams } from "react-router-dom";
import { ChevronRight, GitBranch, Share2 } from "lucide-react";

import { AgentRunTable } from "@/components/AgentRunTable";
import { CreateRunDialog } from "@/components/CreateRunDialog";
import { ProjectCredentialBadges } from "@/components/projectCredentials";
import { ProjectContentSection } from "@/components/project-content/ProjectContentSection";
import { ProjectTriggerRail } from "@/components/project-triggers/ProjectTriggerRail";
import type { ProjectWithTriggers } from "@/components/project-triggers/types";
import { ProjectSettingsDialog } from "@/components/ProjectSettingsDialog";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { ShareDialog } from "@/components/ShareDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
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
import { useProjects } from "@/hooks/useWatchedList";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { formatAge, formatCount, formatRepoShort, formatSuccessRate } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { Project } from "@/rpc/platform/service_pb";

export function ProjectDetail() {
  const { namespace, name } = useParams<{ namespace: string; name: string }>();
  const { projects, loading, error, refetch } = useProjects();
  const { runs, loading: runsLoading } = useAgentRuns(namespace || "", name || "", "Project");
  const [shareOpen, setShareOpen] = useState(false);

  const project = projects.find((p) => p.namespace === namespace && p.name === name);
  const metrics = project?.metrics;
  const canEdit = project?.myPermission !== "viewer";

  return (
    <ListState
      loading={loading}
      error={error}
      empty={!project}
      skeleton={<ListRowSkeleton rows={4} />}
      emptyTitle="Project not found"
      emptyDescription="This project may have been removed or you may not have access."
    >
      {project && (
        <div className="space-y-7">
          <DetailHeader
            parentLabel="Projects"
            parentTo="/projects"
            title={project.displayName || project.name}
            meta={
              <>
                {project.owner && <OwnerAvatar owner={project.owner} />}
                {project.myPermission &&
                  project.myPermission !== "owner" &&
                  project.myPermission !== "admin" && (
                    <Badge variant="outline" className="text-xs">
                      {project.myPermission}
                    </Badge>
                  )}
                {project.kubernetesAdmin && (
                  <Badge variant="secondary" className="text-xs">
                    Kubernetes admin
                  </Badge>
                )}
                {project.repoUrl && (
                  <a
                    href={project.repoUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    title={project.repoUrl}
                    className="inline-flex max-w-[280px] items-center gap-1 truncate font-mono text-[11.5px] text-muted-foreground transition-colors hover:text-foreground"
                  >
                    <GitBranch className="size-3 shrink-0" aria-hidden />
                    <span className="truncate">{formatRepoShort(project.repoUrl)}</span>
                  </a>
                )}
              </>
            }
            actions={
              <>
                {(project.myPermission === "owner" || project.myPermission === "admin") && (
                  <>
                    <Button variant="outline" size="sm" onClick={() => setShareOpen(true)}>
                      <Share2 data-icon="inline-start" />
                      Share
                    </Button>
                    <ShareDialog
                      resourceType="project"
                      resourceId={project.name}
                      resourceNamespace={project.namespace}
                      open={shareOpen}
                      onOpenChange={setShareOpen}
                    />
                  </>
                )}
                {canEdit && (
                  <ProjectSettingsDialog project={project} onUpdated={() => void refetch()} />
                )}
                {canEdit && (
                  <CreateRunDialog
                    defaultSource={project.name}
                    defaultNamespace={project.namespace}
                  />
                )}
              </>
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
              value={metrics?.totalRuns ?? 0}
              mono={false}
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

          <ProjectTriggerRail
            namespace={project.namespace}
            projectName={project.name}
            triggers={(project as unknown as ProjectWithTriggers).triggers ?? []}
            canEdit={canEdit}
            onChanged={() => void refetch()}
          />

          <ProjectContentSection
            namespace={project.namespace}
            projectName={project.name}
            canEdit={canEdit}
          />

          <ProjectConfiguration project={project} />

          <RunsSection count={runs.length} loading={runsLoading}>
            <AgentRunTable
              runs={runs}
              loading={runsLoading}
              emptyMessage="No runs yet — start a chat from Home or create a plan."
              sourceFallbackLabel="Issue"
              sourceAriaLabel="Source issue (opens in new tab)"
              viewKey={`project:${namespace}/${name}`}
            />
          </RunsSection>
        </div>
      )}
    </ListState>
  );
}

/**
 * Configuration facts: the daily-relevant ones inline, the plumbing
 * (runtime profile, MCP policy, custom instructions) behind a collapsible.
 */
function ProjectConfiguration({ project }: { project: Project }) {
  const [advancedOpen, setAdvancedOpen] = useState(false);

  const advancedCount = useMemo(
    () =>
      [
        project.runtimeProfileRef,
        project.permissionMode,
        project.egressMode,
        project.mcpPolicyRef,
        project.mcpPolicyDefaultAction,
        project.mcpPolicyAllowedServers.length > 0
          ? project.mcpPolicyAllowedServers.join(", ")
          : "",
        project.allowedModels.length > 0 ? project.allowedModels.join(", ") : "",
        project.customInstructions,
      ].filter(Boolean).length,
    [project],
  );

  return (
    <DetailSection title="Configuration">
      <FactList>
        <Fact
          label="Repository"
          value={
            project.repoUrl ? (
              <FactLink href={project.repoUrl}>{formatRepoShort(project.repoUrl)}</FactLink>
            ) : null
          }
        />
        <Fact
          label="Additional Repos"
          value={
            project.additionalRepoUrls.length > 0 ? (
              <span className="flex flex-wrap gap-x-2 gap-y-0.5">
                {project.additionalRepoUrls.map((url) => (
                  <FactLink key={url} href={url}>
                    {formatRepoShort(url)}
                  </FactLink>
                ))}
              </span>
            ) : null
          }
        />
        <Fact label="Provider" value={project.provider || "openai"} mono />
        <Fact label="Model" value={project.model} mono />
        <Fact label="Reasoning" value={project.reasoningLevel} mono />
        <Fact label="Base Branch" value={project.baseBranch} mono />
        <Fact label="Timeout" value={project.timeout} mono />
        <Fact label="Credentials" value={<ProjectCredentialBadges project={project} />} />
      </FactList>

      <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
        <CollapsibleTrigger
          render={
            <button
              type="button"
              className="group -mx-1 flex items-center gap-1.5 rounded-sm px-1 py-1 text-[12px] text-muted-foreground transition-colors hover:text-foreground"
            />
          }
        >
          <ChevronRight
            className={cn(
              "size-3.5 shrink-0 transition-transform duration-[var(--dur-fast)]",
              advancedOpen && "rotate-90",
            )}
          />
          <span className="font-medium">Advanced</span>
          <span className="font-mono text-[10.5px] text-muted-foreground/60">
            runtime · policy · instructions{advancedCount > 0 ? ` · ${advancedCount} set` : ""}
          </span>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <FactList className="pt-2 pl-5">
            <Fact label="Allowed Models" value={project.allowedModels.join(", ")} mono />
            <Fact label="RuntimeProfile" value={project.runtimeProfileRef} mono />
            <Fact label="Permission Mode" value={project.permissionMode} mono />
            <Fact label="Network Egress" value={project.egressMode} mono />
            <Fact label="MCPPolicy" value={project.mcpPolicyRef} mono />
            <Fact label="MCP Default" value={project.mcpPolicyDefaultAction} mono />
            <Fact label="MCP Servers" value={project.mcpPolicyAllowedServers.join(", ")} mono />
            <Fact label="Custom Instructions" value={project.customInstructions} wrap />
          </FactList>
        </CollapsibleContent>
      </Collapsible>
    </DetailSection>
  );
}
