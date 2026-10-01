package main

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkruntime "github.com/gratefulagents/sdk/pkg/agentsdk/runtime"
)

// modeOverrides reads the CRD modeSnapshot and returns behavioral/limit
// overrides. Mode templates do not choose models; the run spec remains the
// model source of truth.
type modeOverrides struct {
	ModelSettings          agent.ModelSettings
	MaxTurns               int32
	SubAgentMaxTurns       int32
	ModeInstructions       string   // Behavioral prompt from ModeTemplate.Instructions.
	MaxConcurrentSubAgents int      // from CRD ModeConstraints (0 = unlimited)
	ExecutionStrategy      string   // from CRD (serial/parallel/pipeline)
	FallbackModels         []string // SDK-level fallback models from ModeTemplate.ModelRouting
}

// modeInstructionsCache caches live ModeTemplate instructions with a short TTL
// to avoid hitting the API server every turn on long-running (200+ turn) agents.
type modeInstructionsCache struct {
	mu      sync.Mutex
	name    string
	value   string
	expires time.Time
}

const modeInstructionsCacheTTL = 60 * time.Second

func (c *modeInstructionsCache) get(name string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.name == name && time.Now().Before(c.expires) {
		return c.value, true
	}
	return "", false
}

func (c *modeInstructionsCache) set(name, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.name = name
	c.value = value
	c.expires = time.Now().Add(modeInstructionsCacheTTL)
}

var modeInstrCache modeInstructionsCache

// readModeOverrides derives the turn's mode overrides from run, the AgentRun
// already read for this pass.
func readModeOverrides(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) modeOverrides {
	if run == nil || run.Status.ModeSnapshot == nil {
		return modeOverrides{}
	}
	snap := run.Status.ModeSnapshot

	var mo modeOverrides
	sdkOverrides := sdkruntime.ModeOverridesFromSnapshot(platformModeSnapshotForSDK(snap), "")
	mo.ModelSettings = sdkOverrides.ModelSettings
	if level := strings.TrimSpace(string(run.Spec.ReasoningLevel)); level != "" {
		mo.ModelSettings = mo.ModelSettings.Merge(agent.ModeRoutingSettings(level, ""))
	}
	mo.MaxTurns = int32(sdkOverrides.MaxTurns)
	mo.SubAgentMaxTurns = int32(sdkOverrides.SubAgentMaxTurns)
	mo.MaxConcurrentSubAgents = sdkOverrides.MaxConcurrentSubAgents
	mo.FallbackModels = sdkOverrides.FallbackModels

	// Always read instructions from the live resolved ModeTemplate CRD so edits
	// take effect without restarting the run. The snapshot name is canonical:
	// legacy refs such as "chat" may resolve to "autopilot", and reusing the
	// stale spec ref would combine chat instructions with autonomous pacing.
	// Cached with a short TTL to avoid hammering the API server on long runs.
	liveModeName := strings.TrimSpace(snap.Name)
	if liveModeName == "" && run.Spec.ModeRef != nil {
		liveModeName = strings.TrimSpace(run.Spec.ModeRef.Name)
	}
	if liveModeName != "" {
		if instructions, ok := modeInstrCache.get(liveModeName); ok {
			mo.ModeInstructions = instructions
		} else {
			var liveTmpl platformv1alpha1.ModeTemplate
			if err := c.Get(ctx, client.ObjectKey{Name: liveModeName}, &liveTmpl); err == nil {
				mo.ModeInstructions = liveTmpl.Spec.Instructions
				modeInstrCache.set(liveModeName, liveTmpl.Spec.Instructions)
			} else {
				log.Printf("WARN: failed to read live ModeTemplate %q, falling back to snapshot: %v", liveModeName, err)
				mo.ModeInstructions = sdkOverrides.ModeInstructions
			}
		}
	} else if sdkOverrides.ModeInstructions != "" {
		mo.ModeInstructions = sdkOverrides.ModeInstructions
	}

	// Execution strategy.
	if snap.ExecutionStrategy != "" {
		mo.ExecutionStrategy = string(snap.ExecutionStrategy)
	}

	return mo
}

// ---------------------------------------------------------------------------
// Environment helpers
// ---------------------------------------------------------------------------

// isDelegatedChildFromCRD checks whether this AgentRun is a child delegated
// by a parent, by looking at its delegation metadata.
// Transient read errors are retried: misclassifying a delegated child would
// let it park awaiting a human while its parent team run blocks forever.
func isDelegatedChildFromCRD(ctx context.Context, c client.Client, name, namespace string) (bool, error) {
	run, err := readAgentRun(ctx, c, name, namespace, startupMetricsReadAttempts)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(run.Labels[teamParentLabel]) != "" {
		return true, nil
	}
	for _, owner := range run.OwnerReferences {
		if owner.APIVersion == platformv1alpha1.GroupVersion.String() &&
			owner.Kind == "AgentRun" && strings.TrimSpace(owner.Name) != "" {
			return true, nil
		}
	}
	return false, nil
}

// shouldTerminateAfterFinish reports whether finish is a terminal signal for
// this worker. User-facing runs remain resumable after finish, but orchestrated
// SecurityScan tasks must reach a terminal AgentRun phase so their workflow can
// consume the result and advance.
func shouldTerminateAfterFinish(run *platformv1alpha1.AgentRun, delegatedChild bool) bool {
	if delegatedChild {
		return true
	}
	return run != nil && run.Spec.Trigger.MatchesKind(triggersv1alpha1.SecurityScanTriggerKind)
}

// setRuntimeParentMetadataEnv exports this run's identity for tools that
// spawn child runs: AGENTRUN_CURRENT_* always, AGENTRUN_PARENT_* and RUN_*
// only when the controller did not already set them.
func setRuntimeParentMetadataEnv(cfg runConfig) {
	for _, field := range []struct{ value, current, parent, legacy string }{
		{cfg.Namespace, "AGENTRUN_CURRENT_NAMESPACE", "AGENTRUN_PARENT_NAMESPACE", "RUN_NAMESPACE"},
		{cfg.TaskName, "AGENTRUN_CURRENT_NAME", "AGENTRUN_PARENT_NAME", "RUN_NAME"},
		{cfg.TaskUID, "AGENTRUN_CURRENT_UID", "AGENTRUN_PARENT_UID", "RUN_UID"},
	} {
		value := strings.TrimSpace(field.value)
		if value == "" {
			continue
		}
		_ = os.Setenv(field.current, value)
		parent := strings.TrimSpace(os.Getenv(field.parent))
		if parent == "" {
			parent = value
			_ = os.Setenv(field.parent, parent)
		}
		if strings.TrimSpace(os.Getenv(field.legacy)) == "" {
			_ = os.Setenv(field.legacy, parent)
		}
	}
}
