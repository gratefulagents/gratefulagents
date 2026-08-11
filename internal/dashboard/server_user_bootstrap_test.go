package dashboard

import (
	"context"
	"testing"
	"time"

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

func TestEnsureUserNamespaceSeedsSecurityLibraryButNotSkills(t *testing.T) {
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
		&triggersv1alpha1.SecurityProgram{
			ObjectMeta: bootstrapMeta("bounty-program"),
			Spec: triggersv1alpha1.SecurityProgramSpec{
				Provider: "Immunefi", DisplayName: "Bounty Program",
				ProgramURL: "https://example.com/bounty", ScopePolicy: "authorized scope",
				VerifiedAt: metav1.NewTime(time.Date(2026, time.August, 11, 0, 0, 0, 0, time.UTC)),
			},
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
	if got := personalNamespace.Annotations[bootstrapSyncedVersionAnnotation]; got != "v4:v1" {
		t.Fatalf("bootstrap synced version = %q, want v4:v1", got)
	}

	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "security-scan"}, &platformv1alpha1.Skill{}); err == nil {
		t.Fatal("security Skill was copied without explicit opt-in")
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "grafana"}, &platformv1alpha1.Skill{}); err != nil {
		t.Fatalf("unrelated bootstrap Skill was not seeded: %v", err)
	}

	for name, object := range map[string]client.Object{
		"SecurityWorkflow":   &triggersv1alpha1.SecurityWorkflow{},
		"SecurityRanker":     &triggersv1alpha1.SecurityRanker{},
		"SecurityPostScript": &triggersv1alpha1.SecurityPostScript{},
		"SecurityPolicyPack": &triggersv1alpha1.SecurityPolicyPack{},
		"SecurityProgram":    &triggersv1alpha1.SecurityProgram{},
	} {
		resourceName := map[string]string{
			"SecurityWorkflow": "default-workflow", "SecurityRanker": "default-ranker",
			"SecurityPostScript": "validate", "SecurityPolicyPack": "baseline",
			"SecurityProgram": "bounty-program",
		}[name]
		if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: resourceName}, object); err != nil {
			t.Errorf("get seeded %s: %v", name, err)
		}
	}

	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "private-skill"}, &platformv1alpha1.Skill{}); err == nil {
		t.Fatal("unmarked system Skill was copied into the user namespace")
	}
}

func TestBootstrapV4MigrationSeedsProgramsWithoutAdoptingPersonalCopies(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	programSpec := triggersv1alpha1.SecurityProgramSpec{
		Provider: "Immunefi", DisplayName: "Bounty Program",
		ProgramURL: "https://example.com/bounty", ScopePolicy: "authorized scope",
		VerifiedAt: metav1.NewTime(time.Date(2026, time.August, 11, 0, 0, 0, 0, time.UTC)),
	}
	newDefault := &triggersv1alpha1.SecurityProgram{
		ObjectMeta: bootstrapMeta("new-default"), Spec: programSpec,
	}
	manualSource := &triggersv1alpha1.SecurityProgram{
		ObjectMeta: bootstrapMeta("manual-copy"), Spec: programSpec,
	}
	manualCopy := &triggersv1alpha1.SecurityProgram{
		ObjectMeta: metav1.ObjectMeta{Name: "manual-copy", Namespace: "alice"}, Spec: programSpec,
	}
	targetNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: "alice", Annotations: map[string]string{bootstrapSyncedVersionAnnotation: "v3:v1"},
	}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v1"), newDefault, manualSource, manualCopy, targetNamespace,
	).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}

	if err := srv.syncBootstrapResources(context.Background(), "alice"); err != nil {
		t.Fatalf("v3 to v4 bootstrap migration: %v", err)
	}

	seeded := &triggersv1alpha1.SecurityProgram{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "alice", Name: "new-default"}, seeded); err != nil {
		t.Fatalf("get newly seeded SecurityProgram: %v", err)
	}
	if seeded.Spec.Provider != programSpec.Provider || seeded.Spec.DisplayName != programSpec.DisplayName ||
		seeded.Spec.ProgramURL != programSpec.ProgramURL || seeded.Spec.ScopePolicy != programSpec.ScopePolicy ||
		!seeded.Spec.VerifiedAt.Time.Equal(programSpec.VerifiedAt.Time) {
		t.Fatalf("seeded SecurityProgram spec = %+v, want %+v", seeded.Spec, programSpec)
	}
	if seeded.Annotations[bootstrapSpecHashAnnotation] == "" {
		t.Fatal("newly seeded SecurityProgram has no bootstrap spec hash")
	}

	preserved := &triggersv1alpha1.SecurityProgram{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "alice", Name: "manual-copy"}, preserved); err != nil {
		t.Fatalf("get personal SecurityProgram: %v", err)
	}
	if preserved.Annotations[bootstrapSpecHashAnnotation] != "" || preserved.Annotations[bootstrapSourceAnnotation] != "" {
		t.Fatalf("personal SecurityProgram was adopted by bootstrap: annotations = %v", preserved.Annotations)
	}

	namespace := &corev1.Namespace{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "alice"}, namespace); err != nil {
		t.Fatal(err)
	}
	if got := namespace.Annotations[bootstrapSyncedVersionAnnotation]; got != "v4:v1" {
		t.Fatalf("bootstrap synced version = %q, want v4:v1", got)
	}
}

