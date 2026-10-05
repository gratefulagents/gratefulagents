package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/computeruse"
	opprojectstate "github.com/gratefulagents/gratefulagents/internal/projectstate"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/gratefulagents/internal/tools"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkguardrails "github.com/gratefulagents/sdk/pkg/agentsdk/guardrails"
	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	agentpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	sdkruntime "github.com/gratefulagents/sdk/pkg/agentsdk/runtime"
	sdksandbox "github.com/gratefulagents/sdk/pkg/agentsdk/sandbox"
	metaharness "github.com/gratefulagents/sdk/pkg/agentsdk/tracestore"
)

func browserToolsEnabled() bool {
	if !envFlagEnabled("ENABLE_BROWSER_TOOLS", true) {
		return false
	}
	for _, candidate := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if _, err := exec.LookPath(candidate); err == nil {
			return true
		}
	}
	return false
}

func browserRegistryOptions() []tools.RegistryOption {
	return []tools.RegistryOption{
		tools.WithBrowserTools(),
		tools.WithBrowserScreenshotDir(workspaceScratchDir),
	}
}

// runChatLoop is the main conversational loop. It polls Postgres for user
// messages, runs the agent in autonomous passes, and writes results.
func runChatLoop(ctx context.Context, cfg runConfig, crdClient client.Client, k8sClient *kubernetes.Clientset, tracker *agent.RunProgress, sc *sessionclient.Client, metricsBaseline progressMetricsBaseline, tp agent.TracingProcessor, eventStream *agent.EventWriter, metaharnessWriter *metaharness.TraceWriter) (result runResult) {
	r := &chatRuntime{
		cfg:               cfg,
		crd:               crdClient,
		sc:                sc,
		tracker:           tracker,
		tp:                tp,
		eventStream:       eventStream,
		metaharnessWriter: metaharnessWriter,
		// Spend recorded by earlier provisioning sessions of this run; captured
		// once before this process starts publishing metrics so progress ticks
		// can never become the next cost baseline and compound within the pod.
		costBaselineUSD:      metricsBaseline.CostUSD,
		handledImmediate:     make(map[int64]struct{}),
		resumeAttempted:      make(map[string]struct{}),
		handoffHistoryConfig: resolveHandoffHistoryConfig(),
	}
	defer func() {
		r.runExitHooks(&result)
		if ctx.Err() != nil && result.Status == "failed" {
			result = *r.exitOnShutdown(ctx, nil, nil)
		}
	}()
	if failed := r.setup(ctx, k8sClient); failed != nil {
		return *failed
	}
	if failed := r.restore(ctx); failed != nil {
		return *failed
	}
	return r.messageLoop(ctx)
}

// loopAction is a phase's decision about what the loop does next.
type loopAction int

const (
	// proceed continues with the next phase of the current pass.
	proceed loopAction = iota
	// continueAgent starts another autonomous pass for the current user turn.
	continueAgent
	// awaitUser returns to the message loop for the next user message.
	awaitUser
)

// chatRuntime is the pod-lifetime state of the chat loop: the runtime built
// at setup plus the cursors that survive across user turns.
type chatRuntime struct {
	cfg               runConfig
	crd               client.Client
	sc                *sessionclient.Client
	tracker           *agent.RunProgress
	tp                agent.TracingProcessor
	eventStream       *agent.EventWriter
	metaharnessWriter *metaharness.TraceWriter
	exitHooks         []func(*runResult)

	modelMetadata                 *openAIModelMetadataResolver
	compactionResolver            agent.CompactionModelResolver
	runTrace                      *agent.Trace
	roleCatalog                   resolvedRoleCatalog
	roleCatalogProvider           string
	costBaselineUSD               float64
	pricedModels                  map[string]struct{}
	autonomousTurnMarked          bool
	prevModeName                  string
	maintainedRepositoryName      string
	maintainedRepositoryNamespace string
	psStore                       *opprojectstate.Store
	finishSummary                 *tools.FinishSummaryHolder
	loadSkillTool                 *tools.LoadSkillTool
	mcpPromptBlock                string
	toolInputGuardrails           []agent.ToolInputGuardrail
	toolOutputGuardrails          []agent.ToolOutputGuardrail
	runner                        *agent.Runner
	baseAgent                     *agent.Agent
	specialistAgents              map[string]*agent.Agent
	subAgentRegistry              *agent.SubAgentScheduler
	subAgentCheckpoints           *subAgentCheckpointWriter
	interruptedSubAgentNotice     string
	resumeAttempted               map[string]struct{}
	standingRefreshHooks          *standingWorkspaceRefreshHooks
	handoffHistoryConfig          agent.HandoffHistoryConfig

	turnNumber int32
	// Consecutive automatic budget rollovers for a standing maintainer; reset
	// whenever a genuine (non-rollover) message starts a turn.
	standingRollovers int
	handledImmediate  map[int64]struct{}
	tx                transcriptState
	// historyLoaded is set once this pod has loaded the full post-floor
	// message history; later passes with a live transcript fetch only the
	// messages above the transcript watermark.
	historyLoaded bool
	// stoppedMessageID is the durable ID of the prompt the user explicitly
	// stopped; it must never be claimed again (see withoutStoppedMessage).
	stoppedMessageID int64
	// deferredStopBanner is set when a stop skipped the Stopped banner because
	// a queued message was about to continue the session; the message loop
	// re-checks before it blocks so a cancelled queued message cannot leave
	// the run silently idle.
	deferredStopBanner bool
}

// onExit registers cleanup run when the chat loop returns, in reverse order
// of registration. A hook may replace the final result.
func (r *chatRuntime) onExit(hook func(*runResult)) {
	r.exitHooks = append(r.exitHooks, hook)
}

func (r *chatRuntime) runExitHooks(result *runResult) {
	for _, hook := range slices.Backward(r.exitHooks) {
		hook(result)
	}
}

// transcriptState is the in-memory full-session transcript replay plus the
// durable-message cursors it is consistent with. The runner's exact
// post-run conversation state is replayed verbatim as the next turn's input
// so tool calls/outputs and reasoning survive turn boundaries instead of
// being rebuilt from the lossy durable tail. Empty after pod restarts or
// context clears → durable-tail fallback.
type transcriptState struct {
	items []agent.RunItem
	// floor is the durable history floor the items were captured against.
	floor int64
	// seen is the highest durable message ID already represented in the
	// model's context (transcript replay, durable-tail fallback, or the
	// current user item). Durable messages above it were recorded
	// out-of-band — e.g. the dashboard stores a plan rejection as a system
	// message without starting a turn — and are folded into the replayed
	// transcript at the next turn start so they are not silently lost.
	seen int64
	// selfAssistant is the loop's own durable assistant append for the
	// previous turn: its content is already in FinalHistory, so the
	// out-of-band fold skips it.
	selfAssistant int64
	// resumePending is the durable user message that started a turn
	// interrupted by pod termination (the snapshot was flushed mid-turn).
	// The resume cursor only advances on assistant replies, so that message
	// is re-delivered to this pod even though its prompt and the partial
	// progress it triggered are already in the restored transcript — the
	// loop must open the resumed turn with a continuation instruction, not a
	// verbatim replay.
	resumePending int64
}

// persistInFlight durably upserts a mid-turn transcript; see
// persistInFlightTranscriptSnapshot.
func (tx *transcriptState) persistInFlight(ctx context.Context, sc *sessionclient.Client, pendingUserMessageID int64) {
	persistInFlightTranscriptSnapshot(ctx, sc, tx.items, tx.floor, tx.seen, tx.selfAssistant, pendingUserMessageID)
}

// persistRequired durably writes the turn-end transcript.
func (tx *transcriptState) persistRequired(ctx context.Context, sc *sessionclient.Client) error {
	return persistTranscriptSnapshotRequired(ctx, sc, tx.items, tx.floor, tx.seen, tx.selfAssistant)
}

// flushOnTermination preserves a pod-terminated turn's partial progress.
func (tx *transcriptState) flushOnTermination(
	sc *sessionclient.Client, result *agent.RunResult, pendingUserMessageID int64,
) {
	flushPodTerminationState(sc, result, tx.floor, tx.seen, tx.selfAssistant, pendingUserMessageID)
}

// userTurn is one turn-starting user message and the autonomous passes it
// drives.
type userTurn struct {
	// reply is the raw user message; prompt is what the current pass sends
	// (the reply on the first pass, the continuation nudge afterwards).
	reply  string
	prompt string
	images []sessionclient.MessageImage
	// messageID is the durable user message driving the turn.
	messageID int64
	// promptMessageID is the durable message whose content is prompt (the
	// user message, or the stored nudge). It is sent as this pass's user
	// item, so the transcript fold and durable tail must not include it too.
	promptMessageID int64
	// resumingInterrupted marks a re-delivered message whose turn a pod
	// termination cut short (see transcriptState.resumePending).
	resumingInterrupted bool
	firstPass           bool
	// claimPending is true while the driving message's claim is not yet
	// completed, i.e. a replacement pod would receive it again.
	claimPending  bool
	autoLoopCount int
	tracker       *agent.AutoTracker
}

// turnPolicy is the per-pass policy resolved from one AgentRun read.
type turnPolicy struct {
	run           *platformv1alpha1.AgentRun
	modeName      string
	toolAccess    agent.ToolAccessLevel
	capUSD        float64
	capConfigured bool
	mo            modeOverrides
	model         string
	provider      string
	roleCatalog   resolvedRoleCatalog
}

// preparedTurn is a pass ready to run.
type preparedTurn struct {
	*turnPolicy
	agent       *agent.Agent
	input       []agent.RunItem
	userItem    agent.RunItem
	runCfg      agent.RunConfig
	durablePass int64
	storedRun   *agent.StoredRun
}

// turnOutcome is what runner.Run produced for a pass.
type turnOutcome struct {
	result      *agent.RunResult
	err         error
	interrupted bool
	budgetGuard *turnBudgetGuard
	budgetStop  bool
}

