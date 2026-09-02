import { lazy, Suspense, useState } from "react";
import { Link } from "react-router-dom";
import { AnimatePresence, motion } from "framer-motion";
import {
  Check,
  ChevronLeft,
  Clock,
  Download,
  FileText,
  GitPullRequest,
  MessageSquareReply,
  MoreHorizontal,
  PanelRight,
  Pencil,
  RotateCcw,
  Share2,
  Square,
  Trash2,
  X,
} from "lucide-react";

import { CreatePRDialog } from "@/components/CreatePRDialogButton";
import { ModeSwitcher } from "@/components/ModeSwitcher";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { PresenceAvatars } from "@/components/PresenceAvatars";
import { ShareDialog } from "@/components/ShareDialog";
import { StatusBadge } from "@/components/StatusBadge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { toast } from "@/components/ui/toaster";
import { binaryClient } from "@/lib/client";
import { downloadBlob } from "@/lib/download";
import { fade } from "@/lib/motion";
import { toneSoft, toneText, type StatusTone } from "@/lib/status";
import { isActionableInputType, runStatusLabel, visibleInputType } from "@/lib/runStatus";
import { pullRequestLabel } from "@/lib/pullRequests";
import type { TraceUsageSummary } from "@/lib/traceUsage";
import { cn } from "@/lib/utils";
import type { AgentRun, ResourceOwner } from "@/rpc/platform/service_pb";
import { runAuthLabel, splitRunModel } from "./RunModelSwitcher";
import { useRunActions } from "./RunActionsContext";
import { ExternalLink, fmtTokens, fmtUsd, PlanDialogContent, runtimeExtensionPresets, sourceHref } from "./helpers";
import { OverseerSettings } from "./OverseerSettings";
import { InspectorToggle } from "./RunInspector";
import { resolveRunUsageTokens } from "./runUsage";

const OverseerPresence = lazy(() =>
  import("./OverseerPresence").then((module) => ({ default: module.OverseerPresence })),
);

const permissionTone: Record<string, StatusTone> = {
  "read-only": "warning",
  "workspace-write": "success",
  "danger-full-access": "danger",
};

/** Icon buttons in the 48px header: 28px with a mouse, 36px for touch. */
const headerIconButton = "size-7 [@media(pointer:coarse)]:size-9";

/** Pill for the run's PR artifact — the same soft green as a succeeded status. */
const prPill = cn(
  "hidden h-6 shrink-0 items-center gap-1.5 rounded-full px-2.5 text-xs font-medium transition-colors hover:bg-tone-success/20 sm:inline-flex",
  toneSoft.success,
);

export interface RunHeaderProps {
  namespace: string;
  name: string;
  run: AgentRun;
  viewers: ResourceOwner[];
  /** Every PR opened by the run, most recent last. */
  prUrls: string[];
  showCreatePRButton: boolean;
  displayCostUsd: number | null | undefined;
  sessionMetrics: TraceUsageSummary | null;
  permissions: { isOwnerOrAdmin: boolean; isViewer: boolean };
  inspector: { open: boolean; onToggle: () => void; attention: boolean };
  plan: { hasPlan: boolean; planContent: string };
}

