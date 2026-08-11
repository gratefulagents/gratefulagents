package dashboard

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

type patchHookClient struct {
	client.Client
	beforePatch func(context.Context)
}

func (c *patchHookClient) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if c.beforePatch != nil {
		hook := c.beforePatch
		c.beforePatch = nil
		hook(ctx)
	}
	return c.Client.Patch(ctx, object, patch, opts...)
}

type securitySkillOwnerCall struct {
	resourceType string
	resourceID   string
	namespace    string
	ownerID      string
}

type securitySkillOwnerStore struct {
	store.StateStore
	calls []securitySkillOwnerCall
	err   error
}

func (s *securitySkillOwnerStore) SetResourceOwner(_ context.Context, resourceType, resourceID, namespace, ownerID string) error {
	if s.err != nil {
		return s.err
	}
	s.calls = append(s.calls, securitySkillOwnerCall{resourceType, resourceID, namespace, ownerID})
	return nil
}

func bundledSecuritySkill(name, instructions string) *platformv1alpha1.Skill {
	meta := bootstrapMeta(name)
	meta.Annotations[securitySkillBundleAnnotation] = "true"
	return &platformv1alpha1.Skill{
		ObjectMeta: meta,
		Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
			Inline: &platformv1alpha1.SkillInlineSource{Instructions: instructions},
		}},
	}
}

func TestSecuritySkillsRequireExplicitInstall(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	source := bundledSecuritySkill("security-scan", "scan carefully")
	unrelated := bundledSecuritySkill("grafana", "observe")
	delete(unrelated.Annotations, securitySkillBundleAnnotation)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v1"), source, unrelated,
	).Build()
	owners := &securitySkillOwnerStore{}
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme, stateStore: owners}
	ctx := credActorCtx("alice-id", "Alice")
	namespace := deriveUserNamespaceName("Alice", "alice-id")

	status, err := srv.GetSecuritySkillsStatus(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetSecuritySkillsStatus() error = %v", err)
	}
	if status.State != securitySkillsStateNotInstalled || status.AvailableCount != 1 || status.InstalledCount != 0 {
		t.Fatalf("status before install = %+v", status)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: source.Name}, &platformv1alpha1.Skill{}); err == nil {
		t.Fatal("status lookup installed a security Skill")
	}

	status, err = srv.InstallSecuritySkills(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("InstallSecuritySkills() error = %v", err)
	}
	if status.State != securitySkillsStateInstalled || status.InstalledCount != 1 {
		t.Fatalf("status after install = %+v", status)
	}
	if len(owners.calls) != 1 || owners.calls[0] != (securitySkillOwnerCall{skillResourceType, source.Name, namespace, "alice-id"}) {
		t.Fatalf("ownership calls = %+v", owners.calls)
	}
	installed := &platformv1alpha1.Skill{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: source.Name}, installed); err != nil {
		t.Fatalf("get installed Skill: %v", err)
	}
	if installed.Spec.Source.Inline == nil || installed.Spec.Source.Inline.Instructions != "scan carefully" {
		t.Fatalf("installed Skill spec = %+v", installed.Spec)
	}
	if installed.Annotations[bootstrapSourceAnnotation] != "system" || installed.Annotations[bootstrapSpecHashAnnotation] == "" {
		t.Fatalf("installed Skill provenance = %+v", installed.Annotations)
	}
	if installed.Annotations["helm.sh/hook"] != "" {
		t.Fatalf("Helm hook annotation leaked to installed Skill: %+v", installed.Annotations)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: unrelated.Name}, &platformv1alpha1.Skill{}); err != nil {
		t.Fatalf("unrelated bootstrap Skill should retain automatic seeding: %v", err)
	}

	status, err = srv.InstallSecuritySkills(ctx, &emptypb.Empty{})
	if err != nil || status.InstalledCount != 1 {
		t.Fatalf("repeated install = %+v, %v", status, err)
	}
	if len(owners.calls) != 1 {
		t.Fatalf("repeated install recorded ownership again: %+v", owners.calls)
	}
}

