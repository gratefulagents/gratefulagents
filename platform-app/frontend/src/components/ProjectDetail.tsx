import { useMemo, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import {
  ArrowRight,
  ArrowUpRight,
  ChevronRight,
  GitBranch,
  MessageSquarePlus,
  Share2,
} from "lucide-react";

import { AgentRunTable } from "@/components/AgentRunTable";
import { CreateRunDialog } from "@/components/CreateRunDialog";
import { MaintainerCard } from "@/components/MaintainerPanel";
import { ProjectCredentialBadges } from "@/components/projectCredentials";
import { ProjectContentSection } from "@/components/project-content/ProjectContentSection";
import {
  EntryPointsPreview,
  ProjectEntryPoints,
} from "@/components/project-triggers/ProjectEntryPoints";
import type { ProjectWithTriggers } from "@/components/project-triggers/types";
import type {
  ProjectTrigger as ProjectTriggerModel,
} from "@/components/project-triggers/types";
import { ProjectSettingsDialog } from "@/components/ProjectSettingsDialog";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { ShareDialog } from "@/components/ShareDialog";
import { StatusBadge } from "@/components/StatusBadge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import {
  Item,
  ItemActions,
  ItemContent,
  ItemGroup,
  ItemMedia,
  ItemTitle,
} from "@/components/ui/item";
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
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
import type { AgentRun, Project } from "@/rpc/platform/service_pb";

const TAB_VALUES = ["overview", "runs", "entry-points", "files", "configuration"] as const;
type ProjectTab = (typeof TAB_VALUES)[number];

function isProjectTab(value: string | null): value is ProjectTab {
  return TAB_VALUES.includes(value as ProjectTab);
}

export function ProjectDetail() {
  const { namespace, name } = useParams<{ namespace: string; name: string }>();
  const { projects, loading, error, refetch } = useProjects();
  const { runs, loading: runsLoading } = useAgentRuns(namespace || "", name || "", "Project");
  const [shareOpen, setShareOpen] = useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  const rawTab = searchParams.get("tab");
  const tab: ProjectTab = isProjectTab(rawTab) ? rawTab : "overview";
  const setTab = (next: ProjectTab) => {
    setSearchParams(
      (prev) => {
        if (next === "overview") prev.delete("tab");
        else prev.set("tab", next);
        return prev;
      },
      { replace: true },
    );
  };

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
        <div className="space-y-6">
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

          <Tabs value={tab} onValueChange={(value) => setTab(value as ProjectTab)}>
            <TabsList
              variant="line"
              className="w-full justify-start gap-4 overflow-x-auto border-b border-border/60 pb-1"
            >
              <TabsTrigger value="overview" className="flex-none px-0.5">
                Overview
              </TabsTrigger>
              <TabsTrigger value="runs" className="flex-none px-0.5">
                Runs
                {runs.length > 0 && (
                  <span className="font-mono text-[10.5px] tabular-nums text-muted-foreground">
                    {runs.length}
                  </span>
                )}
              </TabsTrigger>
              <TabsTrigger value="entry-points" className="flex-none px-0.5">
                Entry points
                <span className="font-mono text-[10.5px] tabular-nums text-muted-foreground">
                  {((project as unknown as ProjectWithTriggers).triggers?.length ?? 0) + 1}
                </span>
              </TabsTrigger>
              <TabsTrigger value="files" className="flex-none px-0.5">
                Files
              </TabsTrigger>
              <TabsTrigger value="configuration" className="flex-none px-0.5">
                Configuration
              </TabsTrigger>
            </TabsList>

            <TabsContent value="overview" className="pt-4">
              <div className="space-y-7">
                <RecentRunsPreview
                  runs={runs}
                  loading={runsLoading}
                  canEdit={canEdit}
                  onViewAll={() => setTab("runs")}
                />

                <EntryPointsPreview
                  namespace={project.namespace}
                  projectName={project.name}
                  triggers={(project as unknown as ProjectWithTriggers).triggers ?? []}
                  onManage={() => setTab("entry-points")}
                />

                <ProjectMaintainerSection
                  namespace={project.namespace}
                  triggers={(project as unknown as ProjectWithTriggers).triggers ?? []}
                />
              </div>
            </TabsContent>

            <TabsContent value="entry-points" className="pt-4">
              <ProjectEntryPoints
                namespace={project.namespace}
                projectName={project.name}
                triggers={(project as unknown as ProjectWithTriggers).triggers ?? []}
                canEdit={canEdit}
                onChanged={() => void refetch()}
              />
            </TabsContent>

            <TabsContent value="runs" className="pt-4">
              <RunsSection count={runs.length} loading={runsLoading}>
                <AgentRunTable
                  runs={runs}
                  loading={runsLoading}
                  emptyMessage={
                    canEdit
                      ? "No runs yet. Start one from an entry point or with New Run."
                      : "No runs yet."
                  }
                  sourceFallbackLabel="Issue"
                  sourceAriaLabel="Source issue (opens in new tab)"
                  viewKey={`project:${namespace}/${name}`}
                />
              </RunsSection>
            </TabsContent>

            <TabsContent value="files" className="pt-4">
              <ProjectContentSection
                namespace={project.namespace}
                projectName={project.name}
                canEdit={canEdit}
              />
            </TabsContent>

            <TabsContent value="configuration" className="pt-4">
              <ProjectConfiguration project={project} />
            </TabsContent>
          </Tabs>
        </div>
      )}
    </ListState>
  );
}