/** Inline rename affordance over the run's display name. */
function RunTitle({ run }: { run: AgentRun }) {
  const { rename } = useRunActions();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [saving, setSaving] = useState(false);

  async function submit() {
    const next = value.trim();
    if (saving) return;
    if (!next) {
      setEditing(false);
      return;
    }
    setSaving(true);
    try {
      await rename.run(next);
      setEditing(false);
    } finally {
      setSaving(false);
    }
  }

  if (editing) {
    return (
      <span className="flex min-w-0 items-center gap-1">
        <Input
          autoFocus
          value={value}
          onChange={(event) => setValue(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              void submit();
            } else if (event.key === "Escape") {
              event.preventDefault();
              setEditing(false);
            }
          }}
          disabled={saving}
          placeholder="Run name"
          aria-label="Run name"
          className="h-7 w-56 text-sm"
        />
        <button
          type="button"
          aria-label="Save name"
          onClick={() => void submit()}
          disabled={saving}
          className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
        >
          <Check className="size-4" />
        </button>
        <button
          type="button"
          aria-label="Cancel rename"
          onClick={() => setEditing(false)}
          disabled={saving}
          className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
        >
          <X className="size-4" />
        </button>
      </span>
    );
  }

  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <span
        className="truncate text-sm font-medium text-foreground"
        title={run.displayName ? run.name : undefined}
      >
        {run.displayName || run.name}
      </span>
      {rename.can && (
        <button
          type="button"
          aria-label="Rename run"
          onClick={() => {
            setValue(run.displayName || "");
            setEditing(true);
          }}
          // Always visible (touch has no hover), just quiet until pointed at.
          className="shrink-0 rounded-sm text-muted-foreground opacity-60 transition-opacity hover:opacity-100 focus-visible:opacity-100 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring"
        >
          <Pencil className="size-3.5" />
        </button>
      )}
    </span>
  );
}

function Fact({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 truncate text-right text-foreground">{value}</dd>
    </>
  );
}

export function RunUsageSummary({
  costUsd,
  inputTokens,
  outputTokens,
}: {
  costUsd: number | null | undefined;
  inputTokens: number;
  outputTokens: number;
}) {
  const cost = typeof costUsd === "number" && Number.isFinite(costUsd) ? `$${fmtUsd(costUsd)}` : "$—";
  const input = Number.isFinite(inputTokens) ? fmtTokens(inputTokens) : "—";
  const output = Number.isFinite(outputTokens) ? fmtTokens(outputTokens) : "—";

  return (
    <dl
      // The run title is the header's primary content, but this strip is
      // `shrink-0` and ~120px wide, which squeezed the title to a few
      // characters on phones. The same cost and token figures live in the
      // status chip popover, so drop the inline copy on narrow viewports.
      className="hidden shrink-0 items-center gap-2 whitespace-nowrap font-mono text-2xs tabular-nums text-muted-foreground sm:flex sm:gap-3"
      aria-label="Run usage"
    >
      <div className="flex items-baseline gap-1" title="Cost">
        <dt className="sr-only">Cost</dt>
        {/* A run that has spent nothing yet still has a known cost; only an
            unknown figure is hidden behind the dash. */}
        <dd className={costUsd === 0 ? "text-muted-foreground" : "text-foreground"}>{cost}</dd>
      </div>
      <div className="flex items-baseline gap-1" title="Input tokens">
        <dt className="text-3xs uppercase tracking-wide">In</dt>
        <dd className="text-foreground">{input}</dd>
      </div>
      <div className="flex items-baseline gap-1" title="Output tokens">
        <dt className="text-3xs uppercase tracking-wide">Out</dt>
        <dd className="text-foreground">{output}</dd>
      </div>
    </dl>
  );
}

/**
 * Clicking the status chip opens run details: mode, permissions, model,
 * token and cost meters, step, and overseer settings.
 */