// setup builds the pod-lifetime runtime: tool registry, MCP, guardrails, the
// SDK runtime bundle, and the sub-agent scheduler. Cleanup is registered with
// onExit. Complexity is inherent: every optional subsystem (tools, MCP,
// guardrails, runtime, sub-agents) requires its own conditional wiring.
func (r *chatRuntime) setup(ctx context.Context, k8sClient *kubernetes.Clientset) *runResult { //nolint:gocyclo
	cfg, crdClient, sc, tracker, tp := r.cfg, r.crd, r.sc, r.tracker, r.tp
	modelMetadata := newOpenAIModelMetadataResolver(cfg)
	// Per-model compaction thresholds (models.dev catalog → backend metadata →
	// static defaults); nil when ops pinned thresholds via env.
	compactionResolver := newCompactionModelResolver(cfg, modelMetadata)

	// Create a single trace spanning the entire AgentRun so all tool
	// calls, generations, and subagent spawns appear in one OTel waterfall.
	runTrace := agent.NewTrace(cfg.TaskName)
	tp.OnTraceStart(runTrace)
	tracker.SetRootSpanID(runTrace.ID)
	r.onExit(func(*runResult) {
		runTrace.Finish()
		tp.OnTraceEnd(runTrace)
	})

	// Read the AgentRun early for mode-aware tools and its immutable snapshot of
	// the creating user's personal role-model preferences.
	run := getAgentRun(ctx, crdClient, cfg.TaskName, cfg.Namespace)
	var roleModelOverrides []platformv1alpha1.AgentRunRoleModelOverride
	if run != nil {
		roleModelOverrides = run.Spec.RoleModelOverrides
	}
	roleCatalog, err := loadRoleCatalog(ctx, crdClient, cfg.Provider, roleModelOverrides)
	roleCatalogProvider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if err != nil {
		log.Printf("WARN: failed to load RoleInstruction catalog: %v — specialist roles will be unavailable", err)
	} else {
		log.Printf("Loaded %d RoleInstruction CRDs into SDK role catalog", len(roleCatalog.Roles))
	}

	// A pod whose base permission mode is read-only serves the whole session
	// with a read-only filesystem sandbox and without mutating tools. Say so
	// in the session feed — a silently degraded workspace looks like a broken
	// disk (EROFS) to users and agents.
	if !cfg.PermissionMode.AllowsWriteTools() {
		notice := "Workspace is read-only for this session"
		if cfg.PermissionModeReason != "" {
			notice += ": " + cfg.PermissionModeReason
		}
		if cfg.PermissionModeDegraded {
			notice += ". The run re-checks each turn and restarts compute automatically once write access resolves."
		}
		log.Printf("%s", notice)
		_ = sc.WriteActivity(ctx, "runtime_config", notice, nil)
	}
	if cfg.GitRemoteWrites == agentpolicy.GitRemoteWritesDisabled {
		notice := "Git remote writes are disabled for this session; " +
			"workspace edits, local commits, and remote reads remain available"
		log.Printf("%s", notice)
		_ = sc.WriteActivity(ctx, "runtime_config", notice, nil)
	}

	// Track mode name for mode-switch detection in the chat loop.
	if run != nil {
		r.prevModeName = run.Status.ModeName
	}

	// Build tool registry with all platform tools.
	maintainedRepositoryName := strings.TrimSpace(os.Getenv("AGENTRUN_MAINTAINED_REPOSITORY_NAME"))
	maintainedRepositoryNamespace := strings.TrimSpace(os.Getenv("AGENTRUN_MAINTAINED_REPOSITORY_NAMESPACE"))
	var registryOpts []tools.RegistryOption
	registryOpts = append(registryOpts,
		tools.WithPermissionMode(cfg.PermissionMode),
		tools.WithGitRemoteWrites(cfg.GitRemoteWrites),
		tools.WithSignalTools(),
		tools.WithReadFileImages(),
	)

	if maintainedRepositoryName != "" && maintainedRepositoryNamespace != "" {
		registryOpts = append(registryOpts,
			tools.WithContextualMutatingToolCandidates(tools.MaintainerLegacyMutationToolNames()...),
		)
	}

	// Mode templates may allowlist specific mutating tools that survive both
	// the registry and per-turn SDK read-only clamps (e.g. GitHub review tools).
	// effectiveAllowedMutatingTools includes the legacy reviewer fallback.
	if allowed := effectiveAllowedMutatingTools(run); len(allowed) > 0 {
		registryOpts = append(registryOpts, tools.WithAllowedMutatingTools(allowed...))
		log.Printf("Mode template allowlists mutating tools %v", allowed)
	}

	// AgentRun spec.toolPolicy, forwarded by the controller as env vars: a
	// narrow-only tool name filter (deny wins) layered on top of the
	// mode/profile permission filtering above. Control-flow tools stay exempt
	// inside the registry so the run can always finish.
	allowedToolNames := tools.SplitToolNameList(os.Getenv("AGENTRUN_ALLOWED_TOOLS"))
	deniedToolNames := tools.SplitToolNameList(os.Getenv("AGENTRUN_DENIED_TOOLS"))
	if len(allowedToolNames) > 0 || len(deniedToolNames) > 0 {
		registryOpts = append(registryOpts, tools.WithToolNameFilter(allowedToolNames, deniedToolNames))
		log.Printf("Run tool policy narrows the registry: allowed=%v denied=%v", allowedToolNames, deniedToolNames)
	}

	// Browser tools are on by default when the selected runtime image includes
	// Chromium. Operators can opt out with ENABLE_BROWSER_TOOLS=false.
	if browserToolsEnabled() {
		registryOpts = append(registryOpts, browserRegistryOptions()...)
		log.Printf("Browser tools enabled (disable with ENABLE_BROWSER_TOOLS=false)")
	} else if envFlagEnabled("ENABLE_BROWSER_TOOLS", true) {
		log.Printf("Browser tools unavailable: no Chromium executable found in the runtime image")
	}

	// Persistent PTY terminal for interactive programs (Unix + write
	// permission mode only; the SDK no-ops otherwise). On by default;
	// opt out with ENABLE_TERMINAL_TOOL=false.
	if envFlagEnabled("ENABLE_TERMINAL_TOOL", true) {
		registryOpts = append(registryOpts, tools.WithInteractiveTerminal())
		log.Printf("Interactive terminal tool enabled (disable with ENABLE_TERMINAL_TOOL=false)")
	}

	// Background shell jobs (BashStart/BashPoll/BashKill) for commands that
	// outlive the Bash tool's per-call cap, such as principal builds of large
	// Rust or Gradle workspaces. The SDK only
	// registers them in write-capable permission modes. On by default; opt
	// out with ENABLE_ASYNC_BASH=false.
	if envFlagEnabled("ENABLE_ASYNC_BASH", true) {
		registryOpts = append(registryOpts, tools.WithAsyncShellTools())
		log.Printf("Async bash tools enabled (disable with ENABLE_ASYNC_BASH=false)")
	}

	// Optional: durable project state (SDK backbone, Postgres persistence).
	// Provides task_*, memory_* and prime_context tools plus startup priming.
	psStore, psPool, psStatus := setupProjectState(ctx, cfg)
	if psStatus.err != nil {
		log.Printf("WARN: %s", psStatus.message)
	} else {
		log.Printf("%s", psStatus.message)
	}
	if psPool != nil {
		r.onExit(func(*runResult) { psPool.Close() })
	}
	r.onExit(func(result *runResult) {
		releaseClaimsOnExit(ctx, psStore, cfg.TaskName, *result)
	})

	toolRegistry := tools.NewRegistry(cfg.RepoDir, registryOpts...)
	desktopBroker := computeruse.FromContext(ctx)
	desktopTool := tools.RegisterComputerUseTool(toolRegistry, desktopBroker)
	r.onExit(func(*runResult) {
		for _, closer := range toolRegistry.Closers() {
			_ = closer.Close()
		}
	})
	tools.RegisterGitCommitTool(toolRegistry)
	tools.RegisterGitPushTool(toolRegistry)
	tools.RegisterGitSyncTools(toolRegistry)
	tools.RegisterCreatePRTool(toolRegistry, crdClient, cfg.TaskName, cfg.Namespace)
	tools.RegisterCreateIssueTool(toolRegistry, crdClient, cfg.TaskName, cfg.Namespace)
	tools.RegisterGitHubIssueManagementTools(toolRegistry, cfg.RepoDir)
	tools.RegisterAttachRepositoryTool(toolRegistry, cfg.BaseBranch, cfg.TaskName)
	// PR review tools (read PR/diff, review threads, submit reviews, resolve
	// threads) power the autonomous review loop: reviewer runs critique PRs and
	// implementer runs resolve the resulting feedback.
	tools.RegisterPRReviewTools(toolRegistry, cfg.RepoDir)
	tools.RegisterReviewVerdictTool(toolRegistry, crdClient, cfg.TaskName, cfg.Namespace)
	supervisedRunName := strings.TrimSpace(os.Getenv("AGENTRUN_SUPERVISED_NAME"))
	supervisedRunNamespace := strings.TrimSpace(os.Getenv("AGENTRUN_SUPERVISED_NAMESPACE"))
	if supervisedRunName != "" && supervisedRunNamespace != "" {
		tools.RegisterOverseerVerdictTool(toolRegistry, crdClient, cfg.TaskName, cfg.Namespace)
		tools.RegisterSupervisedActivityTool(
			toolRegistry,
			sc.StateStore(),
			crdClient,
			cfg.TaskName,
			cfg.Namespace,
			supervisedRunName,
			supervisedRunNamespace,
		)
	}
	if maintainedRepositoryName != "" && maintainedRepositoryNamespace != "" {
		tools.RegisterMaintainerTools(
			toolRegistry,
			sc.StateStore(),
			crdClient,
			cfg.TaskName,
			cfg.Namespace,
			cfg.TaskUID,
			maintainedRepositoryName,
			maintainedRepositoryNamespace,
		)
	}
	tools.RegisterPlanTools(toolRegistry, sc.StateStore(), sc.SessionID())
	// submit_task_output: typed-result sink for deterministic workflow task
	// runs, gated on the controller-forwarded output schema. The persister is
	// a narrow callback into this package's status patcher so the tool never
	// holds a raw cluster client; last write wins on status.structuredOutput.
	if outputSchema := strings.TrimSpace(os.Getenv("AGENTRUN_TASK_OUTPUT_SCHEMA")); outputSchema != "" {
		persistStructuredOutput := func(persistCtx context.Context, outputJSON string) error {
			return patchAgentRunStatus(persistCtx, crdClient, cfg.TaskName, cfg.Namespace, func(run *platformv1alpha1.AgentRun) {
				run.Status.StructuredOutput = outputJSON
			})
		}
		if err := tools.RegisterTaskOutputTool(toolRegistry, outputSchema, persistStructuredOutput); err != nil {
			log.Printf("WARN: submit_task_output unavailable: %v", err)
		} else {
			log.Printf("submit_task_output enabled (task output schema present)")
		}
	}
	// Skills use progressive disclosure: advertise only names and summaries,
	// then load full instructions into context when the model chooses one.
	// The computer-use guide is offered as a companion whenever the
	// computer_use tool is registered, so agents can load it without the user
	// attaching it to the run.
	var companionSkills []string
	if desktopTool != nil {
		companionSkills = append(companionSkills, tools.ComputerUseSkillName)
	}
	loadSkillTool := tools.RegisterLoadSkillTool(ctx, toolRegistry, crdClient, run, companionSkills...)
	// Gate on the startup-resolved flag as well as the freshly read run: a
	// transient CRD read failure (run == nil) must not produce a system
	// prompt that advertises Kubernetes-admin tools without registering them.
	if cfg.KubernetesAdmin || (run != nil && run.Spec.KubernetesAdmin) {
		tools.RegisterPlatformAdminToolsWithStore(toolRegistry, crdClient, k8sClient, sc.StateStore(), cfg.Namespace)
		log.Printf("Kubernetes-admin platform introspection tools enabled")
	}

	// All Slack-triggered runs get read-only Slack tools (threads, history,
	// search, users) when the pod carries Slack tokens, so the agent can answer
	// "summarize #eng" or reply with real conversation context. Sends still go
	// through the connector's approval flow.
	if isSlackRun(run) {
		tools.RegisterSlackReadTools(toolRegistry)
	}

	finishSummary := tools.RegisterFinishTool(toolRegistry, crdClient, cfg.TaskName, cfg.Namespace)

	// Lets the agent give the run a short human-readable title (status.displayName)
	// so users recognize it instead of the generated resource name.
	tools.RegisterSetDisplayNameTool(toolRegistry, crdClient, cfg.TaskName, cfg.Namespace)

	// Build MCP config: merge repo .mcp.json (if any) with MCPServer CRDs.
	// CRD-defined skills don't require any config in the user's repo.
	mcpCfg, clusterManagedMCPServers, networkAllowedMCPServers := buildMCPConfig(ctx, crdClient, cfg.Namespace, cfg.RepoDir, run)
	// Install exact-version Python MCP packages in a credential-free installer
	// sandbox. Repository-controlled uvx specs never trigger installation.
	mcpToolRoot, mcpDropped := materializeUvxServers(ctx, &mcpCfg, clusterManagedMCPServers, sandboxedInstallRunner)
	var mcpManager *sdkmcp.Manager
	if len(mcpCfg.MCPServers) > 0 {
		managerOpts := []sdkmcp.ManagerOption{sdkmcp.WithPermissionMode(cfg.PermissionMode)}
		if len(networkAllowedMCPServers) > 0 {
			names := make([]string, 0, len(networkAllowedMCPServers))
			for name := range networkAllowedMCPServers {
				names = append(names, name)
			}
			sort.Strings(names)
			managerOpts = append(managerOpts, sdkmcp.WithNetworkAccessForServers(names...))
		}
		if mcpToolRoot != "" {
			// The normal MCP sandbox masks host /tmp. Bind only the private install
			// root back in read-only so materialized commands work in every
			// permission mode and cannot be replaced by workspace processes.
			sandboxCfg := sdksandbox.ConfigFromEnv()
			sandboxCfg.WorkspaceRoot = cfg.RepoDir
			sandboxCfg.ExtraReadOnlyPaths = append(sandboxCfg.ExtraReadOnlyPaths, mcpToolRoot)
			managerOpts = append(managerOpts, sdkmcp.WithCommandExecutor(sdksandbox.DefaultWithConfig(sandboxCfg)))
		}
		var mcpErr error
		mcpManager, mcpErr = sdkmcp.NewManagerFromConfig(ctx, cfg.RepoDir, mcpCfg, managerOpts...)
		if mcpErr != nil {
			log.Printf("WARN: MCP server setup: %v — some MCP tools may be unavailable", mcpErr)
		}
	}
	if mcpManager != nil {
		r.onExit(func(*runResult) { _ = mcpManager.Close() })
		tools.RegisterMCPTools(toolRegistry, mcpManager, cfg.PermissionMode)
		log.Printf("MCP tools registered: %d tool descriptors", len(mcpManager.ToolDescriptors()))
	}
	// Surface dropped servers to the session activity feed: silently missing
	// tools are indistinguishable from misconfiguration for users.
	if len(mcpDropped) > 0 && sc != nil {
		_ = sc.WriteActivity(ctx, "runtime_config",
			"MCP servers not loaded for this run: "+strings.Join(mcpDropped, "; "), nil)
	}
	// Per-turn prompt block naming the connected servers (parity with the
	// SDK-native runner) so the agent knows its MCP tools exist and how they
	// are prefixed.
	var mcpPromptBlock string
	if mcpManager != nil {
		mcpPromptBlock = mcpPromptContext(mcpManager.ConnectedServerNames())
	}

	agentTools := toolRegistry.Tools()

	// Load guardrails: built-in + CRD policy.
	toolInputGuardrails := sdkguardrails.BuiltinToolInputGuardrails()
	toolOutputGuardrails := sdkguardrails.BuiltinToolOutputGuardrails()

	if run != nil && run.Spec.GuardrailPolicyRef != nil {
		crdInputG, crdOutputG, guardrailErr := loadCRDGuardrails(ctx, crdClient, run.Spec.GuardrailPolicyRef, cfg.Namespace)
		if guardrailErr != nil {
			msg := fmt.Sprintf("referenced GuardrailPolicy %q could not be loaded safely: %v", run.Spec.GuardrailPolicyRef.Name, guardrailErr)
			log.Printf("ERROR: %s", msg)
			if sc != nil {
				_ = sc.WriteActivity(ctx, "guardrail_policy_failed", msg, nil)
			}
			return &runResult{Status: "failed", Error: msg}
		}
		toolInputGuardrails = append(toolInputGuardrails, crdInputG...)
		toolOutputGuardrails = append(toolOutputGuardrails, crdOutputG...)
	}

	var modeSnapshot *platformv1alpha1.ModeTemplateSpec
	if run != nil {
		modeSnapshot = run.Status.ModeSnapshot
	}
	runtimeToolAccess := agent.ToolAccessLevelFull
	if !cfg.PermissionMode.AllowsWriteTools() {
		runtimeToolAccess = agent.ToolAccessLevelReadOnly
	}
	var instructionParts []string
	instructionParts = append(instructionParts, cfg.TaskContext)
	if psStore != nil {
		// Teach the durable-state surface (task_*/memory_*/prime_context)
		// only when its tools are actually registered for this run.
		instructionParts = append(instructionParts, projectStateGuidance())
		// The briefing is rendered once per pod into the system prompt so the
		// prompt prefix stays cache-stable across turns; compaction
		// carry-forward re-renders a fresh copy (compactionCarryForward).
		if prime := refreshPrimeContext(ctx, psStore, cfg.TaskName); prime != "" {
			instructionParts = append(instructionParts, prime)
		}
	}

	hasSpecialists := len(roleCatalog.Roles) > 0
	runtimeCfg := sdkRuntimeProviderConfig(cfg, cfg.Model)
	// Subscription failover (same model, next OAuth subscription) must be
	// tried before any mode-template fallback models.
	runtimeCfg.FallbackModels = mergedFallbackModels(cfg, cfg.Model,
		sdkruntime.ModeOverridesFromSnapshot(platformModeSnapshotForSDK(modeSnapshot), "").FallbackModels)
	runtimeCfg.WorkDir = cfg.RepoDir
	runtimeCfg.AgentName = cfg.TaskName
	runtimeCfg.Instructions = strings.Join(instructionParts, "\n\n---\n\n")
	if run != nil {
		runtimeCfg.ActiveMode = strings.TrimSpace(run.Status.ModeName)
	}
	runtimeCfg.ModeSnapshot = platformModeSnapshotForSDK(modeSnapshot)
	runtimeCfg.RoleCatalog = roleCatalog.Roles
	runtimeCfg.ToolAccess = runtimeToolAccess
	runtimeCfg.AllowedMutatingTools = effectiveRuntimeAllowedMutatingTools(
		ctx, crdClient, run, maintainedRepositoryName, maintainedRepositoryNamespace,
	)
	runtimeCfg.PermissionMode = cfg.PermissionMode
	runtimeCfg.GitRemoteWrites = cfg.GitRemoteWrites
	// Explicit feature selection (SDK v0.0.7+): the operator brings its own
	// tool registry, signal tools, MCP manager, and guardrail rules, so only
	// ExtraTools, specialists, and project state are
	// SDK-built. Zero values keep everything else off.
	runtimeCfg.Features = &sdkruntime.Features{
		Tools: sdkruntime.ToolFeatures{
			ExtraTools: true,
		},
		Handoffs: sdkruntime.HandoffFeatures{
			Enabled:         hasSpecialists,
			GenericFallback: hasSpecialists,
		},
		SubAgents: sdkruntime.SubAgentFeatures{
			GenericFallback: hasSpecialists,
			Async: sdkruntime.AsyncSubAgentFeatures{
				Task:    hasSpecialists,
				Status:  hasSpecialists,
				Control: hasSpecialists,
			},
		},
		Modes: sdkruntime.ModeFeatures{
			Instructions: true,
		},
		ProjectState: sdkruntime.ProjectStateFeatures{
			// The operator renders the briefing into the instructions itself
			// (see projectStateGuidance); the SDK's startup prime only feeds
			// WorkingStateText, which a dynamic carry-forward overrides.
			PrimeContext: false,
			TaskTools:    psStore != nil,
			MemoryTools:  psStore != nil,
			PrimeTool:    psStore != nil,
		},
		Runtime: sdkruntime.RuntimeFeatures{
			Retry:                true,
			Tracing:              tp != nil,
			ParallelToolCalls:    true,
			UntrustedToolOutputs: true,
		},
	}
	if psStore != nil {
		runtimeCfg.ProjectStateStore = psStore
		runtimeCfg.ProjectID = psStatus.projectID
		runtimeCfg.ProjectStateActor = cfg.TaskName
	}
	if hasSpecialists {
		// Normalize specialist sub-agent output before it is returned to the
		// parent agent: trim surrounding whitespace so delegated results merge
		// cleanly into the parent's context. Empty results are handled by the
		// SDK's "(no output)" fallback.
		runtimeCfg.SpecialistOutputExtractor = func(result *agent.RunResult) string {
			if result == nil {
				return ""
			}
			return strings.TrimSpace(result.FinalText())
		}
	}
	runtimeCfg.ExtraTools = agentTools
	runtimeCfg.TracingProcessor = tp
	runtimeCfg.Debug = cfg.Debug
	// Own the session state so the runtime does not register it in
	// Bundle.Closers (SDK v0.0.111+ closes a runtime-owned state with the
	// bundle, cancelling every managed child). This process exits between user
	// messages and on pod recycles while children must stay resumable as
	// reconciling; the loop cancels children explicitly on the exits where
	// that is the intended outcome (user stop, cost cap, turn failure).
	runtimeCfg.SessionState = sdkruntime.NewSessionState()
	runtimeBundle, err := sdkruntime.NewBuilder(runtimeCfg).Build(ctx)
	if err != nil {
		return &runResult{Status: "failed", Error: fmt.Sprintf("build runtime: %v", err)}
	}
	r.onExit(func(*runResult) { closeRuntimeClosers(runtimeBundle.Closers) })
	if desktopBroker != nil {
		available := toolRegistry.Get("computer_use") != nil && cfg.PermissionMode.AllowsWriteTools()
		desktopBroker.SetAvailable(func() bool { return available })
		r.onExit(func(*runResult) { desktopBroker.SetAvailable(nil) })
	}

	runner := runtimeBundle.Runner
	baseAgent := runtimeBundle.Agent
	specialistAgents := runtimeBundle.SpecialistAgents

	// Log what the agent has available.
	var specialistNames []string
	for name := range specialistAgents {
		specialistNames = append(specialistNames, name)
	}
	sort.Strings(specialistNames)
	log.Printf("Agent initialized: %d base tools, %d specialist sub-agents %v",
		len(agentTools), len(specialistAgents), specialistNames)

	var subAgentRegistry *agent.SubAgentScheduler
	if supervisedRunName == "" && runtimeBundle.SessionState != nil {
		subAgentRegistry = runtimeBundle.SessionState.SubAgentScheduler()
	}
	var subAgentCheckpoints *subAgentCheckpointWriter
	if subAgentRegistry != nil {
		log.Printf("SubAgentScheduler enabled: %d specialist agents", len(specialistAgents))
		var restoreErr error
		r.interruptedSubAgentNotice, restoreErr = restoreSubAgentCheckpoint(ctx, sc, subAgentRegistry)
		if restoreErr != nil {
			return &runResult{Status: "failed", Error: restoreErr.Error()}
		}
		subAgentCheckpoints = startSubAgentCheckpointLoop(sc, subAgentRegistry)
		r.onExit(func(result *runResult) {
			if err := subAgentCheckpoints.StopAndFlush(); err != nil {
				log.Printf("ERROR: final sub-agent checkpoint failed: %v", err)
				*result = runResult{Status: "failed", Error: "final sub-agent checkpoint failed: " + err.Error()}
			}
		})
	} else if supervisedRunName != "" {
		log.Printf("SubAgentScheduler disabled for standing overseer run")
	}

	// A read-only standing maintainer cannot fetch from its own Bash, so the
	// runtime keeps its checkout at the base-branch tip after each waiter
	// wake that reports changes (see maintainer_workspace.go).
	var standingRefreshHooks *standingWorkspaceRefreshHooks
	if maintainedRepositoryName != "" && !cfg.Repoless && !cfg.PermissionMode.AllowsWriteTools() {
		standingRefreshHooks = newStandingWorkspaceRefreshHooks(cfg.RepoDir, cfg.BaseBranch)
	}

	// Emit system_init event with session metadata so all consumers have it.
	if r.eventStream != nil {
		var mcpServerNames []string
		if mcpManager != nil {
			mcpServerNames = mcpManager.ConnectedServerNames()
		}
		r.eventStream.EmitSystemInit(cfg.Model, string(cfg.PermissionMode), cfg.RepoDir, 0,
			toolRegistry.Names(), mcpServerNames)
	}

	r.modelMetadata = modelMetadata
	r.compactionResolver = compactionResolver
	r.runTrace = runTrace
	r.roleCatalog = roleCatalog
	r.roleCatalogProvider = roleCatalogProvider
	r.maintainedRepositoryName = maintainedRepositoryName
	r.maintainedRepositoryNamespace = maintainedRepositoryNamespace
	r.psStore = psStore
	r.finishSummary = finishSummary
	r.loadSkillTool = loadSkillTool
	r.mcpPromptBlock = mcpPromptBlock
	r.toolInputGuardrails = toolInputGuardrails
	r.toolOutputGuardrails = toolOutputGuardrails
	r.runner = runner
	r.baseAgent = baseAgent
	r.specialistAgents = specialistAgents
	r.subAgentRegistry = subAgentRegistry
	r.subAgentCheckpoints = subAgentCheckpoints
	r.standingRefreshHooks = standingRefreshHooks
	return nil
}