func TestBootstrapSecurityLibraryWaitsForBundleReadinessMarker(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	source := &triggersv1alpha1.SecurityRanker{
		ObjectMeta: bootstrapMeta("default-ranker"),
		Spec:       triggersv1alpha1.SecurityRankerSpec{Rules: []string{"critical first"}},
	}
	targetNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alice"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source, targetNamespace).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}

	if err := srv.syncBootstrapResources(context.Background(), "alice"); err != nil {
		t.Fatalf("sync without readiness marker: %v", err)
	}
	key := client.ObjectKey{Namespace: "alice", Name: "default-ranker"}
	if err := c.Get(context.Background(), key, &triggersv1alpha1.SecurityRanker{}); err == nil {
		t.Fatal("SecurityRanker was seeded before the bundle readiness marker existed")
	}
	if err := c.Create(context.Background(), bootstrapReadyMarker("v1")); err != nil {
		t.Fatal(err)
	}
	if err := srv.syncBootstrapResources(context.Background(), "alice"); err != nil {
		t.Fatalf("sync after readiness marker: %v", err)
	}
	if err := c.Get(context.Background(), key, &triggersv1alpha1.SecurityRanker{}); err != nil {
		t.Fatalf("SecurityRanker was not seeded after bundle became ready: %v", err)
	}
}

func TestBootstrapUpgradeRefreshesOnlyUnmodifiedResources(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	makeRanker := func(name, rule string) *triggersv1alpha1.SecurityRanker {
		return &triggersv1alpha1.SecurityRanker{
			ObjectMeta: bootstrapMeta(name),
			Spec:       triggersv1alpha1.SecurityRankerSpec{Rules: []string{rule}},
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v1"),
		makeRanker("unmodified", "version one"),
		makeRanker("customized", "version one"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alice"}},
	).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}
	ctx := context.Background()
	if err := srv.syncBootstrapResources(ctx, "alice"); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	customized := &triggersv1alpha1.SecurityRanker{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "alice", Name: "customized"}, customized); err != nil {
		t.Fatal(err)
	}
	customized.Spec.Rules = []string{"my customization"}
	if err := c.Update(ctx, customized); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unmodified", "customized"} {
		source := &triggersv1alpha1.SecurityRanker{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: "system", Name: name}, source); err != nil {
			t.Fatal(err)
		}
		source.Spec.Rules = []string{"version two"}
		if err := c.Update(ctx, source); err != nil {
			t.Fatal(err)
		}
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
		t.Fatalf("upgrade sync: %v", err)
	}
	for name, want := range map[string]string{
		"unmodified": "version two",
		"customized": "my customization",
	} {
		got := &triggersv1alpha1.SecurityRanker{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: "alice", Name: name}, got); err != nil {
			t.Fatal(err)
		}
		if rule := got.Spec.Rules[0]; rule != want {
			t.Errorf("%s rule = %q, want %q", name, rule, want)
		}
	}
}