function RunStatusChip({
  namespace,
  name,
  run,
  isViewer,
  isOwnerOrAdmin,
  displayCostUsd,
  sessionMetrics,
}: {
  namespace: string;
  name: string;
  run: AgentRun;
  isViewer: boolean;
  isOwnerOrAdmin: boolean;
  displayCostUsd: number | null | undefined;
  sessionMetrics: TraceUsageSummary | null;
}) {
  const label = runStatusLabel(run);
  const permMode = run.resolvedPermissionMode || "read-only";
  const current = splitRunModel(run.model || run.resolvedModel || "");
  const inputTokens = sessionMetrics?.hasUsage ? sessionMetrics.inputTokens : Number(run.inputTokens);
  const outputTokens = sessionMetrics?.hasUsage ? sessionMetrics.outputTokens : Number(run.outputTokens);
  const cacheReadTokens = sessionMetrics?.hasUsage ? sessionMetrics.cacheReadTokens : 0;

  return (
    <Popover>
      <PopoverTrigger
        aria-label={`Run status: ${label}. Open run details`}
        className="flex shrink-0 rounded-full transition-opacity hover:opacity-85 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring data-[popup-open]:opacity-85"
      >
        {/* Cross-fade the pill when the label changes so a phase flip reads
            as a change of state, not a flicker. */}
        <AnimatePresence mode="wait" initial={false}>
          <motion.span key={label} className="inline-flex" {...fade}>
            <StatusBadge phase={run.phase} run={run} />
          </motion.span>
        </AnimatePresence>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-80 gap-3 p-3">
        <dl className="grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-1.5 text-xs">
          {run.modeName && (
            <Fact
              label="Mode"
              value={
                isViewer ? (
                  run.modeName
                ) : (
                  <ModeSwitcher
                    namespace={namespace}
                    runName={name}
                    currentMode={run.modeName}
                    onSwitched={() => {}}
                    segment
                  />
                )
              }
            />
          )}
          <Fact
            label="Permissions"
            value={
              <span className={cn(toneText[permissionTone[permMode] ?? "neutral"])} title={
                permMode === "read-only"
                  ? "Sandbox is read-only. To allow file edits, set a runtime profile with workspace-write in the project settings."
                  : "Sandbox permission mode, from the project's runtime profile."
              }>
                {permMode}
              </span>
            }
          />
          <Fact label="Model" value={<span className="font-mono">{run.resolvedModel || run.model}</span>} />
          <Fact
            label="Provider"
            value={<span className="font-mono">{current.provider} · {runAuthLabel(run, current.provider)}</span>}
          />
          {run.sshTunnelRef && (
            <Fact
              label="SSH tunnel"
              value={
                <span
                  className="font-mono"
                  title="Model traffic goes through this SSHTunnel's per-run sidecar; it overrides the OpenAI base URL."
                >
                  {run.sshTunnelRef}
                </span>
              }
            />
          )}
          <Fact label="Reasoning" value={<span className="font-mono">{run.resolvedReasoningLevel || "default"}</span>} />
          <Fact label="Branch" value={<span className="font-mono">{run.baseBranch || "main"}</span>} />
          {run.maxRuntime && <Fact label="Max runtime" value={<span className="font-mono">{run.maxRuntime}</span>} />}
          {run.currentStep && <Fact label="Step" value={run.currentStep} />}
          {(inputTokens > 0 || outputTokens > 0) && (
            <Fact
              label="Tokens"
              value={
                <span className="font-mono tabular-nums">
                  {fmtTokens(inputTokens)}↓ {fmtTokens(outputTokens)}↑
                  {cacheReadTokens > 0 ? ` · ${fmtTokens(cacheReadTokens)} cached` : ""}
                </span>
              }
            />
          )}
          {typeof displayCostUsd === "number" && Number.isFinite(displayCostUsd) && (
            <Fact
              label="Cost"
              value={
                <span className={cn("font-mono tabular-nums", displayCostUsd === 0 && "text-muted-foreground")}>
                  ${fmtUsd(displayCostUsd)}
                </span>
              }
            />
          )}
          {run.trigger?.externalUrl && (
            <Fact
              label="Issue"
              value={
                <ExternalLink href={run.trigger.externalUrl} className="text-foreground hover:underline">
                  {run.trigger.externalIdentifier || "link"}
                </ExternalLink>
              }
            />
          )}
        </dl>
        <OverseerSettings run={run} canManage={isOwnerOrAdmin} />
      </PopoverContent>
    </Popover>
  );
}

/**
 * A single 48px row: where you are, what the run is doing, and the one action
 * that matters right now. Everything else is one click deep — the overflow
 * menu for run management, the inspector toggle for artifacts and diagnostics.
 */