// restore loads the durable cursors a replacement pod resumes from: the
// stopped-prompt floor, recovered claims, the startup idle boundary, and the
// previous pod's transcript snapshot.
func (r *chatRuntime) restore(ctx context.Context) *runResult {
	sc := r.sc
	// On resume: load the durable ID of the prompt the user explicitly
	// stopped. Its claim is completed on every stop path, but should that
	// write have failed (or predate the fix), RecoverClaimedUserMessages
	// below hands the message back as pending — the floor makes every queue
	// read skip that exact prompt so a replacement pod never re-runs it.
	state, stateErr := sc.ReadWorkingState(ctx)
	if stateErr != nil {
		log.Printf("WARN: failed to read working state at resume: %v", stateErr)
	}
	r.stoppedMessageID = state.LastStoppedUserMessageID
	if err := sc.RecoverClaimedUserMessages(ctx); err != nil {
		return &runResult{Status: "failed", Error: fmt.Sprintf("recovering claimed messages: %v", err)}
	}
	resumeSession, _, _, resumeErr := sc.ResumeState(ctx)
	if resumeErr != nil {
		log.Printf("WARN: failed to resume session state: %v — starting fresh", resumeErr)
	}

	// Publish the initial idle boundary only when there is neither a prompt
	// waiting to run nor an existing input request waiting to be answered.
	// Seeded runs already have their kickoff message queued, while restarted
	// runs may be preserving a question, approval, or other pending boundary.
	// If either state cannot be inspected, fail safe by leaving the run active;
	// the normal polling loop below will retry the queue read.
	startupMessages, startupErr := sc.PeekForUserMessages(ctx)
	if stateErr == nil && state.AutonomousTurnActive {
		r.autonomousTurnMarked = true
		// A graceful exit already enqueued its continuation; only a crash
		// (OOM, SIGKILL) leaves an active autonomous turn with nothing queued.
		if startupErr == nil && len(withoutStoppedMessage(startupMessages, r.stoppedMessageID)) == 0 {
			log.Printf("Autonomous turn was interrupted by a crash — resuming it")
			r.enqueueContinuation(ctx, &userTurn{})
			startupMessages, startupErr = sc.PeekForUserMessages(ctx)
		}
	}
	if shouldPublishStartupIdle(resumeSession, resumeErr, startupMessages, startupErr) {
		if err := sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputIdle, "", nil); err != nil {
			return &runResult{Status: "failed", Error: fmt.Sprintf("writing idle status: %v", err)}
		}
	} else if startupErr != nil {
		log.Printf("WARN: checking startup user-message queue: %v", startupErr)
	}

	// Rehydrate the previous pod's persisted transcript snapshot so a
	// restart resumes with full conversation context (tool calls/outputs,
	// reasoning, compaction summaries) instead of the lossy durable-tail
	// fallback. Discarded when the durable history floor moved (external
	// context clear) while the pod was down.
	if stateErr == nil {
		if restored := loadTranscriptSnapshot(ctx, sc, state.HistoryFloorMessageID); restored != nil {
			r.tx = transcriptState{
				items: restored.Items,
				floor: restored.FloorMessageID,
				seen:  restored.SeenMessageID,
				// The turn-end commit records its own reply in working state
				// after the (single) transcript write of that pass.
				selfAssistant: max(restored.SelfAssistantMessageID, state.SelfAssistantMessageID),
				resumePending: restored.PendingUserMessageID,
			}
			log.Printf("Restored session transcript snapshot: %d items (floor=%d seen=%d)",
				len(r.tx.items), r.tx.floor, r.tx.seen)
			_ = sc.WriteActivity(ctx, "transcript_restored",
				fmt.Sprintf("Restored %d conversation items from the previous session", len(r.tx.items)), nil)
		}
	}
	return nil
}

// messageLoop waits for turn-starting user messages and drives each through
// its autonomous passes until the run exits.
func (r *chatRuntime) messageLoop(ctx context.Context) runResult {
	for {
		turn, exit := r.nextUserTurn(ctx)
		if exit != nil {
			return *exit
		}
		if turn == nil {
			continue
		}
		if exit := r.agentLoop(ctx, turn); exit != nil {
			return *exit
		}
	}
}

// nextUserTurn claims the next turn-starting user message. A nil turn with a
// nil result means the message was consumed without starting a turn.
func (r *chatRuntime) nextUserTurn(ctx context.Context) (*userTurn, *runResult) {
	sc := r.sc
	var next sessionclient.UserMessage
	claimedQueued := false
	if r.deferredStopBanner {
		// The queued message seen at stop time is a peek, not a claim: the
		// user may have cancelled it since. Claim it right now or park the
		// session in the Stopped state before blocking on the queue.
		r.deferredStopBanner = false
		msg, ok, claimErr := claimQueuedUserMessage(ctx, sc, r.stoppedMessageID, r.handledImmediate)
		if claimErr != nil {
			log.Printf("WARN: failed to claim queued message after stop: %v", claimErr)
		}
		if ok {
			next, claimedQueued = msg, true
		} else {
			_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputStopped, "Stopped by user.", nil)
		}
	}
	if !claimedQueued {
		var err error
		next, err = waitForNextUserReply(ctx, sc, r.stoppedMessageID, 3*time.Second, r.handledImmediate)
		if err != nil {
			if ctx.Err() != nil {
				return nil, r.exitOnShutdown(ctx, nil, nil)
			}
			return nil, &runResult{Status: "failed", Error: fmt.Sprintf("waiting for user reply: %v", err)}
		}
	}
	reply := strings.TrimSpace(next.Content)
	if reply != standingBudgetRolloverPrompt {
		r.standingRollovers = 0
	}
	if reply == "" && len(next.Images) == 0 {
		return nil, nil
	}
	if isControlSlashCommand(reply) {
		return nil, nil
	}
	// A turn interrupted by pod termination left its prompt (and the
	// partial progress it triggered) in the restored transcript, and the
	// resume cursor re-delivers that same message here. Mark it so the
	// turn opens with a continuation instruction instead of replaying
	// the prompt verbatim. Consumed at the first real turn-starting
	// message: anything after it is a genuinely new prompt.
	resuming := r.tx.resumePending != 0 && next.ID == r.tx.resumePending
	r.tx.resumePending = 0

	// Clear only the request this message answered. Kickoff and legacy
	// messages have no request nonce in their metadata, so consume the current
	// idle boundary with a nonce-checked fallback. Replacement requests remain
	// visible in either path.
	if requestID := sessionclient.PendingRequestIDFromMetadata(next.Metadata); requestID != "" {
		_ = sc.ClearUserInputRequestIfID(ctx, requestID)
	} else {
		_ = sc.ClearIdleUserInputRequest(ctx)
	}
	// Honor a stop that survived pod replacement (or landed while this
	// message sat in the queue). Drain every pending stop now, then decide
	// by time: a stop requested at or after this message targets it — park
	// instead of starting the turn. An older stop is stale: the newer user
	// input is an explicit request to resume, so it is consumed without
	// cancelling the new turn.
	interrupt, interruptErr := sc.DrainInterruptsThrough(ctx, time.Now().UTC())
	if interruptErr != nil {
		log.Printf("WARN: failed to drain pending stop requests: %v", interruptErr)
	}
	if interruptAppliesToMessage(interrupt, next.CreatedAt) {
		return nil, r.parkAfterStop(ctx, next.ID,
			"Stopped by user before the replacement runtime started the turn.",
			"Stopped by user before the replacement runtime started the turn — continuing with the next queued message.")
	}
	return &userTurn{
		reply:               reply,
		prompt:              reply,
		images:              next.Images,
		messageID:           next.ID,
		promptMessageID:     next.ID,
		resumingInterrupted: resuming,
		firstPass:           true,
		claimPending:        true,
		tracker:             &agent.AutoTracker{},
	}, nil
}

// agentLoop runs autonomous passes for one user turn until it parks for the
// user (nil) or the run exits (non-nil).
func (r *chatRuntime) agentLoop(ctx context.Context, t *userTurn) *runResult {
	for {
		action, exit := r.runPass(ctx, t)
		if exit != nil {
			// A pod shutdown surfaces as a failed store/API call wherever the
			// SIGTERM lands. It is a routine recycle, not a run failure.
			if exit.Status == "failed" && ctx.Err() != nil {
				log.Printf("Pod shutdown interrupted the turn (%s) — exiting resumable", exit.Error)
				return r.exitOnShutdown(ctx, t, nil)
			}
			return exit
		}
		if action == awaitUser {
			if r.autonomousTurnMarked {
				r.setAutonomousTurnMarker(ctx, false)
			}
			return nil
		}
	}
}

// setAutonomousTurnMarker persists whether an autonomous turn is in flight
// (see sessionclient.WorkingState.AutonomousTurnActive).
func (r *chatRuntime) setAutonomousTurnMarker(ctx context.Context, active bool) {
	if err := r.sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
		state.AutonomousTurnActive = active
		return nil
	}); err != nil {
		log.Printf("WARN: failed to update the autonomous turn marker: %v", err)
		return
	}
	r.autonomousTurnMarked = active
}

