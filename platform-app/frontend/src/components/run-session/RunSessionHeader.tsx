import type { Dispatch, FormEvent, SetStateAction } from "react";
import { lazy, Suspense, useState } from "react";
import { Link } from "react-router-dom";
import { Activity, Check, CheckCircle2, ChevronDown, ChevronRight, CircleAlert, Clock, Download, FileDiff, FileText, GitPullRequest, Info, MessageSquare, MoreHorizontal, PanelRight, Pencil, RotateCcw, Share2, Square, SquareTerminal, Trash2, Workflow, X } from "lucide-react";

import { CreatePRDialog } from "@/components/CreatePRDialogButton";
import { ModeSwitcher } from "@/components/ModeSwitcher";
import { OwnerAvatar } from "@/components/OwnerAvatar";
import { PresenceAvatars } from "@/components/PresenceAvatars";
import { ShareDialog } from "@/components/ShareDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Separator } from "@/components/ui/separator";
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
import { fmtTokens, fmtUsd, PlanDialogContent, runtimeExtensionPresets, sourceHref, type MainView } from "./helpers";
import { OverseerSettings } from "./OverseerSettings";

const OverseerPresence = lazy(() =>
  import("./OverseerPresence").then((module) => ({ default: module.OverseerPresence })),
);

// One icon per run view so the segmented control can collapse to icons on
// narrow desktops without hiding views behind a dropdown.
const mainViewIcons: Record<MainView, typeof MessageSquare> = {
  chat: MessageSquare,
  graph: Workflow,
  diff: FileDiff,
  pr: GitPullRequest,
  errors: CircleAlert,
  logs: SquareTerminal,
  trace: Activity,
};

interface RunSessionHeaderProps {
  namespace: string;
  name: string;
  run: AgentRun;
  viewers: ResourceOwner[];
  showRepositories: boolean;
  sandboxReady: boolean;
  sandboxStartupMessage: string;
  mainTabs: Array<{ value: MainView; label: string }>;
  activeMainView: MainView;
  setMainView: (view: MainView) => void;
  /** Every PR created by the run, most recent last. Empty when none yet. */
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
}

// Sandbox permission mode → semantic tone for the statusline.
const permissionTone: Record<string, StatusTone> = {
  "read-only": "warning",
  "workspace-write": "success",
  "danger-full-access": "danger",
};

// Read-only runtime summary for the Run Details dialog. Provider/model/
// reasoning switching lives in the composer footer (RunModelSwitcher).
function RuntimeConfigDetails({ run }: { run: AgentRun }) {
  const current = splitRunModel(run.model || run.resolvedModel || "");
  return (
    <>
      <dt className="text-muted-foreground">Provider</dt>
      <dd className="font-mono text-foreground">
        {current.provider} · {runAuthLabel(run, current.provider)}
      </dd>
      <dt className="text-muted-foreground">Model</dt>
      <dd className="min-w-0 truncate font-mono text-foreground">{run.resolvedModel || run.model}</dd>
      <dt className="text-muted-foreground">Reasoning</dt>
      <dd className="font-mono text-foreground">{run.resolvedReasoningLevel || "default"}</dd>
    </>
  );
}

// RunTitle renders the run's display name (falling back to the resource name)
// with an inline rename affordance for collaborators.
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
  const label = run.displayName || run.name;

  async function submit() {
    const next = value.trim();
    if (saving) {
      return;
    }
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
          className="h-6 w-48 text-xs"
        />
        <button
          type="button"
          aria-label="Save name"
          onClick={() => void submit()}
          disabled={saving}
          className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
        >
          <Check className="size-3.5" />
        </button>
        <button
          type="button"
          aria-label="Cancel rename"
          onClick={() => setEditing(false)}
          disabled={saving}
          className="shrink-0 text-muted-foreground transition-colors hover:text-foreground"
        >
          <X className="size-3.5" />
        </button>
      </span>
    );
  }

  return (
    <span className="flex min-w-0 items-center gap-1">
      <span
        className="truncate font-medium text-foreground"
        title={run.displayName ? run.name : undefined}
      >
        {label}
      </span>
      {canRename && (
        <button
          type="button"
          aria-label="Rename run"
          onClick={() => {
            setValue(run.displayName || "");
            setEditing(true);
          }}
          className="shrink-0 text-muted-foreground/60 transition-colors hover:text-foreground"
        >
          <Pencil className="size-3" />
        </button>
      )}
    </span>
  );
}

