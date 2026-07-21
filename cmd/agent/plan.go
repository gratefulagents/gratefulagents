package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/google/uuid"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/agentplatform"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkotel "github.com/gratefulagents/sdk/pkg/agentsdk/otel"
	agentpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	sdkproviders "github.com/gratefulagents/sdk/pkg/agentsdk/providers"
	sdkruntime "github.com/gratefulagents/sdk/pkg/agentsdk/runtime"
	metaharness "github.com/gratefulagents/sdk/pkg/agentsdk/tracestore"
)

const (
	teamParentLabel = "platform.gratefulagents.dev/team-parent"

	// parallelToolCallingOneLiner is a compact reminder for the model to batch
	// independent tool calls. Replaces the former verbose parallelToolCallingPrompt.
	parallelToolCallingOneLiner = "Batch independent tool calls in a single response."
)

type runResult struct {
	Status string
	Error  string
}

func writeRuntimeErrorActivity(ctx context.Context, sc *sessionclient.Client, message string) {
	if sc == nil || strings.TrimSpace(message) == "" {
		return
	}
	event := agent.ContentEvent{
		Timestamp: time.Now().UTC(),
		Type:      "runtime_error",
		Message:   message,
		IsError:   true,
		Status:    "failed",
	}
	detail, err := json.Marshal(event)
	if err != nil {
		return
	}
	if err := sc.WriteActivity(ctx, event.Type, event.Message, detail); err != nil {
		log.Printf("WARN: failed to persist runtime error activity: %v", err)
	}
}

type usageCostEstimator interface {
	EstimateCost(agent.Usage) (float64, bool)
}

func sdkRuntimeProviderConfig(cfg runConfig, model string) sdkruntime.Config {
	return sdkruntime.Config{
		Provider:                 "multi",
		DefaultProvider:          cfg.Provider,
		Model:                    model,
		BaseURL:                  cfg.BaseURL,
		APIKey:                   cfg.APIKey,
		AuthMode:                 cfg.AuthMode,
		APIMode:                  cfg.APIMode,
		OpenAIOAuthPath:          cfg.OpenAIOAuthPath,
		OpenAIOAuthAccountID:     cfg.OpenAIOAuthAccountID,
		OpenAIOAuthAccountIDPath: cfg.OpenAIOAuthAccountIDPath,
		CopilotOAuthPath:         cfg.CopilotOAuthPath,
		AnthropicOAuthPath:       cfg.AnthropicOAuthPath,
		ProviderAPIKeys:          cloneStringMap(cfg.ProviderAPIKeys),
		ProviderBaseURLs:         cloneStringMap(cfg.ProviderBaseURLs),
		ProviderAPIModes:         cloneStringMap(cfg.ProviderAPIModes),
		ModelFallbacks:           append([]string(nil), cfg.ModelFallbacks...),
	}
}

func resolveConfiguredModel(cfg runConfig, model string) agent.Model {
	provider, err := sdkproviders.NewProviderFromConfig(sdkruntime.ProviderSpec(sdkRuntimeProviderConfig(cfg, model)))
	if err != nil {
		log.Printf("WARN: failed to initialize model provider for cost metadata: %v", err)
		return nil
	}
	resolved, err := provider.GetModel(model)
	if err != nil {
		log.Printf("WARN: failed to resolve model %q for cost metadata: %v", model, err)
		return nil
	}
	return resolved
}

