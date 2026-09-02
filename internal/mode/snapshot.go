package mode

import (
	toolscache "k8s.io/client-go/tools/cache"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// StatusSnapshot returns the copy of a resolved ModeTemplateSpec that is
// pinned on AgentRun.Status.ModeSnapshot.
//
// Instructions are deliberately left out. Every consumer that renders or
// prompts with mode instructions (the agent runtime, the dashboard) reads
// them from the live ModeTemplate by name so template edits apply to running
// work; the snapshot pins only what must stay fixed for the run — identity,
// category, permission mode, constraints, default refs. Instructions are the
// bulk of a template (4–16 KB for the shipped templates), and the controller
// keeps every AgentRun in its informer cache and deep-copies the whole fleet
// on every list, so carrying them per run is the single largest avoidable
// heap cost at thousands of runs.
func StatusSnapshot(spec *platformv1alpha1.ModeTemplateSpec) *platformv1alpha1.ModeTemplateSpec {
	if spec == nil {
		return nil
	}
	out := spec.DeepCopy()
	out.Instructions = ""
	return out
}

// StripSnapshotInstructions clears Status.ModeSnapshot.Instructions on a run
// in place. Reports whether anything changed.
func StripSnapshotInstructions(run *platformv1alpha1.AgentRun) bool {
	if run == nil || run.Status.ModeSnapshot == nil || run.Status.ModeSnapshot.Instructions == "" {
		return false
	}
	run.Status.ModeSnapshot.Instructions = ""
	return true
}

// AgentRunCacheTransform returns an informer transform for AgentRun objects
// that applies next (typically the managed-fields strip shared by every
// kind) and then drops Status.ModeSnapshot.Instructions, so runs pinned
// before StatusSnapshot omitted instructions stop retaining them in the
// controller's cache. Nothing in the manager reads instructions from the
// snapshot: the agent runtime reads them in its own pod straight from the
// API server, and the dashboard reads the live template.
func AgentRunCacheTransform(next toolscache.TransformFunc) toolscache.TransformFunc {
	return func(obj any) (any, error) {
		if next != nil {
			var err error
			if obj, err = next(obj); err != nil {
				return nil, err
			}
		}
		if run, ok := obj.(*platformv1alpha1.AgentRun); ok {
			StripSnapshotInstructions(run)
		}
		return obj, nil
	}
}