// runPass runs one autonomous pass: steering, policy, turn, commit, and the
// decision whether to continue.
func (r *chatRuntime) runPass(ctx context.Context, t *userTurn) (loopAction, *runResult) {
	if ctx.Err() != nil {
		return awaitUser, r.exitOnShutdown(ctx, t, nil)
	}
	if action, exit := r.pollSteering(ctx, t); action != proceed || exit != nil {
		return action, exit
	}
	// Once the driving claim is completed (autonomous pass 2+), a crash
	// leaves no pending message to resume from; the marker lets restore
	// enqueue a continuation instead.
	if !t.claimPending && !r.autonomousTurnMarked {
		r.setAutonomousTurnMarker(ctx, true)
	}
	r.turnNumber++
	policy, action, exit := r.preflight(ctx, t)
	if policy == nil {
		return action, exit
	}
	pt, action, exit := r.prepareTurn(ctx, t, policy)
	if pt == nil {
		return action, exit
	}
	out := r.executeTurn(ctx, pt)
	if out.err != nil {
		return r.handleTurnError(ctx, t, pt, out)
	}
	t.firstPass = false
	// Carry the runner's post-run conversation state into the next turn
	// (full-transcript replay); runs interrupted by an unresolved tool
	// approval reset to the durable-tail fallback (user stops and failures
	// preserve their partial transcript in handleTurnError).
	r.tx.items = transcriptAfterRun(out.result)
	post := r.readRunAfterTurn(ctx)
	if exit := r.commitTurn(ctx, t, pt, out, post); exit != nil {
		return awaitUser, exit
	}
	if !out.interrupted && ctx.Err() == nil {
		r.consolidateMemoryAfterTurn(ctx, t, out.result, pt.toolAccess)
	}
	return r.decideNext(ctx, t, out, post)
}

// pollSteering enforces the autonomous pass cap and folds a user message sent
// between passes into the next pass. The check runs before the cap so
// steering at the boundary receives a fresh autonomous budget.
func (r *chatRuntime) pollSteering(ctx context.Context, t *userTurn) (loopAction, *runResult) {
	t.autoLoopCount++
	if t.autoLoopCount > 1 { // The first pass runs the initial prompt.
		if action, exit := r.claimSteering(ctx, t); action != proceed || exit != nil {
			return action, exit
		}
	}
	if t.autoLoopCount > agent.DefaultMaxAutoLoops {
		log.Printf("Auto mode: global turn cap (%d) reached, exiting", agent.DefaultMaxAutoLoops)
		notice := agent.BuildAutoTurnCapPrompt(agent.DefaultMaxAutoLoops)
		if r.cfg.DelegatedChild {
			return awaitUser, &runResult{
				Status: "failed",
				Error:  fmt.Sprintf("autonomous pass cap (%d) reached", agent.DefaultMaxAutoLoops),
			}
		}
		_ = r.sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputTurnLimit, notice, nil)
		return awaitUser, nil
	}
	return proceed, nil
}

// claimSteering makes a non-blocking check for a user message (including
// /stop) sent while the previous pass ran.
func (r *chatRuntime) claimSteering(ctx context.Context, t *userTurn) (loopAction, *runResult) {
	peeked, peekErr := r.sc.PeekForUserMessages(ctx)
	if peekErr != nil || len(peeked) == 0 {
		return proceed, nil
	}
	nextMsg, ok, _, immediate := nextPendingUserMessage(
		withoutStoppedMessage(peeked, r.stoppedMessageID), r.handledImmediate)
	if !ok {
		return proceed, nil
	}
	if immediate {
		r.handledImmediate[nextMsg.ID] = struct{}{}
	}
	claimed, won, claimErr := claimUserMessageWithRetry(ctx, r.sc, nextMsg)
	if claimErr != nil {
		return awaitUser, &runResult{Status: "failed", Error: fmt.Sprintf("claiming steering message: %v", claimErr)}
	}
	if !won {
		delete(r.handledImmediate, nextMsg.ID)
		return continueAgent, nil
	}
	trimmed := strings.TrimSpace(claimed.Content)
	if strings.EqualFold(trimmed, "/stop") {
		stoppedTasks := cancelActiveSubAgentTasks(r.subAgentRegistry)
		log.Printf("Autonomous run: /stop received from user — pausing (cancelled %d sub-agent tasks)", stoppedTasks)
		_ = r.sc.WriteActivity(ctx, "auto_stop", "User requested stop — pausing autonomous work", nil)
		return awaitUser, r.parkAfterStop(ctx, claimed.ID, "",
			"Stopped by user — a queued message is waiting; continuing with it now.")
	}
	if trimmed != "" || len(claimed.Images) > 0 {
		// Any other text or image message becomes the next interactive
		// runner pass. Do not leave the agent loop: doing so acknowledges
		// and permanently drops the input.
		log.Printf("Autonomous run: user message received — steering next pass")
		_ = r.sc.WriteActivity(ctx, "auto_interrupt", "User sent a message — steering autonomous work", nil)
		t.prompt = trimmed
		t.reply = trimmed
		t.images = claimed.Images
		// This message now drives the turn: a mid-turn flush must record it
		// as the pending prompt.
		t.messageID = claimed.ID
		t.promptMessageID = claimed.ID
		t.claimPending = true
		t.resumingInterrupted = false
		t.firstPass = true
		resetAutoLoopForSteering(&t.autoLoopCount, &t.tracker)
	}
	return proceed, nil
}

// livePolicyAttempts bounds the per-turn RuntimeProfile read retries when the
// pod still holds Git remote-write access that a failed read cannot verify.
const livePolicyAttempts = 3

// preflight reads the AgentRun once for the pass and resolves everything that
// can stop the turn before it starts (pending restart, Git policy change,
// degraded-pod heal, cost cap) plus the turn's mode, model, and limits.
func (r *chatRuntime) preflight(ctx context.Context, t *userTurn) (*turnPolicy, loopAction, *runResult) {
	cfg, sc := r.cfg, r.sc
	activeRun, err := readAgentRun(ctx, r.crd, cfg.TaskName, cfg.Namespace, 0)
	if err != nil {
		// Permission, restart, and cost policy all live on this object. A
		// turn must never start when current policy cannot be verified.
		log.Printf("ERROR: refusing to start turn %d: AgentRun policy read failed: %v", r.turnNumber, err)
		return nil, awaitUser, &runResult{
			Status: "failed",
			Error:  "unable to verify current run policy; refusing to start another turn",
		}
	}

	// A pending compute restart (spec.restartRequests above the handled
	// count) means the controller is about to tear this pod down and
	// re-provision it — e.g. a mid-run provider switch that must remount
	// credentials. Do not start a turn on the stale pod: it still runs with
	// the old credential env and would fail to resolve the new model (e.g.
	// "OpenAI API key is required"). Exit cleanly instead; the replacement
	// pod answers this turn with the new credentials.
	if restartPending(activeRun) {
		log.Printf("Turn %d: compute restart pending (restartRequests=%d handled=%d)"+
			" — exiting so the replacement pod handles this turn",
			r.turnNumber, activeRun.Spec.RestartRequests, activeRun.Status.RestartRequestsHandled)
		return nil, awaitUser, r.exitForReplacement(ctx, t,
			"Compute restart pending — the replacement pod will pick up this message")
	}

	// Git remote-write policy is baked into the registry and command
	// sandbox. Re-resolve it before every turn so a RuntimeProfile
	// restriction acts as a revocation rather than waiting for an unrelated
	// pod restart. A pod whose remote writes are already disabled has
	// nothing to revoke, so an unverifiable policy (no or missing
	// RuntimeProfile, API errors) only blocks a pod that still holds them.
	attempts := 1
	if cfg.GitRemoteWrites != agentpolicy.GitRemoteWritesDisabled {
		attempts = livePolicyAttempts
	}
	livePolicy := resolveRunPermissionMode(ctx, r.crd, activeRun, attempts)
	if ctx.Err() != nil {
		return nil, awaitUser, &runResult{Status: "failed", Error: "pod shutting down"}
	}
	if livePolicy.Degraded {
		if cfg.GitRemoteWrites != agentpolicy.GitRemoteWritesDisabled {
			msg := fmt.Sprintf("Unable to verify the current Git remote-write policy (%s);"+
				" refusing to start this turn — send a message to retry.", livePolicy.Reason)
			return nil, awaitUser, r.refuseTurn(ctx, "runtime_config", msg)
		}
	} else if liveGitRemoteWrites := agentpolicy.NormalizeGitRemoteWrites(
		livePolicy.GitRemoteWrites,
	); liveGitRemoteWrites != cfg.GitRemoteWrites {
		msg := fmt.Sprintf(
			"Git remote-write policy changed to %s — restarting compute before handling this message",
			liveGitRemoteWrites,
		)
		log.Printf("Turn %d: %s", r.turnNumber, msg)
		if err := patchAgentRunSpec(ctx, r.crd, cfg.TaskName, cfg.Namespace, func(fresh *platformv1alpha1.AgentRun) {
			fresh.Spec.RestartRequests++
		}); err != nil {
			log.Printf("ERROR: failed to request compute restart for Git policy change: %v", err)
			return nil, awaitUser, r.refuseTurn(ctx, "runtime_config", fmt.Sprintf(
				"Git remote-write policy changed to %s but a compute restart could not be requested; "+
					"refusing to start this turn — send a message to retry.", liveGitRemoteWrites))
		}
		return nil, awaitUser, r.exitForReplacement(ctx, t, msg)
	}

	// Self-heal a degraded read-only pod. When the startup fallback was
	// caused by an API failure or race, re-resolve from the live CRDs before
	// each turn: once resolution succeeds with a write mode, the registry
	// and command sandbox baked at startup are provably stale, so bounce
	// compute through the restart-request path (the same mechanism as
	// mid-run provider switches); the replacement pod answers this turn with
	// a writable workspace.
	if cfg.PermissionModeDegraded {
		if healedMode, ok := healedWritePermissionMode(livePolicy, activeRun); ok {
			msg := fmt.Sprintf("Write access recovered (%s) — restarting compute to lift the degraded"+
				" read-only workspace; the replacement pod will pick up this message", healedMode)
			log.Printf("Turn %d: %s", r.turnNumber, msg)
			if err := patchAgentRunSpec(ctx, r.crd, cfg.TaskName, cfg.Namespace, func(fresh *platformv1alpha1.AgentRun) {
				fresh.Spec.RestartRequests++
			}); err != nil {
				log.Printf("WARN: failed to request compute restart for permission-mode heal: %v — continuing read-only", err)
			} else {
				return nil, awaitUser, r.exitForReplacement(ctx, t, msg)
			}
		}
	}

	// Cost ceiling: pause before the next turn once the cap is reached,
	// mirroring the maxRuntime timeout pause. Raising spec.limits.maxCostUsd
	// resumes the run via the controller.
	capUSD, capConfigured, capErr := validatedCostCapUSD(activeRun)
	if capErr != nil {
		msg := "invalid configured cost cap; refusing to start another turn"
		log.Printf("ERROR: %s: %v", msg, capErr)
		_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, msg, nil)
		return nil, awaitUser, &runResult{Status: "failed", Error: msg}
	}
	model, provider := liveRuntimeModelAndProvider(cfg, activeRun)
	if capConfigured {
		if !r.turnModelPriced(model) {
			msg := fmt.Sprintf("A cost cap (spec.limits.maxCostUsd) is set, but model %q has no pricing metadata, "+
				"so spend cannot be enforced against the cap; refusing to start this turn — "+
				"switch to a priced model or remove the cap, then send a message to retry.", model)
			return nil, awaitUser, r.refuseTurn(ctx, "cost_cap_unenforced", msg)
		}
		if spentUSD := r.costBaselineUSD + r.tracker.Snapshot().CostUsd; spentUSD >= capUSD {
			// Background children would keep spending against a cap that is
			// already exhausted; the pause must stop them too.
			stoppedTasks := cancelActiveSubAgentTasks(r.subAgentRegistry)
			msg := fmt.Sprintf("Cost cap reached: $%.4f spent of the $%.2f limit"+
				" — increase spec.limits.maxCostUsd to resume.", spentUSD, capUSD)
			log.Printf("Cost cap reached ($%.4f >= $%.2f) — pausing run (cancelled %d sub-agent tasks)",
				spentUSD, capUSD, stoppedTasks)
			r.enqueueContinuation(ctx, t)
			return nil, awaitUser, r.pauseForCostCap(ctx, msg)
		}
	}

	p := &turnPolicy{
		run:           activeRun,
		modeName:      activeRun.Status.ModeName,
		toolAccess:    agent.ToolAccessLevelFull,
		capUSD:        capUSD,
		capConfigured: capConfigured,
	}
	sessionLabel := "chat"
	if p.modeName != "" {
		sessionLabel = p.modeName
	}
	if r.metaharnessWriter != nil && p.modeName != r.prevModeName && r.prevModeName != "" {
		r.metaharnessWriter.RecordModeSwitch(r.prevModeName, p.modeName)
	}
	r.prevModeName = p.modeName

	// Read-only modes (review, overseer, …) clamp the turn's tool access:
	// the runner adapts write tools to their read-only variants and the
	// command sandbox enforces the filesystem boundary. The pod-level
	// registry stays write-capable so a write-capable snapshot takes effect
	// without a pod restart.
	if snapshotClampsReadOnly(activeRun) {
		p.toolAccess = agent.ToolAccessLevelReadOnly
		log.Printf("Turn %d: mode %q is read-only — tools clamped for this turn", r.turnNumber, p.modeName)
	}
	r.tracker.SetSession(r.turnNumber, sessionLabel)
	r.tracker.SetStep("starting")
	if r.eventStream != nil {
		r.eventStream.SetSession(r.turnNumber)
		r.eventStream.SetStep("starting")
	}

	// Apply mode instructions and limits from the CRD snapshot.
	p.mo = readModeOverrides(ctx, r.crd, activeRun)

	p.model, p.provider = model, provider
	// Role model routing follows the provider selected for this turn. Each
	// turn gets immutable specialist clones so queued/running async tasks
	// retain the routing snapshot they were spawned with.
	p.roleCatalog = r.roleCatalog
	if refreshed, roleErr := loadRoleCatalog(ctx, r.crd, p.provider, activeRun.Spec.RoleModelOverrides); roleErr != nil {
		log.Printf("WARN: failed to refresh role models for provider %q: %v", p.provider, roleErr)
		if !strings.EqualFold(strings.TrimSpace(p.provider), r.roleCatalogProvider) {
			p.roleCatalog = roleCatalogWithoutModelOverrides(r.roleCatalog)
		}
	} else {
		p.roleCatalog = refreshed
		r.roleCatalog = refreshed
		r.roleCatalogProvider = strings.ToLower(strings.TrimSpace(p.provider))
	}
	return p, proceed, nil
}

