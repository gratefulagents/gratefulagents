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
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/gratefulagents/internal/tools"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkguardrails "github.com/gratefulagents/sdk/pkg/agentsdk/guardrails"
	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	sdkmode "github.com/gratefulagents/sdk/pkg/agentsdk/mode"
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

// runChatLoop is the main conversational loop.
// It polls Postgres for user messages, runs the agent, and writes results.
// In plan mode (toggled via /plan slash command), the agent switches to read-only tools.
func runChatLoop(ctx context.Context, cfg runConfig, crdClient client.Client, k8sClient *kubernetes.Clientset, tracker *agent.RunProgress, sc *sessionclient.Client, metricsBaseline progressMetricsBaseline, tp agent.TracingProcessor, eventStream *agent.EventWriter, metaharnessWriter *metaharness.TraceWriter) (result runResult) {
	modelMetadata := newOpenAIModelMetadataResolver(cfg)
	// Per-model compaction thresholds (models.dev catalog → backend metadata →
	// static defaults); nil when ops pinned thresholds via env.
	compactionResolver := newCompactionModelResolver(cfg, modelMetadata)

	// Create a single trace spanning the entire AgentRun so all tool
	// calls, generations, and subagent spawns appear in one OTel waterfall.
	runTrace := agent.NewTrace(cfg.TaskName)
	tp.OnTraceStart(runTrace)
	tracker.SetRootSpanID(runTrace.ID)
	defer func() {
		runTrace.Finish()
		tp.OnTraceEnd(runTrace)
	}()

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

	// Spend recorded by earlier provisioning sessions of this run; captured
	// once before this process starts publishing metrics so progress ticks can
	// never become the next cost baseline and compound within the same pod.
	costBaselineUSD := metricsBaseline.CostUSD

	// Track mode name for mode-switch detection in the chat loop.
	prevModeName := ""
	if run != nil && run.Status.ModeName != "" {
		prevModeName = run.Status.ModeName
	}

	// Build tool registry with all platform tools.
	var registryOpts []tools.RegistryOption
	registryOpts = append(registryOpts,
		tools.WithPermissionMode(cfg.PermissionMode),
		tools.WithSignalTools(),
	)

	// Mode templates may allowlist specific mutating tools that survive both
	// the registry and per-turn SDK read-only clamps (e.g. GitHub review tools).
	// effectiveAllowedMutatingTools includes the legacy reviewer fallback.
	if allowed := effectiveAllowedMutatingTools(run); len(allowed) > 0 {
		registryOpts = append(registryOpts, tools.WithAllowedMutatingTools(allowed...))
		log.Printf("Mode template allowlists mutating tools %v", allowed)
	}

	// Browser tools are on by default when the selected runtime image includes
	// Chromium. Operators can opt out with ENABLE_BROWSER_TOOLS=false.
	if browserToolsEnabled() {
		registryOpts = append(registryOpts, tools.WithBrowserTools())
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

	// Optional: durable project state (SDK backbone, Postgres persistence).
	// Provides task_*, memory_* and prime_context tools plus startup priming.
	psStore, psPool, psStatus := setupProjectState(ctx, cfg)
	if psStatus.err != nil {
		log.Printf("WARN: %s", psStatus.message)
	} else {
		log.Printf("%s", psStatus.message)
	}
	if psPool != nil {
		defer psPool.Close()
	}
	defer func() {
		saveSessionSummaryOnExit(psStore, sc, cfg.TaskName, result)
	}()

	toolRegistry := tools.NewRegistry(cfg.RepoDir, registryOpts...)
	defer func() {
		for _, closer := range toolRegistry.Closers() {
			_ = closer.Close()
		}
	}()
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
	maintainedRepositoryName := strings.TrimSpace(os.Getenv("AGENTRUN_MAINTAINED_REPOSITORY_NAME"))
	maintainedRepositoryNamespace := strings.TrimSpace(os.Getenv("AGENTRUN_MAINTAINED_REPOSITORY_NAMESPACE"))
	if maintainedRepositoryName != "" && maintainedRepositoryNamespace != "" {
		tools.RegisterMaintainerTools(
			toolRegistry,
			sc.StateStore(),
			crdClient,
			cfg.TaskName,
			cfg.Namespace,
			maintainedRepositoryName,
			maintainedRepositoryNamespace,
		)
	}
	tools.RegisterPlanTools(toolRegistry, sc.StateStore(), sc.SessionID())
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

	// ask_teammate: consult a teammate's personal SOUL persona for their likely
	// perspective on a question, plan, or diff. The persona runner is wired in
	// after the runtime (and its model provider) is built, below.
	askTeammateTool, askTeammateClose := setupAskTeammateTool(ctx, toolRegistry)
	defer askTeammateClose()

	// Build MCP config: merge repo .mcp.json (if any) with MCPServer CRDs.
	// CRD-defined skills don't require any config in the user's repo.
	mcpCfg, clusterManagedMCPServers, networkAllowedMCPServers, mcpDropped := buildMCPConfig(ctx, crdClient, cfg.Namespace, cfg.RepoDir, run, cfg.PermissionMode)
	// Install exact-version Python MCP packages in a credential-free installer
	// sandbox. Repository-controlled uvx specs never trigger installation.
	mcpToolRoot, materializationDropped := materializeUvxServers(ctx, &mcpCfg, clusterManagedMCPServers, sandboxedInstallRunner)
	mcpDropped = append(mcpDropped, materializationDropped...)
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
		defer mcpManager.Close()
		tools.RegisterMCPTools(ctx, toolRegistry, mcpManager, cfg.PermissionMode, crdClient, cfg.Namespace, cfg.TaskName, sc)
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
			return runResult{Status: "failed", Error: msg}
		}
		toolInputGuardrails = append(toolInputGuardrails, crdInputG...)
		toolOutputGuardrails = append(toolOutputGuardrails, crdOutputG...)
	}

	// Auto-retrieve durable project state for this run (priming happens again
	// per turn via workingStateContext so the model sees fresh task/memory state).

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
	}

	hasSpecialists := len(roleCatalog.Roles) > 0
	runtimeCfg := sdkRuntimeProviderConfig(cfg, cfg.Model)
	runtimeCfg.WorkDir = cfg.RepoDir
	runtimeCfg.AgentName = cfg.TaskName
	runtimeCfg.Instructions = strings.Join(instructionParts, "\n\n---\n\n")
	runtimeCfg.ActiveMode = firstNonEmptyRunMode(run)
	runtimeCfg.ModeSnapshot = platformModeSnapshotForSDK(modeSnapshot)
	runtimeCfg.RoleCatalog = roleCatalog.Roles
	runtimeCfg.ToolAccess = runtimeToolAccess
	runtimeCfg.AllowedMutatingTools = effectiveAllowedMutatingTools(run)
	runtimeCfg.PermissionMode = cfg.PermissionMode
	// Explicit feature selection (SDK v0.0.7+): the operator brings its own
	// tool registry, signal tools, MCP manager, and guardrail rules, so only
	// ExtraTools, vision attachment, specialists, and project state are
	// SDK-built. Zero values keep everything else off.
	runtimeCfg.Features = &sdkruntime.Features{
		Tools: sdkruntime.ToolFeatures{
			ExtraTools:     true,
			VisionAnalyzer: true,
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
			PrimeContext: psStore != nil,
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
	runtimeBundle, err := sdkruntime.NewBuilder(runtimeCfg).Build(ctx)
	if err != nil {
		return runResult{Status: "failed", Error: fmt.Sprintf("build runtime: %v", err)}
	}
	defer closeRuntimeClosers(runtimeBundle.Closers)

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

	// Wire the ask_teammate persona runner now that the runner and base agent
	// exist. A teammate consult runs a one-shot, tool-less, read-only persona
	// (the colleague's SOUL as instructions) on the run's own model/provider, so
	// it is billed to this run and never touches the workspace.
	if askTeammateTool != nil {
		askTeammateTool.SetPersonaRunner(func(personaCtx context.Context, soul, prompt string) (string, error) {
			personaModel, personaProvider := liveRuntimeModelAndProvider(cfg, getAgentRun(personaCtx, crdClient, cfg.TaskName, cfg.Namespace))
			personaAgent := baseAgent.Clone(
				agent.WithName("teammate-persona"),
				agent.WithInstructions(soul),
				agent.WithModel(personaModel),
				agent.WithTools(),
				agent.WithHandoffs(),
			)
			personaRunCfg := sdkruntime.BuildRunConfig(sdkruntime.Config{
				Provider:         personaProvider,
				Model:            personaModel,
				WorkDir:          cfg.RepoDir,
				MaxTurns:         1,
				ToolAccess:       agent.ToolAccessLevelReadOnly,
				TracingProcessor: tp,
				Trace:            runTrace,
				ParentSpanID:     runTrace.ID,
				Features: &sdkruntime.Features{
					Runtime: sdkruntime.RuntimeFeatures{
						Retry:   true,
						Tracing: tp != nil,
					},
				},
			}, nil)
			personaItems := []agent.RunItem{{
				Type:    agent.RunItemMessage,
				Message: &agent.MessageOutput{Text: prompt},
			}}
			res, err := runner.Run(personaCtx, personaAgent, personaItems, personaRunCfg)
			if err != nil {
				return "", err
			}
			return res.FinalText(), nil
		})
	}

	var subAgentRegistry *agent.SubAgentScheduler
	if supervisedRunName == "" && runtimeBundle.SessionState != nil {
		subAgentRegistry = runtimeBundle.SessionState.SubAgentScheduler()
	}
	var interruptedSubAgentNotice string
	if subAgentRegistry != nil {
		log.Printf("SubAgentScheduler enabled: %d specialist agents", len(specialistAgents))
		var restoreErr error
		interruptedSubAgentNotice, restoreErr = restoreSubAgentCheckpoint(ctx, sc, subAgentRegistry)
		if restoreErr != nil {
			return runResult{Status: "failed", Error: restoreErr.Error()}
		}
		subAgentCheckpoints := startSubAgentCheckpointLoop(sc, subAgentRegistry)
		defer func() {
			if err := subAgentCheckpoints.StopAndFlush(); err != nil {
				log.Printf("ERROR: final sub-agent checkpoint failed: %v", err)
				result = runResult{Status: "failed", Error: "final sub-agent checkpoint failed: " + err.Error()}
			}
		}()
	} else if supervisedRunName != "" {
		log.Printf("SubAgentScheduler disabled for standing overseer run")
	}

	var pgCursor int64
	var immediateCursor int64
	var turnNumber int32
	handledImmediate := make(map[int64]struct{})
	handoffHistoryConfig := resolveHandoffHistoryConfig()

	reportCompactionFailure := func(scope, reason string, tokensBefore, tokensAfter int) {
		detail, _ := json.Marshal(map[string]any{
			"scope":         scope,
			"reason":        reason,
			"tokens_before": tokensBefore,
			"tokens_after":  tokensAfter,
		})
		_ = sc.WriteActivity(ctx, "compact_boundary_skipped", fmt.Sprintf("Context compaction skipped (%s): %s", scope, reason), detail)
	}
	recordCompaction := func(tokensBefore, tokensAfter int, summary string) {
		tracker.RecordCompactBoundary(tokensBefore, tokensAfter, summary)
		if eventStream != nil {
			eventStream.EmitCompaction(tokensBefore, tokensAfter, summary)
		}
	}

	// On resume: load the assistant cursor plus the durable floor for a turn
	// explicitly stopped by the user. The latter prevents a replacement pod
	// from automatically restarting that same prompt.
	workingStateAtResume, stateErr := sc.ReadWorkingState(ctx)
	if stateErr != nil {
		log.Printf("WARN: failed to read stopped-turn cursor: %v", stateErr)
	}
	if err := sc.RecoverClaimedUserMessages(ctx); err != nil {
		return runResult{Status: "failed", Error: fmt.Sprintf("recovering claimed messages: %v", err)}
	}
	resumeSession, _, cursor, resumeErr := sc.ResumeState(ctx)
	if resumeErr != nil {
		log.Printf("WARN: failed to resume session state: %v — starting fresh", resumeErr)
	} else {
		if workingStateAtResume.LastStoppedUserMessageID > cursor {
			cursor = workingStateAtResume.LastStoppedUserMessageID
		}
		pgCursor = cursor
		immediateCursor = cursor
	}

	// Publish the initial idle boundary only when there is neither a prompt
	// waiting to run nor an existing input request waiting to be answered.
	// Seeded runs already have their kickoff message queued, while restarted
	// runs may be preserving a question, approval, or other pending boundary.
	// If either state cannot be inspected, fail safe by leaving the run active;
	// the normal polling loop below will retry the queue read.
	startupMessages, startupErr := sc.PeekForUserMessages(ctx, pgCursor)
	if shouldPublishStartupIdle(resumeSession, resumeErr, startupMessages, startupErr) {
		if err := sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputIdle, "", nil); err != nil {
			return runResult{Status: "failed", Error: fmt.Sprintf("writing idle status: %v", err)}
		}
	} else if startupErr != nil {
		log.Printf("WARN: checking startup user-message queue: %v", startupErr)
	}

	// Emit system_init event with session metadata so all consumers have it.
	if eventStream != nil {
		var toolNames []string
		for _, name := range toolRegistry.Names() {
			toolNames = append(toolNames, name)
		}
		var mcpServerNames []string
		if mcpManager != nil {
			mcpServerNames = mcpManager.ConnectedServerNames()
		}
		eventStream.EmitSystemInit(cfg.Model, string(cfg.PermissionMode), cfg.RepoDir, 0, toolNames, mcpServerNames)
	}

	// In-memory full-session transcript replay: the
	// runner's exact post-run conversation state, replayed verbatim as the
	// next turn's input so tool calls/outputs and reasoning survive turn
	// boundaries instead of being rebuilt from the lossy durable tail.
	// Empty after pod restarts or context clears → durable-tail fallback.
	var sessionTranscript []agent.RunItem
	var transcriptFloor int64
	// Watermark: highest durable message ID already represented in the
	// model's context (transcript replay, durable-tail fallback, or the
	// current user item). Durable messages above it were recorded
	// out-of-band — e.g. the dashboard stores a plan rejection as a system
	// message without starting a turn — and are folded into the replayed
	// transcript at the next turn start so they are not silently lost.
	var transcriptSeenMessageID int64
	// The loop's own durable assistant append for the previous turn: its
	// content is already in FinalHistory, so the out-of-band fold skips it.
	var selfAssistantMessageID int64
	// The durable user message that started a turn interrupted by pod
	// termination (the snapshot was flushed mid-turn). The resume cursor
	// only advances on assistant replies, so that message is re-delivered to
	// this pod even though its prompt and the partial progress it triggered
	// are already in the restored transcript — the loop must open the
	// resumed turn with a continuation instruction, not a verbatim replay.
	var resumePendingUserMessageID int64

	// Rehydrate the previous pod's persisted transcript snapshot so a
	// restart resumes with full conversation context (tool calls/outputs,
	// reasoning, compaction summaries) instead of the lossy durable-tail
	// fallback. Discarded when the durable history floor moved (external
	// context clear) while the pod was down.
	if state, err := sc.ReadWorkingState(ctx); err != nil {
		log.Printf("WARN: failed to read working state for transcript restore: %v", err)
	} else if restored := loadTranscriptSnapshot(ctx, sc, state.HistoryFloorMessageID); restored != nil {
		sessionTranscript = restored.Items
		transcriptFloor = restored.FloorMessageID
		transcriptSeenMessageID = restored.SeenMessageID
		selfAssistantMessageID = restored.SelfAssistantMessageID
		resumePendingUserMessageID = restored.PendingUserMessageID
		log.Printf("Restored session transcript snapshot: %d items (floor=%d seen=%d)",
			len(sessionTranscript), transcriptFloor, transcriptSeenMessageID)
		_ = sc.WriteActivity(ctx, "transcript_restored",
			fmt.Sprintf("Restored %d conversation items from the previous session", len(sessionTranscript)), nil)
	}

messageLoop:
	for {
		nextReply, newCursor, err := waitForNextUserReply(ctx, sc, pgCursor, 3*time.Second, handledImmediate)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = sc.WriteActivity(writeCtx, "pod_terminating", "Pod terminating; flushing run state", nil)
				cancel()
				return runResult{}
			}
			return runResult{Status: "failed", Error: fmt.Sprintf("waiting for user reply: %v", err)}
		}
		pgCursor = newCursor
		if immediateCursor < newCursor {
			immediateCursor = newCursor
		}
		reply := strings.TrimSpace(nextReply.Content)
		currentUserMessageID := nextReply.ID
		if reply == "" && len(nextReply.Images) == 0 {
			continue messageLoop
		}
		if isControlSlashCommand(reply) {
			continue messageLoop
		}
		// A turn interrupted by pod termination left its prompt (and the
		// partial progress it triggered) in the restored transcript, and the
		// resume cursor re-delivers that same message here. Mark it so the
		// turn opens with a continuation instruction instead of replaying
		// the prompt verbatim. Consumed at the first real turn-starting
		// message: anything after it is a genuinely new prompt.
		resumingInterruptedTurn := resumePendingUserMessageID != 0 && currentUserMessageID == resumePendingUserMessageID
		resumePendingUserMessageID = 0

		// Clear only the request this message answered. Kickoff and legacy
		// messages have no request nonce in their metadata, so consume the current
		// idle boundary with a nonce-checked fallback. Replacement requests remain
		// visible in either path.
		if requestID := sessionclient.PendingRequestIDFromMetadata(nextReply.Metadata); requestID != "" {
			_ = sc.ClearUserInputRequestIfID(ctx, requestID)
		} else {
			_ = sc.ClearIdleUserInputRequest(ctx)
		}
		// Honor a stop that survived pod replacement when it was requested for
		// this message. A stop older than this message is stale: the newer user
		// input is an explicit request to resume, so consuming it must not cancel
		// the new turn.
		if interrupt, interruptErr := sc.DrainInterruptsThrough(ctx, nextReply.CreatedAt); interruptErr != nil {
			log.Printf("WARN: failed to drain pending stop requests: %v", interruptErr)
		} else if interruptAppliesToMessage(interrupt, nextReply.CreatedAt) {
			_ = sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
				state.LastStoppedUserMessageID = currentUserMessageID
				return nil
			})
			_ = sc.WriteActivity(ctx, "turn_interrupted", "Stopped by user before the replacement runtime started the turn.", nil)
			_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputStopped, "Stopped by user.", nil)
			continue messageLoop
		}

		autoLoopCount := 0     // Global safety cap for autonomous mode.
		firstAgentPass := true // True on the first iteration of agentLoop for this user message.
		prompt := reply
		currentUserImages := nextReply.Images
		aTracker := &agent.AutoTracker{} // Smart continuation tracker for autonomous mode.

	agentLoop:
		for {
			// Safety cap: prevent infinite autonomous loops.
			if cfg.AutoMode {
				autoLoopCount++

				// Check for a new user turn before enforcing the old turn's cap so
				// steering at the boundary can receive a fresh autonomous budget.
				// Non-blocking check: has the user sent a message (including /stop)?
				if autoLoopCount > 1 { // Skip first iteration — that's the initial prompt.
					if peeked, peekErr := sc.PeekForUserMessages(ctx, pgCursor); peekErr == nil && len(peeked) > 0 {
						nextMsg, ok, skipCursor, immediate := nextPendingUserMessage(peeked, handledImmediate)
						if skipCursor > pgCursor {
							pgCursor = skipCursor
						}
						if immediateCursor < latestUserMessageID(peeked, immediateCursor) {
							immediateCursor = latestUserMessageID(peeked, immediateCursor)
						}
						if ok {
							if immediate {
								handledImmediate[nextMsg.ID] = struct{}{}
							} else {
								pgCursor = nextMsg.ID
							}
							claimed, won, claimErr := sc.ClaimUserMessage(ctx, nextMsg)
							if claimErr != nil {
								return runResult{Status: "failed", Error: fmt.Sprintf("claiming steering message: %v", claimErr)}
							}
							if !won {
								delete(handledImmediate, nextMsg.ID)
								continue agentLoop
							}
							nextMsg = claimed
							trimmed := strings.TrimSpace(nextMsg.Content)
							if strings.EqualFold(trimmed, "/stop") {
								log.Printf("Autonomous run: /stop received from user — pausing")
								_ = sc.WriteActivity(ctx, "auto_stop", "User requested stop — pausing autonomous work", nil)
								_ = sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
									state.LastStoppedUserMessageID = nextMsg.ID
									return nil
								})
								_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputStopped, "Stopped by user. Send a message to resume.", nil)
								break agentLoop
							}
							if trimmed != "" || len(nextMsg.Images) > 0 {
								// Any other text or image message becomes the next
								// interactive runner pass. Do not break out of agentLoop:
								// doing so acknowledges and permanently drops the input.
								log.Printf("Autonomous run: user message received — steering next pass")
								_ = sc.WriteActivity(ctx, "auto_interrupt", "User sent a message — steering autonomous work", nil)
								prompt = trimmed
								reply = trimmed
								currentUserImages = nextMsg.Images
								// This message now drives the turn: a mid-turn flush
								// must record it as the pending prompt.
								currentUserMessageID = nextMsg.ID
								resumingInterruptedTurn = false
								firstAgentPass = true
								resetAutoLoopForSteering(&autoLoopCount, &aTracker)
							}
						}
					}
				}

				if autoLoopCount > agent.DefaultMaxAutoLoops {
					log.Printf("Auto mode: global turn cap (%d) reached, exiting", agent.DefaultMaxAutoLoops)
					_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputTurnLimit, agent.BuildAutoTurnCapPrompt(agent.DefaultMaxAutoLoops), nil)
					break agentLoop
				}
			}

			turnNumber++
			toolAccessLevel := agent.ToolAccessLevelFull

			// Derive session label from the active CRD mode.
			sessionLabel := "chat"
			currentModeName := ""
			activeRun := &platformv1alpha1.AgentRun{}
			if err := crdClient.Get(ctx, client.ObjectKey{Name: cfg.TaskName, Namespace: cfg.Namespace}, activeRun); err != nil {
				// Permission, restart, and cost policy all live on this object. A
				// turn must never start when current policy cannot be verified.
				log.Printf("ERROR: refusing to start turn %d: AgentRun policy read failed: %v", turnNumber, err)
				return runResult{Status: "failed", Error: "unable to verify current run policy; refusing to start another turn"}
			}

			// A pending compute restart (spec.restartRequests above the
			// handled count) means the controller is about to tear this pod
			// down and re-provision it — e.g. a mid-run provider switch that
			// must remount credentials. Do not start a turn on the stale pod:
			// it still runs with the old credential env and would fail to
			// resolve the new model (e.g. "OpenAI API key is required"). Exit
			// cleanly instead; the resume cursor only advances on assistant
			// replies, so the replacement pod re-reads this user message and
			// answers it with the new credentials.
			if restartPending(activeRun) {
				log.Printf("Turn %d: compute restart pending (restartRequests=%d handled=%d) — exiting so the replacement pod handles this turn",
					turnNumber, activeRun.Spec.RestartRequests, activeRun.Status.RestartRequestsHandled)
				_ = sc.WriteActivity(ctx, "runtime_config", "Compute restart pending — the replacement pod will pick up this message", nil)
				return runResult{}
			}

			// Self-heal a degraded read-only pod. When the startup fallback
			// was caused by an API failure or race, re-resolve from the live
			// CRDs before each turn: once resolution succeeds with a write
			// mode, the registry and command sandbox baked at startup are
			// provably stale, so bounce compute through the restart-request
			// path (the same mechanism as mid-run provider switches). The
			// resume cursor only advances on assistant replies, so the
			// replacement pod re-reads this user message and answers it with
			// a writable workspace.
			if cfg.PermissionModeDegraded {
				if healedMode, ok := healedWritePermissionMode(ctx, crdClient, activeRun); ok {
					msg := fmt.Sprintf("Write access recovered (%s) — restarting compute to lift the degraded read-only workspace; the replacement pod will pick up this message", healedMode)
					log.Printf("Turn %d: %s", turnNumber, msg)
					if err := patchAgentRunSpec(ctx, crdClient, cfg.TaskName, cfg.Namespace, func(fresh *platformv1alpha1.AgentRun) {
						fresh.Spec.RestartRequests++
					}); err != nil {
						log.Printf("WARN: failed to request compute restart for permission-mode heal: %v — continuing read-only", err)
					} else {
						_ = sc.WriteActivity(ctx, "runtime_config", msg, nil)
						return runResult{}
					}
				}
			}

			// Cost ceiling: pause before the next turn once the cap is reached,
			// mirroring the maxRuntime timeout pause. Raising
			// spec.limits.maxCostUsd resumes the run via the controller.
			capUSD, capConfigured, capErr := validatedCostCapUSD(activeRun)
			if capErr != nil {
				msg := "invalid configured cost cap; refusing to start another turn"
				log.Printf("ERROR: %s: %v", msg, capErr)
				_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, msg, nil)
				return runResult{Status: "failed", Error: msg}
			}
			if capConfigured {
				spentUSD := costBaselineUSD + tracker.Snapshot().CostUsd
				if spentUSD >= capUSD {
					msg := fmt.Sprintf("Cost cap reached: $%.4f spent of the $%.2f limit — increase spec.limits.maxCostUsd to resume.", spentUSD, capUSD)
					log.Printf("Cost cap reached ($%.4f >= $%.2f) — pausing run", spentUSD, capUSD)
					_ = sc.WriteActivity(ctx, "cost_cap", msg, nil)
					_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, msg, nil)
					if err := patchAgentRunStatus(ctx, crdClient, cfg.TaskName, cfg.Namespace, func(fresh *platformv1alpha1.AgentRun) {
						fresh.Status.Phase = platformv1alpha1.AgentRunPhasePaused
						fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Paused", BlockedReason: msg}
					}); err != nil {
						log.Printf("WARN: failed to patch Paused status for cost cap: %v", err)
					}
					// Empty status: the deferred result writer leaves the
					// Paused phase untouched and the pod exits cleanly.
					return runResult{}
				}
			}

			if activeRun != nil && activeRun.Status.ModeName != "" {
				currentModeName = activeRun.Status.ModeName
				sessionLabel = activeRun.Status.ModeName
			}
			if metaharnessWriter != nil && currentModeName != prevModeName && prevModeName != "" {
				metaharnessWriter.RecordModeSwitch(prevModeName, currentModeName)
			}
			prevModeName = currentModeName

			// Read-only modes (plan, review, …) clamp the turn's tool access:
			// the runner adapts write tools to their read-only variants and the
			// command sandbox enforces the filesystem boundary. The pod-level
			// registry stays write-capable so switching to a write mode
			// upgrades without a pod restart.
			if snapshotClampsReadOnly(activeRun) {
				toolAccessLevel = agent.ToolAccessLevelReadOnly
				log.Printf("Turn %d: mode %q is read-only — tools clamped for this turn", turnNumber, currentModeName)
			}
			tracker.SetSession(turnNumber, sessionLabel)
			tracker.SetStep("starting")
			if eventStream != nil {
				eventStream.SetSession(turnNumber)
				eventStream.SetStep("starting")
			}

			// Apply mode instructions and limits from the CRD snapshot.
			mo := readModeOverrides(ctx, crdClient, cfg.TaskName, cfg.Namespace)

			// Re-assert the single autonomous pacing contract each turn.
			cfg.AutoMode = resolveAutoModeFromCRD(ctx, crdClient, cfg.TaskName, cfg.Namespace)

			model, provider := liveRuntimeModelAndProvider(cfg, activeRun)
			// Role model routing follows the provider selected for this turn. Each
			// turn gets immutable specialist clones so queued/running async tasks
			// retain the routing snapshot they were spawned with.
			activeRoleCatalog := roleCatalog
			roleModelOverrides = nil
			if activeRun != nil {
				roleModelOverrides = activeRun.Spec.RoleModelOverrides
			}
			if refreshedRoleCatalog, roleErr := loadRoleCatalog(ctx, crdClient, provider, roleModelOverrides); roleErr != nil {
				log.Printf("WARN: failed to refresh role models for provider %q: %v", provider, roleErr)
				if !strings.EqualFold(strings.TrimSpace(provider), roleCatalogProvider) {
					activeRoleCatalog = roleCatalogWithoutModelOverrides(roleCatalog)
				}
			} else {
				activeRoleCatalog = refreshedRoleCatalog
				roleCatalog = refreshedRoleCatalog
				roleCatalogProvider = strings.ToLower(strings.TrimSpace(provider))
			}
			parentModelSettings := parentModelSettingsForTurn(baseAgent.ModelSettings, mo.ModelSettings)
			turnSpecialistAgents := specialistAgentsForRoleCatalog(specialistAgents, activeRoleCatalog, model, parentModelSettings)
			turnHandoffs := handoffsForSpecialists(baseAgent.Handoffs, turnSpecialistAgents)
			maxTurns := int32(agent.DefaultMaxTurns)
			subAgentMaxTurns := int32(agent.DefaultSubAgentMaxTurns)
			if mo.MaxTurns > 0 {
				maxTurns = mo.MaxTurns
			}
			if mo.SubAgentMaxTurns > 0 {
				subAgentMaxTurns = mo.SubAgentMaxTurns
			}

			platformHooks := agent.NewEventHooks(tracker, eventStream)
			platformHooks.Turn = int(turnNumber)
			checkpointHooks := &workspaceCheckpointHooks{snapshotter: activeWorkspaceSnapshotter.Load()}
			// Subagent runs fall back to DefaultHooks. Meta-Harness capture
			// policy is explicit full capture: subagent tool and LLM hook
			// events are recorded in the same trace as the parent run.
			if metaharnessWriter != nil {
				runner.DefaultHooks = agent.NewCompositeHooks(platformHooks, checkpointHooks, metaharnessWriter)
			} else {
				runner.DefaultHooks = agent.NewCompositeHooks(platformHooks, checkpointHooks)
			}

			// Composite: workspace durability + MetaHarness trace writer (optional)
			// + the context-usage gauge feeding the dashboard's context bar.
			// Sub-agent runs fall back to DefaultHooks and never touch the gauge.
			ctxUsageHooks := &contextUsageHooks{mainAgentName: baseAgent.Name}
			var runHooks agent.RunHooks = agent.NewCompositeHooks(platformHooks, checkpointHooks, ctxUsageHooks)
			if metaharnessWriter != nil {
				runHooks = agent.NewCompositeHooks(platformHooks, checkpointHooks, ctxUsageHooks, metaharnessWriter)
			}

			// Phases were removed; tool access is governed by mode and session
			// mode (chat/plan) only.
			currentPhase := ""
			if eventStream != nil {
				eventStream.SetPhase(currentPhase)
			}

			// Resolve compaction thresholds per-turn using the active model.
			compactionConfig := resolveCompactionConfig(ctx, model, provider, modelMetadata)
			// Publish the budget the runner will actually use (the per-model
			// resolver supersedes the base config, mirroring runner logic) so
			// the dashboard's context bar shows the real compaction point.
			effectiveBudget := compactionConfig
			if compactionResolver != nil && effectiveBudget.Enabled {
				if trigger, target, ok := compactionResolver(ctx, model); ok && trigger > 0 {
					effectiveBudget.TriggerTokens = trigger
					effectiveBudget.TargetTokens = target
				}
			}
			publishContextBudget(effectiveBudget)

			workingState, err := sc.ReadWorkingState(ctx)
			if err != nil {
				log.Printf("WARN: failed to read durable working state: %v", err)
			}
			if firstAgentPass {
				goal := deriveWorkingStateGoal(reply, prompt)
				if err := sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
					state.Goal = goal
					state.LastUserMessage = strings.TrimSpace(prompt)
					state.CurrentMode = currentModeName
					state.CurrentPhase = currentPhase
					return nil
				}); err != nil {
					log.Printf("WARN: failed to update working state for user turn: %v", err)
				} else {
					workingState.Goal = strings.TrimSpace(goal)
					workingState.LastUserMessage = strings.TrimSpace(prompt)
					workingState.CurrentMode = currentModeName
					workingState.CurrentPhase = currentPhase
				}
			}

			messages, err := sc.GetMessagesSince(ctx, workingState.HistoryFloorMessageID)
			if err != nil {
				log.Printf("WARN: failed to load session messages for context rebuild: %v", err)
			} else if prepared, assetErr := materializeRecentConversationAssets(ctx, cfg.RepoDir, activeRun, sc.StateStore(), messages, recentConversationMessageLimit+1); assetErr != nil {
				log.Printf("WARN: failed to materialize recent project assets: %v", assetErr)
			} else {
				messages = prepared
			}

			// A moved history floor means the session context was cleared or
			// compacted externally — the in-memory transcript is stale.
			if workingState.HistoryFloorMessageID != transcriptFloor {
				if len(sessionTranscript) > 0 {
					// The persisted snapshot is stale for the same reason.
					if err := sc.ClearTranscriptBlob(ctx); err != nil {
						log.Printf("WARN: failed to clear transcript snapshot after context clear: %v", err)
					}
				}
				sessionTranscript = nil
				transcriptFloor = workingState.HistoryFloorMessageID
			}

			// Fold durable messages recorded since the transcript was
			// captured (plan rejections, mode-switch notes, …) into the
			// transcript: they are in neither FinalHistory nor the current
			// user item, so verbatim replay would silently drop them. The
			// durable-tail fallback includes them by construction. Folding
			// into sessionTranscript itself (not just this turn's input)
			// keeps them across turn failures too.
			if len(sessionTranscript) > 0 {
				sessionTranscript = append(sessionTranscript, outOfBandMessageItems(messages, transcriptSeenMessageID, selfAssistantMessageID, workingState)...)
			}
			transcriptSeenMessageID = maxSeenMessageID(transcriptSeenMessageID, messages)

			excludeMessageID := int64(0)
			if firstAgentPass {
				excludeMessageID = currentUserMessageID
			}
			inputItems := buildTurnInput(sessionTranscript, messages, workingState, excludeMessageID, recentConversationMessageLimit)
			turnOpeningText := prompt
			if firstAgentPass && resumingInterruptedTurn && len(sessionTranscript) > 0 {
				// This message started the turn a pod termination cut short:
				// its prompt is already in the replayed transcript, followed
				// by the partial work it triggered. Open with a continuation
				// instruction instead of repeating the prompt. (If the
				// restored transcript was discarded — floor moved, decode
				// failure — the verbatim prompt is kept: nothing replays it.)
				turnOpeningText = podResumeContinuationPrompt
				log.Printf("Turn %d resumes the pod-terminated turn for message %d — continuing from the preserved partial transcript", turnNumber, currentUserMessageID)
			}
			if firstAgentPass && interruptedSubAgentNotice != "" {
				turnOpeningText += "\n\n" + interruptedSubAgentNotice
				interruptedSubAgentNotice = ""
			}
			if assetPaths, assetErr := materializeMessageAssets(ctx, cfg.RepoDir, activeRun, sc.StateStore(), currentUserImages); assetErr != nil {
				// Preserve ordinary vision delivery if workspace materialization is
				// unavailable; the model still receives the image attachment below.
				log.Printf("WARN: failed to materialize current message project assets: %v", assetErr)
			} else {
				turnOpeningText = appendMessageAssetNotice(turnOpeningText, assetPaths)
			}
			userItem := agent.RunItem{
				Type:    agent.RunItemMessage,
				Message: &agent.MessageOutput{Text: turnOpeningText, Images: toSDKImageAttachments(currentUserImages)},
			}
			inputItems = append(inputItems, userItem)
			workingStateContext := buildWorkingStateContext(workingState)
			// Refresh the durable project state briefing each turn so the model
			// sees current tasks and memories (SDK prime, operator persistence).
			if prime := refreshPrimeContext(ctx, psStore, cfg.TaskName); prime != "" {
				if workingStateContext != "" {
					workingStateContext += "\n\n" + prime
				} else {
					workingStateContext = prime
				}
			}

			if cfg.Debug {
				// Prompts and working state may contain credentials or private source.
				// Diagnostics expose only sizes, never content.
				log.Printf("[input-diag] prompt_len=%d images=%d input_items=%d mode_instructions_len=%d working_state_len=%d",
					len(prompt), len(currentUserImages), len(inputItems), len(mo.ModeInstructions), len(workingStateContext))
			}

			log.Printf("Turn %d: mode=%q, %d tools",
				turnNumber, currentModeName, len(baseAgent.Tools))

			compactionCarryForward := func(ctx context.Context) string {
				state := workingState
				if latestState, err := sc.ReadWorkingState(ctx); err == nil {
					state = latestState
				}

				liveModeName := currentModeName
				liveStep := ""
				if liveRun := getAgentRun(ctx, crdClient, cfg.TaskName, cfg.Namespace); liveRun != nil {
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
				return strings.Join(parts, "\n\n")
			}

			cloneOpts := []agent.AgentOption{
				agent.WithModel(model),
				agent.WithTools(baseAgent.Tools...),
				agent.WithHandoffs(turnHandoffs...),
			}
			turnAgent := baseAgent.Clone(cloneOpts...)
			turnAgent.ModelSettings = parentModelSettings

			var sdkModeSnapshot *sdkmode.TemplateSpec
			if activeRun != nil {
				sdkModeSnapshot = platformModeSnapshotForSDK(activeRun.Status.ModeSnapshot)
			}
			modeDirectiveText := strings.TrimSpace(mo.ModeInstructions)
			modeDirectiveText = strings.TrimSpace(modeDirectiveText + "\n\n" + commitAttributionPolicyPrompt())
			// Skill-shipped guidance (e.g. PromQL/LogQL discipline) rides
			// with the mode directive so attached tool bundles come with their
			// usage instructions.
			if skillInstr := skillInstructionsForRun(ctx, crdClient, activeRun); skillInstr != "" {
				modeDirectiveText = strings.TrimSpace(modeDirectiveText + "\n\n" + skillInstr)
			}
			// Connected MCP servers ride along too, so the agent knows the
			// mcp__<server>__<tool> tools exist.
			if mcpPromptBlock != "" {
				modeDirectiveText = strings.TrimSpace(modeDirectiveText + "\n\n" + mcpPromptBlock)
			}
			runCfg := sdkruntime.BuildRunConfig(sdkruntime.Config{
				Provider:               provider,
				Model:                  model,
				WorkDir:                cfg.RepoDir,
				ActiveMode:             currentModeName,
				ModeSnapshot:           sdkModeSnapshot,
				MaxTurns:               int(maxTurns),
				SubAgentMaxTurns:       int(subAgentMaxTurns),
				MaxConcurrentSubAgents: mo.MaxConcurrentSubAgents,
				ToolAccess:             toolAccessLevel,
				AllowedMutatingTools:   effectiveAllowedMutatingTools(activeRun),
				TracingProcessor:       tp,
				Debug:                  cfg.Debug,
				ModeDirectiveText:      modeDirectiveText,
				WorkingStateText:       workingStateContext,
				Trace:                  runTrace,
				ParentSpanID:           runTrace.ID,
				// Explicit per-turn runtime features (SDK v0.0.7+): tools and
				// guardrails are passed directly above; only run-loop behavior
				// is feature-gated here.
				Features: &sdkruntime.Features{
					Modes: sdkruntime.ModeFeatures{
						Instructions: true,
					},
					Runtime: sdkruntime.RuntimeFeatures{
						Retry:                 true,
						Tracing:               tp != nil,
						ImmediateInputPolling: true,
						ParallelToolCalls:     true,
						UntrustedToolOutputs:  true,
						// Enables the per-model compaction threshold resolver;
						// the explicit CompactionConfig below is used either way.
						Compaction: true,
					},
				},
				ImmediateInputPoller: func(ctx context.Context) ([]agent.RunItem, error) {
					items, newCursor, err := pollImmediateInputs(ctx, sc, cfg.RepoDir, activeRun, immediateCursor, handledImmediate)
					if newCursor > immediateCursor {
						immediateCursor = newCursor
					}
					return items, err
				},
				CompactionConfig:          &compactionConfig,
				CompactionModelResolver:   compactionResolver,
				CompactionRecorder:        recordCompaction,
				CompactionFailureReporter: reportCompactionFailure,
				CompactionCarryForward:    compactionCarryForward,
				HandoffHistory:            &handoffHistoryConfig,
				ToolInputRules:            toolInputGuardrails,
				ToolOutputRules:           toolOutputGuardrails,
			}, runHooks)

			if cfg.AutoMode {
				// Long autonomous tasks: bounce the first final answer back with
				// a verify-your-work prompt (Terminus 2 double-confirm pattern).
				runCfg.RequireCompletionConfirmation = true
				if criticVerifierEnabled() {
					// One-round adversarial review by a read-only critic before
					// finalizing (SDK v0.0.9 NewCriticVerifier).
					critic := turnAgent.Clone()
					critic.Instructions = ""
					runCfg.FinalAnswerVerifier = newCriticVerifier(
						runner,
						critic,
						prompt,
						runCfg.RetryPolicy,
						runCfg.ModelCallTimeout,
					)
				}
			}

			if subAgentRegistry != nil {
				agent.ConfigureSubAgentScheduler(subAgentRegistry, agent.SubAgentSchedulerConfig{
					Runner:           runner,
					Agents:           turnSpecialistAgents,
					Tracker:          tracker,
					EventStream:      eventStream,
					MaxConcurrent:    mo.MaxConcurrentSubAgents,
					MaxTurns:         int(subAgentMaxTurns),
					WorkDir:          cfg.RepoDir,
					ToolAccessLevel:  toolAccessLevel,
					ToolPolicy:       runCfg.ToolPolicy,
					CompactionConfig: compactionConfig,
					// Sub-agents pinned to other models compact at their own
					// model's window instead of inheriting the parent's.
					CompactionModelResolver: compactionResolver,
				})
			}

			// Run the turn under its own cancellable context watched by the
			// interrupt poller: a user stop request cancels the in-flight
			// model call and running tools without touching the run context.
			turnCtx, cancelTurn := context.WithCancel(ctx)
			interruptWatcher := startTurnInterruptWatcher(ctx, sc, cancelTurn)
			result, err := runner.Run(turnCtx, turnAgent, inputItems, runCfg)
			turnInterrupted := interruptWatcher.Finish()
			cancelTurn()
			if !turnInterrupted {
				// A stop request that raced the end of the turn: claim it now
				// so it stops this loop instead of killing a later turn.
				if req, cErr := sc.DrainInterruptsThrough(ctx, time.Now().UTC()); cErr == nil && req != nil {
					turnInterrupted = true
				}
			}
			if err != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					flushPodTerminationState(sc, result, transcriptFloor, transcriptSeenMessageID, selfAssistantMessageID, currentUserMessageID)
					return runResult{}
				}
				// User-requested interrupt: stop sub-agents too, surface the
				// stop, and keep the session alive awaiting the next message.
				if turnInterrupted {
					stoppedTasks := cancelActiveSubAgentTasks(subAgentRegistry)
					log.Printf("Turn %d interrupted by user (cancelled %d sub-agent tasks)", turnNumber, stoppedTasks)
					// The cancelled turn hands back its accumulated
					// conversation (SDK partial-result semantics). Persist it
					// so the stop does not amnesia the session: the user's
					// next message continues with the interrupted turn's tool
					// outputs, findings, and delivered sub-agent results
					// instead of the pre-turn state. Unlike pod termination,
					// user stop records no pending resume prompt: a replacement
					// must wait for a genuinely newer user message.
					if preserved := transcriptAfterRun(result); len(preserved) > 0 {
						sessionTranscript = preserved
						// Preserve the history but deliberately omit a pending
						// resume marker: a user stop may only resume on a newer input.
						persistInFlightTranscriptSnapshot(ctx, sc, sessionTranscript, transcriptFloor, transcriptSeenMessageID, selfAssistantMessageID, 0)
						activeWorkspaceSnapshotter.Load().SnapshotAsync("turn-interrupted")
						log.Printf("Turn %d: preserved %d interrupted-turn conversation items", turnNumber, len(sessionTranscript))
					}
					_ = sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
						state.LastStoppedUserMessageID = currentUserMessageID
						return nil
					})
					_ = sc.WriteActivity(ctx, "turn_interrupted", turnInterruptNotice(stoppedTasks), nil)
					_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputStopped, "Stopped by user.", nil)
					break agentLoop
				}
				if errors.Is(err, context.Canceled) {
					flushPodTerminationState(sc, result, transcriptFloor, transcriptSeenMessageID, selfAssistantMessageID, currentUserMessageID)
					return runResult{}
				}
				// Turn budget exhausted: unlike other turn failures, the SDK
				// hands back the accumulated conversation as a partial result.
				// Persist the transcript + working state and park the session
				// so the user's next message CONTINUES with full context
				// instead of retrying from the pre-turn history with amnesia.
				// Older SDKs return a nil result here; the branch then falls
				// through to the generic turn-failure path below.
				var budgetErr *agent.MaxTurnsExceeded
				if errors.As(err, &budgetErr) && result != nil && len(result.FinalHistory) > 0 {
					sessionTranscript = transcriptAfterRun(result)
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
					// Durable snapshot: a pod recycle must not lose the
					// preserved conversation; same for this turn's file
					// changes in the workspace. The in-flight prompt rides
					// along as pending — the resume cursor re-delivers it to
					// a recycled pod, which must not replay it verbatim on
					// top of the preserved partial progress.
					persistInFlightTranscriptSnapshot(ctx, sc, sessionTranscript, transcriptFloor, transcriptSeenMessageID, selfAssistantMessageID, currentUserMessageID)
					activeWorkspaceSnapshotter.Load().SnapshotAsync("turn-budget")
					if cfg.DelegatedChild {
						msg := fmt.Sprintf("chat session %d exhausted its %d-turn budget", turnNumber, budgetErr.MaxTurns)
						if turnSummary != "" {
							msg += "\nPartial progress before the budget ran out: " + turnSummary
						}
						return runResult{Status: "failed", Error: msg}
					}
					notice := turnBudgetNotice(turnNumber, budgetErr.MaxTurns)
					log.Printf("Turn %d exhausted the %d-turn budget — transcript preserved (%d items), awaiting user", turnNumber, budgetErr.MaxTurns, len(sessionTranscript))
					_ = sc.WriteActivity(ctx, "turn_budget_exhausted", notice, nil)
					_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, notice, nil)
					break agentLoop
				}
				// Delegated children must reach a terminal phase — a parent
				// team run is blocked on this run's result.
				if cfg.DelegatedChild {
					return runResult{Status: "failed", Error: fmt.Sprintf("chat session %d failed: %v", turnNumber, err)}
				}
				// A failed turn (LLM/API error after the SDK exhausted its
				// retries and model fallbacks, guardrail trip, or a turn cap
				// on an SDK without partial-result support) must not kill
				// the session: stop, surface the error, and wait for the
				// user's next message to retry.
				notice := turnFailureNotice(turnNumber, err)
				log.Printf("ERROR: chat session %d failed (recoverable, awaiting user): %v", turnNumber, err)
				_ = sc.WriteActivity(ctx, "turn_failed", notice, nil)
				_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, notice, nil)
				// Newer SDKs hand the failed turn's accumulated conversation
				// back alongside the error (partial-result semantics):
				// persist it so the retry continues from the completed turns
				// — tool outputs, findings, delivered sub-agent results —
				// instead of re-running the whole turn with amnesia. The
				// in-flight prompt rides along as pending so a pod recycle
				// re-delivers it as a continuation, not a verbatim replay.
				if preserved := transcriptAfterRun(result); len(preserved) > 0 {
					sessionTranscript = preserved
					persistInFlightTranscriptSnapshot(ctx, sc, sessionTranscript, transcriptFloor, transcriptSeenMessageID, selfAssistantMessageID, currentUserMessageID)
					activeWorkspaceSnapshotter.Load().SnapshotAsync("turn-failed")
					log.Printf("Turn %d: preserved %d failed-turn conversation items", turnNumber, len(sessionTranscript))
				} else if len(sessionTranscript) > 0 {
					// Older SDKs return no partial state. Keep the failed
					// turn's user message visible next turn (parity with the
					// durable tail, which includes it). An empty transcript
					// keeps the durable-tail fallback intact.
					sessionTranscript = append(sessionTranscript, userItem)
				}
				break agentLoop
			}
			firstAgentPass = false
			// Carry the runner's post-run conversation state into the next
			// turn (full-transcript replay); runs interrupted by an
			// unresolved tool approval reset to the durable-tail fallback
			// (user stops and failures preserve their partial transcript in
			// the error branches above).
			sessionTranscript = transcriptAfterRun(result)

			updatedRun := getAgentRun(ctx, crdClient, cfg.TaskName, cfg.Namespace)
			updatedModeName := currentModeName
			if updatedRun != nil {
				if updatedRun.Status.ModeName != "" {
					updatedModeName = updatedRun.Status.ModeName
				}
			}

			// A mode switch made while runner.Run was in flight takes effect on the
			// next pass. It may change instructions and permissions, never pacing.
			cfg.AutoMode = true

			// The displayed assistant message is the model's actual reply
			// (FinalText). buildAssistantTurnSummary produces a compact,
			// tool-annotated summary ("Tools: …", "Key results: …") meant for
			// durable cross-turn context — it must NOT be shown to the user as
			// the assistant's chat message. When there is no natural-language
			// reply (e.g. a turn that ends by calling finish), prefer the
			// finish tool's summary, falling back to the turn summary.
			turnSummary := buildAssistantTurnSummary(result.NewItems)
			displayMessage := strings.TrimSpace(result.FinalText())
			if displayMessage == "" {
				if fs := strings.TrimSpace(finishSummary.Summary()); fs != "" {
					displayMessage = fs
				} else {
					displayMessage = turnSummary
				}
			}
			if displayMessage != "" {
				if msg, err := sc.AppendAssistantAndCompleteClaims(ctx, displayMessage); err != nil {
					log.Printf("WARN: failed to persist assistant message: %v", err)
				} else if msg != nil {
					// Already in FinalHistory — the next turn's out-of-band
					// fold must not duplicate it.
					selfAssistantMessageID = msg.ID
				}
			}
			if err := sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
				state.CurrentMode = updatedModeName
				state.LastResponseID = result.LastResponseID
				if turnSummary != "" {
					state.LastAssistantSummary = turnSummary
					state.RecentTurnSummaries = append(state.RecentTurnSummaries, turnSummary)
				}
				return nil
			}); err != nil {
				log.Printf("WARN: failed to persist working state after turn: %v", err)
			}

			// Durably snapshot the post-turn transcript (one bounded,
			// upserted row per session) so a pod restart replays full
			// context. An interruption-reset (empty) transcript clears the
			// row and resume falls back to the durable tail.
			persistTranscriptSnapshot(ctx, sc, sessionTranscript, transcriptFloor, transcriptSeenMessageID, selfAssistantMessageID)

			// Checkpoint the workspace to the hidden snapshot ref so a hard
			// kill (OOM, node loss) cannot lose this turn's file changes.
			// Delta push, skipped when nothing changed.
			activeWorkspaceSnapshotter.Load().SnapshotAsync("turn-end")

			// Update the autonomous continuation tracker with this turn's results.
			if cfg.AutoMode {
				aTracker.Update(result.NewItems)
			}

			// --- User stop that raced the natural end of the turn ---
			// The turn finished before the watcher could cancel it, but the
			// user asked to stop: halt here (matters in autonomous mode)
			// instead of letting the claimed request vanish silently.
			if turnInterrupted {
				if err := sc.CompleteClaims(ctx); err != nil {
					return runResult{Status: "failed", Error: fmt.Sprintf("completing interrupted claims: %v", err)}
				}
				log.Printf("Turn %d: user stop request arrived as the turn completed — breaking to await user", turnNumber)
				_ = sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
					state.LastStoppedUserMessageID = currentUserMessageID
					return nil
				})
				_ = sc.WriteActivity(ctx, "turn_interrupted", "Stopped by user — the turn had just completed; send a message to continue.", nil)
				_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputStopped, "Stopped by user.", nil)
				break agentLoop
			}

			// --- AskUserQuestion / present_plan breaks auto-continue ---
			// Even in auto mode, if the LLM called AskUserQuestion or present_plan
			// it means it's genuinely blocked and needs user input. Break to await
			// the answer instead of firing another "[SYSTEM] Continue..." prompt.
			inputPause := agent.DetectUserInputPause(result.NewItems, result.FinalText())
			if inputPause.Requested {
				log.Printf("User interaction requested — breaking to await user response")
				inputType := platformv1alpha1.UserInputQuestion
				if len(inputPause.Actions) > 0 {
					if err := sc.SetUserInputRequest(ctx, inputType, strings.TrimSpace(inputPause.Question), inputPause.Actions); err != nil {
						return runResult{Status: "failed", Error: fmt.Sprintf("writing user input request: %v", err)}
					}
				} else {
					if err := sc.SetUserInputRequest(ctx, inputType, strings.TrimSpace(inputPause.Question), nil); err != nil {
						return runResult{Status: "failed", Error: fmt.Sprintf("writing user input request: %v", err)}
					}
				}
				break agentLoop
			}

			// --- MCP break-glass approval pause ---
			if pendingRun := getAgentRun(ctx, crdClient, cfg.TaskName, cfg.Namespace); pendingRun != nil &&
				pendingRun.Status.Phase == platformv1alpha1.AgentRunPhaseWaitingApproval {
				if pendingRequest, err := mcppolicy.PendingRequest(pendingRun); err != nil {
					log.Printf("WARN: failed to decode MCP break-glass request annotation: %v", err)
				} else if pendingRequest != nil {
					log.Printf("MCP break-glass request for %q paused the agent loop", pendingRequest.Server)
					break agentLoop
				}
			}

			// Compatibility guard: pacing is always autonomous, but keep a safe
			// input boundary if an older runtime configuration disables it.
			if !cfg.AutoMode {
				if err := sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputIdle, "", nil); err != nil {
					return runResult{Status: "failed", Error: fmt.Sprintf("writing user input request: %v", err)}
				}
				break agentLoop
			}

			// --- Finish tool completion ---
			// The finish tool marks CompletionRequested=true when the agent signals it is done.
			if run := getAgentRun(ctx, crdClient, cfg.TaskName, cfg.Namespace); run != nil && run.Status.CompletionRequested {
				log.Printf("Run %q complete — finish called", run.Status.ModeName)
				completionDetail, _ := json.Marshal(map[string]string{
					"from_mode": run.Status.ModeName,
					"result":    "completed",
				})
				_ = sc.WriteActivity(ctx, "mode_complete", fmt.Sprintf("Mode %q completed", run.Status.ModeName), completionDetail)

				// Clear the completion flag so a later user message resumes the run
				// instead of immediately re-detecting completion.
				if err := patchAgentRunStatus(ctx, crdClient, cfg.TaskName, cfg.Namespace, func(fresh *platformv1alpha1.AgentRun) {
					fresh.Status.CompletionRequested = false
				}); err != nil {
					log.Printf("WARN: failed to clear completion flag: %v", err)
				}

				if cfg.DelegatedChild {
					return runResult{Status: "succeeded"}
				}
				_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputIdle, "", nil)
				break agentLoop
			}

			// --- Autonomous mode: keep the loop spinning ---
			if cfg.AutoMode {
				// Circuit breakers: detect stalled or stuck agents.
				if cb := aTracker.CheckCircuitBreakers(); cb.Tripped {
					log.Printf("Auto mode: circuit breaker tripped — %s", cb.Reason)
					_ = sc.WriteActivity(ctx, "circuit_breaker", cb.Reason, nil)
					_ = sc.SetUserInputRequest(ctx, platformv1alpha1.UserInputCircuitBreak, cb.Reason, nil)
					break agentLoop
				}

				// Smart nudge: context-aware continuation prompt.
				prompt = agent.BuildSmartNudge(aTracker, currentPhase)
				_, _ = sc.AppendSystemMessage(ctx, prompt)
				log.Printf("Auto mode: agent turn complete (tools=%d, noToolTurns=%d), continuing loop (%d/%d)", aTracker.ToolCallCount(), aTracker.ConsecutiveNoToolTurns(), autoLoopCount, agent.DefaultMaxAutoLoops)
				continue agentLoop
			}

		} // end agentLoop

	}
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

