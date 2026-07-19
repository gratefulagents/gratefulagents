package dashboard

import (
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// k8sTeamRuntimeToProto converts a K8s AgentRunTeamRuntime to a proto TeamRuntime.
func k8sTeamRuntimeToProto(rt *platformv1alpha1.AgentRunTeamRuntime) *platform.TeamRuntime {
	if rt == nil {
		return nil
	}

	pb := &platform.TeamRuntime{
		Namespace:  rt.Namespace,
		Name:       rt.Name,
		ParentName: rt.Spec.ParentRef.Name,
		ParentUid:  rt.Spec.ParentRef.UID,
		Generation: rt.Spec.Generation,

		Phase:          string(rt.Status.Phase),
		TotalTasks:     rt.Status.TotalTasks,
		ReadyTasks:     rt.Status.ReadyTasks,
		RunningTasks:   rt.Status.RunningTasks,
		CompletedTasks: rt.Status.CompletedTasks,
		FailedTasks:    rt.Status.FailedTasks,
		BlockedTasks:   rt.Status.BlockedTasks,

		CreatedAtUnix: rt.CreationTimestamp.Unix(),
	}

	if rt.Spec.DelegationPolicy != nil {
		pb.DelegationPolicy = &platform.AgentRunDelegationPolicy{
			MaxChildren: rt.Spec.DelegationPolicy.MaxChildren,
			MaxDepth:    rt.Spec.DelegationPolicy.MaxDepth,
			ParentOnly:  rt.Spec.DelegationPolicy.ParentOnly,
		}
	}

	// Convert spec tasks (authoritative list)
	for _, t := range rt.Spec.Tasks {
		pb.Tasks = append(pb.Tasks, teamRuntimeTaskToProto(&t))
	}

	// Overlay status tasks (runtime state)
	statusMap := make(map[string]*platformv1alpha1.TeamRuntimeTaskInstance, len(rt.Status.Tasks))
	for i := range rt.Status.Tasks {
		statusMap[rt.Status.Tasks[i].Name] = &rt.Status.Tasks[i]
	}
	for i, specTask := range pb.Tasks {
		if st, ok := statusMap[specTask.Name]; ok {
			pb.Tasks[i] = teamRuntimeTaskToProto(st)
		}
	}

	if cp := rt.Status.EventCheckpoint; cp != nil {
		pb.EventCheckpoint = &platform.TeamRuntimeEventCheckpoint{
			StreamId:            cp.StreamID,
			Sequence:            cp.Sequence,
			RuntimeStateVersion: cp.RuntimeStateVersion,
			PostgresEventId:     cp.PostgresEventID,
		}
	}

	return pb
}

func teamRuntimeTaskToProto(t *platformv1alpha1.TeamRuntimeTaskInstance) *platform.TeamRuntimeTask {
	pb := &platform.TeamRuntimeTask{
		Name:              t.Name,
		StepName:          t.StepName,
		Role:              t.Role,
		State:             string(t.State),
		StateVersion:      t.StateVersion,
		DependsOn:         t.DependsOn,
		ArtifactContract:  t.ArtifactContract,
		ArtifactSatisfied: t.ArtifactSatisfied,
		MaxRetries:        t.MaxRetries,
		AttemptCount:      t.AttemptCount,
		ClaimOwner:        t.ClaimOwner,
		LeaseToken:        t.LeaseToken,
		Diagnostic:        string(t.Diagnostic),
		DiagnosticMessage: t.DiagnosticMessage,
		SuggestedAction:   t.SuggestedAction,
	}

	if t.LeaseExpiresAt != nil {
		pb.LeaseExpiresAtUnix = t.LeaseExpiresAt.Unix()
	}
	if t.LastHeartbeatAt != nil {
		pb.LastHeartbeatAtUnix = t.LastHeartbeatAt.Unix()
	}
	if t.ClaimedAt != nil {
		pb.ClaimedAtUnix = t.ClaimedAt.Unix()
	}
	if t.CompletedAt != nil {
		pb.CompletedAtUnix = t.CompletedAt.Unix()
	}

	return pb
}