// modelPricingKnownForTurn resolves whether the tracker can price usage on
// a turn's model; a var so tests can stub provider resolution.
var modelPricingKnownForTurn = func(cfg runConfig, model string) bool {
	return modelPricingKnown(resolveConfiguredModel(cfg, model), agent.Usage{InputTokens: 1, OutputTokens: 1})
}

// turnModelPriced reports whether model has pricing metadata. Only positive
// results are cached: an unpriced model refuses the turn, and the next
// message re-checks.
func (r *chatRuntime) turnModelPriced(model string) bool {
	if _, ok := r.pricedModels[model]; ok {
		return true
	}
	if !modelPricingKnownForTurn(r.cfg, model) {
		return false
	}
	if r.pricedModels == nil {
		r.pricedModels = map[string]struct{}{}
	}
	r.pricedModels[model] = struct{}{}
	return true
}

// refuseTurn parks the session behind a circuit-break notice instead of
// starting the turn; delegated children fail since nobody can answer.
func (r *chatRuntime) refuseTurn(ctx context.Context, activity, msg string) *runResult {
	log.Printf("ERROR: turn %d: %s", r.turnNumber, msg)
	if r.cfg.DelegatedChild {
		return &runResult{Status: "failed", Error: msg}
	}
	_ = r.sc.WriteActivity(ctx, activity, msg, nil)
	_ = r.sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, msg, nil)
	return nil
}

// prepareTurn builds the pass's agent, input, and run config, and opens its
// durable SDK run.
func (r *chatRuntime) prepareTurn(
	ctx context.Context, t *userTurn, p *turnPolicy,
) (*preparedTurn, loopAction, *runResult) {
	cfg, sc := r.cfg, r.sc
	parentModelSettings := parentModelSettingsForTurn(r.baseAgent.ModelSettings, p.mo.ModelSettings)
	turnSpecialistAgents := specialistAgentsForRoleCatalog(r.specialistAgents, p.roleCatalog, p.model, parentModelSettings)
	turnHandoffs := handoffsForSpecialists(r.baseAgent.Handoffs, turnSpecialistAgents)
	maxTurns := int32(agent.DefaultMaxTurns)
	subAgentMaxTurns := int32(agent.DefaultSubAgentMaxTurns)
	if p.mo.MaxTurns > 0 {
		maxTurns = p.mo.MaxTurns
	}
	if p.mo.SubAgentMaxTurns > 0 {
		subAgentMaxTurns = p.mo.SubAgentMaxTurns
	}

	platformHooks := agent.NewEventHooks(r.tracker, r.eventStream)
	platformHooks.Turn = int(r.turnNumber)
	checkpointHooks := &workspaceCheckpointHooks{snapshotter: activeWorkspaceSnapshotter.Load()}
	// Subagent runs fall back to DefaultHooks. Meta-Harness capture policy is
	// explicit full capture: subagent tool and LLM hook events are recorded
	// in the same trace as the parent run.
	if r.metaharnessWriter != nil {
		r.runner.DefaultHooks = agent.NewCompositeHooks(platformHooks, checkpointHooks, r.metaharnessWriter)
	} else {
		r.runner.DefaultHooks = agent.NewCompositeHooks(platformHooks, checkpointHooks)
	}

	// Composite: workspace durability + MetaHarness trace writer (optional)
	// + the context-usage gauge feeding the dashboard's context bar.
	// Sub-agent runs fall back to DefaultHooks and never touch the gauge.
	ctxUsageHooks := &contextUsageHooks{mainAgentName: r.baseAgent.Name}
	var runHooks agent.RunHooks = agent.NewCompositeHooks(platformHooks, checkpointHooks, ctxUsageHooks)
	if r.metaharnessWriter != nil {
		runHooks = agent.NewCompositeHooks(platformHooks, checkpointHooks, ctxUsageHooks, r.metaharnessWriter)
	}
	if r.standingRefreshHooks != nil {
		runHooks = agent.NewCompositeHooks(runHooks, r.standingRefreshHooks)
	}

	// Resolve compaction thresholds per-turn using the active model.
	compactionConfig := resolveCompactionConfig(ctx, p.model, p.provider, r.modelMetadata)
	// Publish the budget the runner will actually use (the per-model
	// resolver supersedes the base config, mirroring runner logic) so the
	// dashboard's context bar shows the real compaction point.
	effectiveBudget := compactionConfig
	if r.compactionResolver != nil && effectiveBudget.Enabled {
		if trigger, target, ok := r.compactionResolver(ctx, p.model); ok && trigger > 0 {
			effectiveBudget.TriggerTokens = trigger
			effectiveBudget.TargetTokens = target
		}
	}
	publishContextBudget(effectiveBudget)

	workingState := r.applyFirstPassWorkingStateUpdate(ctx, t, p.modeName, r.readTurnWorkingState(ctx))

	messages := r.loadTurnMessages(ctx, p.run, workingState.HistoryFloorMessageID)

	// A moved history floor means the session context was cleared or
	// compacted externally — the in-memory transcript is stale.
	if workingState.HistoryFloorMessageID != r.tx.floor {
		if len(r.tx.items) > 0 {
			// The persisted snapshot is stale for the same reason.
			if err := sc.ClearTranscriptBlob(ctx); err != nil {
				log.Printf("WARN: failed to clear transcript snapshot after context clear: %v", err)
			}
		}
		r.tx.items = nil
		r.tx.floor = workingState.HistoryFloorMessageID
	}

	// Fold durable messages recorded since the transcript was captured
	// (plan rejections, mode-switch notes, …) into the transcript: they are
	// in neither FinalHistory nor the current user item, so verbatim replay
	// would silently drop them. The durable-tail fallback includes them by
	// construction. Folding into the transcript itself (not just this turn's
	// input) keeps them across turn failures too. This pass's own prompt is
	// sent as the user item below, so it is skipped in both.
	if len(r.tx.items) > 0 {
		r.tx.items = append(r.tx.items, outOfBandMessageItems(
			messages, r.tx.seen, workingState, r.tx.selfAssistant, t.promptMessageID)...)
	}
	r.tx.seen = maxSeenMessageID(r.tx.seen, messages)

	inputItems := buildTurnInput(r.tx.items, messages, workingState, t.promptMessageID, recentConversationMessageLimit)
	turnOpeningText := t.prompt
	if t.firstPass && t.resumingInterrupted && len(r.tx.items) > 0 {
		// This message started the turn a pod termination cut short: its
		// prompt is already in the replayed transcript, followed by the
		// partial work it triggered. Open with a continuation instruction
		// instead of repeating the prompt. (If the restored transcript was
		// discarded — floor moved, decode failure — the verbatim prompt is
		// kept: nothing replays it.)
		turnOpeningText = podResumeContinuationPrompt
		log.Printf("Turn %d resumes the pod-terminated turn for message %d"+
			" — continuing from the preserved partial transcript", r.turnNumber, t.messageID)
	}
	if t.firstPass && r.interruptedSubAgentNotice != "" {
		turnOpeningText += "\n\n" + r.interruptedSubAgentNotice
		r.interruptedSubAgentNotice = ""
	}
	if assetPaths, assetErr := materializeMessageAssets(
		ctx, cfg.RepoDir, p.run, sc.StateStore(), t.images); assetErr != nil {
		// Preserve ordinary vision delivery if workspace materialization is
		// unavailable; the model still receives the image attachment below.
		log.Printf("WARN: failed to materialize current message project assets: %v", assetErr)
	} else {
		turnOpeningText = appendMessageAssetNotice(turnOpeningText, assetPaths)
	}
	userItem := agent.RunItem{
		Type:    agent.RunItemMessage,
		Message: &agent.MessageOutput{Text: turnOpeningText, Images: toSDKImageAttachments(t.images)},
	}
	inputItems = append(inputItems, userItem)
	workingStateContext := buildWorkingStateContext(workingState)

	if cfg.Debug {
		// Prompts and working state may contain credentials or private source.
		// Diagnostics expose only sizes, never content.
		log.Printf("[input-diag] prompt_len=%d images=%d input_items=%d mode_instructions_len=%d working_state_len=%d",
			len(t.prompt), len(t.images), len(inputItems), len(p.mo.ModeInstructions), len(workingStateContext))
	}

	log.Printf("Turn %d: mode=%q, %d tools", r.turnNumber, p.modeName, len(r.baseAgent.Tools))

	turnAgent := r.baseAgent.Clone(
		agent.WithModel(p.model),
		agent.WithTools(r.baseAgent.Tools...),
		agent.WithHandoffs(turnHandoffs...),
	)
	turnAgent.ModelSettings = parentModelSettings
	attachLoadedSkillInstructions(turnAgent, r.loadSkillTool)

	modeDirectiveText := strings.TrimSpace(p.mo.ModeInstructions)
	modeDirectiveText = strings.TrimSpace(modeDirectiveText + "\n\n" + commitAttributionPolicyPrompt())
	// Connected MCP servers ride along too, so the agent knows the
	// mcp__<server>__<tool> tools exist.
	if r.mcpPromptBlock != "" {
		modeDirectiveText = strings.TrimSpace(modeDirectiveText + "\n\n" + r.mcpPromptBlock)
	}
	allowedMutatingTools := effectiveRuntimeAllowedMutatingTools(
		ctx, r.crd, p.run, r.maintainedRepositoryName, r.maintainedRepositoryNamespace,
	)
	activeRun := p.run
	runCfg := sdkruntime.BuildRunConfig(sdkruntime.Config{
		Provider:               p.provider,
		Model:                  p.model,
		FallbackModels:         mergedFallbackModels(cfg, p.model, p.mo.FallbackModels),
		WorkDir:                cfg.RepoDir,
		ToolOutputDir:          workspaceScratchDir,
		ActiveMode:             p.modeName,
		ModeSnapshot:           platformModeSnapshotForSDK(activeRun.Status.ModeSnapshot),
		MaxTurns:               int(maxTurns),
		SubAgentMaxTurns:       int(subAgentMaxTurns),
		MaxConcurrentSubAgents: p.mo.MaxConcurrentSubAgents,
		ToolAccess:             p.toolAccess,
		AllowedMutatingTools:   allowedMutatingTools,
		GitRemoteWrites:        cfg.GitRemoteWrites,
		TracingProcessor:       r.tp,
		Debug:                  cfg.Debug,
		ModeDirectiveText:      modeDirectiveText,
		WorkingStateText:       workingStateContext,
		Trace:                  r.runTrace,
		ParentSpanID:           r.runTrace.ID,
		// Explicit per-turn runtime features (SDK v0.0.7+): tools and
		// guardrails are passed directly above; only run-loop behavior is
		// feature-gated here.
		Features: &sdkruntime.Features{
			Modes: sdkruntime.ModeFeatures{
				Instructions: true,
			},
			Runtime: sdkruntime.RuntimeFeatures{
				Retry:                 true,
				Tracing:               r.tp != nil,
				ImmediateInputPolling: true,
				ParallelToolCalls:     true,
				UntrustedToolOutputs:  true,
				// Enables the per-model compaction threshold resolver; the
				// explicit CompactionConfig below is used either way.
				Compaction: true,
			},
		},
		ImmediateInputPoller: func(ctx context.Context) ([]agent.RunItem, error) {
			return pollImmediateInputs(ctx, sc, cfg.RepoDir, activeRun, r.stoppedMessageID, r.handledImmediate)
		},
		CompactionConfig:          &compactionConfig,
		CompactionModelResolver:   r.compactionResolver,
		CompactionRecorder:        r.recordCompaction,
		CompactionFailureReporter: r.reportCompactionFailure,
		CompactionCarryForward:    r.compactionCarryForward(workingState, p.modeName),
		HandoffHistory:            &r.handoffHistoryConfig,
		ToolInputRules:            r.toolInputGuardrails,
		ToolOutputRules:           r.toolOutputGuardrails,
	}, runHooks)

	// Long autonomous tasks: bounce the first final answer back with a
	// verify-your-work prompt (Terminus 2 double-confirm pattern).
	runCfg.RequireCompletionConfirmation = true
	if criticVerifierEnabled() {
		// One-round adversarial review by a read-only critic before
		// finalizing (SDK v0.0.9 NewCriticVerifier).
		critic := turnAgent.Clone()
		critic.Instructions = ""
		runCfg.FinalAnswerVerifier = newCriticVerifier(
			r.runner,
			critic,
			t.prompt,
			runCfg.RetryPolicy,
			runCfg.ModelCallTimeout,
		)
	}

	durablePass, err := reserveSDKDurablePass(ctx, sc, t.messageID)
	if err != nil {
		return nil, awaitUser, &runResult{Status: "failed", Error: err.Error()}
	}
	storedRun, err := openSDKStoredRun(ctx, cfg, t.messageID, durablePass, r.stopRequested)
	if errors.Is(err, errDurableRunOpenStopped) {
		log.Printf("Turn %d: stopped by user while waiting for the durable run lease", r.turnNumber)
		return nil, awaitUser, r.parkAfterStop(ctx, t.messageID,
			"Stopped by user before the turn started.",
			"Stopped by user before the turn started — continuing with the next queued message.")
	}
	if err != nil {
		return nil, awaitUser, &runResult{Status: "failed", Error: fmt.Sprintf("opening durable SDK run: %v", err)}
	}
	runCfg.Durable = storedRun.RunConfig()

	if r.subAgentRegistry != nil {
		runCfg.Durable.Children = r.subAgentRegistry.SchedulerCheckpoint
		agent.ConfigureSubAgentScheduler(r.subAgentRegistry, agent.SubAgentSchedulerConfig{
			Runner:           r.runner,
			Agents:           turnSpecialistAgents,
			Tracker:          r.tracker,
			EventStream:      r.eventStream,
			MaxConcurrent:    p.mo.MaxConcurrentSubAgents,
			MaxTurns:         int(subAgentMaxTurns),
			WorkDir:          cfg.RepoDir,
			ToolOutputDir:    workspaceScratchDir,
			ToolAccessLevel:  p.toolAccess,
			ToolPolicy:       runCfg.ToolPolicy,
			CompactionConfig: compactionConfig,
			// Sub-agents pinned to other models compact at their own model's
			// window instead of inheriting the parent's.
			CompactionModelResolver: r.compactionResolver,
			Checkpoint:              r.subAgentCheckpoints.persistCheckpoint,
		})
		// One automatic resume attempt per restored task. Tasks the SDK
		// refuses to resume are surfaced to the model and the user exactly
		// once instead of being retried every turn.
		resumeCtx := agent.WithNestedRunConfig(ctx, runCfg)
		if stuck := resumeReconcilingSubAgentTasks(resumeCtx, r.subAgentRegistry, r.resumeAttempted); len(stuck) > 0 {
			_ = sc.WriteActivity(ctx, "subagent_reconcile_required", stuckSubAgentActivity(stuck), nil)
			inputItems = append(inputItems, agent.RunItem{
				Type:    agent.RunItemMessage,
				Message: &agent.MessageOutput{Text: stuckSubAgentNotice(stuck)},
			})
		}
	}

	return &preparedTurn{
		turnPolicy:  p,
		agent:       turnAgent,
		input:       inputItems,
		userItem:    userItem,
		runCfg:      runCfg,
		durablePass: durablePass,
		storedRun:   storedRun,
	}, proceed, nil
}