func TestBootstrapUpgradeRefreshesExplicitlyReplacedPrunedSpec(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	old := &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: metav1.ObjectMeta{
			Name: "smart-contract-review", Namespace: "alice",
			Annotations: map[string]string{
				bootstrapDefaultAnnotation:  "true",
				bootstrapSourceAnnotation:   "system",
				bootstrapSpecHashAnnotation: "hash-recorded-before-a-field-was-pruned",
			},
		},
		Spec: triggersv1alpha1.SecurityWorkflowSpec{Tasks: []triggersv1alpha1.SecurityScanTask{{
			Name: "old-review", Objective: "old workflow",
		}}},
	}
	oldHash, err := bootstrapSpecHash(old)
	if err != nil {
		t.Fatal(err)
	}
	source := &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: bootstrapMeta("smart-contract-review"),
		Spec: triggersv1alpha1.SecurityWorkflowSpec{Tasks: []triggersv1alpha1.SecurityScanTask{{
			Name: "new-review", Objective: "new workflow",
		}}},
	}
	source.Annotations[bootstrapReplacesHashesAnnotation] = oldHash
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v2"), source, old,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alice"}},
	).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}

	if err := srv.syncBootstrapResources(context.Background(), "alice"); err != nil {
		t.Fatalf("upgrade sync: %v", err)
	}
	got := &triggersv1alpha1.SecurityWorkflow{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "alice", Name: source.Name}, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Tasks[0].Name != "new-review" {
		t.Fatalf("workflow task = %q, want new-review", got.Spec.Tasks[0].Name)
	}
	desiredHash, err := bootstrapSpecHash(source)
	if err != nil {
		t.Fatal(err)
	}
	if got.Annotations[bootstrapSpecHashAnnotation] != desiredHash {
		t.Fatalf("recorded hash = %q, want %q", got.Annotations[bootstrapSpecHashAnnotation], desiredHash)
	}
}

func TestBootstrapReplacementHashDoesNotOverwriteDifferentCustomization(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	oldDefault := &triggersv1alpha1.SecurityWorkflow{
		Spec: triggersv1alpha1.SecurityWorkflowSpec{Tasks: []triggersv1alpha1.SecurityScanTask{{
			Name: "old-review", Objective: "old workflow",
		}}},
	}
	oldHash, err := bootstrapSpecHash(oldDefault)
	if err != nil {
		t.Fatal(err)
	}
	source := &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: bootstrapMeta("smart-contract-review"),
		Spec: triggersv1alpha1.SecurityWorkflowSpec{Tasks: []triggersv1alpha1.SecurityScanTask{{
			Name: "new-review", Objective: "new workflow",
		}}},
	}
	source.Annotations[bootstrapReplacesHashesAnnotation] = oldHash
	customized := &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: metav1.ObjectMeta{
			Name: source.Name, Namespace: "alice",
			Annotations: map[string]string{
				bootstrapDefaultAnnotation:  "true",
				bootstrapSourceAnnotation:   "system",
				bootstrapSpecHashAnnotation: "hash-recorded-before-a-field-was-pruned",
			},
		},
		Spec: triggersv1alpha1.SecurityWorkflowSpec{Tasks: []triggersv1alpha1.SecurityScanTask{{
			Name: "my-review", Objective: "my customization",
		}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v2"), source, customized,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alice"}},
	).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}

	if err := srv.syncBootstrapResources(context.Background(), "alice"); err != nil {
		t.Fatalf("upgrade sync: %v", err)
	}
	got := &triggersv1alpha1.SecurityWorkflow{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "alice", Name: source.Name}, got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Tasks[0].Name != "my-review" {
		t.Fatalf("customized workflow was overwritten: %+v", got.Spec)
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
