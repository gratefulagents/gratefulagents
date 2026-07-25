import { useMemo, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { ArrowRight, ArrowUpRight, ChevronRight, GitBranch, Share2 } from "lucide-react";

import { AgentRunTable } from "@/components/AgentRunTable";
import { CreateRunDialog } from "@/components/CreateRunDialog";
import { MaintainerCard } from "@/components/MaintainerPanel";
import { ProjectCredentialBadges } from "@/components/projectCredentials";
import { ProjectContentSection } from "@/components/project-content/ProjectContentSection";
import {
  EntryPointsPreview,
  ProjectEntryPoints,
} from "@/components/project-triggers/ProjectEntryPoints";
import type {
  ProjectTrigger as ProjectTriggerModel,
  ProjectWithTriggers,
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
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  DetailHeader,
  DetailSection,
  Fact,
  FactLink,
  FactList,
  RunCountSummary,
  RunsSection,
} from "@/components/detail-page";
import { useProjects } from "@/hooks/useWatchedList";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { formatAge, formatCount, formatRepoShort, formatSuccessRate } from "@/lib/format";
import { useScrollEdgeFade } from "@/hooks/useScrollEdgeFade";
import { cn } from "@/lib/utils";
import type { AgentRun, Project, ProjectMetrics } from "@/rpc/platform/service_pb";

/**
 * Project detail page.
 *
 * Layout contract, so every band on this page reads as one document:
 *   - Every section starts on the same left edge; nothing indents itself.
 *   - Structure comes from hairlines and alignment, never from nested cards.
 *     The stat grid and the activity list are separated by `border-y` rules
 *     only, which keeps the page flat while still giving rows real columns.
 *   - Hierarchy is carried by weight and color, not size: section headings are
 *     `font-semibold text-foreground`, everything secondary is
 *     `text-muted-foreground`. Nothing dimmer than that, so headings always
 *     outrank the timestamps underneath them.
 */

const TAB_VALUES = ["overview", "runs", "entry-points", "maintainer", "files", "configuration"] as const;
type ProjectTab = (typeof TAB_VALUES)[number];

function isProjectTab(value: string | null): value is ProjectTab {
  return TAB_VALUES.includes(value as ProjectTab);
}

function triggersOf(project: Project): ProjectTriggerModel[] {
  return (project as unknown as ProjectWithTriggers).triggers ?? [];
}

export function ProjectDetail() {
  const { namespace, name } = useParams<{ namespace: string; name: string }>();
  const { projects, loading, error, refetch } = useProjects();
  const { runs, loading: runsLoading } = useAgentRuns(namespace || "", name || "", "Project");
  const [shareOpen, setShareOpen] = useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  // Six project tabs need ~567px; a phone leaves ~358px, so the trailing
  // tabs scrolled off with no cue.
  const [projectTabsRef, projectTabsFadeStyle] = useScrollEdgeFade<HTMLDivElement>();

  const project = projects.find((p) => p.namespace === namespace && p.name === name);
  const canEdit = project?.myPermission !== "viewer";
  const canShare = project?.myPermission === "owner" || project?.myPermission === "admin";
  const triggers = project ? triggersOf(project) : [];
  const maintainerTriggers = maintainerTriggersOf(triggers);

  const rawTab = searchParams.get("tab");
  const requestedTab: ProjectTab = isProjectTab(rawTab) ? rawTab : "overview";
  const tab = requestedTab === "maintainer" && maintainerTriggers.length === 0
    ? "overview"
    : requestedTab;
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
        <div className="flex flex-col gap-5">
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
                    <Badge variant="outline" className="text-[11px]">
                      {project.myPermission}
                    </Badge>
                  )}
                {project.kubernetesAdmin && (
                  <Badge variant="secondary" className="text-[11px]">
                    Kubernetes admin
                  </Badge>
                )}
              </>
            }
            subtitle={<ProjectIdentity project={project} />}
            actions={
              <>
                {canShare && (
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
                    key={`${project.namespace}/${project.name}`}
                    defaultSource={project.name}
                    defaultNamespace={project.namespace}
                  />
                )}
              </>
            }
          />

          <ProjectStats metrics={project.metrics} />

          <Tabs value={tab} onValueChange={(value) => setTab(value as ProjectTab)}>
            <TabsList
              ref={projectTabsRef}
              style={projectTabsFadeStyle}
              variant="line"
              className="w-full justify-start gap-5 overflow-x-auto border-b border-border/60"
            >
              <TabsTrigger value="overview" className="flex-none px-0.5">
                Overview
              </TabsTrigger>
              <TabsTrigger value="runs" className="flex-none px-0.5">
                Runs
                <TabCount value={runs.length} />
              </TabsTrigger>
              <TabsTrigger value="entry-points" className="flex-none px-0.5">
                Entry points
                <TabCount value={triggers.length + 1} />
              </TabsTrigger>
              {maintainerTriggers.length > 0 && (
                <TabsTrigger value="maintainer" className="flex-none px-0.5">
                  Maintainer
                </TabsTrigger>
              )}
              <TabsTrigger value="files" className="flex-none px-0.5">
                Files
              </TabsTrigger>
              <TabsTrigger value="configuration" className="flex-none px-0.5">
                Configuration
              </TabsTrigger>
            </TabsList>

            <TabsContent value="overview" className="pt-6">
              <div className="flex flex-col gap-8">
                <RecentActivity
                  runs={runs}
                  loading={runsLoading}
                  canEdit={canEdit}
                  onViewAll={() => setTab("runs")}
                />
                <EntryPointsPreview
                  namespace={project.namespace}
                  projectName={project.name}
                  triggers={triggers}
                  onManage={() => setTab("entry-points")}
                />
              </div>
            </TabsContent>

            <TabsContent value="runs" className="pt-6">
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

            <TabsContent value="entry-points" className="pt-6">
              <ProjectEntryPoints
                namespace={project.namespace}
                projectName={project.name}
                triggers={triggers}
                canEdit={canEdit}
                onChanged={() => void refetch()}
              />
            </TabsContent>

            {maintainerTriggers.length > 0 && (
              <TabsContent value="maintainer" className="pt-6">
                <ProjectMaintainerSection
                  namespace={project.namespace}
                  triggers={maintainerTriggers}
                />
              </TabsContent>
            )}

            <TabsContent value="files" className="pt-6">
              <ProjectContentSection
                namespace={project.namespace}
                projectName={project.name}
                canEdit={canEdit}
              />
            </TabsContent>

            <TabsContent value="configuration" className="pt-6">
              <ProjectConfiguration project={project} />
            </TabsContent>
          </Tabs>
        </div>
      )}
    </ListState>
  );
}