// readTurnWorkingState reads the durable working state, retrying briefly. An
// unreadable state must not pass for a moved history floor (which would wipe
// the transcript and its snapshot), so it keeps the in-memory floor.
func (r *chatRuntime) readTurnWorkingState(ctx context.Context) sessionclient.WorkingState {
	var state sessionclient.WorkingState
	err := retryTransient(ctx, "reading durable working state", 3, func(ctx context.Context) error {
		var err error
		state, err = r.sc.ReadWorkingState(ctx)
		return err
	})
	if err != nil {
		log.Printf("WARN: failed to read durable working state: %v — skipping the history floor check", err)
		state = sessionclient.WorkingState{HistoryFloorMessageID: r.tx.floor}
	}
	return state
}

// applyFirstPassWorkingStateUpdate writes goal, last user message, and current
// mode to the durable working state on the first pass of a user turn.
// It returns the updated snapshot so prepareTurn sees consistent values.
// When t.firstPass is false the state is returned unchanged.
func (r *chatRuntime) applyFirstPassWorkingStateUpdate(
	ctx context.Context, t *userTurn, modeName string,
	ws sessionclient.WorkingState,
) sessionclient.WorkingState {
	if !t.firstPass {
		return ws
	}
	goal := deriveWorkingStateGoal(t.reply, t.prompt)
	if err := r.sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
		state.Goal = goal
		state.LastUserMessage = strings.TrimSpace(t.prompt)
		state.CurrentMode = modeName
		return nil
	}); err != nil {
		log.Printf("WARN: failed to update working state for user turn: %v", err)
	} else {
		ws.Goal = strings.TrimSpace(goal)
		ws.LastUserMessage = strings.TrimSpace(t.prompt)
		ws.CurrentMode = modeName
	}
	return ws
}

// loadTurnMessages returns the durable messages the turn's context needs.
// The full post-floor history is loaded on this pod's first pass (and
// whenever the transcript is not live): the durable-tail fallback and asset
// re-materialization need the recent messages the transcript does not
// cover. With a live transcript only messages above its watermark matter —
// the out-of-band fold ignores everything else — so later passes fetch
// just those instead of the whole, ever-growing history.
func (r *chatRuntime) loadTurnMessages(
	ctx context.Context, run *platformv1alpha1.AgentRun, floor int64,
) []store.Message {
	since := floor
	if r.historyLoaded && len(r.tx.items) > 0 && floor == r.tx.floor && r.tx.seen > since {
		since = r.tx.seen
	}
	messages, err := r.sc.GetMessagesSince(ctx, since)
	if err != nil {
		log.Printf("WARN: failed to load session messages for context rebuild: %v", err)
		return nil
	}
	r.historyLoaded = true
	prepared, assetErr := materializeRecentConversationAssets(
		ctx, r.cfg.RepoDir, run, r.sc.StateStore(), messages, recentConversationMessageLimit+1)
	if assetErr != nil {
		log.Printf("WARN: failed to materialize recent project assets: %v", assetErr)
		return messages
	}
	return prepared
}

func (r *chatRuntime) reportCompactionFailure(scope, reason string, tokensBefore, tokensAfter int) {
	detail, _ := json.Marshal(map[string]any{
		"scope":         scope,
		"reason":        reason,
		"tokens_before": tokensBefore,
		"tokens_after":  tokensAfter,
	})
	_ = r.sc.WriteActivity(context.Background(), "compact_boundary_skipped",
		fmt.Sprintf("Context compaction skipped (%s): %s", scope, reason), detail)
}

func (r *chatRuntime) recordCompaction(tokensBefore, tokensAfter int, summary string) {
	r.tracker.RecordCompactBoundary(tokensBefore, tokensAfter, summary)
	if r.eventStream != nil {
		r.eventStream.EmitCompaction(tokensBefore, tokensAfter, summary)
	}
}

// compactionCarryForward returns the briefing a mid-turn compaction keeps:
// the live mode and step plus the latest durable working state.
func (r *chatRuntime) compactionCarryForward(
	workingState sessionclient.WorkingState, modeName string,
) func(context.Context) string {
	return func(ctx context.Context) string {
		state := workingState
		if latestState, err := r.sc.ReadWorkingState(ctx); err == nil {
			state = latestState
		}

		liveModeName := modeName
		liveStep := ""
		if liveRun := getAgentRun(ctx, r.crd, r.cfg.TaskName, r.cfg.Namespace); liveRun != nil {
			if liveRun.Status.ModeName != "" {
				liveModeName = liveRun.Status.ModeName
			}
			liveStep = liveRun.Status.CurrentStep
		}

		if liveModeName != "" {
			state.CurrentMode = liveModeName
		}

		var parts []string
		var liveState []string
		if liveModeName != "" {
			liveState = append(liveState, "mode="+liveModeName)
		}
		if liveStep != "" {
			liveState = append(liveState, "step="+liveStep)
		}
		if len(liveState) > 0 {
			parts = append(parts, "Live AgentRun state: "+strings.Join(liveState, " "))
		}
		if stateContext := buildWorkingStateContext(state); stateContext != "" {
			parts = append(parts, stateContext)
		}
		// Compaction already invalidates the cached prefix, so this is the
		// cheap moment to surface a fresh durable-state briefing.
		if prime := refreshPrimeContext(ctx, r.psStore, r.cfg.TaskName); prime != "" {
			parts = append(parts, prime)
		}
		return strings.Join(parts, "\n\n")
	}
}

// stopRequested consumes a pending user stop; used while a pass waits for its
// durable run lease, before the turn's own interrupt watcher exists.
func (r *chatRuntime) stopRequested(ctx context.Context) bool {
	req, err := r.sc.ConsumeInterrupt(ctx)
	return err == nil && req != nil
}

// executeTurn runs the pass under its own cancellable context watched by the
// interrupt poller: a user stop request cancels the in-flight model call and
// running tools without touching the run context. The budget guard shares
// the same cancellation to enforce spec.limits.maxCostUsd mid-turn (soft
// stop at the next model call, hard stop past the overshoot margin).
func (r *chatRuntime) executeTurn(ctx context.Context, pt *preparedTurn) turnOutcome {
	turnCtx, cancelTurn := context.WithCancel(ctx)
	defer cancelTurn()
	watcher := startTurnInterruptWatcher(ctx, r.sc, cancelTurn)
	var out turnOutcome
	if pt.capConfigured {
		out.budgetGuard = startTurnBudgetGuard(ctx, r.costBaselineUSD, pt.capUSD, r.tracker, cancelTurn)
		pt.runCfg.Hooks = agent.NewCompositeHooks(pt.runCfg.Hooks, out.budgetGuard)
	}
	// A finish summary belongs to the pass that called finish.
	r.finishSummary.Reset()
	out.result, out.err = r.runner.Run(turnCtx, pt.agent, pt.input, pt.runCfg)
	out.interrupted = watcher.Finish()
	if out.budgetGuard != nil {
		out.budgetStop = out.budgetGuard.Finish()
	}
	if r.subAgentRegistry != nil {
		if checkpointErr := r.subAgentRegistry.FlushCheckpoint(); checkpointErr != nil {
			r.subAgentRegistry.CancelAll()
			if out.err == nil {
				out.err = fmt.Errorf("sub-agent durability checkpoint failed: %w", checkpointErr)
			}
		}
	}
	// Claim stop requests that raced the end of the turn (or duplicates of
	// the one the watcher consumed) so they stop this loop instead of
	// killing a later, innocent turn.
	if req, err := r.sc.DrainInterruptsThrough(ctx, time.Now().UTC()); err != nil {
		log.Printf("WARN: failed to drain stop requests after the turn: %v", err)
	} else if req != nil {
		out.interrupted = true
	}
	return out
}

// handleTurnError handles a pass whose runner.Run failed: pod shutdown, user
// stop, mid-turn cost cap, turn-budget exhaustion, or a recoverable failure.
// The pass's durable SDK run is released on every path.
func (r *chatRuntime) handleTurnError(
	ctx context.Context, t *userTurn, pt *preparedTurn, out turnOutcome,
) (loopAction, *runResult) {
	defer func() {
		if closeErr := closeSDKStoredRun(pt.storedRun); closeErr != nil {
			log.Printf("WARN: closing failed durable SDK run: %v", closeErr)
		}
	}()
	sc := r.sc
	result, err := out.result, out.err
	if ctx.Err() != nil {
		return awaitUser, r.exitOnShutdown(ctx, t, result)
	}
	// User-requested interrupt: stop sub-agents too, surface the stop, and
	// keep the session alive awaiting the next message.
	if out.interrupted {
		stoppedTasks := cancelActiveSubAgentTasks(r.subAgentRegistry)
		log.Printf("Turn %d interrupted by user (cancelled %d sub-agent tasks)", r.turnNumber, stoppedTasks)
		// The cancelled turn hands back its accumulated conversation (SDK
		// partial-result semantics). Persist it so the stop does not amnesia
		// the session: the user's next message continues with the
		// interrupted turn's tool outputs, findings, and delivered sub-agent
		// results instead of the pre-turn state. Unlike pod termination,
		// user stop records no pending resume prompt: a replacement must
		// wait for a genuinely newer user message.
		if preserved := transcriptAfterRun(result); len(preserved) > 0 {
			r.tx.items = preserved
			r.tx.persistInFlight(ctx, sc, 0)
			activeWorkspaceSnapshotter.Load().SnapshotAsync("turn-interrupted")
			log.Printf("Turn %d: preserved %d interrupted-turn conversation items", r.turnNumber, len(r.tx.items))
		}
		_ = sc.WriteActivity(ctx, "turn_interrupted", turnInterruptNotice(stoppedTasks), nil)
		// A steered message the user queued before (or while) stopping must
		// flow directly into the next turn instead of bouncing the session
		// through the Stopped awaiting-input state.
		return awaitUser, r.parkAfterStop(ctx, t.messageID, "", "A queued message is waiting — continuing with it now.")
	}
	// Budget guard stop: the turn crossed spec.limits.maxCostUsd mid-flight.
	// Preserve the accumulated progress (with the pending resume marker, so
	// a replacement pod continues the turn instead of replaying it) and
	// pause the run exactly like the pre-turn cost check: raising the cap
	// resumes it via the controller.
	if out.budgetStop {
		stoppedTasks := cancelActiveSubAgentTasks(r.subAgentRegistry)
		notice := out.budgetGuard.notice()
		log.Printf("Turn %d stopped by cost cap (cancelled %d sub-agent tasks)", r.turnNumber, stoppedTasks)
		if preserved := transcriptAfterRun(result); len(preserved) > 0 {
			r.tx.items = preserved
			r.tx.persistInFlight(ctx, sc, t.messageID)
			activeWorkspaceSnapshotter.Load().SnapshotAsync("cost-cap")
			log.Printf("Turn %d: preserved %d cost-capped conversation items", r.turnNumber, len(r.tx.items))
		}
		r.enqueueContinuation(ctx, t)
		return awaitUser, r.pauseForCostCap(ctx, notice)
	}
	if errors.Is(err, context.Canceled) {
		return awaitUser, r.exitOnShutdown(ctx, t, result)
	}
	// Turn budget exhausted: unlike other turn failures, the SDK hands back
	// the accumulated conversation as a partial result. Persist the
	// transcript + working state and park the session so the user's next
	// message CONTINUES with full context instead of retrying from the
	// pre-turn history with amnesia. Older SDKs return a nil result here;
	// the branch then falls through to the generic turn-failure path below.
	var budgetErr *agent.MaxTurnsExceeded
	if errors.As(err, &budgetErr) && result != nil && len(result.FinalHistory) > 0 {
		return r.handleTurnBudgetExhausted(ctx, t, result, budgetErr.MaxTurns)
	}
	// Delegated children must reach a terminal phase — a parent team run is
	// blocked on this run's result.
	if r.cfg.DelegatedChild {
		return awaitUser, &runResult{Status: "failed", Error: fmt.Sprintf("chat session %d failed: %v", r.turnNumber, err)}
	}
	// A failed turn (LLM/API error after the SDK exhausted its retries and
	// model fallbacks, guardrail trip, or a turn cap on an SDK without
	// partial-result support) must not kill the session: stop, surface the
	// error, and wait for the user's next message to retry. The parent turn
	// is gone, so nothing will join or steer the children it spawned; stop
	// them before parking the session.
	stoppedTasks := cancelActiveSubAgentTasks(r.subAgentRegistry)
	notice := turnFailureNotice(r.turnNumber, err)
	log.Printf("ERROR: chat session %d failed (recoverable, awaiting user; cancelled %d sub-agent tasks): %v",
		r.turnNumber, stoppedTasks, err)
	_ = sc.WriteActivity(ctx, "turn_failed", notice, nil)
	_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, notice, nil)
	// Newer SDKs hand the failed turn's accumulated conversation back
	// alongside the error (partial-result semantics): persist it so the
	// retry continues from the completed turns — tool outputs, findings,
	// delivered sub-agent results — instead of re-running the whole turn
	// with amnesia. The in-flight prompt rides along as pending so a pod
	// recycle re-delivers it as a continuation, not a verbatim replay.
	if preserved := transcriptAfterRun(result); len(preserved) > 0 {
		r.tx.items = preserved
		r.tx.persistInFlight(ctx, sc, t.messageID)
		activeWorkspaceSnapshotter.Load().SnapshotAsync("turn-failed")
		log.Printf("Turn %d: preserved %d failed-turn conversation items", r.turnNumber, len(r.tx.items))
	} else if len(r.tx.items) > 0 {
		// Older SDKs return no partial state. Keep the failed turn's user
		// message visible next turn (parity with the durable tail, which
		// includes it). An empty transcript keeps the durable-tail fallback
		// intact.
		r.tx.items = append(r.tx.items, pt.userItem)
	}
	return awaitUser, nil
}

