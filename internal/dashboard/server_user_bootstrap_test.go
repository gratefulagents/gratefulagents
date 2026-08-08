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

func TestEnsureUserNamespaceSeedsBootstrapSkillsAndSecurityLibrary(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	objects := []client.Object{
		bootstrapReadyMarker("v1"),
		&platformv1alpha1.Skill{
			ObjectMeta: bootstrapMeta("security-scan"),
			Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
				Inline: &platformv1alpha1.SkillInlineSource{Instructions: "scan carefully"},
			}},
		},
		&platformv1alpha1.Skill{
			ObjectMeta: metav1.ObjectMeta{Name: "private-skill", Namespace: "system"},
			Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
				Inline: &platformv1alpha1.SkillInlineSource{Instructions: "not shared"},
			}},
		},
		&triggersv1alpha1.SecurityWorkflow{
			ObjectMeta: bootstrapMeta("default-workflow"),
			Spec: triggersv1alpha1.SecurityWorkflowSpec{Tasks: []triggersv1alpha1.SecurityScanTask{{
				Name: "hunt", Objective: "find issues",
			}}},
		},
		&triggersv1alpha1.SecurityRanker{
			ObjectMeta: bootstrapMeta("default-ranker"),
			Spec:       triggersv1alpha1.SecurityRankerSpec{Rules: []string{"critical first"}},
		},
		&triggersv1alpha1.SecurityPostScript{
			ObjectMeta: bootstrapMeta("validate"),
			Spec:       triggersv1alpha1.SecurityPostScriptSpec{Prompt: "validate findings"},
		},
		&triggersv1alpha1.SecurityPolicyPack{
			ObjectMeta: bootstrapMeta("baseline"),
			Spec:       triggersv1alpha1.SecurityPolicyPackSpec{Description: "baseline policy"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ctx := credActorCtx("alice-id", "Alice Smith")

	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}

	personalNamespace := &corev1.Namespace{}
	if err := c.Get(ctx, client.ObjectKey{Name: namespace}, personalNamespace); err != nil {
		t.Fatalf("get personal Namespace: %v", err)
	}
	if got := personalNamespace.Annotations[bootstrapSyncedVersionAnnotation]; got != "v1" {
		t.Fatalf("bootstrap synced version = %q, want v1", got)
	}

	seededSkill := &platformv1alpha1.Skill{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "security-scan"}, seededSkill); err != nil {
		t.Fatalf("get seeded Skill: %v", err)
	}
	if seededSkill.Spec.Source.Inline == nil || seededSkill.Spec.Source.Inline.Instructions != "scan carefully" {
		t.Fatalf("seeded Skill spec = %+v", seededSkill.Spec)
	}
	if seededSkill.Annotations[bootstrapSourceAnnotation] != "system" {
		t.Fatalf("bootstrap source annotation = %q", seededSkill.Annotations[bootstrapSourceAnnotation])
	}
	if seededSkill.Annotations["helm.sh/hook"] != "" {
		t.Fatalf("Helm hook annotation leaked to seeded resource: %+v", seededSkill.Annotations)
	}
	if seededSkill.Annotations["example.gratefulagents.dev/value"] != "preserved" {
		t.Fatalf("source annotation was not preserved: %+v", seededSkill.Annotations)
	}

	for name, object := range map[string]client.Object{
		"SecurityWorkflow":   &triggersv1alpha1.SecurityWorkflow{},
		"SecurityRanker":     &triggersv1alpha1.SecurityRanker{},
		"SecurityPostScript": &triggersv1alpha1.SecurityPostScript{},
		"SecurityPolicyPack": &triggersv1alpha1.SecurityPolicyPack{},
	} {
		resourceName := map[string]string{
			"SecurityWorkflow": "default-workflow", "SecurityRanker": "default-ranker",
			"SecurityPostScript": "validate", "SecurityPolicyPack": "baseline",
		}[name]
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: resourceName}, object); err != nil {
			t.Errorf("get seeded %s: %v", name, err)
		}
	}

	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "private-skill"}, &platformv1alpha1.Skill{}); err == nil {
		t.Fatal("unmarked system Skill was copied into the user namespace")
	}
}

func TestBootstrapSeedWaitsForBundleReadinessMarker(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	source := &platformv1alpha1.Skill{
		ObjectMeta: bootstrapMeta("security-scan"),
		Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
			Inline: &platformv1alpha1.SkillInlineSource{Instructions: "bootstrap"},
		}},
	}
	targetNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source, targetNamespace).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}

	if err := srv.syncBootstrapResources(context.Background(), "alice"); err != nil {
		t.Fatalf("sync without readiness marker: %v", err)
	}
	key := client.ObjectKey{Namespace: "alice", Name: "security-scan"}
	if err := c.Get(context.Background(), key, &platformv1alpha1.Skill{}); err == nil {
		t.Fatal("Skill was seeded before the bundle readiness marker existed")
	}
	if err := c.Create(context.Background(), bootstrapReadyMarker("v1")); err != nil {
		t.Fatal(err)
	}
	if err := srv.syncBootstrapResources(context.Background(), "alice"); err != nil {
		t.Fatalf("sync after readiness marker: %v", err)
	}
	if err := c.Get(context.Background(), key, &platformv1alpha1.Skill{}); err != nil {
		t.Fatalf("Skill was not seeded after bundle became ready: %v", err)
	}
}

func TestBootstrapSeedDoesNotOverwritePersonalResource(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	personalNamespace := deriveUserNamespaceName("Alice Smith", "alice-id")
	source := &platformv1alpha1.Skill{
		ObjectMeta: bootstrapMeta("security-scan"),
		Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
			Inline: &platformv1alpha1.SkillInlineSource{Instructions: "bootstrap"},
		}},
	}
	personal := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "security-scan", Namespace: personalNamespace},
		Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
			Inline: &platformv1alpha1.SkillInlineSource{Instructions: "my customized version"},
		}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bootstrapReadyMarker("v1"), source, personal).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	if _, err := srv.ensureNamespaceForUser(context.Background(), "alice-id", "Alice Smith"); err != nil {
		t.Fatalf("ensureNamespaceForUser() error = %v", err)
	}
	got := &platformv1alpha1.Skill{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: personalNamespace, Name: "security-scan"}, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Source.Inline == nil || got.Spec.Source.Inline.Instructions != "my customized version" {
		t.Fatalf("personal Skill was overwritten: %+v", got.Spec)
	}
}