/**
 * Namespace / name / repository, on their own line under the title. Keeping
 * this off the title baseline stops the avatar and repo slug from reading as
 * trailing title text.
 */
function ProjectIdentity({ project }: { project: Project }) {
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[12px] text-muted-foreground">
      <span className="truncate">
        {project.namespace}/{project.name}
      </span>
      {project.repoUrl && (
        <>
          <span aria-hidden className="text-border">
            |
          </span>
          <a
            href={project.repoUrl}
            target="_blank"
            rel="noopener noreferrer"
            title={project.repoUrl}
            className="inline-flex max-w-[320px] items-center gap-1.5 rounded-sm underline-offset-2 transition-colors hover:text-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
          >
            <GitBranch className="size-3.5 shrink-0" aria-hidden />
            <span className="truncate">{formatRepoShort(project.repoUrl)}</span>
          </a>
        </>
      )}
    </div>
  );
}

/** Count badge on a tab. Rendered as a real chip so it cannot read as a stray superscript. */
function TabCount({ value }: { value: number }) {
  if (value <= 0) return null;
  return (
    <span className="ml-1.5 rounded-full bg-muted px-1.5 py-px font-mono text-[11px] leading-4 tabular-nums text-foreground/75">
      {value}
    </span>
  );
}

/**
 * Metrics band. A real grid rather than a flex strip, so the numbers span the
 * full content width and stay column-aligned at every breakpoint. The `gap-px`
 * over a hairline background draws the dividers, which keeps the band flat and
 * avoids one bordered card per number.
 */