// handleTurnBudgetExhausted preserves a turn that used its whole LLM turn
// budget and parks it (or rolls a standing maintainer over).
func (r *chatRuntime) handleTurnBudgetExhausted(
	ctx context.Context, t *userTurn, result *agent.RunResult, maxTurns int,
) (loopAction, *runResult) {
	sc := r.sc
	r.tx.items = transcriptAfterRun(result)
	turnSummary := buildAssistantTurnSummary(result.NewItems)
	if updateErr := sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
		if turnSummary != "" {
			state.LastAssistantSummary = turnSummary
			state.RecentTurnSummaries = append(state.RecentTurnSummaries, turnSummary)
		}
		return nil
	}); updateErr != nil {
		log.Printf("WARN: failed to persist working state after budget exhaustion: %v", updateErr)
	}
	// Durable snapshot: a pod recycle must not lose the preserved
	// conversation; same for this turn's file changes in the workspace. The
	// in-flight prompt rides along as pending — the resume cursor
	// re-delivers it to a recycled pod, which must not replay it verbatim on
	// top of the preserved partial progress.
	r.tx.persistInFlight(ctx, sc, t.messageID)
	activeWorkspaceSnapshotter.Load().SnapshotAsync("turn-budget")
	if r.cfg.DelegatedChild {
		msg := fmt.Sprintf("chat session %d exhausted its %d-turn budget", r.turnNumber, maxTurns)
		if turnSummary != "" {
			msg += "\nPartial progress before the budget ran out: " + turnSummary
		}
		return awaitUser, &runResult{Status: "failed", Error: msg}
	}
	// A standing maintainer lives in an event loop where every wake is a
	// turn, so the per-message budget is an episode boundary, not a reason
	// to wait for a human. Roll over: enqueue a durable continuation prompt
	// so the next message loop pass resumes on the preserved transcript with
	// a fresh budget (continue-as-new). Bounded so a maintainer that burns
	// whole budgets without ever blocking on the event wait still ends up
	// parked for a human.
	if r.maintainedRepositoryName != "" && r.standingRollovers < maxStandingBudgetRollovers {
		r.standingRollovers++
		notice := standingBudgetRolloverNotice(r.turnNumber, maxTurns, r.standingRollovers, maxStandingBudgetRollovers)
		log.Printf("Turn %d exhausted the %d-turn budget — standing maintainer rollover %d/%d",
			r.turnNumber, maxTurns, r.standingRollovers, maxStandingBudgetRollovers)
		_ = sc.WriteActivity(ctx, "turn_budget_rollover", notice, nil)
		if _, appendErr := sc.AppendUserMessageWithMode(
			ctx, standingBudgetRolloverPrompt, sessionclient.UserMessageModeEnqueue); appendErr != nil {
			log.Printf("WARN: failed to enqueue standing maintainer continuation: %v", appendErr)
			_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak,
				turnBudgetNotice(r.turnNumber, maxTurns), nil)
		}
		return awaitUser, nil
	}
	notice := turnBudgetNotice(r.turnNumber, maxTurns)
	log.Printf("Turn %d exhausted the %d-turn budget — transcript preserved (%d items), awaiting user", r.turnNumber, maxTurns, len(r.tx.items))
	_ = sc.WriteActivity(ctx, "turn_budget_exhausted", notice, nil)
	_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, notice, nil)
	return awaitUser, nil
}

// turnCommitTimeout bounds the commit-critical writes of a finished pass.
// They run detached from pod shutdown so a SIGTERM mid-commit does not
// abandon a completed pass; the bound keeps them inside the termination
// grace period (the workspace snapshot alone may take
// workspaceCheckpointTimeout).
const turnCommitTimeout = workspaceCheckpointTimeout + 15*time.Second

// commitTurn durably commits a successful pass: transcript and workspace
// first (so a crashed commit replays the completed SDK pass without
// repeating model/tool effects), then the assistant reply that completes the
// user claim, then one working-state write. The durable SDK run is closed
// exactly once.
func (r *chatRuntime) commitTurn(ctx context.Context, t *userTurn, pt *preparedTurn, out turnOutcome, post *platformv1alpha1.AgentRun) *runResult {
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), turnCommitTimeout)
	defer cancel()
	err := r.commitTurnState(commitCtx, t, pt, out.result, post)
	if closeErr := closeSDKStoredRun(pt.storedRun); closeErr != nil && err == nil {
		err = fmt.Errorf("closing durable SDK run: %w", closeErr)
	}
	if err != nil {
		return &runResult{Status: "failed", Error: err.Error()}
	}
	// Retain the completed checkpoint: a replacement may already be waiting
	// to open this pass and must replay it rather than recreate the same ID.
	return nil
}

func (r *chatRuntime) commitTurnState(ctx context.Context, t *userTurn, pt *preparedTurn, result *agent.RunResult, post *platformv1alpha1.AgentRun) error {
	sc := r.sc
	// The displayed assistant message is the model's actual reply
	// (FinalText). buildAssistantTurnSummary produces a compact,
	// tool-annotated summary ("Tools: …", "Key results: …") meant for
	// durable cross-turn context — it must NOT be shown to the user as the
	// assistant's chat message. When there is no natural-language reply
	// (e.g. a turn that ends by calling finish), prefer the finish tool's
	// summary, falling back to the turn summary.
	turnSummary := buildAssistantTurnSummary(result.NewItems)
	displayMessage := strings.TrimSpace(result.FinalText())
	if displayMessage == "" {
		if fs := strings.TrimSpace(r.finishSummary.Summary()); fs != "" {
			displayMessage = fs
		} else {
			displayMessage = turnSummary
		}
	}
	// Persist recoverable state before consuming the user claim. If the
	// host commit crashes, a replacement reclaims the message and reopens
	// this same completed SDK pass without repeating model/tool effects.
	if err := r.tx.persistRequired(ctx, sc); err != nil {
		return fmt.Errorf("persisting pre-commit transcript: %w", err)
	}
	if err := activeWorkspaceSnapshotter.Load().snapshotSync(ctx, "turn-end"); err != nil {
		return fmt.Errorf("persisting pre-commit workspace: %w", err)
	}

	hostTurnCommitted := false
	if displayMessage != "" {
		passKey := fmt.Sprintf("%s:%d:%d", r.cfg.TaskUID, t.messageID, pt.durablePass)
		msg, err := sc.AppendAssistantForDurablePass(ctx, passKey, displayMessage)
		if err != nil {
			return fmt.Errorf("committing durable assistant response: %w", err)
		}
		if msg != nil {
			// Already in FinalHistory — the next turn's out-of-band fold must
			// not duplicate it.
			r.tx.selfAssistant = msg.ID
			hostTurnCommitted = true
		}
	} else if err := sc.CompleteClaims(ctx); err != nil {
		return fmt.Errorf("completing claims for empty durable response: %w", err)
	} else {
		hostTurnCommitted = true
	}
	if hostTurnCommitted {
		t.claimPending = false
	}

	updatedModeName := pt.modeName
	if post != nil && post.Status.ModeName != "" {
		updatedModeName = post.Status.ModeName
	}
	// One write records the turn, the reply the transcript fold must skip
	// (instead of rewriting the multi-MiB transcript just for that ID), and
	// retires the committed durable pass.
	if err := sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
		state.CurrentMode = updatedModeName
		state.LastResponseID = result.LastResponseID
		if turnSummary != "" {
			state.LastAssistantSummary = turnSummary
			state.RecentTurnSummaries = append(state.RecentTurnSummaries, turnSummary)
		}
		state.SelfAssistantMessageID = r.tx.selfAssistant
		if hostTurnCommitted {
			return completeDurablePassState(state, t.messageID, pt.durablePass)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("persisting working state after durable turn: %w", err)
	}
	return nil
}

// readRunAfterTurn reads the AgentRun once after a pass for the mode
// and finish checks. nil when unreadable: those checks then
// wait for the next pass.
func (r *chatRuntime) readRunAfterTurn(ctx context.Context) *platformv1alpha1.AgentRun {
	run, err := readAgentRun(ctx, r.crd, r.cfg.TaskName, r.cfg.Namespace, 3)
	if err != nil {
		log.Printf("WARN: failed to read AgentRun after turn %d: %v", r.turnNumber, err)
		return nil
	}
	return run
}

// decideNext decides, after a committed pass, whether the agent keeps going
// autonomously or yields: user stop, input request, finish, or a tripped
// circuit breaker.
func (r *chatRuntime) decideNext(ctx context.Context, t *userTurn, out turnOutcome, post *platformv1alpha1.AgentRun) (loopAction, *runResult) {
	sc := r.sc
	result := out.result
	t.tracker.Update(result.NewItems)

	// A user stop that raced the natural end of the turn: the turn finished
	// before the watcher could cancel it, but the user asked to stop. Halt
	// here instead of letting the claimed request vanish silently.
	if out.interrupted {
		log.Printf("Turn %d: user stop request arrived as the turn completed — breaking to await user", r.turnNumber)
		return awaitUser, r.parkAfterStop(ctx, t.messageID,
			"Stopped by user — the turn had just completed; send a message to continue.",
			"Stopped by user as the turn completed — a queued message is waiting; continuing with it now.")
	}

	// AskUserQuestion / present_plan means the agent is genuinely blocked on
	// user input: await the answer instead of firing another continuation.
	inputPause := agent.DetectUserInputPause(result.NewItems, result.FinalText())
	if inputPause.Requested {
		log.Printf("User interaction requested — breaking to await user response")
		question := strings.TrimSpace(inputPause.Question)
		if r.cfg.DelegatedChild {
			// No human answers a delegated child; its parent is blocked on it.
			return awaitUser, &runResult{Status: "failed", Error: "delegated run requested user input: " + question}
		}
		inputType := platformv1alpha1.UserInputQuestion
		if inputPause.PlanReview {
			inputType = platformv1alpha1.UserInputPlanReview
		}
		actions := inputPause.Actions
		if len(actions) == 0 {
			actions = nil
		}
		if err := sc.SetUserInputRequest(ctx, inputType, question, actions); err != nil {
			return awaitUser, &runResult{Status: "failed", Error: fmt.Sprintf("writing user input request: %v", err)}
		}
		return awaitUser, nil
	}

	// The finish tool marks CompletionRequested=true when the agent signals
	// it is done.
	if post != nil && post.Status.CompletionRequested {
		log.Printf("Run %q complete — finish called", post.Status.ModeName)
		completionDetail, _ := json.Marshal(map[string]string{
			"from_mode": post.Status.ModeName,
			"result":    "completed",
		})
		_ = sc.WriteActivity(ctx, "mode_complete", fmt.Sprintf("Mode %q completed", post.Status.ModeName), completionDetail)

		// Clear the completion flag so a later user message resumes the run
		// instead of immediately re-detecting completion.
		if err := patchAgentRunStatus(ctx, r.crd, r.cfg.TaskName, r.cfg.Namespace, func(fresh *platformv1alpha1.AgentRun) {
			fresh.Status.CompletionRequested = false
		}); err != nil {
			log.Printf("WARN: failed to clear completion flag: %v", err)
		}

		if r.cfg.DelegatedChild {
			return awaitUser, &runResult{Status: "succeeded"}
		}
		_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputIdle, "", nil)
		return awaitUser, nil
	}

	// Circuit breakers: detect stalled or stuck agents.
	if cb := t.tracker.CheckCircuitBreakers(); cb.Tripped {
		log.Printf("Auto mode: circuit breaker tripped — %s", cb.Reason)
		if r.cfg.DelegatedChild {
			return awaitUser, &runResult{Status: "failed", Error: "circuit breaker tripped: " + cb.Reason}
		}
		_ = sc.WriteActivity(ctx, "circuit_breaker", cb.Reason, nil)
		_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, cb.Reason, nil)
		return awaitUser, nil
	}

	// Smart nudge: context-aware continuation prompt. It is stored for the
	// session feed and sent as the next pass's user item; recording its ID
	// keeps the next pass from also folding it into the transcript.
	t.prompt = agent.BuildSmartNudge(t.tracker, "")
	t.promptMessageID = 0
	if msg, err := sc.AppendSystemMessage(ctx, t.prompt); err != nil {
		log.Printf("WARN: failed to record continuation nudge: %v", err)
	} else if msg != nil {
		t.promptMessageID = msg.ID
	}
	log.Printf("Auto mode: agent turn complete (tools=%d, noToolTurns=%d), continuing loop (%d/%d)", t.tracker.ToolCallCount(), t.tracker.ConsecutiveNoToolTurns(), t.autoLoopCount, agent.DefaultMaxAutoLoops)
	return continueAgent, nil
}

// pauseForCostCap parks the run once spend reached spec.limits.maxCostUsd,
// before or during a turn. A delegated child fails instead: its parent team
// run is blocked on a terminal result.
func (r *chatRuntime) pauseForCostCap(ctx context.Context, notice string) *runResult {
	if r.cfg.DelegatedChild {
		return &runResult{Status: "failed", Error: notice}
	}
	_ = r.sc.WriteActivity(ctx, "cost_cap", notice, nil)
	_ = r.sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, notice, nil)
	if err := patchAgentRunStatus(ctx, r.crd, r.cfg.TaskName, r.cfg.Namespace, func(fresh *platformv1alpha1.AgentRun) {
		fresh.Status.Phase = platformv1alpha1.AgentRunPhasePaused
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Paused", BlockedReason: notice}
	}); err != nil {
		log.Printf("WARN: failed to patch Paused status for cost cap: %v", err)
	}
	// Empty result: the deferred result writer leaves the Paused phase
	// untouched and the pod exits cleanly.
	return &runResult{}
}

// noteUserStop records a user stop durably (recordUserStop) and updates this
// pod's in-memory stopped-message filter.
func (r *chatRuntime) noteUserStop(ctx context.Context, messageID int64) {
	r.stoppedMessageID = messageID
	recordUserStop(ctx, r.sc, messageID)
}

// parkAfterStop records a user stop of messageID, then continues directly
// with a user message queued behind it, or parks the session in the Stopped
// awaiting-input state. Empty stoppedActivity skips the activity line.
func (r *chatRuntime) parkAfterStop(ctx context.Context, messageID int64, stoppedActivity, queuedActivity string) *runResult {
	if r.subAgentRegistry != nil {
		r.subAgentRegistry.CancelAll()
	}
	r.noteUserStop(ctx, messageID)
	if r.cfg.DelegatedChild {
		return &runResult{Status: "failed", Error: "delegated run stopped by user"}
	}
	if queuedUserMessageWaiting(ctx, r.sc, r.stoppedMessageID, r.handledImmediate) {
		r.deferredStopBanner = true
		log.Printf("Queued user message found after stop of message %d — continuing with it directly", messageID)
		_ = r.sc.WriteActivity(ctx, "turn_interrupted", queuedActivity, nil)
		return nil
	}
	if stoppedActivity != "" {
		_ = r.sc.WriteActivity(ctx, "turn_interrupted", stoppedActivity, nil)
	}
	_ = r.sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputStopped, "Stopped by user.", nil)
	return nil
}

// exitForReplacement ends this pod so a replacement handles the turn (pending
// restart, Git policy change, degraded-pod heal).
func (r *chatRuntime) exitForReplacement(ctx context.Context, t *userTurn, notice string) *runResult {
	r.enqueueContinuation(ctx, t)
	_ = r.sc.WriteActivity(ctx, "runtime_config", notice, nil)
	return &runResult{}
}

// exitOnShutdown handles a pod shutdown (SIGTERM) as a resumable exit: it
// keeps the autonomous work alive across the replacement and flushes the
// interrupted turn's partial progress (result may be nil).
func (r *chatRuntime) exitOnShutdown(ctx context.Context, t *userTurn, result *agent.RunResult) *runResult {
	var pending int64
	if t != nil {
		pending = t.messageID
	}
	r.enqueueContinuation(ctx, t)
	r.tx.flushOnTermination(r.sc, result, pending)
	return &runResult{}
}