export function RunSessionHeader({
  namespace,
  name,
  run,
  viewers,
  showRepositories,
  sandboxReady,
  sandboxStartupMessage,
  mainTabs,
  activeMainView,
  setMainView,
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
}: RunSessionHeaderProps) {
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [contextOpen, setContextOpen] = useState(false);
  const [planOpen, setPlanOpen] = useState(false);
  const [createPROpen, setCreatePROpen] = useState(false);
  const [exporting, setExporting] = useState(false);

  // Download a zip snapshot of the run's logs and traces. Works mid-run:
  // the server bundles whatever has been captured so far. Uses the binary
  // wire format so the archive bytes are not base64-inflated through JSON.
  const handleExportArchive = async () => {
    setExporting(true);
    try {
      const resp = await binaryClient.exportAgentRunArchive({ namespace, name });
      downloadBlob(resp.filename || `${name}-export.zip`, resp.archive, "application/zip");
      toast.success("Export ready", {
        description: "Run logs & traces saved as a zip archive.",
      });
    } catch (e) {
      toast.error("Couldn't export logs & traces", {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setExporting(false);
    }
  };

  // Trace-derived usage when available; Postgres session metrics (mirrored
  // onto the run) as fallback so deployments without a tracing backend still
  // show tokens.
  const inputTokens = sessionMetrics?.hasUsage ? sessionMetrics.inputTokens : Number(run.inputTokens);
  const outputTokens = sessionMetrics?.hasUsage ? sessionMetrics.outputTokens : Number(run.outputTokens);
  const cacheReadTokens = sessionMetrics?.hasUsage ? sessionMetrics.cacheReadTokens : 0;

  // An empty resolved mode means no RuntimeProfile grants write access and
  // the agent runtime enforces its zero-trust read-only default — show what
  // is actually enforced.
  const permMode = run.resolvedPermissionMode || "read-only";
  const live = isRunComputing(run);
  const showRunContext = Boolean(run.modeInstructions) || showRepositories;
  // One contextual primary action keeps the toolbar calm: ship the work,
  // recover a failed run, or unblock a paused one — everything else lives in
  // the overflow menu. Stop stays inline separately because interrupting a
  // live agent must never hide behind a menu. Once a PR exists the pill is
  // the artifact; creating another PR demotes to the overflow menu.
  const canCreatePR = showCreatePRButton && !isViewer;
  const primaryAction: "createPR" | "retry" | "extend" | null =
    canCreatePR && prUrls.length === 0
      ? "createPR"
      : canRetry
        ? "retry"
        : canExtendRuntime && isPaused
          ? "extend"
          : null;
  // Menu grouping: whether the action group has items visible on desktop
  // (always-rendered entries) vs. only the phone-only duplicates of inline
  // buttons — the group separator must match, or it floats above nothing.
  const menuActionsAlwaysVisible =
    canPromote ||
    (canCreatePR && primaryAction !== "createPR") ||
    (canRetry && primaryAction !== "retry") ||
    (canExtendRuntime && primaryAction !== "extend");
  const menuActionsPhoneOnly =
    canStop ||
    (canCreatePR && primaryAction === "createPR") ||
    (canRetry && primaryAction === "retry") ||
    (canExtendRuntime && primaryAction === "extend");
  const overseerRunName = run.overseerSummary?.runName.trim();
  const overseerHref = overseerRunName
    ? `/runs/${encodeURIComponent(namespace)}/${encodeURIComponent(overseerRunName)}`
    : undefined;

  return (
        <div className="sticky top-0 z-10 shrink-0 border-b bg-background">
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
        <div className="flex items-center justify-between gap-2 overflow-hidden px-3 py-2 md:gap-4 md:px-4">
          <div className="flex min-w-0 flex-1 items-center gap-1.5 overflow-hidden text-xs md:flex-initial">
            <Link
              to="/projects"
              className="hidden text-muted-foreground transition-colors hover:text-foreground md:inline"
            >
              Projects
            </Link>
            <ChevronRight className="hidden size-3 shrink-0 text-muted-foreground/60 md:block" />
            {run.project?.name || run.trigger?.name ? (
              <Link
                to={sourceHref(
                  run.project?.kind || run.trigger?.kind || "",
                  namespace,
                  run.project?.name || run.trigger?.name || "",
                )}
                className="hidden max-w-32 truncate text-muted-foreground transition-colors hover:text-foreground sm:inline"
              >
                {run.project?.name || run.trigger?.name}
              </Link>
            ) : (
              <span className="hidden text-muted-foreground/50 sm:inline">…</span>
            )}
            <ChevronRight className="hidden size-3 shrink-0 text-muted-foreground/60 sm:block" />
            <RunTitle run={run} canRename={canRename} onRename={onRename} />
            <span className="hidden items-center gap-1.5 md:flex">
              {run.owner && <OwnerAvatar owner={run.owner} />}
              <PresenceAvatars viewers={viewers} />
            </span>
            {run.myPermission && run.myPermission !== "owner" && run.myPermission !== "admin" && (
              <Badge variant="outline" className="text-xs">
                {run.myPermission}
              </Badge>
            )}
          </div>

          <div className="flex min-w-0 shrink-0 items-center gap-2 md:shrink">
            {/* Every view stays one click away at any desktop width: icons
                always render, the active tab keeps its label so the current
                view is readable, and full labels return at xl+. */}
            <div className="hidden min-w-0 shrink items-center overflow-x-auto rounded-md bg-muted p-0.5 md:flex [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
              {mainTabs.map((tab) => {
                const Icon = mainViewIcons[tab.value];
                const active = activeMainView === tab.value;
                return (
                  <button
                    key={tab.value}
                    type="button"
                    onClick={() => setMainView(tab.value)}
                    title={tab.label}
                    aria-label={tab.label}
                    aria-pressed={active}
                    className={cn(
                      "flex items-center gap-1.5 whitespace-nowrap rounded-sm px-2 py-1 text-xs font-medium transition-colors",
                      active
                        ? "bg-background text-foreground shadow-sm"
                        : "text-muted-foreground hover:text-foreground",
                    )}
                  >
                    <Icon className="size-3.5 shrink-0" />
                    <span className={active ? "inline" : "hidden xl:inline"}>{tab.label}</span>
                  </button>
                );
              })}
            </div>

            {activeMainView === "chat" && run.overseer && (
              <Suspense fallback={null}>
                <OverseerPresence run={run} href={overseerHref} />
              </Suspense>
            )}

            <Separator orientation="vertical" className="hidden h-4 md:block" />

            {prUrls.length === 1 && (
              <button
                type="button"
                onClick={() => void openExternal(prUrls[0])}
                className="inline-flex shrink-0 items-center gap-1.5 rounded-full bg-emerald-500/10 px-2.5 py-1 text-xs font-medium text-emerald-600 transition-colors hover:bg-emerald-500/20 dark:text-emerald-400"
              >
                <GitPullRequest className="size-3.5" />
                <span className="hidden md:inline">Pull Request</span>
                <span className="md:hidden">PR</span>
              </button>
            )}
            {prUrls.length > 1 && (
              <DropdownMenu>
                <DropdownMenuTrigger className="inline-flex shrink-0 items-center gap-1.5 rounded-full bg-emerald-500/10 px-2.5 py-1 text-xs font-medium text-emerald-600 transition-colors hover:bg-emerald-500/20 data-[popup-open]:bg-emerald-500/20 dark:text-emerald-400">
                  <GitPullRequest className="size-3.5" />
                  {prUrls.length}
                  <span className="hidden md:inline">Pull Requests</span>
                  <span className="md:hidden">PRs</span>
                  <ChevronDown className="size-3" />
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" className="min-w-52">
                  {prUrls.map((url) => (
                    <DropdownMenuItem
                      key={url}
                      render={<a href={url} target="_blank" rel="noopener noreferrer" />}
                    >
                      <GitPullRequest className="size-3.5" />
                      <span className="truncate">{pullRequestLabel(url)}</span>
                    </DropdownMenuItem>
                  ))}
                </DropdownMenuContent>
              </DropdownMenu>
            )}
            {canCreatePR && (
              <>
                {primaryAction === "createPR" && (
                  <Button size="sm" className="hidden md:inline-flex" onClick={() => setCreatePROpen(true)}>
                    Create PR
                  </Button>
                )}
                <CreatePRDialog
                  namespace={namespace}
                  name={name}
                  open={createPROpen}
                  onOpenChange={setCreatePROpen}
                />
              </>
            )}

            {primaryAction === "extend" && (
              <Button
                type="button"
                variant="default"
                size="sm"
                onClick={() => setExtendRuntimeOpen(true)}
                disabled={extendingRuntime}
                className="hidden gap-1.5 md:inline-flex"
                title="Extend runtime"
              >
                <Clock className="size-3.5" />
                Extend runtime
              </Button>
            )}
            <Dialog open={extendRuntimeOpen} onOpenChange={setExtendRuntimeOpen}>
              <DialogContent className="max-w-sm">
                <DialogHeader>
                  <DialogTitle className="text-sm">Extend Runtime</DialogTitle>
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
                      {extendingRuntime ? "Extending..." : "Extend Runtime"}
                    </Button>
                  </DialogFooter>
                </form>
              </DialogContent>
            </Dialog>

            {hasPlan && (
              <Dialog open={planOpen} onOpenChange={setPlanOpen}>
                <PlanDialogContent planContent={planContent} />
              </Dialog>
            )}

            <Dialog open={detailsOpen} onOpenChange={setDetailsOpen}>
              <DialogContent className="max-h-[90vh] max-w-lg overflow-y-auto">
                <DialogHeader>
                  <DialogTitle className="text-sm">Run Details</DialogTitle>
                </DialogHeader>
                <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1.5 text-xs">
                  <dt className="text-muted-foreground">Repo</dt>
                  <dd className="truncate font-mono text-foreground">{run.repoUrl}</dd>
                  <dt className="text-muted-foreground">Branch</dt>
                  <dd className="font-mono text-foreground">{run.baseBranch || "main"}</dd>
                  <RuntimeConfigDetails run={run} />
                  {run.maxRuntime && (
                    <>
                      <dt className="text-muted-foreground">Max Runtime</dt>
                      <dd className="font-mono text-foreground">{run.maxRuntime}</dd>
                    </>
                  )}
                  {run.resolvedModel && run.model && run.resolvedModel !== run.model && (
                    <>
                      <dt className="text-muted-foreground">Requested Model</dt>
                      <dd className="font-mono text-foreground">{run.model}</dd>
                    </>
                  )}
                  {run.currentStep && (
                    <>
                      <dt className="text-muted-foreground">Step</dt>
                      <dd className="text-foreground">{run.currentStep}</dd>
                    </>
                  )}
                  {run.trigger?.externalUrl && (
                    <>
                      <dt className="text-muted-foreground">Issue</dt>
                      <dd>
                        <a
                          href={run.trigger.externalUrl}
                          target="_blank"
                          rel="noopener noreferrer"
                          className="text-foreground hover:underline"
                        >
                          {run.trigger.externalIdentifier || run.trigger.externalUrl}
                        </a>
                      </dd>
                    </>
                  )}
                  {run.modeName && (
                    <>
                      <dt className="text-muted-foreground">Mode</dt>
                      <dd className="text-foreground">
                        {run.modeName}
                        {run.modeCategory ? ` · ${run.modeCategory}` : ""}
                      </dd>
                    </>
                  )}
                  <dt className="text-muted-foreground">Permissions</dt>
                  <dd className="text-foreground">
                    {run.resolvedPermissionMode || "read-only"}
                  </dd>
                </dl>
                <OverseerSettings run={run} canManage={isOwnerOrAdmin} />
              </DialogContent>
            </Dialog>

            {isOwnerOrAdmin && (
              <ShareDialog
                resourceType="agent_run"
                resourceId={name}
                resourceNamespace={namespace}
                open={shareOpen}
                onOpenChange={setShareOpen}
              />
            )}

            {primaryAction === "retry" && (
              <Button
                type="button"
                size="sm"
                onClick={handleRetry}
                disabled={retrying}
                className="hidden gap-1.5 md:inline-flex"
                title="Retry this run"
              >
                <RotateCcw className="size-3.5" />
                {retrying ? "Retrying..." : "Retry"}
              </Button>
            )}

            {canStop && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={handleStop}
                disabled={stopping}
                className="hidden gap-1.5 md:inline-flex"
                title="Stop this run"
              >
                <Square className="size-3.5" />
                {stopping ? "Stopping..." : "Stop"}
              </Button>
            )}

            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-label="More actions"
                    className="size-9 p-0 text-muted-foreground hover:text-foreground data-[popup-open]:text-foreground md:size-7"
                  />
                }
              >
                <MoreHorizontal className="size-4" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-48">
                {/* Run actions. Items that duplicate the inline primary/Stop
                    buttons only surface here on phones, where those buttons
                    are hidden. Non-primary actions are always reachable. */}
                {canCreatePR && (
                  <DropdownMenuItem
                    className={primaryAction === "createPR" ? "md:hidden" : undefined}
                    onClick={() => setCreatePROpen(true)}
                  >
                    <GitPullRequest className="size-3.5" />
                    {prUrls.length > 0 ? "Create another PR…" : "Create PR…"}
                  </DropdownMenuItem>
                )}
                {canRetry && (
                  <DropdownMenuItem
                    className={primaryAction === "retry" ? "md:hidden" : undefined}
                    onClick={handleRetry}
                    disabled={retrying}
                  >
                    <RotateCcw className="size-3.5" />
                    {retrying ? "Retrying..." : "Retry run"}
                  </DropdownMenuItem>
                )}
                {canStop && (
                  <DropdownMenuItem className="md:hidden" onClick={handleStop} disabled={stopping}>
                    <Square className="size-3.5" />
                    {stopping ? "Stopping..." : "Stop run"}
                  </DropdownMenuItem>
                )}
                {canExtendRuntime && (
                  <DropdownMenuItem
                    className={primaryAction === "extend" ? "md:hidden" : undefined}
                    onClick={() => setExtendRuntimeOpen(true)}
                    disabled={extendingRuntime}
                  >
                    <Clock className="size-3.5" />
                    Extend runtime…
                  </DropdownMenuItem>
                )}
                {canPromote && (
                  <DropdownMenuItem onClick={handlePromote} disabled={promoting}>
                    <CheckCircle2 className="size-3.5" />
                    {promoting ? "Marking as succeeded…" : "Mark as succeeded"}
                  </DropdownMenuItem>
                )}
                {menuActionsAlwaysVisible ? (
                  <DropdownMenuSeparator />
                ) : menuActionsPhoneOnly ? (
                  <DropdownMenuSeparator className="md:hidden" />
                ) : null}
                {/* Inspect the run. */}
                {hasPlan && (
                  <DropdownMenuItem onClick={() => setPlanOpen(true)}>
                    <FileText className="size-3.5" />
                    View plan
                  </DropdownMenuItem>
                )}
                {showRunContext && (
                  <DropdownMenuItem onClick={() => setContextOpen(true)}>
                    <PanelRight className="size-3.5" />
                    Run context
                  </DropdownMenuItem>
                )}
                <DropdownMenuItem onClick={() => setDetailsOpen(true)}>
                  <Info className="size-3.5" />
                  Run details
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                {/* Take the run's data elsewhere. */}
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
          </div>
        </div>

        {/* Phone-only main-view tabs: full-width row so navigation is always
            reachable at 390px (the inline segmented control is md+). */}
        <div className="flex border-t border-border/60 md:hidden">
          {mainTabs.map((tab) => (
            <button
              key={tab.value}
              type="button"
              onClick={() => setMainView(tab.value)}
              className={cn(
                "min-w-0 flex-1 whitespace-nowrap border-b-2 px-1 py-2 text-xs font-medium transition-colors",
                activeMainView === tab.value
                  ? "border-primary text-foreground"
                  : "border-transparent text-muted-foreground hover:text-foreground",
              )}
            >
              {tab.label}
            </button>
          ))}
        </div>

        {/* Statusline: run state on the left, meters on the right. State is
            read here; actions live in the row above. */}
        <div className="flex h-7 items-center overflow-x-auto whitespace-nowrap border-t border-border/60 bg-muted/30 px-3 font-mono text-[11px] tracking-tight text-muted-foreground md:px-4 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
          <div className="flex shrink-0 items-center divide-x divide-border/60">
            <span className={cn("flex items-center gap-1.5 pr-3", toneText[runStatusTone(run)])}>
              <span className="relative inline-flex size-1.5 shrink-0 rounded-full bg-current">
                {live && (
                  <span className="absolute inset-0 rounded-full bg-current opacity-60 motion-safe:animate-ping" />
                )}
              </span>
              {runStatusLabel(run).replace(/([a-z])([A-Z])/g, "$1 $2").toLowerCase()}
            </span>
            {run.modeName && (
              <span
                className={cn(
                  "flex items-center gap-1 px-3",
                  toneText[run.modeCategory === "orchestrated" ? "purple" : "info"],
                )}
              >
                {isViewer ? (
                  run.modeName
                ) : (
                  <ModeSwitcher
                    namespace={namespace}
                    runName={name}
                    currentMode={run.modeName}
                    onSwitched={() => {}}
                    segment
                  />
                )}
                {run.modeExecutionStrategy && run.modeExecutionStrategy !== "serial" && (
                  <span className="opacity-60">{run.modeExecutionStrategy}</span>
                )}
              </span>
            )}
            <span
              className={cn("px-3", toneText[permissionTone[permMode] ?? "neutral"])}
              title={
                permMode === "read-only"
                  ? "Sandbox is read-only. To allow file edits, set a runtime profile with workspace-write in the project settings."
                  : "Sandbox permission mode, from the project's runtime profile."
              }
            >
              {permMode}
            </span>
          </div>
          <div className="ml-auto flex shrink-0 items-center divide-x divide-border/60 pl-3 tabular-nums">
            {displayCostUsd ? (
              <span className="px-3 last:pr-0" title="Session cost">
                ${fmtUsd(displayCostUsd)}
              </span>
            ) : null}
            {(inputTokens > 0 || outputTokens > 0) && (
              <span className="px-3 last:pr-0" title="Input / output tokens">
                {fmtTokens(inputTokens)}↓ {fmtTokens(outputTokens)}↑
              </span>
            )}
            {cacheReadTokens > 0 && (
              <span className="px-3 last:pr-0" title="Cache read tokens">
                {fmtTokens(cacheReadTokens)} cached
              </span>
            )}
          </div>
        </div>
        </div>

  );
}