function ProjectStats({ metrics }: { metrics?: ProjectMetrics }) {
  const inputTokens = Number(metrics?.totalInputTokens ?? 0n);
  const outputTokens = Number(metrics?.totalOutputTokens ?? 0n);
  const totalRuns = metrics?.totalRuns ?? 0;
  const lastRunAt = metrics?.lastRunAtUnix ?? 0n;

  return (
    <dl
      className={cn(
        "grid grid-cols-2 gap-px border-y border-border/60 bg-border/60 sm:grid-cols-3 lg:grid-cols-5",
        // The first cell of every row sits flush with the page's left edge so
        // the band starts on the same axis as the title and the tabs. Which
        // cell that is changes with the column count, hence one rule per
        // breakpoint.
        "[&>*:nth-child(2n+1)]:pl-0",
        "sm:[&>*:nth-child(2n+1)]:pl-4 sm:[&>*:nth-child(3n+1)]:pl-0",
        "lg:[&>*:nth-child(3n+1)]:pl-4 lg:[&>*:nth-child(5n+1)]:pl-0",
      )}
    >
      <StatCell
        label="Total cost"
        value={`$${(metrics?.totalCostUsd ?? 0).toFixed(2)}`}
        sub={totalRuns > 0 ? `$${(metrics?.averageCostPerRun ?? 0).toFixed(3)} per run` : undefined}
      />
      <StatCell
        label="Tokens"
        value={formatCount(inputTokens + outputTokens)}
        sub={`${formatCount(inputTokens)} in · ${formatCount(outputTokens)} out`}
      />
      <StatCell
        label="Runs"
        value={totalRuns}
        mono={false}
        sub={
          <RunCountSummary
            success={metrics?.successfulRuns ?? 0}
            failed={metrics?.failedRuns ?? 0}
            running={metrics?.runningRuns ?? 0}
          />
        }
      />
      <StatCell
        label="Tool calls"
        value={formatCount(metrics?.totalToolCalls ?? 0)}
        sub={lastRunAt !== 0n ? `last run ${formatAge(lastRunAt)} ago` : undefined}
      />
      <StatCell
        label="Success rate"
        mono={false}
        value={formatSuccessRate(metrics?.successfulRuns ?? 0, metrics?.failedRuns ?? 0)}
        sub={totalRuns > 0 ? `across ${totalRuns} runs` : undefined}
      />
    </dl>
  );
}

function StatCell({
  label,
  value,
  sub,
  mono = true,
}: {
  label: string;
  value: React.ReactNode;
  sub?: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="flex flex-col gap-1.5 bg-background px-4 py-3">
      <dt className="text-[11px] font-medium uppercase tracking-[0.08em] text-muted-foreground">
        {label}
      </dt>
      <dd
        className={cn(
          "text-[21px] font-semibold leading-none tracking-tight tabular-nums text-foreground",
          mono && "font-mono",
        )}
      >
        {value}
      </dd>
      <dd className="min-h-[16px] text-[11.5px] leading-tight text-muted-foreground">
        {sub ?? ""}
      </dd>
    </div>
  );
}

/** Shared heading row for the Overview sections: strong title, quiet action. */
function SectionHeader({
  id,
  title,
  action,
}: {
  id: string;
  title: string;
  action?: React.ReactNode;
}) {
  return (
    <div className="flex h-7 items-center justify-between gap-3">
      <h2 id={id} className="text-[14px] font-semibold tracking-[-0.01em] text-foreground">
        {title}
      </h2>
      {action}
    </div>
  );
}

