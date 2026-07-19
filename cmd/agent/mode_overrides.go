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
	ModeInstructions       string // Behavioral prompt from ModeTemplate.Instructions.
	MaxConcurrentSubAgents int    // from CRD ModeConstraints (0 = unlimited)
	ExecutionStrategy      string // from CRD (serial/parallel/pipeline)
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

func readModeOverrides(ctx context.Context, c client.Client, taskName, namespace string) modeOverrides {
	run := getAgentRun(ctx, c, taskName, namespace)
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

// autoModeFromRun preserves the old helper boundary while enforcing the single
// pacing contract: every run is autonomous and yields only for explicit input
// requests, safety stops, or finish. WorkflowMode and ModeTemplate.Autonomous
// remain readable for backward compatibility but no longer alter pacing.
func autoModeFromRun(_ *platformv1alpha1.AgentRun) bool {
	return true
}

// resolveAutoModeFromCRD reads the AgentRun CRD to determine if this pod
// should run in autonomous mode.
func resolveAutoModeFromCRD(ctx context.Context, c client.Client, name, namespace string) bool {
	run := getAgentRun(ctx, c, name, namespace)
	if run == nil {
		log.Printf("WARN: could not read AgentRun %s/%s to resolve mode — defaulting to chat", namespace, name)
		return false
	}
	return autoModeFromRun(run)
}

// isDelegatedChildFromCRD checks whether this AgentRun is a child delegated
// by a parent, by looking at its delegation metadata.
func isDelegatedChildFromCRD(ctx context.Context, c client.Client, name, namespace string) bool {
	run := getAgentRun(ctx, c, name, namespace)
	if run == nil {
		return false
	}
	if strings.TrimSpace(run.Labels[teamParentLabel]) != "" {
		return true
	}
	for _, owner := range run.OwnerReferences {
		if owner.APIVersion == platformv1alpha1.GroupVersion.String() &&
			owner.Kind == "AgentRun" && strings.TrimSpace(owner.Name) != "" {
			return true
		}
	}
	return false
}

// autoModeIntent reads the AgentRun CRD intent to use as the first prompt
// for autonomous runs. Returns empty string if no intent is set.
func setRuntimeParentMetadataEnv(cfg runConfig) {
	if namespace := strings.TrimSpace(cfg.Namespace); namespace != "" {
		_ = os.Setenv("AGENTRUN_CURRENT_NAMESPACE", namespace)
		parentNamespace := strings.TrimSpace(os.Getenv("AGENTRUN_PARENT_NAMESPACE"))
		if parentNamespace == "" {
			parentNamespace = namespace
			_ = os.Setenv("AGENTRUN_PARENT_NAMESPACE", parentNamespace)
		}
		if strings.TrimSpace(os.Getenv("RUN_NAMESPACE")) == "" {
			_ = os.Setenv("RUN_NAMESPACE", parentNamespace)
		}
	}
	if name := strings.TrimSpace(cfg.TaskName); name != "" {
		_ = os.Setenv("AGENTRUN_CURRENT_NAME", name)
		parentName := strings.TrimSpace(os.Getenv("AGENTRUN_PARENT_NAME"))
		if parentName == "" {
			parentName = name
			_ = os.Setenv("AGENTRUN_PARENT_NAME", parentName)
		}
		if strings.TrimSpace(os.Getenv("RUN_NAME")) == "" {
			_ = os.Setenv("RUN_NAME", parentName)
		}
	}
	if uid := strings.TrimSpace(cfg.TaskUID); uid != "" {
		_ = os.Setenv("AGENTRUN_CURRENT_UID", uid)
		parentUID := strings.TrimSpace(os.Getenv("AGENTRUN_PARENT_UID"))
		if parentUID == "" {
			parentUID = uid
			_ = os.Setenv("AGENTRUN_PARENT_UID", parentUID)
		}
		if strings.TrimSpace(os.Getenv("RUN_UID")) == "" {
			_ = os.Setenv("RUN_UID", parentUID)
		}
	}
}
