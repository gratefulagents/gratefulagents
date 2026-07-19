package agentplatform

import (
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// The agent pod's CRD scheme must know every type agent-side tools touch.
// The maintainer toolbelt reads GitHubRepository (caps, merge opt-in), so a
// missing triggers registration breaks every maintainer tool with
// "no kind is registered for the type v1alpha1.GitHubRepository".
func TestNewCRDSchemeRegistersToolTypes(t *testing.T) {
	t.Parallel()

	scheme, err := NewCRDScheme()
	if err != nil {
		t.Fatalf("NewCRDScheme: %v", err)
	}
	for _, obj := range []runtime.Object{
		&platformv1alpha1.AgentRun{},
		&platformv1alpha1.ModeTemplate{},
		&triggersv1alpha1.GitHubRepository{},
		&corev1.Secret{},
	} {
		if _, _, err := scheme.ObjectKinds(obj); err != nil {
			t.Errorf("scheme is missing %T: %v", obj, err)
		}
	}
}