/**
 * Column template shared by the activity list's header and its rows, so the
 * two can never drift out of alignment.
 */
const ACTIVITY_COLUMNS =
  "grid grid-cols-[minmax(0,1fr)_auto] gap-x-4 gap-y-1 sm:grid-cols-[minmax(0,22rem)_minmax(0,1fr)_auto]";

/**
 * The five most recent runs, as a real list: a labelled header row over
 * hairline-separated rows with fixed columns. The header is what stops the
 * status and timestamp from reading as values floating in empty space, and it
 * names the unit for the age column.
 */
function RecentActivity({
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
    () => [...runs].sort((a, b) => Number(b.createdAtUnix - a.createdAtUnix)).slice(0, 5),
    [runs],
  );

  return (
    <section className="flex flex-col gap-2" aria-labelledby="recent-activity">
      <SectionHeader
        id="recent-activity"
        title="Recent activity"
        action={
          runs.length > 0 ? (
            <Button variant="ghost" size="xs" onClick={onViewAll}>
              All runs
              <ArrowRight data-icon="inline-end" />
            </Button>
          ) : undefined
        }
      />

      {loading && recent.length === 0 ? (
        <ListRowSkeleton rows={3} />
      ) : recent.length === 0 ? (
        <p className="border-y border-border/60 py-6 text-[13px] text-muted-foreground">
          {canEdit
            ? "No runs yet. Start one from an entry point or with New Run."
            : "No runs yet."}
        </p>
      ) : (
        <div className="border-y border-border/60">
          <div
            aria-hidden
            className={cn(
              ACTIVITY_COLUMNS,
              "hidden border-b border-border/60 py-1.5 text-[10.5px] font-medium uppercase tracking-[0.08em] text-muted-foreground sm:grid",
            )}
          >
            <span>Run</span>
            <span>Source</span>
            <span className="flex items-center gap-3">
              <span className="w-[104px]">Status</span>
              <span className="w-10 text-right">Age</span>
            </span>
          </div>
          <ul className="divide-y divide-border/60">
            {recent.map((run) => (
              <li key={`${run.namespace}/${run.name}`}>
                <RecentActivityRow run={run} />
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

/**
 * Label for the activity list's Source column: how this run started.
 *
 * Only trigger identity is consulted. `workflowMode` and `model` are run
 * settings rather than provenance — and `workflowMode` is reported as a
 * constant "auto" by the dashboard adapter — so falling back to either would
 * put a value in this column that misidentifies how the run started.
 */
function runSource(run: AgentRun): string {
  const trigger = run.trigger;
  if (!trigger) return "";
  // The concrete external reference (issue, PR, ticket) is the most specific
  // answer when the trigger has one.
  if (trigger.externalIdentifier) return trigger.externalIdentifier;
  // "manual" is the reserved entry point for runs started from the dashboard;
  // name it the way the Entry points section does.
  if (trigger.type === "manual" || trigger.name === "manual") return "Dashboard";
  // Otherwise the project-local trigger identity, then its provenance.
  return trigger.name || trigger.type || "";
}

function RecentActivityRow({ run }: { run: AgentRun }) {
  return (
    <Link
      to={`/runs/${run.namespace}/${run.name}`}
      className={cn(
        ACTIVITY_COLUMNS,
        "group items-center py-2.5 outline-none transition-colors hover:bg-muted/40 focus-visible:ring-2 focus-visible:ring-ring/60",
      )}
    >
      <span className="min-w-0 truncate text-[13px] font-medium text-foreground">
        {run.displayName || run.intentTitle || run.name}
      </span>

      <span className="col-span-2 min-w-0 truncate font-mono text-[11.5px] text-muted-foreground sm:col-span-1">
        {runSource(run)}
      </span>

      {/* Status sits in a fixed-width, left-aligned slot so pills of different
          widths still line up on a common left edge down the column. */}
      <span className="flex items-center gap-3 sm:col-start-3 sm:row-start-1">
        <span className="flex w-[104px] justify-start">
          <StatusBadge phase={run.phase} run={run} />
        </span>
        <span className="w-10 text-right font-mono text-[11.5px] tabular-nums text-muted-foreground group-hover:hidden">
          {formatAge(run.createdAtUnix)}
        </span>
        <span className="hidden w-10 justify-end group-hover:flex">
          <ArrowUpRight className="size-3.5 text-muted-foreground" aria-hidden />
        </span>
      </span>
    </Link>
  );
}

function maintainerTriggersOf(triggers: ProjectTriggerModel[]): ProjectTriggerModel[] {
  return triggers.filter(
    (trigger) =>
      trigger.type === "github" &&
      // A disabled project trigger tears down its generated runtime, so its
      // maintainer cannot run — don't present it as active.
      trigger.enabled !== false &&
      Boolean(trigger.github?.maintainerEnabled),
  );
}

/** Standing maintainer(s) for the project's enabled GitHub triggers. */
function ProjectMaintainerSection({
  namespace,
  triggers,
}: {
  namespace: string;
  triggers: ProjectTriggerModel[];
}) {
  return (
    <section className="flex flex-col gap-2" aria-labelledby="maintainer">
      <SectionHeader id="maintainer" title="Maintainer" />
      <div className="flex flex-col gap-4">
        {triggers.map((trigger) => (
          <div key={trigger.name} className="flex flex-col gap-1.5">
            {triggers.length > 1 && (
              <p className="font-mono text-[11.5px] text-muted-foreground">{trigger.name}</p>
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
              fullControl={Boolean(trigger.github?.maintainerFullControl)}
              repositoryName={trigger.generatedResourceName}
            />
          </div>
        ))}
      </div>
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
          label="Additional repos"
          value={
            project.additionalRepoUrls.length > 0 ? (
              <span className="flex flex-wrap gap-x-3 gap-y-0.5">
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
        <Fact label="Base branch" value={project.baseBranch} mono />
        <Fact label="Timeout" value={project.timeout} mono />
        <Fact label="Credentials" value={<ProjectCredentialBadges project={project} />} />
      </FactList>

      <Collapsible open={advancedOpen} onOpenChange={setAdvancedOpen}>
        <CollapsibleTrigger
          render={
            <button
              type="button"
              className="group -mx-1 flex items-center gap-1.5 rounded-sm px-1 py-1 text-[12px] text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
            />
          }
        >
          <ChevronRight
            className={cn(
              "size-3.5 shrink-0 transition-transform duration-[var(--dur-fast)]",
              advancedOpen && "rotate-90",
            )}
            aria-hidden
          />
          <span className="font-medium">Advanced</span>
          <span className="font-mono text-[11px] text-muted-foreground">
            runtime · policy · instructions{advancedCount > 0 ? ` · ${advancedCount} set` : ""}
          </span>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <FactList className="pt-2 pl-5">
            <Fact label="Allowed models" value={project.allowedModels.join(", ")} mono />
            <Fact label="RuntimeProfile" value={project.runtimeProfileRef} mono />
            <Fact label="Permission mode" value={project.permissionMode} mono />
            <Fact label="Network egress" value={project.egressMode} mono />
            <Fact label="MCPPolicy" value={project.mcpPolicyRef} mono />
            <Fact label="MCP default" value={project.mcpPolicyDefaultAction} mono />
            <Fact label="MCP servers" value={project.mcpPolicyAllowedServers.join(", ")} mono />
            <Fact label="Custom instructions" value={project.customInstructions} wrap />
          </FactList>
        </CollapsibleContent>
      </Collapsible>
    </DetailSection>
  );
}
