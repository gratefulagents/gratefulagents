import type { Dispatch, FormEvent, SetStateAction } from "react";
import { lazy, Suspense, useState } from "react";
import { Link } from "react-router-dom";
import {
  Check,
  ChevronLeft,
  Clock,
  Download,
  FileText,
  GitPullRequest,
  MoreHorizontal,
  PanelRightOpen,
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
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { toast } from "@/components/ui/toaster";
import { binaryClient } from "@/lib/client";
import { downloadBlob } from "@/lib/download";
import { openExternal } from "@/lib/native";
import { toneText, type StatusTone } from "@/lib/status";
import { isRunComputing, runStatusLabel, runStatusTone } from "@/lib/runStatus";
import { pullRequestLabel } from "@/lib/pullRequests";
import type { TraceUsageSummary } from "@/lib/traceUsage";
import { cn } from "@/lib/utils";
import type { AgentRun, ResourceOwner } from "@/rpc/platform/service_pb";
import { runAuthLabel, splitRunModel } from "./RunModelSwitcher";
import { RunContextSheet } from "./RunContextSheet";
import { fmtTokens, fmtUsd, PlanDialogContent, runtimeExtensionPresets, sourceHref } from "./helpers";
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

export interface RunHeaderProps {
  namespace: string;
  name: string;
  run: AgentRun;
  viewers: ResourceOwner[];
  showRepositories: boolean;
  sandboxReady: boolean;
  sandboxStartupMessage: string;
  /** Every PR opened by the run, most recent last. */
  prUrls: string[];
  showCreatePRButton: boolean;
  canExtendRuntime: boolean;
  isPaused: boolean;
  extendingRuntime: boolean;
  extendRuntimeOpen: boolean;
  setExtendRuntimeOpen: Dispatch<SetStateAction<boolean>>;
  runtimeExtension: string;
  setRuntimeExtension: Dispatch<SetStateAction<string>>;
  handleExtendRuntime: (event?: FormEvent<HTMLFormElement>) => void | Promise<void>;
  hasPlan: boolean;
  planContent: string;
  shareOpen: boolean;
  setShareOpen: Dispatch<SetStateAction<boolean>>;
  isOwnerOrAdmin: boolean;
  isViewer: boolean;
  canRetry: boolean;
  handleRetry: () => void | Promise<void>;
  retrying: boolean;
  canStop: boolean;
  handleStop: () => void | Promise<void>;
  stopping: boolean;
  canPromote: boolean;
  handlePromote: () => void | Promise<void>;
  promoting: boolean;
  canDelete: boolean;
  handleDelete: () => void | Promise<void>;
  deleting: boolean;
  displayCostUsd: number | null | undefined;
  sessionMetrics: TraceUsageSummary | null;
  canRename: boolean;
  onRename: (displayName: string) => void | Promise<void>;
  inspectorOpen: boolean;
  onToggleInspector: () => void;
  inspectorAttention: boolean;
}

/** Inline rename affordance over the run's display name. */
function RunTitle({
  run,
  canRename,
  onRename,
}: {
  run: AgentRun;
  canRename: boolean;
  onRename: (displayName: string) => void | Promise<void>;
}) {
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
      await onRename(next);
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
    <span className="group/title flex min-w-0 items-center gap-1.5">
      <span
        className="truncate text-sm font-medium text-foreground"
        title={run.displayName ? run.name : undefined}
      >
        {run.displayName || run.name}
      </span>
      {canRename && (
        <button
          type="button"
          aria-label="Rename run"
          onClick={() => {
            setValue(run.displayName || "");
            setEditing(true);
          }}
          className="shrink-0 text-muted-foreground/0 transition-colors group-hover/title:text-muted-foreground/70 hover:text-foreground! focus-visible:text-muted-foreground"
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
      className="hidden shrink-0 items-center gap-2 whitespace-nowrap font-mono text-[11px] tabular-nums text-muted-foreground sm:flex sm:gap-3"
      aria-label="Run usage"
    >
      <div className="flex items-baseline gap-1" title="Cost">
        <dt className="sr-only">Cost</dt>
        <dd className="text-foreground">{cost}</dd>
      </div>
      <div className="flex items-baseline gap-1" title="Input tokens">
        <dt className="text-[10px] uppercase tracking-wide">In</dt>
        <dd className="text-foreground">{input}</dd>
      </div>
      <div className="flex items-baseline gap-1" title="Output tokens">
        <dt className="text-[10px] uppercase tracking-wide">Out</dt>
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
  const live = isRunComputing(run);
  const tone = runStatusTone(run);
  const label = runStatusLabel(run).replace(/([a-z])([A-Z])/g, "$1 $2").toLowerCase();
  const permMode = run.resolvedPermissionMode || "read-only";
  const current = splitRunModel(run.model || run.resolvedModel || "");
  const inputTokens = sessionMetrics?.hasUsage ? sessionMetrics.inputTokens : Number(run.inputTokens);
  const outputTokens = sessionMetrics?.hasUsage ? sessionMetrics.outputTokens : Number(run.outputTokens);
  const cacheReadTokens = sessionMetrics?.hasUsage ? sessionMetrics.cacheReadTokens : 0;

  return (
    <Popover>
      <PopoverTrigger
        className={cn(
          "flex shrink-0 items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-medium transition-colors hover:bg-muted data-[popup-open]:bg-muted",
          toneText[tone],
        )}
      >
        <span className="relative inline-flex size-1.5 shrink-0 rounded-full bg-current">
          {live && (
            <span className="absolute inset-0 rounded-full bg-current opacity-60 motion-safe:animate-ping" />
          )}
        </span>
        {label}
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
          {displayCostUsd ? (
            <Fact label="Cost" value={<span className="font-mono tabular-nums">${fmtUsd(displayCostUsd)}</span>} />
          ) : null}
          {run.trigger?.externalUrl && (
            <Fact
              label="Issue"
              value={
                <a
                  href={run.trigger.externalUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="text-foreground hover:underline"
                >
                  {run.trigger.externalIdentifier || "link"}
                </a>
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
  showRepositories,
  sandboxReady,
  sandboxStartupMessage,
  prUrls,
  showCreatePRButton,
  canExtendRuntime,
  isPaused,
  extendingRuntime,
  extendRuntimeOpen,
  setExtendRuntimeOpen,
  runtimeExtension,
  setRuntimeExtension,
  handleExtendRuntime,
  hasPlan,
  planContent,
  shareOpen,
  setShareOpen,
  isOwnerOrAdmin,
  isViewer,
  canRetry,
  handleRetry,
  retrying,
  canStop,
  handleStop,
  stopping,
  canPromote,
  handlePromote,
  promoting,
  canDelete,
  handleDelete,
  deleting,
  displayCostUsd,
  sessionMetrics,
  canRename,
  onRename,
  inspectorOpen,
  onToggleInspector,
  inspectorAttention,
}: RunHeaderProps) {
  const [contextOpen, setContextOpen] = useState(false);
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
  // Exactly one inline action. Wrap the run up, recover a failed one, unblock
  // a paused one, or stop a live one — in that order. Everything not chosen
  // stays reachable in the overflow menu.
  const primaryAction: "promote" | "retry" | "extend" | "stop" | null =
    canPromote
      ? "promote"
      : canRetry
        ? "retry"
        : canExtendRuntime && isPaused
          ? "extend"
          : canStop
            ? "stop"
            : null;
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
      <RunContextSheet
        open={contextOpen}
        onOpenChange={setContextOpen}
        namespace={namespace}
        name={name}
        run={run}
        showRepositories={showRepositories}
        canClone={!isViewer}
        sandboxReady={sandboxReady}
        startupMessage={sandboxStartupMessage}
      />

      <Link
        to={sourceName ? sourceHref(sourceKind, namespace, sourceName) : "/projects"}
        aria-label={sourceName ? `Back to ${sourceName}` : "Back to projects"}
        title={sourceName || "Projects"}
        className="flex size-7 shrink-0 items-center justify-center rounded-[min(var(--radius-md),12px)] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
      >
        <ChevronLeft className="size-4" />
      </Link>

      <div className="flex min-w-0 flex-1 items-center gap-2">
        <RunTitle run={run} canRename={canRename} onRename={onRename} />
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
          <button
            type="button"
            onClick={() => void openExternal(prUrls[0])}
            className="hidden shrink-0 items-center gap-1.5 rounded-full bg-[color:var(--tone-success)]/10 px-2.5 py-1 text-xs font-medium text-[color:var(--tone-success)] transition-colors hover:bg-[color:var(--tone-success)]/20 sm:inline-flex"
          >
            <GitPullRequest className="size-3.5" />
            Pull request
          </button>
        )}
        {prUrls.length > 1 && (
          <DropdownMenu>
            <DropdownMenuTrigger className="hidden shrink-0 items-center gap-1.5 rounded-full bg-[color:var(--tone-success)]/10 px-2.5 py-1 text-xs font-medium text-[color:var(--tone-success)] transition-colors hover:bg-[color:var(--tone-success)]/20 sm:inline-flex">
              <GitPullRequest className="size-3.5" />
              {prUrls.length} pull requests
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="min-w-52">
              {prUrls.map((url) => (
                <DropdownMenuItem key={url} render={<a href={url} target="_blank" rel="noopener noreferrer" />}>
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
            visually merge. */}
        {primaryAction === "promote" && (
          <Button
            variant="outline"
            size="sm"
            onClick={handlePromote}
            disabled={promoting}
            className={cn("hidden sm:inline-flex", prUrls.length > 0 && "ml-1")}
          >
            <Check />
            {promoting ? "Marking…" : "Mark succeeded"}
          </Button>
        )}
        {primaryAction === "retry" && (
          <Button
            size="sm"
            onClick={handleRetry}
            disabled={retrying}
            className="hidden sm:inline-flex"
          >
            <RotateCcw />
            {retrying ? "Retrying…" : "Retry"}
          </Button>
        )}
        {primaryAction === "extend" && (
          <Button
            size="sm"
            onClick={() => setExtendRuntimeOpen(true)}
            disabled={extendingRuntime}
            className="hidden sm:inline-flex"
          >
            <Clock />
            Extend runtime
          </Button>
        )}
        {primaryAction === "stop" && (
          <Button
            variant="outline"
            size="sm"
            onClick={handleStop}
            disabled={stopping}
            className="hidden sm:inline-flex"
          >
            <Square />
            {stopping ? "Stopping…" : "Stop"}
          </Button>
        )}

        {/* Run context is always one click away: it is the fastest way to see
            which repositories and instructions the agent is working from. */}
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={() => setContextOpen(true)}
          aria-label="Run context"
          title="Run context"
          className="shrink-0 text-muted-foreground hover:text-foreground"
        >
          <PanelRightOpen />
        </Button>

        <DropdownMenu>
          <DropdownMenuTrigger
            render={
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label="Run actions"
                className="shrink-0 text-muted-foreground hover:text-foreground data-[popup-open]:text-foreground"
              />
            }
          >
            <MoreHorizontal />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-52">
            {canCreatePR && (
              <DropdownMenuItem onClick={() => setCreatePROpen(true)}>
                <GitPullRequest className="size-3.5" />
                {prUrls.length > 0 ? "Create another PR…" : "Create PR…"}
              </DropdownMenuItem>
            )}
            {canRetry && (
              <DropdownMenuItem
                className={primaryAction === "retry" ? "sm:hidden" : undefined}
                onClick={handleRetry}
                disabled={retrying}
              >
                <RotateCcw className="size-3.5" />
                {retrying ? "Retrying…" : "Retry run"}
              </DropdownMenuItem>
            )}
            {canExtendRuntime && (
              <DropdownMenuItem
                className={primaryAction === "extend" ? "sm:hidden" : undefined}
                onClick={() => setExtendRuntimeOpen(true)}
                disabled={extendingRuntime}
              >
                <Clock className="size-3.5" />
                Extend runtime…
              </DropdownMenuItem>
            )}
            {canStop && (
              <DropdownMenuItem
                className={primaryAction === "stop" ? "sm:hidden" : undefined}
                onClick={handleStop}
                disabled={stopping}
              >
                <Square className="size-3.5" />
                {stopping ? "Stopping…" : "Stop run"}
              </DropdownMenuItem>
            )}
            {canPromote && (
              <DropdownMenuItem
                className={primaryAction === "promote" ? "sm:hidden" : undefined}
                onClick={handlePromote}
                disabled={promoting}
              >
                <Check className="size-3.5" />
                {promoting ? "Marking…" : "Mark succeeded"}
              </DropdownMenuItem>
            )}

            <DropdownMenuSeparator />
            {hasPlan && (
              <DropdownMenuItem onClick={() => setPlanOpen(true)}>
                <FileText className="size-3.5" />
                View plan
              </DropdownMenuItem>
            )}
            <DropdownMenuItem onClick={handleExportArchive} disabled={exporting}>
              <Download className="size-3.5" />
              {exporting ? "Exporting…" : "Export logs & traces"}
            </DropdownMenuItem>
            {isOwnerOrAdmin && (
              <DropdownMenuItem onClick={() => setShareOpen(true)}>
                <Share2 className="size-3.5" />
                Share…
              </DropdownMenuItem>
            )}
            {canDelete && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem variant="destructive" onClick={handleDelete} disabled={deleting}>
                  <Trash2 className="size-3.5" />
                  {deleting ? "Deleting…" : "Delete run"}
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>

        <InspectorToggle open={inspectorOpen} onToggle={onToggleInspector} attention={inspectorAttention} />
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
          open={shareOpen}
          onOpenChange={setShareOpen}
        />
      )}

      <Dialog open={extendRuntimeOpen} onOpenChange={setExtendRuntimeOpen}>
        <DialogContent className="max-w-sm">
          <DialogHeader>
            <DialogTitle className="text-sm">Extend runtime</DialogTitle>
          </DialogHeader>
          <form className="space-y-4" onSubmit={handleExtendRuntime}>
            <div className="space-y-2">
              <Label htmlFor="runtime-extension" className="text-xs text-muted-foreground">
                Add runtime
              </Label>
              <Input
                id="runtime-extension"
                value={runtimeExtension}
                onChange={(event) => setRuntimeExtension(event.target.value)}
                placeholder="1h"
                disabled={extendingRuntime}
              />
            </div>
            <div className="flex flex-wrap gap-2">
              {runtimeExtensionPresets.map((preset) => (
                <Button
                  key={preset.value}
                  type="button"
                  variant={runtimeExtension === preset.value ? "default" : "outline"}
                  size="sm"
                  onClick={() => setRuntimeExtension(preset.value)}
                  disabled={extendingRuntime}
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
                onClick={() => setExtendRuntimeOpen(false)}
                disabled={extendingRuntime}
              >
                Cancel
              </Button>
              <Button type="submit" size="sm" disabled={extendingRuntime || !runtimeExtension.trim()}>
                {extendingRuntime ? "Extending…" : "Extend runtime"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </header>
  );
}