// runChat is the single entry point for the agent pod.
// It sets up the workspace and enters the chat loop.
func runChat() error {
	cfg, err := loadRunConfig()
	if err != nil {
		log.Printf("ERROR: %v", err)
		return err
	}
	if cfg.Debug {
		log.Printf("DEBUG mode enabled — verbose logging active")
	}
	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stopSignals()

	k8sClient, err := agentplatform.BuildK8sClient()
	if err != nil {
		log.Printf("ERROR: failed to build k8s client: %v", err)
		return err
	}

	crdClient, _, err := agentplatform.BuildCRDClientWithScheme()
	if err != nil {
		log.Printf("ERROR: failed to build CRD client: %v", err)
		return err
	}

	// Resolve mode from the AgentRun CRD — single source of truth.
	cfg.AutoMode = resolveAutoModeFromCRD(ctx, crdClient, cfg.TaskName, cfg.Namespace)
	cfg.DelegatedChild = isDelegatedChildFromCRD(ctx, crdClient, cfg.TaskName, cfg.Namespace)
	sc, err := initSessionClient(ctx, crdClient, cfg.TaskName, cfg.Namespace, "pending", "setup")
	if err != nil {
		log.Printf("ERROR: Postgres session client required: %v", err)
		return err
	}

	var result runResult
	var eventsLogURL string
	defer func() {
		if result.Status == "" {
			return
		}

		// Terminal CRD status is authoritative and must not wait behind optional
		// Postgres writes. Give each sink an independent deadline so one degraded
		// backend cannot consume the others' write budget.
		statusCtx, cancelStatus := context.WithTimeout(context.Background(), 5*time.Second)
		if err := writeResultToStatus(statusCtx, crdClient, cfg.TaskName, cfg.Namespace, result, eventsLogURL); err != nil {
			log.Printf("ERROR: failed to write result to status: %v", err)
		}
		cancelStatus()

		resultCtx, cancelResult := context.WithTimeout(context.Background(), 5*time.Second)
		_ = sc.WriteResult(resultCtx, result.Status, "", result.Error, "", "")
		cancelResult()

		if result.Status == "failed" && strings.TrimSpace(result.Error) != "" {
			activityCtx, cancelActivity := context.WithTimeout(context.Background(), 5*time.Second)
			writeRuntimeErrorActivity(activityCtx, sc, result.Error)
			cancelActivity()
		}
	}()

	result, eventsLogURL = doRun(ctx, cfg, k8sClient, crdClient, sc)
	if result.Status == "failed" {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}

// doRun sets up the workspace, starts the progress loop, and enters the chat loop.
func doRun(ctx context.Context, cfg runConfig, k8sClient *kubernetes.Clientset, crdClient client.Client, sc *sessionclient.Client) (result runResult, eventsLogURL string) {
	if err := os.MkdirAll(cfg.WorkspaceDir, 0o755); err != nil {
		log.Printf("ERROR: failed to create workspace dir: %v", err)
		result = runResult{Status: "failed", Error: err.Error()}
		return
	}

	// RunProgress aggregates metrics (cost, tokens, step) for CRD status
	// via Snapshot(). Content events go through EventWriter; structural
	// observability through OTel spans.
	tracker := agent.NewRunProgress(agent.WithMaxToolResultBytes(10 * 1024))

	// Create thin event stream for real-time content delivery.
	resolvedModel := resolveConfiguredModel(cfg, cfg.Model)
	streamPath := filepath.Join(cfg.WorkspaceDir, "events.jsonl")
	streamFile, err := os.Create(streamPath)
	if err != nil {
		log.Printf("WARN: failed to create event stream file: %v — streaming disabled", err)
	}
	var eventStream *agent.EventWriter
	if streamFile != nil {
		defer streamFile.Close()

		// Tee event stream to Postgres for crash-resilient durability.
		var streamWriter io.Writer = streamFile
		var pgWriter *pgEventWriter
		if sc != nil && sc.SessionID() != uuid.Nil {
			pgWriter = newPGEventWriter(sc.StateStore(), sc.SessionID())
			streamWriter = io.MultiWriter(streamFile, pgWriter)
		}
		if pgWriter != nil {
			defer pgWriter.Close()
		}

		eventStream = agent.NewEventWriter(streamWriter)
		logLevel := agent.LogLevelNormal
		if cfg.Debug {
			logLevel = agent.LogLevelDebug
		}
		eventStream.SetLogger(agent.NewAgentLogger(logLevel))
	}

	// Capture prior-process counters exactly once before this pod publishes any
	// progress. All writes and cost-cap checks add the current tracker delta to
	// this immutable baseline.
	metricsBaseline := progressMetricsBaselineFromRun(getAgentRun(ctx, crdClient, cfg.TaskName, cfg.Namespace))
	progressCtx, cancelProgress := context.WithCancel(ctx)
	var progressWg sync.WaitGroup
	progressWg.Add(1)
	go func() {
		defer progressWg.Done()
		startProgressLoop(progressCtx, crdClient, cfg, tracker, sc, metricsBaseline)
	}()

	var metaharnessWriter *metaharness.TraceWriter
	var metaharnessTraceDir string
	metaharnessStartedAt := time.Now()
	defer func() {
		tracker.WriteResult(result.Status, "", result.Error, "")
		if eventStream != nil {
			snap := tracker.Snapshot()
			costKnown := false
			if estimator, ok := resolvedModel.(usageCostEstimator); ok && estimator != nil {
				_, costKnown = estimator.EstimateCost(agent.Usage{
					InputTokens:       snap.InputTokens,
					OutputTokens:      snap.OutputTokens,
					CacheReadTokens:   snap.CacheReadInputTokens,
					CacheCreateTokens: snap.CacheCreationInputTokens,
				})
			} else if resolvedModel != nil && resolvedModel.Provider() != "openai" {
				costKnown = true
			}
			eventStream.EmitSessionEnd(result.Status,
				snap.CostUsd,
				costKnown,
				snap.InputTokens, snap.OutputTokens,
				snap.CacheReadInputTokens, snap.CacheCreationInputTokens,
				0, 0, "")
		}
		cancelProgress()
		progressWg.Wait()
		if streamFile != nil {
			_ = streamFile.Sync()
		}
	}()

	// Resolve RuntimeProfile → permission mode before workspace setup.
	// Zero trust: default to read-only when no RuntimeProfile is configured.
	// Transient API failures and creation races are retried inside the
	// resolver; when the fallback still applies the resolution is marked
	// degraded so the chat loop can announce it, re-check every turn, and
	// bounce compute once write access resolves — a startup blip must not
	// silently pin a whole session to a read-only filesystem.
	resolution := resolveStartupPermissionMode(ctx, crdClient, cfg.TaskName, cfg.Namespace)
	clampedRun := resolution.Run
	cfg.KubernetesAdmin = clampedRun != nil && clampedRun.Spec.KubernetesAdmin
	if err := setupKubernetesAdminSandboxEnv(cfg.KubernetesAdmin); err != nil {
		log.Printf("WARN: failed to prepare Kubernetes-admin kubeconfig for sandbox: %v", err)
	}
	cfg.PermissionMode = clampResolvedPermissionMode(resolution.Mode, clampedRun)
	cfg.GitRemoteWrites = agentpolicy.NormalizeGitRemoteWrites(resolution.GitRemoteWrites)
	cfg.PermissionModeDegraded = resolution.Degraded
	cfg.PermissionModeReason = resolution.Reason
	if cfg.PermissionMode != resolution.Mode && !cfg.PermissionMode.AllowsWriteTools() {
		cfg.PermissionModeReason = "the run's mode template clamps this run to read-only"
	}

	// Load (or create once) the private key used to encrypt object-storage
	// workspace checkpoints. The key is persisted in Postgres before any
	// checkpoint is published, so a replacement pod can decrypt it.
	workspaceSnapshotKey, err := loadOrCreateWorkspaceSnapshotKey(ctx, sc)
	if err != nil {
		log.Printf("ERROR: workspace snapshot key setup failed: %v", err)
		result = runResult{Status: "failed", Error: err.Error()}
		return
	}
	cfg.WorkspaceSnapshotKey = workspaceSnapshotKey

	checkpointStore, checkpointRoot, err := newWorkspaceObjectStoreFromEnv()
	if err != nil {
		log.Printf("ERROR: workspace checkpoint store setup failed: %v", err)
		result = runResult{Status: "failed", Error: err.Error()}
		return
	}
	cfg.WorkspaceCheckpointStore = checkpointStore
	cfg.WorkspaceCheckpointPrefix = workspaceCheckpointRunPrefix(checkpointRoot, cfg.Namespace, cfg.TaskUID)
	checkpoint, err := loadWorkspaceCheckpoint(ctx, cfg.WorkspaceCheckpointStore, cfg.WorkspaceCheckpointPrefix, cfg.WorkspaceSnapshotKey)
	if err != nil {
		log.Printf("ERROR: workspace checkpoint load failed: %v", err)
		result = runResult{Status: "failed", Error: err.Error()}
		return
	}
	cfg.WorkspaceCheckpoint = checkpoint

	// Repository setup can take long enough to look stalled. Publish the exact
	// startup step immediately instead of waiting for the first progress tick.
	workspaceStep := "cloning-repository"
	if cfg.Repoless {
		workspaceStep = "setting-up-workspace"
	}
	tracker.SetStep(workspaceStep)
	if err := sc.UpdatePhase(ctx, "running", workspaceStep); err != nil {
		log.Printf("WARN: failed to publish workspace startup status: %v", err)
	}
	if err := setupWorkspace(&cfg); err != nil {
		log.Printf("ERROR: setup failed: %v", err)
		result = runResult{Status: "failed", Error: err.Error()}
		return
	}
	tracker.SetStep("setup")
	if err := sc.UpdatePhase(ctx, "running", "setup"); err != nil {
		log.Printf("WARN: failed to publish workspace setup status: %v", err)
	}
	setRuntimeParentMetadataEnv(cfg)

	// Workspace WIP snapshots: checkpoint after mutating tools, periodically,
	// after each turn, and on shutdown so pod replacement loses at most the
	// active checkpoint window. Repoless runs snapshot extra repositories only.
	if err := restoreExtraRepos(ctx, cfg, sc); err != nil {
		log.Printf("ERROR: restoring additional repositories failed: %v", err)
		return runResult{Status: "failed", Error: err.Error()}, eventsLogURL
	}
	if err := cloneAdditionalRepos(&cfg); err != nil {
		log.Printf("ERROR: cloning additional repositories failed: %v", err)
		return runResult{Status: "failed", Error: err.Error()}, eventsLogURL
	}
	snapshotter := newWorkspaceSnapshotter(cfg, sc)
	activeWorkspaceSnapshotter.Store(snapshotter)
	defer func() {
		if err := finalizeWorkspaceSnapshot(result, snapshotter); err != nil {
			log.Printf("ERROR: final workspace checkpoint failed: %v", err)
			result = runResult{Status: "failed", Error: "final workspace checkpoint failed: " + err.Error()}
		}
	}()
	stopPeriodicSnapshots := snapshotter.StartPeriodic(ctx)
	defer stopPeriodicSnapshots() // runs before the final synchronous snapshot

	// Initialize OTel tracing processor.
	var tp agent.TracingProcessor
	var otelProc *sdkotel.OTelTracingProcessor
	otelEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if otelEndpoint == "" {
		log.Printf("WARN: OTEL_EXPORTER_OTLP_ENDPOINT not set; OTel tracing disabled")
		tp = agent.NoOpTracingProcessor{}
	} else if p, otelErr := sdkotel.NewOTelTracingProcessorWithEndpoint(ctx, "gratefulagents-agent", otelEndpoint); otelErr != nil {
		log.Printf("WARN: OTel tracing disabled: %v", otelErr)
		tp = agent.NoOpTracingProcessor{}
	} else {
		otelProc = p
		tp = otelProc
		defer func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := otelProc.Shutdown(shutCtx); err != nil {
				log.Printf("WARN: OTel shutdown error: %v", err)
			}
			cancel()
		}()
	}
	// Meta-Harness observer: capture full execution traces for harness
	// optimization. Composed into tp BEFORE the tracker installs the
	// processor so structural spans (session, subagent, retry, compaction)
	// reach the trace writer too.
	if metaHarnessEnabled() {
		initialModeName := ""
		if run := getAgentRun(ctx, crdClient, cfg.TaskName, cfg.Namespace); run != nil && run.Status.ModeName != "" {
			initialModeName = run.Status.ModeName
		}
		metaharnessWriter, metaharnessTraceDir = newMetaHarnessWriter(cfg, initialModeName)
		if metaharnessWriter != nil {
			tp = &agent.MultiTracingProcessor{Processors: []agent.TracingProcessor{tp, metaharnessWriter}}
			// Registered after the final-workspace-snapshot defer so it runs
			// BEFORE it (defers are LIFO): the shutdown checkpoint can spend
			// up to workspaceCheckpointTimeout of the pod's 60s termination
			// grace, and trace finalization must not be starved behind it.
			// Its own budget is bounded by metaHarnessFinalizeTimeout so the
			// combined shutdown work stays inside the grace period.
			defer func() {
				status := "unknown"
				if result.Status != "" {
					status = result.Status
				}
				finalizeMetaHarnessTrace(cfg, crdClient, metaharnessWriter, metaharnessTraceDir, tracker.Snapshot(), metaharnessStartedAt, status)
			}()
		}
	}

	// Wire the fully composed processor into the tracker so Record* methods
	// emit structural spans to every processor, including Meta-Harness.
	tracker.SetTracingProcessor(tp)

	// Publish trace ID to CRD as soon as the first trace starts so the
	// dashboard can show live traces while the run is still in progress.
	if otelProc != nil {
		otelProc.SetOnTraceIDReady(func(tid string) {
			writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := patchAgentRunStatus(writeCtx, crdClient, cfg.TaskName, cfg.Namespace, func(run *platformv1alpha1.AgentRun) {
				run.Status.Artifacts = ensureRunArtifacts(run.Status.Artifacts)
				run.Status.Artifacts.TraceID = tid
			}); err != nil {
				log.Printf("WARN: failed to store early trace ID: %v", err)
			}
		})
	}

	// Enter chat loop. The first user message is already seeded in Postgres
	// by the creator (dashboard, trigger, or team service).
	tracker.SetStep("chatting")
	result = runChatLoop(ctx, cfg, crdClient, k8sClient, tracker, sc, metricsBaseline, tp, eventStream, metaharnessWriter)
	if result.Error != "" {
		log.Printf("ERROR: chat loop failed (%s): %s", result.Status, result.Error)
	} else {
		log.Printf("Chat loop finished: %s", result.Status)
	}

	// Store OTel trace ID on CRD so dashboard can query Jaeger.
	if otelProc != nil {
		if tid := otelProc.TraceID(); tid != "" {
			writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := patchAgentRunStatus(writeCtx, crdClient, cfg.TaskName, cfg.Namespace, func(run *platformv1alpha1.AgentRun) {
				run.Status.Artifacts = ensureRunArtifacts(run.Status.Artifacts)
				run.Status.Artifacts.TraceID = tid
			}); err != nil {
				log.Printf("WARN: failed to store trace ID: %v", err)
			}
			cancel()
		}
	}

	return
}
