import { useParams, useSearchParams } from "react-router-dom";
import { useState } from "react";
import { Settings, Share2 } from "lucide-react";

import { AgentRunTable } from "@/components/AgentRunTable";
import { TriggerDefaultsDialog } from "@/components/TriggerDefaultsDialog";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ReadyBadge } from "@/components/ReadyBadge";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { ShareDialog } from "@/components/ShareDialog";
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
} from "@/components/detail-page";
import { useLinearProjects } from "@/hooks/useWatchedList";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import { formatAge, formatCount, formatPollTime, formatRepoShort, formatSuccessRate } from "@/lib/format";
import { useNow } from "@/hooks/useNow";

export function LinearProjectDetail() {
  const { namespace, name } = useParams<{ namespace: string; name: string }>();
  const { projects, loading: projectLoading, error: projectError, refetch } = useLinearProjects();
  const { runs, loading: runsLoading } = useAgentRuns(namespace || "", name || "", "LinearProject");
  const [searchParams, setSearchParams] = useSearchParams();
  const activeTab = (searchParams.get("tab") as "runs" | "instructions") || "runs";
  const setActiveTab = (tab: "runs" | "instructions") => {
    setSearchParams(tab === "runs" ? {} : { tab }, { replace: true });
  };
  const [instructionsDraft, setInstructionsDraft] = useState<{ key: string; value: string } | null>(null);
  const [instructionsSaving, setInstructionsSaving] = useState(false);
  const [instructionsSaved, setInstructionsSaved] = useState(false);
  const [instructionsError, setInstructionsError] = useState("");
  const [shareOpen, setShareOpen] = useState(false);
  const now = useNow();
  const projectKey = `${namespace || ""}/${name || ""}`;

  const project = projects.find((p) => p.namespace === namespace && p.name === name);
  const metrics = project?.metrics;

  const instructions =
    instructionsDraft?.key === projectKey
      ? instructionsDraft.value
      : project?.customInstructions || "";

  async function handleSaveInstructions() {
    if (!namespace || !name) return;
    setInstructionsSaving(true);
    setInstructionsSaved(false);
    setInstructionsError("");
    try {
      await client.updateLinearProjectInstructions({ namespace, name, customInstructions: instructions });
      setInstructionsSaved(true);
      setTimeout(() => setInstructionsSaved(false), 2000);
    } catch (err) {
      setInstructionsError(err instanceof Error ? err.message : "Failed to save instructions");
    } finally {
      setInstructionsSaving(false);
    }
  }

  return (
    <ListState
      loading={projectLoading}
      error={projectError}
      empty={!project}
      skeleton={<ListRowSkeleton rows={4} />}
      emptyTitle="Project not found"
      emptyDescription="This Linear project may have been removed or you may not have access."
    >
      {project && (
        <div className="space-y-7">
          <DetailHeader
            parentLabel="Linear"
            parentTo="/linear"
            title={project.name}
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
                <ReadyBadge status={project.conditionReady} />
              </>
            }
            actions={
              (project.myPermission === "owner" || project.myPermission === "admin") && (
                <>
                  <Button variant="outline" size="sm" onClick={() => setShareOpen(true)}>
                    <Share2 data-icon="inline-start" />
                    Share
                  </Button>
                  <ShareDialog
                    resourceType="linear_project"
                    resourceId={project.name}
                    resourceNamespace={project.namespace}
                    open={shareOpen}
                    onOpenChange={setShareOpen}
                  />
                  <TriggerDefaultsDialog
                    source={project}
                    idPrefix="linear-defaults"
                    title={`Run defaults — ${project.name}`}
                    description="Defaults applied to runs this Linear project triggers. Saving replaces the current run defaults."
                    trigger={
                      <Button variant="outline" size="sm">
                        <Settings data-icon="inline-start" />
                        Run defaults
                      </Button>
                    }
                    onSubmit={async (defaults, policies, useSavedCredentials) => {
                      await client.updateLinearProject({
                        namespace: project.namespace,
                        name: project.name,
                        defaults,
                        policies,
                        useSavedCredentials,
                      });
                      void refetch();
                    }}
                  />
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
              <Fact label="Project ID" value={project.projectId} mono />
              <Fact
                label="Repository"
                value={
                  project.repoUrl ? (
                    <FactLink href={project.repoUrl}>{formatRepoShort(project.repoUrl)}</FactLink>
                  ) : null
                }
              />
              <Fact label="Provider" value={project.provider || "anthropic"} mono />
              <Fact label="Issues Processed" value={String(project.issuesProcessed)} />
              <Fact label="Last Poll" value={formatPollTime(project.lastPollTimeUnix, now)} />
            </FactList>
          </DetailSection>

          <div>
            <div className="mb-4 flex items-center gap-1 border-b border-border" role="tablist">
              {(["runs", "instructions"] as const).map((tab) => (
                <button
                  key={tab}
                  type="button"
                  role="tab"
                  aria-selected={activeTab === tab}
                  aria-controls={`tabpanel-${tab}`}
                  id={`tab-${tab}`}
                  className={cn(
                    "-mb-px border-b-2 px-4 py-2 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 rounded-t-sm",
                    activeTab === tab
                      ? "border-[color:var(--color-primary)] text-foreground"
                      : "border-transparent text-muted-foreground hover:text-foreground",
                  )}
                  onClick={() => setActiveTab(tab)}
                >
                  {{ runs: "Runs", instructions: "Instructions" }[tab]}
                  {tab === "runs" && !runsLoading && runs.length > 0 && (
                    <span className="ml-2 text-xs text-muted-foreground tabular-nums">{runs.length}</span>
                  )}
                </button>
              ))}
            </div>

            {activeTab === "runs" && (
              <div id="tabpanel-runs" role="tabpanel" aria-labelledby="tab-runs">
                <AgentRunTable
                  runs={runs}
                  loading={runsLoading}
                  emptyMessage="No runs yet. Create one to get started."
                  sourceFallbackLabel="Issue"
                  sourceAriaLabel="Source issue (opens in new tab)"
                  viewKey={`linear:${namespace}/${name}`}
                />
              </div>
            )}

            {activeTab === "instructions" && (
              <div id="tabpanel-instructions" role="tabpanel" aria-labelledby="tab-instructions">
                <DetailSection
                  title="Custom Instructions"
                  description="These instructions are prepended to CLAUDE.md for every run in this project. Use this for team coding standards, framework-specific rules, or architecture guidelines. The repo's own CLAUDE.md can override these."
                >
                  <div className="space-y-4">
                    <textarea
                      className="min-h-[300px] w-full resize-y rounded-md border border-border bg-muted/40 p-3 font-mono text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
                      placeholder={"# Project Instructions\n\n## Coding Standards\n- Use Go 1.22+ features\n- Follow existing patterns in the codebase\n- Always run `make lint` before committing\n\n## Architecture\n- All new endpoints must have OpenAPI docs\n- Database migrations use golang-migrate"}
                      value={instructions}
                      onChange={(e) => {
                        setInstructionsDraft({ key: projectKey, value: e.target.value });
                        setInstructionsSaved(false);
                        setInstructionsError("");
                      }}
                    />
                    <div className="flex flex-wrap items-center gap-3">
                      <Button onClick={handleSaveInstructions} disabled={instructionsSaving}>
                        {instructionsSaving ? "Saving..." : "Save Instructions"}
                      </Button>
                      {instructionsSaved && (
                        <span className={cn("text-sm", toneText.success)}>Saved</span>
                      )}
                      {instructionsError && (
                        <p role="alert" className="text-[12.5px] text-destructive">
                          {instructionsError}
                        </p>
                      )}
                    </div>
                  </div>
                </DetailSection>
              </div>
            )}
          </div>
        </div>
      )}
    </ListState>
  );
}
