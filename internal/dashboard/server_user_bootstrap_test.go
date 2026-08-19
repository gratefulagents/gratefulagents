package dashboard

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
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

func TestEnsureUserNamespaceDoesNotSeedSecurityCatalog(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	objects := []client.Object{
		bootstrapReadyMarker("v1"),
		bundledSecuritySkill("security-scan", "scan carefully"),
		&platformv1alpha1.Skill{
			ObjectMeta: bootstrapMeta("grafana"),
			Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
				Inline: &platformv1alpha1.SkillInlineSource{Instructions: "observe carefully"},
			}},
		},
		&triggersv1alpha1.SecurityWorkflow{ObjectMeta: bootstrapMeta("workflow")},
		&triggersv1alpha1.SecurityRanker{ObjectMeta: bootstrapMeta("ranker")},
		&triggersv1alpha1.SecurityPostScript{ObjectMeta: bootstrapMeta("post-script")},
		&triggersv1alpha1.SecurityPolicyPack{ObjectMeta: bootstrapMeta("policy-pack")},
		&triggersv1alpha1.SecurityProgram{ObjectMeta: bootstrapMeta("program")},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}
	ctx := credActorCtx("alice-id", "Alice Smith")

	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	personalNamespace := &corev1.Namespace{}
	if err := c.Get(ctx, client.ObjectKey{Name: namespace}, personalNamespace); err != nil {
		t.Fatal(err)
	}
	if got := personalNamespace.Annotations[bootstrapSyncedVersionAnnotation]; got != "v5:v1" {
		t.Fatalf("bootstrap synced version = %q, want v5:v1", got)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "grafana"}, &platformv1alpha1.Skill{}); err != nil {
		t.Fatalf("non-security bootstrap Skill was not seeded: %v", err)
	}
	securityObjects := map[string]client.Object{
		"security-scan": &platformv1alpha1.Skill{},
		"workflow":      &triggersv1alpha1.SecurityWorkflow{},
		"ranker":        &triggersv1alpha1.SecurityRanker{},
		"post-script":   &triggersv1alpha1.SecurityPostScript{},
		"policy-pack":   &triggersv1alpha1.SecurityPolicyPack{},
		"program":       &triggersv1alpha1.SecurityProgram{},
	}
	for name, object := range securityObjects {
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, object); err == nil {
			t.Errorf("security catalog resource %s was copied without opt-in", name)
		}
	}
}

func TestBootstrapV5PreservesExistingSecurityResourcesWithoutRefreshing(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	source := &triggersv1alpha1.SecurityRanker{
		ObjectMeta: bootstrapMeta("existing"),
		Spec:       triggersv1alpha1.SecurityRankerSpec{Rules: []string{"new catalog rules"}},
	}
	newSource := &triggersv1alpha1.SecurityRanker{
		ObjectMeta: bootstrapMeta("new-default"),
		Spec:       triggersv1alpha1.SecurityRankerSpec{Rules: []string{"new resource"}},
	}
	existing := &triggersv1alpha1.SecurityRanker{
		ObjectMeta: metav1.ObjectMeta{
			Name: "existing", Namespace: "alice",
			Annotations: map[string]string{
				bootstrapDefaultAnnotation:  "true",
				bootstrapSourceAnnotation:   "system",
				bootstrapSpecHashAnnotation: "older-seed-hash",
			},
		},
		Spec: triggersv1alpha1.SecurityRankerSpec{Rules: []string{"older seeded rules"}},
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "alice", Annotations: map[string]string{bootstrapSyncedVersionAnnotation: "v4:old"},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bootstrapReadyMarker("new"), source, newSource, existing, namespace).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}

	if err := srv.syncBootstrapResources(context.Background(), "alice"); err != nil {
		t.Fatalf("v5 bootstrap migration: %v", err)
	}
	preserved := &triggersv1alpha1.SecurityRanker{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "alice", Name: "existing"}, preserved); err != nil {
		t.Fatal(err)
	}
	if got := preserved.Spec.Rules[0]; got != "older seeded rules" {
		t.Fatalf("existing seeded resource was refreshed to %q", got)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "alice", Name: "new-default"}, &triggersv1alpha1.SecurityRanker{}); err == nil {
		t.Fatal("v5 migration seeded a newly shipped security resource")
	}
	updatedNamespace := &corev1.Namespace{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "alice"}, updatedNamespace); err != nil {
		t.Fatal(err)
	}
	if got := updatedNamespace.Annotations[bootstrapSyncedVersionAnnotation]; got != "v5:new" {
		t.Fatalf("bootstrap synced version = %q, want v5:new", got)
	}
}

func TestBootstrapStillRefreshesUntouchedNonSecuritySkills(t *testing.T) {
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
		t.Fatalf("non-security Skill = %q, want refreshed version", got)
	}
}
