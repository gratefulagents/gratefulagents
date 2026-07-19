import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { create } from "@bufbuild/protobuf";
import { AlertTriangle, Pause, Pencil, Play, Share2, Trash2 } from "lucide-react";

import { AgentRunTable } from "@/components/AgentRunTable";
import { ReadyBadge } from "@/components/ReadyBadge";
import { CronFormDialog } from "@/components/CronFormDialog";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { ShareDialog } from "@/components/ShareDialog";
import {
  buildCronRequest,
  cronToDefaults,
  cronUsesSavedCredentials,
} from "@/components/run-defaults/helpers";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { ListState, ListRowSkeleton } from "@/components/ui/list-state";
import {
  DetailHeader,
  DetailSection,
  StatBar,
  Stat,
  FactList,
  Fact,
  FactLink,
  RunsSection,
} from "@/components/detail-page";
import { useCrons } from "@/hooks/useWatchedList";
import { useAgentRuns } from "@/hooks/useAgentRuns";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";
import { formatCount, formatRepoShort, formatScheduleTime } from "@/lib/format";
import { useNow } from "@/hooks/useNow";
import { UpdateCronRequestSchema, type Cron } from "@/rpc/platform/service_pb";

export function CronDetail() {
  const { namespace, name } = useParams<{ namespace: string; name: string }>();
  const navigate = useNavigate();
  const { crons, loading, error } = useCrons();
  const { runs, loading: runsLoading } = useAgentRuns(namespace || "", name || "", "Cron");
  const now = useNow();
  const [deleteOpen, setDeleteOpen] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);
  const [toggling, setToggling] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

  const cron = crons.find((item) => item.namespace === namespace && item.name === name);

  async function toggleSuspend(target: Cron) {
    setActionError(null);
    setToggling(true);
    try {
      await client.updateCron(
        create(
          UpdateCronRequestSchema,
          buildCronRequest({
            namespace: target.namespace,
            name: target.name,
            schedule: target.schedule,
            timeZone: target.timeZone,
            suspend: !target.suspend,
            concurrencyPolicy: target.concurrencyPolicy,
            prompt: target.prompt,
            defaults: cronToDefaults(target),
            useSavedCredentials: cronUsesSavedCredentials(target),
          }),
        ),
      );
    } catch (err) {
      setActionError(err instanceof Error ? err.message : "Failed to update cron trigger");
    } finally {
      setToggling(false);
    }
  }

  async function handleDelete(target: Cron) {
    setActionError(null);
    try {
      await client.deleteCron({ namespace: target.namespace, name: target.name });
      navigate("/cron");
    } catch (err) {
      setActionError(err instanceof Error ? err.message : "Failed to delete cron trigger");
    }
  }

  return (
    <ListState
      loading={loading}
      error={error}
      empty={!cron}
      skeleton={<ListRowSkeleton rows={4} />}
      emptyTitle="Cron trigger not found"
      emptyDescription="This cron trigger may have been removed or you may not have access."
    >
      {cron && (
        <div className="space-y-7">
          <DetailHeader
            parentLabel="Cron Triggers"
            parentTo="/cron"
            title={cron.name}
            meta={
              <>
                {cron.owner && <OwnerAvatar owner={cron.owner} />}
                {cron.myPermission &&
                  cron.myPermission !== "owner" &&
                  cron.myPermission !== "admin" && (
                    <Badge variant="outline" className="text-xs">
                      {cron.myPermission}
                    </Badge>
                  )}
                {cron.suspend ? (
                  <Badge variant="secondary">Suspended</Badge>
                ) : (
                  <ReadyBadge status={cron.conditionReady} />
                )}
              </>
            }
            actions={
              <>
                {(cron.myPermission === "owner" || cron.myPermission === "admin") && (
                  <>
                    <Button variant="outline" size="sm" onClick={() => setShareOpen(true)}>
                      <Share2 data-icon="inline-start" />
                      Share
                    </Button>
                    <ShareDialog
                      resourceType="cron"
                      resourceId={cron.name}
                      resourceNamespace={cron.namespace}
                      open={shareOpen}
                      onOpenChange={setShareOpen}
                    />
                  </>
                )}
                <Button
                  variant="outline"
                  size="sm"
                  disabled={toggling}
                  onClick={() => void toggleSuspend(cron)}
                >
                  {cron.suspend ? <Play className="size-3.5" /> : <Pause className="size-3.5" />}
                  {cron.suspend ? "Resume" : "Suspend"}
                </Button>
                <CronFormDialog
                  key={`${cron.namespace}/${cron.name}/${cron.schedule}/${cron.prompt}`}
                  cron={cron}
                  trigger={
                    <Button variant="outline" size="sm">
                      <Pencil className="size-3.5" />
                      Edit
                    </Button>
                  }
                />
                <Button variant="destructive" size="sm" onClick={() => setDeleteOpen(true)}>
                  <Trash2 className="size-3.5" />
                  Delete
                </Button>
              </>
            }
          />

          {actionError && (
            <p role="alert" className={cn("text-sm", toneText.danger)}>
              {actionError}
            </p>
          )}

          <ConfirmDialog
            open={deleteOpen}
            onOpenChange={setDeleteOpen}
            title={`Delete ${cron.name}?`}
            description="This permanently removes the cron trigger. Existing runs are kept."
            confirmLabel="Delete"
            destructive
            onConfirm={() => handleDelete(cron)}
          />

          <StatBar>
            <Stat label="Runs Created" mono={false} value={cron.runsCreated} />
            <Stat label="Total Cost" value={`$${(cron.metrics?.totalCostUsd ?? 0).toFixed(2)}`} />
            <Stat
              label="Tokens Used"
              value={formatCount(
                Number(cron.metrics?.totalInputTokens ?? 0n) +
                  Number(cron.metrics?.totalOutputTokens ?? 0n),
              )}
            />
          </StatBar>

          {cron.lastError && (
            <div
              className="flex items-start gap-2 rounded-md border border-[color-mix(in_oklch,var(--tone-danger)_30%,transparent)] bg-[color-mix(in_oklch,var(--tone-danger)_10%,transparent)] px-3 py-2.5 text-[12.5px] text-[color:var(--tone-danger-fg)]"
              role="alert"
            >
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" aria-hidden />
              <p>{cron.lastError}</p>
            </div>
          )}

          <DetailSection title="Configuration">
            <FactList>
              <Fact label="Next Schedule" value={formatScheduleTime(cron.nextScheduleTimeUnix, now)} />
              <Fact label="Schedule" value={`${cron.schedule} (${cron.timeZone || "UTC"})`} mono />
              <Fact label="Last Schedule" value={formatScheduleTime(cron.lastScheduleTimeUnix, now)} />
              <Fact label="Last Run" value={cron.lastRunName} mono />
              <Fact
                label="Repository"
                value={
                  cron.repoUrl ? (
                    <FactLink href={cron.repoUrl}>{formatRepoShort(cron.repoUrl)}</FactLink>
                  ) : null
                }
              />
              <Fact label="Base Branch" value={cron.baseBranch || "main"} mono />
              <Fact label="Provider" value={cron.provider || "openai"} mono />
              <Fact label="Model" value={cron.model} mono />
            </FactList>
          </DetailSection>

          <DetailSection title="Prompt">
            <p className="max-w-[88ch] whitespace-pre-wrap text-[13px] leading-relaxed">
              {cron.prompt}
            </p>
          </DetailSection>

          <RunsSection count={runs.length} loading={runsLoading}>
            <AgentRunTable
              runs={runs}
              loading={runsLoading}
              emptyMessage="No runs yet."
              sourceFallbackLabel="Schedule"
              sourceAriaLabel="Scheduled run source"
              viewKey={`cron:${namespace}/${name}`}
            />
          </RunsSection>
        </div>
      )}
    </ListState>
  );
}