// enqueueContinuation keeps an autonomous turn going across a pod
// replacement. The resume cursor re-delivers a user message whose claim is
// still pending, but once a pass committed (autonomous pass 2+) nothing would
// wake the replacement pod — it would find no pending message and park
// idle. A durable continuation message resumes the work there instead.
func (r *chatRuntime) enqueueContinuation(ctx context.Context, t *userTurn) {
	if t == nil || t.claimPending {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := r.sc.AppendUserMessageWithMode(writeCtx, podResumeContinuationPrompt, sessionclient.UserMessageModeEnqueue); err != nil {
		log.Printf("WARN: failed to enqueue autonomous continuation for the replacement pod: %v", err)
		return
	}
	t.claimPending = true
	log.Printf("Enqueued autonomous continuation for the replacement pod")
}

// --- Session mode helpers ---

func shouldPublishStartupIdle(session *store.Session, sessionErr error, messages []sessionclient.UserMessage, queueErr error) bool {
	return sessionErr == nil && session != nil &&
		strings.TrimSpace(session.PendingInputType) == "" && strings.TrimSpace(session.PendingRequestID) == "" &&
		strings.TrimSpace(session.PendingQuestion) == "" && len(session.PendingActions) == 0 &&
		queueErr == nil && len(messages) == 0
}

// resetAutoLoopForSteering gives a newly delivered user turn its own safety
// budget and continuation history. The current pass is turn one because the
// outer loop incremented before checking for queued steering.
func resetAutoLoopForSteering(loopCount *int, tracker **agent.AutoTracker) {
	*loopCount = 1
	*tracker = &agent.AutoTracker{}
}

// snapshotClampsReadOnly reports whether the run's active mode template
// declares a read-only permission mode (e.g. plan, review). Such modes clamp
// each turn's tool access; the write-capable pod registry is untouched so a
// later switch to a write mode restores full access without a restart.
func snapshotClampsReadOnly(run *platformv1alpha1.AgentRun) bool {
	if run == nil || run.Status.ModeSnapshot == nil {
		return false
	}
	return run.Status.ModeSnapshot.PermissionMode == platformv1alpha1.PermissionModeReadOnly
}

// turnFailureNotice is the user-facing notice for a chat turn that failed
// after the SDK exhausted its own retries and model fallbacks (provider
// outage, rate limiting, guardrail trip, …). The session stays alive: the
// notice is surfaced via the activity feed and the pending-input banner, and
// the user's next message retries with the conversation history intact.
func turnFailureNotice(turnNumber int32, err error) string {
	return fmt.Sprintf("Turn %d failed: %v — the agent stopped; send a message to try again.", turnNumber, err)
}

// turnBudgetNotice is the user-facing notice for a chat turn that exhausted
// its LLM turn budget. Unlike turnFailureNotice, the conversation state WAS
// persisted (the SDK hands back a partial result): the next user message
// continues from exactly where the turn stopped with a fresh budget.
func turnBudgetNotice(turnNumber int32, maxTurns int) string {
	return fmt.Sprintf("Turn %d used its entire %d-turn budget and was stopped. All progress is preserved — send a message (e.g. \"continue\") to pick up exactly where it left off with a fresh budget.", turnNumber, maxTurns)
}

// maxStandingBudgetRollovers bounds consecutive automatic turn-budget
// rollovers for a standing maintainer before it parks for a human. Each
// rollover is a whole exhausted budget (hundreds of LLM steps); reaching the
// cap without a human or controller message in between means the loop is
// spinning rather than waiting on events.
const maxStandingBudgetRollovers = 12

// standingBudgetRolloverPrompt is the durable continuation the runtime
// enqueues for a standing maintainer whose episode budget ran out. It is a
// runtime-authored user message so the resume path is identical to a human
// "continue", and it names the exact re-entry step so the model does not
// replay the whole snapshot.
const standingBudgetRolloverPrompt = "Standing maintainer: the previous episode reached its turn budget " +
	"and was rolled over automatically. The transcript is preserved. Do not repeat completed commands. " +
	"Re-establish state with wait_for_repo_events using cursor \"latest\" (it returns every projection " +
	"change since your last successful wait, including receipts for commands you had in flight), " +
	"act on the highest-priority frontier, and return to the event wait."

func standingBudgetRolloverNotice(turnNumber int32, maxTurns, rollover, maxRollovers int) string {
	return fmt.Sprintf("Turn %d used its entire %d-turn budget; standing maintainer rolled over automatically "+
		"(%d/%d consecutive) and continues on the preserved transcript.", turnNumber, maxTurns, rollover, maxRollovers)
}

// isControlSlashCommand reports whether a user message is a control command that
// the dashboard already handled (session-mode or mode switch) and must not be
// processed as a normal agent turn.
func isControlSlashCommand(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	switch msg {
	case "/plan", "/chat", "/stop", "/autopilot", "/exit-plan":
		return true
	}
	return strings.HasPrefix(msg, "/mode ")
}

func closeRuntimeClosers(closers []io.Closer) {
	for _, closer := range closers {
		if closer != nil {
			if err := closer.Close(); err != nil {
				log.Printf("WARN: failed to close runtime resource: %v", err)
			}
		}
	}
}

// queuedUserMessageWaiting reports whether an undelivered, turn-starting user
// message is already queued for this session — e.g. steering the user sent
// just before stopping the turn. When one is waiting, the loop should break
// straight back to the message loop and consume it instead of parking the
// session in the Stopped awaiting-input state: the user's clear intent is
// "stop what you are doing and do this instead", not "stop and wait".
func queuedUserMessageWaiting(ctx context.Context, sc *sessionclient.Client, stoppedMessageID int64, handledImmediate map[int64]struct{}) bool {
	peeked, err := sc.PeekForUserMessages(ctx)
	if err != nil || len(peeked) == 0 {
		return false
	}
	msg, ok, _ := nextTurnStartingUserMessage(withoutStoppedMessage(peeked, stoppedMessageID), handledImmediate)
	if !ok {
		return false
	}
	content := strings.TrimSpace(msg.Content)
	if content == "" && len(msg.Images) == 0 {
		return false
	}
	// Control commands never start a turn; a lone /stop must still park.
	return !isControlSlashCommand(content)
}

// recordUserStop persists a user stop of the prompt that drives the current
// turn: the durable stopped-message floor for replacement pods, plus the
// claim completion that marks the prompt delivered. Without the latter the
// message stays claimed under this pod's token and a replacement pod's
// RecoverClaimedUserMessages hands it back as pending — re-running the very
// prompt the user stopped. Best-effort: a failed write must not turn a user
// stop into a failed run.
func recordUserStop(ctx context.Context, sc *sessionclient.Client, messageID int64) {
	if err := sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
		state.LastStoppedUserMessageID = messageID
		return nil
	}); err != nil {
		log.Printf("WARN: failed to record stopped message %d: %v", messageID, err)
	}
	if err := sc.CompleteClaims(ctx); err != nil {
		log.Printf("WARN: failed to complete claims for stopped message %d: %v", messageID, err)
	}
}

// claimQueuedUserMessage makes one non-blocking attempt to claim the next
// turn-starting user message. It reports false when nothing is pending or the
// claim was lost (the user cancelled the message after it was peeked).
func claimQueuedUserMessage(
	ctx context.Context,
	sc *sessionclient.Client,
	stoppedMessageID int64,
	handledImmediate map[int64]struct{},
) (sessionclient.UserMessage, bool, error) {
	peeked, err := sc.PeekForUserMessages(ctx)
	if err != nil {
		return sessionclient.UserMessage{}, false, err
	}
	return claimNextUserMessage(ctx, sc, peeked, stoppedMessageID, handledImmediate)
}

// waitForNextUserReply blocks until a turn-starting user message is claimed.
// Pending delivery state in the store is authoritative — there is no cursor;
// stoppedMessageID is the one prompt that must never be claimed again (see
// withoutStoppedMessage).
func waitForNextUserReply(
	ctx context.Context,
	sc *sessionclient.Client,
	stoppedMessageID int64,
	pollInterval time.Duration,
	handledImmediate map[int64]struct{},
) (sessionclient.UserMessage, error) {
	for {
		messages, err := sc.PollForUserMessages(ctx, pollInterval)
		if err != nil {
			return sessionclient.UserMessage{}, err
		}
		msg, ok, err := claimNextUserMessage(ctx, sc, messages, stoppedMessageID, handledImmediate)
		if err != nil {
			return sessionclient.UserMessage{}, err
		}
		if ok {
			return msg, nil
		}
		// Nothing claimable from a non-empty snapshot (the stopped prompt,
		// an empty message, a lost claim). PollForUserMessages returns the
		// same rows again immediately, so pause briefly instead of spinning
		// against the store until the state changes.
		select {
		case <-ctx.Done():
			return sessionclient.UserMessage{}, ctx.Err()
		case <-time.After(unclaimableSnapshotBackoff):
		}
	}
}

// unclaimableSnapshotBackoff bounds the re-poll rate when a pending snapshot
// contains only messages this loop will not start a turn from.
const unclaimableSnapshotBackoff = 250 * time.Millisecond

// claimNextUserMessage selects the next turn-starting message from a queue
// snapshot and claims it. Selection is oldest-first regardless of mode (see
// nextTurnStartingUserMessage): no turn is running here, so an immediate
// message has nothing to interrupt and must not overtake an older queued
// prompt such as the seeded kickoff request. ok is false when nothing was
// claimable: no candidate, a lost claim (cancelled or taken by another
// claimant after the snapshot), or a claimed message with no content.
func claimNextUserMessage(
	ctx context.Context,
	sc *sessionclient.Client,
	messages []sessionclient.UserMessage,
	stoppedMessageID int64,
	handledImmediate map[int64]struct{},
) (sessionclient.UserMessage, bool, error) {
	messages = consumeStoppedMessage(ctx, sc, messages, stoppedMessageID)
	msg, ok, immediate := nextTurnStartingUserMessage(messages, handledImmediate)
	if !ok {
		return sessionclient.UserMessage{}, false, nil
	}
	if immediate {
		handledImmediate[msg.ID] = struct{}{}
	}
	// Establish ownership before the content enters model context. A cancel
	// or another claimant may have won after the poll snapshot.
	claimed, won, claimErr := claimUserMessageWithRetry(ctx, sc, msg)
	if claimErr != nil {
		return sessionclient.UserMessage{}, false, claimErr
	}
	if !won {
		// The message never entered the turn; a later claimant (or a retry
		// after a cancel is undone) must still be able to select it.
		delete(handledImmediate, msg.ID)
		return sessionclient.UserMessage{}, false, nil
	}
	content := strings.TrimSpace(claimed.Content)
	if content == "" && len(claimed.Images) == 0 {
		return sessionclient.UserMessage{}, false, nil
	}
	return claimed, true, nil
}

// claimUserMessageWithRetry claims msg, retrying transient store errors until
// ctx ends: a Postgres failover at the moment a user replies must not end a
// long-lived session.
func claimUserMessageWithRetry(ctx context.Context, sc *sessionclient.Client, msg sessionclient.UserMessage) (sessionclient.UserMessage, bool, error) {
	var claimed sessionclient.UserMessage
	var won bool
	err := retryTransient(ctx, fmt.Sprintf("claiming user message %d", msg.ID), 0, func(ctx context.Context) error {
		var err error
		claimed, won, err = sc.ClaimUserMessage(ctx, msg)
		return err
	})
	return claimed, won, err
}

// consumeStoppedMessage durably retires the user-stopped prompt if a queue
// snapshot still lists it as pending (its claim completion was lost and the
// replacement pod's RecoverClaimedUserMessages handed it back). It is claimed
// and completed in place so it never re-enters model context and stops
// reappearing in every poll; the returned snapshot omits it either way.
func consumeStoppedMessage(ctx context.Context, sc *sessionclient.Client, messages []sessionclient.UserMessage, stoppedMessageID int64) []sessionclient.UserMessage {
	if stoppedMessageID == 0 {
		return messages
	}
	for _, msg := range messages {
		if msg.ID != stoppedMessageID {
			continue
		}
		if _, won, err := sc.ClaimUserMessage(ctx, msg); err != nil {
			log.Printf("WARN: failed to retire stopped user message %d: %v", msg.ID, err)
		} else if won {
			if err := sc.CompleteClaims(ctx); err != nil {
				log.Printf("WARN: failed to complete retired stopped user message %d: %v", msg.ID, err)
			} else {
				log.Printf("Retired user-stopped message %d instead of re-running it", msg.ID)
			}
		}
		break
	}
	return withoutStoppedMessage(messages, stoppedMessageID)
}

func pollImmediateInputs(
	ctx context.Context,
	sc *sessionclient.Client,
	workDir string,
	run *platformv1alpha1.AgentRun,
	stoppedMessageID int64,
	handledImmediate map[int64]struct{},
) ([]agent.RunItem, error) {
	messages, err := sc.PeekForUserMessages(ctx)
	if err != nil {
		return nil, err
	}
	messages = withoutStoppedMessage(messages, stoppedMessageID)
	_, consumedIDs, _ := collectImmediateRunItems(messages, handledImmediate)
	items := []agent.RunItem(nil)
	if len(consumedIDs) > 0 {
		claimedItems := make([]agent.RunItem, 0, len(consumedIDs))
		for _, id := range consumedIDs {
			var selected sessionclient.UserMessage
			for _, message := range messages {
				if message.ID == id {
					selected = message
					break
				}
			}
			claimed, won, claimErr := sc.ClaimUserMessage(ctx, selected)
			if claimErr != nil {
				return nil, claimErr
			}
			if !won {
				continue
			}
			// Immediate replies enter the SDK's current Run invocation instead of
			// passing through the outer message loop. Consume the exact request here
			// so the AgentRun no longer remains Question/awaiting-user while the
			// model has already resumed.
			if requestID := sessionclient.PendingRequestIDFromMetadata(claimed.Metadata); requestID != "" {
				if clearErr := sc.ClearUserInputRequestIfID(ctx, requestID); clearErr != nil {
					// Status repair is best-effort after the message has been claimed.
					// Never drop an answer that can no longer be polled again.
					log.Printf("WARN: failed to clear answered input request %s: %v", requestID, clearErr)
				}
			}
			handledImmediate[id] = struct{}{}
			text := strings.TrimSpace(claimed.Content)
			if assetPaths, assetErr := materializeMessageAssets(ctx, workDir, run, sc.StateStore(), claimed.Images); assetErr != nil {
				log.Printf("WARN: failed to materialize immediate message project assets: %v", assetErr)
			} else {
				text = appendMessageAssetNotice(text, assetPaths)
			}
			claimedItems = append(claimedItems, agent.RunItem{Type: agent.RunItemMessage, Message: &agent.MessageOutput{Text: text, Images: toSDKImageAttachments(claimed.Images)}})
		}
		items = claimedItems
	}
	return items, nil
}