/**
 * Standing maintainer(s) for the project's GitHub triggers. Rendered only when
 * at least one github trigger opted into the maintainer, so the Overview stays
 * quiet for projects without one.
 */
function ProjectMaintainerSection({
  namespace,
  triggers,
}: {
  namespace: string;
  triggers: ProjectTriggerModel[];
}) {
  const maintainerTriggers = triggers.filter(
    (trigger) =>
      trigger.type === "github" &&
      // A disabled project trigger tears down its generated runtime, so its
      // maintainer cannot run — don't present it as active.
      trigger.enabled !== false &&
      Boolean(trigger.github?.maintainerEnabled),
  );
  if (maintainerTriggers.length === 0) return null;

  return (
    <DetailSection title="Maintainer">
      <div className="space-y-4">
        {maintainerTriggers.map((trigger) => (
          <div key={trigger.name} className="space-y-1.5">
            {maintainerTriggers.length > 1 && (
              <p className="text-[10.5px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
                {trigger.name}
              </p>
            )}
            <MaintainerCard
              namespace={namespace}
              enabled
              maintainer={trigger.maintainerStatus}
              maxDispatchesPerDay={
                typeof trigger.github?.maintainerMaxDispatchesPerDay === "number"
                  ? trigger.github.maintainerMaxDispatchesPerDay
                  : undefined
              }
              allowPrMerge={Boolean(trigger.github?.maintainerAllowPrMerge)}
            />
          </div>
        ))}
      </div>
    </DetailSection>
  );
}

/** Latest few runs, so the Overview answers "what is happening here?" at a glance. */
function RecentRunsPreview({
  runs,
  loading,
  canEdit,
  onViewAll,
}: {
  runs: AgentRun[];
  loading: boolean;
  canEdit: boolean;
  onViewAll: () => void;
}) {
  const recent = useMemo(
    () =>
      [...runs]
        .sort((a, b) => Number(b.createdAtUnix - a.createdAtUnix))
        .slice(0, 5),
    [runs],
  );

  return (
    <section className="flex flex-col gap-2" aria-label="Recent activity">
      <div className="flex h-7 items-center justify-between">
        <h2 className="text-[13px] font-medium text-muted-foreground">Recent activity</h2>
        {runs.length > 0 && (
          <Button
            variant="ghost"
            size="xs"
            className="text-muted-foreground"
            onClick={onViewAll}
          >
            All runs
            <ArrowRight data-icon="inline-end" />
          </Button>
        )}
      </div>
      {loading && recent.length === 0 ? (
        <ListRowSkeleton rows={3} />
      ) : recent.length === 0 ? (
        <p className="rounded-md border border-dashed px-4 py-5 text-[13px] text-muted-foreground">
          {canEdit
            ? "No runs yet. Start one from an entry point or with New Run."
            : "No runs yet."}
        </p>
      ) : (
        <ItemGroup className="gap-1">
          {recent.map((r) => (
            <Item
              key={`${r.namespace}/${r.name}`}
              size="xs"
              render={<Link to={`/runs/${r.namespace}/${r.name}`} />}
            >
              <ItemMedia variant="icon">
                <MessageSquarePlus className="text-muted-foreground" />
              </ItemMedia>
              <ItemContent className="min-w-0">
                <ItemTitle>{r.displayName || r.intentTitle || r.name}</ItemTitle>
              </ItemContent>
              <ItemActions>
                <StatusBadge phase={r.phase} run={r} />
                <span className="w-9 text-right font-mono text-xs tabular-nums text-muted-foreground group-hover/item:hidden">
                  {formatAge(r.createdAtUnix)}
                </span>
                <span className="hidden w-9 justify-end group-hover/item:flex">
                  <ArrowUpRight className="size-3.5 text-muted-foreground" />
                </span>
              </ItemActions>
            </Item>
          ))}
        </ItemGroup>
      )}
    </section>
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