export function RunHeader({
  namespace,
  name,
  run,
  viewers,
  prUrls,
  showCreatePRButton,
  displayCostUsd,
  sessionMetrics,
  permissions: { isOwnerOrAdmin, isViewer },
  inspector,
  plan: { hasPlan, planContent },
}: RunHeaderProps) {
  const { retry, stop, promote, delete: remove, extendRuntime, share, focusComposer, openInspectorTab } =
    useRunActions();
  const [planOpen, setPlanOpen] = useState(false);
  const [createPROpen, setCreatePROpen] = useState(false);
  const [exporting, setExporting] = useState(false);

  const handleExportArchive = async () => {
    setExporting(true);
    try {
      const resp = await binaryClient.exportAgentRunArchive({ namespace, name });
      downloadBlob(resp.filename || `${name}-export.zip`, resp.archive, "application/zip");
      toast.success("Export ready", { description: "Run logs & traces saved as a zip archive." });
    } catch (e) {
      toast.error("Couldn't export logs & traces", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setExporting(false);
    }
  };

  const canCreatePR = showCreatePRButton && !isViewer;
  const inputType = visibleInputType(run);
  const awaitingReply = !isViewer && isActionableInputType(inputType);
  // Exactly one inline action. Answer the agent when it is waiting on you,
  // otherwise wrap the run up, recover a failed one, unblock a paused one, or
  // stop a live one — in that order. Everything not chosen stays reachable in
  // the overflow menu.
  const primaryAction: "reply" | "promote" | "retry" | "extend" | "stop" | null = awaitingReply
    ? "reply"
    : promote.can
      ? "promote"
      : retry.can
        ? "retry"
        : extendRuntime.can && extendRuntime.isPaused
          ? "extend"
          : stop.can
            ? "stop"
            : null;
  const replyLabel = inputType === "plan_review" ? "Review plan" : "Reply";
  const { inputTokens, outputTokens } = resolveRunUsageTokens(
    run.inputTokens,
    run.outputTokens,
    sessionMetrics,
  );

  const sourceName = run.project?.name || run.trigger?.name || "";
  const sourceKind = run.project?.kind || run.trigger?.kind || "";
  const overseerRunName = run.overseerSummary?.runName.trim();
  const overseerHref = overseerRunName
    ? `/runs/${encodeURIComponent(namespace)}/${encodeURIComponent(overseerRunName)}`
    : undefined;

  return (
    <header className="flex h-12 shrink-0 items-center gap-2 border-b bg-background px-2 md:px-3">
      <Link
        to={sourceName ? sourceHref(sourceKind, namespace, sourceName) : "/projects"}
        aria-label={sourceName ? `Back to ${sourceName}` : "Back to projects"}
        title={sourceName || "Projects"}
        className={cn(
          "flex shrink-0 items-center justify-center rounded-[min(var(--radius-md),12px)] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring",
          headerIconButton,
        )}
      >
        <ChevronLeft className="size-4" />
      </Link>

      <div className="flex min-w-0 flex-1 items-center gap-2">
        <RunTitle run={run} />
        <RunStatusChip
          namespace={namespace}
          name={name}
          run={run}
          isViewer={isViewer}
          isOwnerOrAdmin={isOwnerOrAdmin}
          displayCostUsd={displayCostUsd}
          sessionMetrics={sessionMetrics}
        />
      </div>

      <RunUsageSummary
        costUsd={displayCostUsd}
        inputTokens={inputTokens}
        outputTokens={outputTokens}
      />

      <div className="flex shrink-0 items-center gap-1">
        {run.overseer && (
          <Suspense fallback={null}>
            <OverseerPresence run={run} href={overseerHref} />
          </Suspense>
        )}

        {/* The owner already has an avatar, so drop them from the presence
            list — otherwise viewing your own run shows you twice. */}
        <span className="hidden items-center gap-1.5 md:flex">
          {run.owner && <OwnerAvatar owner={run.owner} />}
          <PresenceAvatars
            viewers={viewers.filter((viewer) => viewer.userId !== run.owner?.userId)}
          />
        </span>

        {prUrls.length === 1 && (
          <ExternalLink href={prUrls[0]} className={prPill}>
            <GitPullRequest className="size-3.5" />
            Pull request
          </ExternalLink>
        )}
        {prUrls.length > 1 && (
          <DropdownMenu>
            <DropdownMenuTrigger className={prPill}>
              <GitPullRequest className="size-3.5" />
              {prUrls.length} pull requests
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-52">
              {prUrls.map((url) => (
                <DropdownMenuItem key={url} render={<ExternalLink href={url} />}>
                  <GitPullRequest className="size-3.5" />
                  <span className="truncate">{pullRequestLabel(url)}</span>
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        )}

        {/* Green belongs to state and artifacts in this header (status, PR),
            while actions stay neutral. A plain check reads as an action; a
            circled green check reads as if the run already succeeded. Leave a
            little extra space after a PR pill so artifact and action do not
            visually merge. The swap between actions cross-fades so the slot
            never blinks empty. */}
        <AnimatePresence mode="wait" initial={false}>
          {primaryAction && (
            <motion.span
              key={primaryAction}
              className={cn("hidden sm:inline-flex", prUrls.length > 0 && "ml-1")}
              {...fade}
            >
              {primaryAction === "reply" && (
                <Button size="sm" onClick={focusComposer}>
                  <MessageSquareReply />
                  {replyLabel}
                </Button>
              )}
              {primaryAction === "promote" && (
                <Button variant="outline" size="sm" onClick={promote.run} disabled={promote.busy}>
                  <Check />
                  {promote.busy ? "Marking…" : "Mark succeeded"}
                </Button>
              )}
              {primaryAction === "retry" && (
                <Button size="sm" onClick={retry.run} disabled={retry.busy}>
                  <RotateCcw />
                  {retry.busy ? "Retrying…" : "Retry"}
                </Button>
              )}
              {primaryAction === "extend" && (
                <Button size="sm" onClick={() => extendRuntime.setOpen(true)} disabled={extendRuntime.busy}>
                  <Clock />
                  Extend runtime
                </Button>
              )}
              {primaryAction === "stop" && (
                <Button variant="outline" size="sm" onClick={stop.run} disabled={stop.busy}>
                  <Square />
                  {stop.busy ? "Stopping…" : "Stop"}
                </Button>
              )}
            </motion.span>
          )}
        </AnimatePresence>

        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label="Run actions"
                className={cn(
                  "shrink-0 text-muted-foreground hover:text-foreground data-[popup-open]:text-foreground",
                  headerIconButton,
                )}
              />
            }
          >
            <MoreHorizontal />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-52">
            {awaitingReply && (
              <DropdownMenuItem className="sm:hidden" onClick={focusComposer}>
                <MessageSquareReply className="size-3.5" />
                {replyLabel}
              </DropdownMenuItem>
            )}
            {canCreatePR && (
              <DropdownMenuItem onClick={() => setCreatePROpen(true)}>
                <GitPullRequest className="size-3.5" />
                {prUrls.length > 0 ? "Create another PR…" : "Create PR…"}
              </DropdownMenuItem>
            )}
            {/* The PR pill is hidden on phones; the artifact stays reachable here. */}
            {prUrls.map((url) => (
              <DropdownMenuItem key={url} className="sm:hidden" render={<ExternalLink href={url} />}>
                <GitPullRequest className="size-3.5" />
                <span className="truncate">
                  {prUrls.length === 1 ? "Open pull request" : pullRequestLabel(url)}
                </span>
              </DropdownMenuItem>
            ))}
            {retry.can && (
              <DropdownMenuItem
                className={primaryAction === "retry" ? "sm:hidden" : undefined}
                onClick={retry.run}
                disabled={retry.busy}
              >
                <RotateCcw className="size-3.5" />
                {retry.busy ? "Retrying…" : "Retry run"}
              </DropdownMenuItem>
            )}
            {extendRuntime.can && (
              <DropdownMenuItem
                className={primaryAction === "extend" ? "sm:hidden" : undefined}
                onClick={() => extendRuntime.setOpen(true)}
                disabled={extendRuntime.busy}
              >
                <Clock className="size-3.5" />
                Extend runtime…
              </DropdownMenuItem>
            )}
            {stop.can && (
              <DropdownMenuItem
                className={primaryAction === "stop" ? "sm:hidden" : undefined}
                onClick={stop.run}
                disabled={stop.busy}
              >
                <Square className="size-3.5" />
                {stop.busy ? "Stopping…" : "Stop run"}
              </DropdownMenuItem>
            )}
            {promote.can && (
              <DropdownMenuItem
                className={primaryAction === "promote" ? "sm:hidden" : undefined}
                onClick={promote.run}
                disabled={promote.busy}
              >
                <Check className="size-3.5" />
                {promote.busy ? "Marking…" : "Mark succeeded"}
              </DropdownMenuItem>
            )}

            <DropdownMenuSeparator />
            {hasPlan && (
              <DropdownMenuItem onClick={() => setPlanOpen(true)}>
                <FileText className="size-3.5" />
                View plan
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onClick={() => openInspectorTab("context")}>
              <PanelRight className="size-3.5" />
              Run context
            </DropdownMenuItem>
            <DropdownMenuItem onClick={handleExportArchive} disabled={exporting}>
              <Download className="size-3.5" />
              {exporting ? "Exporting…" : "Export logs & traces"}
            </DropdownMenuItem>
            {isOwnerOrAdmin && (
              <DropdownMenuItem onClick={() => share.setOpen(true)}>
                <Share2 className="size-3.5" />
                Share…
              </DropdownMenuItem>
            )}
            {remove.can && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onClick={remove.run} disabled={remove.busy}>
                  <Trash2 className="size-3.5" />
                  {remove.busy ? "Deleting…" : "Delete run"}
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>

        <InspectorToggle open={inspector.open} onToggle={inspector.onToggle} attention={inspector.attention} />
      </div>

      {canCreatePR && (
        <CreatePRDialog namespace={namespace} name={name} open={createPROpen} onOpenChange={setCreatePROpen} />
      )}

      {hasPlan && (
        <Dialog open={planOpen} onOpenChange={setPlanOpen}>
          <PlanDialogContent planContent={planContent} />
        </Dialog>
      )}

      {isOwnerOrAdmin && (
        <ShareDialog
          resourceType="agent_run"
          resourceId={name}
          resourceNamespace={namespace}
          open={share.open}
          onOpenChange={share.setOpen}
        />
      )}

      <Dialog open={extendRuntime.open} onOpenChange={extendRuntime.setOpen}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle className="text-sm">Extend runtime</DialogTitle>
          </DialogHeader>
          <form className="space-y-4" onSubmit={extendRuntime.submit}>
            <div className="space-y-2">
              <Label htmlFor="runtime-extension" className="text-xs text-muted-foreground">
                Add runtime
              </Label>
              <Input
                id="runtime-extension"
                value={extendRuntime.value}
                onChange={(event) => extendRuntime.setValue(event.target.value)}
                placeholder="1h"
                disabled={extendRuntime.busy}
              />
            </div>
            <div className="flex flex-wrap gap-2">
              {runtimeExtensionPresets.map((preset) => (
                <Button
                  key={preset.value}
                  type="button"
                  variant={extendRuntime.value === preset.value ? "default" : "outline"}
                  size="sm"
                  onClick={() => extendRuntime.setValue(preset.value)}
                  disabled={extendRuntime.busy}
                  className="h-7 px-2 text-xs"
                >
                  {preset.label}
                </Button>
              ))}
            </div>
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5 text-xs">
              <dt className="text-muted-foreground">Current limit</dt>
              <dd className="font-mono text-foreground">{run.maxRuntime || "default"}</dd>
              <dt className="text-muted-foreground">Status</dt>
              <dd className="text-foreground">{run.phase || "Unknown"}</dd>
            </dl>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => extendRuntime.setOpen(false)}
                disabled={extendRuntime.busy}
              >
                Cancel
              </Button>
              <Button type="submit" size="sm" disabled={extendRuntime.busy || !extendRuntime.value.trim()}>
                {extendRuntime.busy ? "Extending…" : "Extend runtime"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </header>
  );
}