func firstNonEmptyRunMode(run *platformv1alpha1.AgentRun) string {
	if run == nil {
		return ""
	}
	return strings.TrimSpace(run.Status.ModeName)
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

func latestUserMessageID(messages []sessionclient.UserMessage, fallback int64) int64 {
	if len(messages) == 0 {
		return fallback
	}
	return messages[len(messages)-1].ID
}

func waitForNextUserReply(
	ctx context.Context,
	sc *sessionclient.Client,
	afterID int64,
	pollInterval time.Duration,
	handledImmediate map[int64]struct{},
) (sessionclient.UserMessage, int64, error) {
	cursor := afterID
	for {
		messages, err := sc.PollForUserMessages(ctx, cursor, pollInterval)
		if err != nil {
			return sessionclient.UserMessage{}, cursor, err
		}
		msg, ok, skipCursor, immediate := nextPendingUserMessage(messages, handledImmediate)
		if skipCursor > cursor {
			cursor = skipCursor
		}
		if !ok {
			continue
		}
		if immediate {
			handledImmediate[msg.ID] = struct{}{}
		} else {
			cursor = msg.ID
		}
		// Establish ownership before the content enters model context. A cancel
		// or another claimant may have won after the poll snapshot.
		claimed, won, claimErr := sc.ClaimUserMessage(ctx, msg)
		if claimErr != nil {
			return sessionclient.UserMessage{}, cursor, claimErr
		}
		if !won {
			continue
		}
		msg = claimed
		content := strings.TrimSpace(msg.Content)
		if content == "" && len(msg.Images) == 0 {
			continue
		}
		return msg, cursor, nil
	}
}

func pollImmediateInputs(
	ctx context.Context,
	sc *sessionclient.Client,
	workDir string,
	run *platformv1alpha1.AgentRun,
	afterID int64,
	handledImmediate map[int64]struct{},
) ([]agent.RunItem, int64, error) {
	messages, err := sc.PeekForUserMessages(ctx, afterID)
	if err != nil {
		return nil, afterID, err
	}
	_, consumedIDs, cursor := collectImmediateRunItems(messages, handledImmediate)
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
				return nil, afterID, claimErr
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
	if cursor == 0 {
		cursor = afterID
	}
	return items, cursor, nil
}