func TestInstallSecuritySkillsOwnershipFailureRollsBack(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	source := bundledSecuritySkill("security-scan", "scan carefully")
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v1"), source,
	).Build()
	srv := &Server{
		k8sClient: c,
		apiReader: c,
		scheme:    scheme,
		stateStore: &securitySkillOwnerStore{
			err: errors.New("ownership unavailable"),
		},
	}
	ctx := credActorCtx("alice-id", "Alice")
	namespace := deriveUserNamespaceName("Alice", "alice-id")

	_, err := srv.InstallSecuritySkills(ctx, &emptypb.Empty{})
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("InstallSecuritySkills() error = %v, want Internal", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: source.Name}, &platformv1alpha1.Skill{}); err == nil {
		t.Fatal("Skill remained after ownership recording failed")
	}
}

func TestInstallSecuritySkillsPreservesSameNameCustomSkill(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	namespace := deriveUserNamespaceName("Alice", "alice-id")
	custom := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: "security-scan", Namespace: namespace},
		Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
			Inline: &platformv1alpha1.SkillInlineSource{Instructions: "my version"},
		}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v1"),
		bundledSecuritySkill("security-scan", "official"),
		bundledSecuritySkill("web-app-hunting", "official web"),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		custom,
	).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}
	ctx := credActorCtx("alice-id", "Alice")

	status, err := srv.InstallSecuritySkills(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("InstallSecuritySkills() error = %v", err)
	}
	if status.State != securitySkillsStatePartiallyInstalled || status.InstalledCount != 1 || status.ConflictCount != 1 {
		t.Fatalf("status = %+v", status)
	}
	got := &platformv1alpha1.Skill{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(custom), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Source.Inline.Instructions != "my version" || got.Annotations[bootstrapSourceAnnotation] != "" {
		t.Fatalf("custom Skill was changed or adopted: %+v", got)
	}
}

func TestInstallSecuritySkillsRefreshesOnlyUntouchedSeededSkill(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	namespace := deriveUserNamespaceName("Alice", "alice-id")
	source := bundledSecuritySkill("security-scan", "version two")
	old := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: source.Name, Namespace: namespace, Annotations: map[string]string{
			bootstrapSourceAnnotation: "system",
		}},
		Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
			Inline: &platformv1alpha1.SkillInlineSource{Instructions: "version one"},
		}},
	}
	oldHash, err := bootstrapSpecHash(old)
	if err != nil {
		t.Fatal(err)
	}
	old.Annotations[bootstrapSpecHashAnnotation] = oldHash
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v2"), source,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, old,
	).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}
	ctx := credActorCtx("alice-id", "Alice")

	status, err := srv.InstallSecuritySkills(ctx, &emptypb.Empty{})
	if err != nil || status.State != securitySkillsStateInstalled {
		t.Fatalf("InstallSecuritySkills() = %+v, %v", status, err)
	}
	got := &platformv1alpha1.Skill{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(old), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Source.Inline.Instructions != "version two" {
		t.Fatalf("untouched seeded Skill was not refreshed: %+v", got.Spec)
	}

	got.Spec.Source.Inline.Instructions = "my modified version"
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	source.Spec.Source.Inline.Instructions = "version three"
	if err := c.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	status, err = srv.InstallSecuritySkills(ctx, &emptypb.Empty{})
	if err != nil || status.ConflictCount != 1 {
		t.Fatalf("install with modified Skill = %+v, %v", status, err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(old), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Source.Inline.Instructions != "my modified version" {
		t.Fatalf("modified seeded Skill was overwritten: %+v", got.Spec)
	}
}

func TestInstallSecuritySkillsPreservesConcurrentUserEdit(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	namespace := deriveUserNamespaceName("Alice", "alice-id")
	source := bundledSecuritySkill("security-scan", "version two")
	installed := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: source.Name, Namespace: namespace, Annotations: map[string]string{
			bootstrapSourceAnnotation: "system",
		}},
		Spec: platformv1alpha1.SkillSpec{Source: platformv1alpha1.SkillSource{
			Inline: &platformv1alpha1.SkillInlineSource{Instructions: "version one"},
		}},
	}
	installedHash, err := bootstrapSpecHash(installed)
	if err != nil {
		t.Fatal(err)
	}
	installed.Annotations[bootstrapSpecHashAnnotation] = installedHash
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v2"), source,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, installed,
	).Build()
	hooked := &patchHookClient{Client: base}
	hooked.beforePatch = func(ctx context.Context) {
		concurrent := &platformv1alpha1.Skill{}
		if err := base.Get(ctx, client.ObjectKeyFromObject(installed), concurrent); err != nil {
			t.Fatal(err)
		}
		concurrent.Spec.Source.Inline.Instructions = "concurrent user edit"
		if err := base.Update(ctx, concurrent); err != nil {
			t.Fatal(err)
		}
	}
	srv := &Server{k8sClient: hooked, apiReader: base, scheme: scheme}
	ctx := credActorCtx("alice-id", "Alice")

	status, err := srv.InstallSecuritySkills(ctx, &emptypb.Empty{})
	if err != nil || status.ConflictCount != 1 {
		t.Fatalf("InstallSecuritySkills() = %+v, %v", status, err)
	}
	got := &platformv1alpha1.Skill{}
	if err := base.Get(ctx, client.ObjectKeyFromObject(installed), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Source.Inline.Instructions != "concurrent user edit" {
		t.Fatalf("concurrent user edit was overwritten: %+v", got.Spec)
	}
}

