package mcpattach

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func TestEffectiveMCPServerRefs(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	skillA := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "skill-a", Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Source:   platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "x"}},
			Requires: &platformv1alpha1.SkillRequires{MCPServers: []platformv1alpha1.NamedRef{{Name: "grafana"}, {Name: "fetch"}}},
		},
	}
	skillNoReq := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "skill-b", Namespace: "ns"},
		Spec: platformv1alpha1.SkillSpec{
			Source: platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{Instructions: "y"}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(skillA, skillNoReq).Build()

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "ns"},
		Spec: platformv1alpha1.AgentRunSpec{
			MCPServerRefs: []platformv1alpha1.NamedRef{{Name: "grafana"}, {Name: ""}},
			SkillRefs:     []platformv1alpha1.NamedRef{{Name: "skill-a"}, {Name: "skill-b"}, {Name: "missing"}},
		},
	}
	got := EffectiveMCPServerRefs(context.Background(), c, run)
	want := []string{"grafana", "fetch"} // deduped, explicit first, missing skill skipped
	if len(got) != len(want) {
		t.Fatalf("refs = %+v, want %v", got, want)
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("refs[%d] = %q, want %q (all: %+v)", i, got[i].Name, w, got)
		}
	}

	if got := EffectiveMCPServerRefs(context.Background(), c, nil); got != nil {
		t.Fatalf("nil run should produce nil, got %+v", got)
	}
}
