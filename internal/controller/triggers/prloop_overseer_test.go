package triggers

import (
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func TestReviewerSpecDoesNotInheritOverseer(t *testing.T) {
	t.Parallel()
	implementer := &platformv1alpha1.AgentRun{Spec: platformv1alpha1.AgentRunSpec{
		Overseer: &platformv1alpha1.AgentRunOverseerSpec{
			Authority:        platformv1alpha1.AgentRunOverseerAuthorityEnforce,
			IntervalMinutes:  10,
			MaxInterventions: 5,
		},
	}}
	if got := reviewerSpecFromImplementer(implementer, "main").Overseer; got != nil {
		t.Fatalf("reviewer inherited overseer config: %#v", got)
	}
}
