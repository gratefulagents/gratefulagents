package dashboard

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func bootstrapReadyMarker(version string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: "bootstrap-ready", Namespace: "system",
			Labels: map[string]string{bootstrapReadyLabel: "true"},
		},
		Data: map[string]string{bootstrapBundleVersionKey: version},
	}
}

func bootstrapMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:      name,
		Namespace: "system",
		Annotations: map[string]string{
			bootstrapDefaultAnnotation:         "true",
			"helm.sh/hook":                     "post-install,post-upgrade",
			"example.gratefulagents.dev/value": "preserved",
		},
	}
}

func TestBootstrapStillRefreshesUntouchedSkills(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	source := &platformv1alpha1.Skill{
		ObjectMeta: bootstrapMeta("grafana"),
		Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{Inline: &platformv1alpha1.SkillInlineSource{
			Instructions: "version one",
		}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v1"), source,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alice"}},
	).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}
	ctx := context.Background()
	if err := srv.syncBootstrapResources(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	source.Spec.Source.Inline.Instructions = "version two"
	if err := c.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	marker := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "system", Name: "bootstrap-ready"}, marker); err != nil {
		t.Fatal(err)
	}
	marker.Data[bootstrapBundleVersionKey] = "v2"
	if err := c.Update(ctx, marker); err != nil {
		t.Fatal(err)
	}
	if err := srv.syncBootstrapResources(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	installed := &platformv1alpha1.Skill{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "alice", Name: "grafana"}, installed); err != nil {
		t.Fatal(err)
	}
	if got := installed.Spec.Source.Inline.Instructions; got != "version two" {
		t.Fatalf("Skill = %q, want refreshed version", got)
	}
}