func TestInstallSecuritySkillsAdoptsOnlyMatchingHashlessLegacyCopy(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	namespace := deriveUserNamespaceName("Alice", "alice-id")
	source := bundledSecuritySkill("security-scan", "official")
	matching := &platformv1alpha1.Skill{
		ObjectMeta: metav1.ObjectMeta{Name: source.Name, Namespace: namespace, Annotations: map[string]string{
			bootstrapSourceAnnotation: "system",
		}},
		Spec: source.DeepCopy().Spec,
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		bootstrapReadyMarker("v1"), source,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}, matching,
	).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}
	ctx := credActorCtx("alice-id", "Alice")

	status, err := srv.GetSecuritySkillsStatus(ctx, &emptypb.Empty{})
	if err != nil || status.State != securitySkillsStateNotInstalled {
		t.Fatalf("matching hashless legacy status = %+v, %v", status, err)
	}
	if _, err := srv.InstallSecuritySkills(ctx, &emptypb.Empty{}); err != nil {
		t.Fatalf("InstallSecuritySkills() error = %v", err)
	}
	got := &platformv1alpha1.Skill{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(matching), got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[bootstrapSpecHashAnnotation] == "" {
		t.Fatal("matching hashless legacy Skill was not adopted")
	}
	source.Spec.Source.Inline.Instructions = "official version two"
	if err := c.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.InstallSecuritySkills(ctx, &emptypb.Empty{}); err != nil {
		t.Fatalf("refresh adopted legacy Skill: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(matching), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Source.Inline.Instructions != "official version two" {
		t.Fatalf("adopted legacy Skill did not refresh: %+v", got.Spec)
	}

	got.Annotations[bootstrapSpecHashAnnotation] = ""
	got.Spec.Source.Inline.Instructions = "my legacy customization"
	if err := c.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	status, err = srv.InstallSecuritySkills(ctx, &emptypb.Empty{})
	if err != nil || status.ConflictCount != 1 {
		t.Fatalf("modified hashless legacy install = %+v, %v", status, err)
	}
	if err := c.Get(ctx, client.ObjectKeyFromObject(matching), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Source.Inline.Instructions != "my legacy customization" || got.Annotations[bootstrapSpecHashAnnotation] != "" {
		t.Fatalf("modified hashless legacy Skill was changed: %+v", got)
	}
}

func TestSecuritySkillsUnavailableWithoutReadyBundle(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}
	ctx := credActorCtx("alice-id", "Alice")

	status, err := srv.GetSecuritySkillsStatus(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetSecuritySkillsStatus() error = %v", err)
	}
	if status.State != securitySkillsStateUnavailable {
		t.Fatalf("status = %+v", status)
	}
}
