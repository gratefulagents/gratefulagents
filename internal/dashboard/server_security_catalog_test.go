package dashboard

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

type failingCatalogCreateClient struct {
	client.Client
	name string
}

func (c failingCatalogCreateClient) Create(ctx context.Context, object client.Object, options ...client.CreateOption) error {
	if object.GetName() == c.name {
		return errors.New("injected catalog create failure")
	}
	return c.Client.Create(ctx, object, options...)
}

func readySecurityLibraryStatus(generation int64) triggersv1alpha1.SecurityLibraryResourceStatus {
	return triggersv1alpha1.SecurityLibraryResourceStatus{
		ObservedGeneration: generation,
		Conditions: []metav1.Condition{{
			Type:               triggersv1alpha1.ConditionSecurityLibraryReady,
			Status:             metav1.ConditionTrue,
			ObservedGeneration: generation,
			Reason:             "Validated",
		}},
	}
}

func readySecurityProgramStatus(generation int64) triggersv1alpha1.SecurityProgramStatus {
	status := readySecurityLibraryStatus(generation)
	return triggersv1alpha1.SecurityProgramStatus{
		ObservedGeneration: status.ObservedGeneration,
		Conditions:         status.Conditions,
	}
}

func readyCatalogSkill(name string) *platformv1alpha1.Skill {
	skill := bundledSecuritySkill(name, "Use "+name)
	skill.Generation = 1
	skill.Spec.Description = name + " guidance"
	skill.Status = platformv1alpha1.SkillStatus{
		Phase:              "Ready",
		ObservedGeneration: 1,
		Resolved:           &platformv1alpha1.SkillResolved{Name: name, Description: skill.Spec.Description, Instructions: "Use " + name},
	}
	return skill
}

func readyCatalogObjects() []client.Object {
	workflow := &triggersv1alpha1.SecurityWorkflow{
		ObjectMeta: bootstrapMeta("review-workflow"),
		Spec: triggersv1alpha1.SecurityWorkflowSpec{
			Description: "Review workflow",
			Tasks: []triggersv1alpha1.SecurityScanTask{{
				Name: "review", Objective: "Review", SkillRefs: []platformv1alpha1.NamedRef{{Name: "review-skill"}},
			}},
		},
	}
	workflow.Generation = 1
	workflow.Status = readySecurityLibraryStatus(1)
	ranker := &triggersv1alpha1.SecurityRanker{
		ObjectMeta: bootstrapMeta("immunefi-v2-3"),
		Spec:       triggersv1alpha1.SecurityRankerSpec{Description: "Immunefi severity", Rules: []string{"critical first"}},
	}
	ranker.Generation = 1
	ranker.Status = readySecurityLibraryStatus(1)
	script := &triggersv1alpha1.SecurityPostScript{
		ObjectMeta: bootstrapMeta("verify-script"),
		Spec:       triggersv1alpha1.SecurityPostScriptSpec{Description: "Verify findings", Prompt: "Verify"},
	}
	script.Generation = 1
	script.Status = readySecurityLibraryStatus(1)
	pack := &triggersv1alpha1.SecurityPolicyPack{
		ObjectMeta: bootstrapMeta("bounty-policy"),
		Spec: triggersv1alpha1.SecurityPolicyPackSpec{
			Description:           "Bounty policy",
			DefaultRankerRefs:     []triggersv1alpha1.SecurityResourceRef{{Name: ranker.Name}},
			DefaultPostScriptRefs: []triggersv1alpha1.SecurityResourceRef{{Name: script.Name}},
		},
	}
	pack.Generation = 1
	pack.Status = readySecurityLibraryStatus(1)
	program := &triggersv1alpha1.SecurityProgram{
		ObjectMeta: bootstrapMeta("dynamic-program"),
		Spec: triggersv1alpha1.SecurityProgramSpec{
			DisplayName: "Dynamic Program", Provider: "Example", SeveritySystem: "immunefi-v2.3",
			ScanTargets: []triggersv1alpha1.SecurityProgramScanTarget{{
				WorkflowRef: workflow.Name, PolicyPackRef: pack.Name, ScanName: "dynamic-scan", DisplayName: "Dynamic scan",
			}},
		},
	}
	program.Generation = 1
	program.Status = readySecurityProgramStatus(1)
	return []client.Object{readyCatalogSkill("review-skill"), workflow, ranker, script, pack, program}
}

func newSecurityCatalogServer(t *testing.T, extra ...client.Object) (*Server, client.Client, context.Context) {
	t.Helper()
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	objects := []client.Object{bootstrapReadyMarker("catalog-v1")}
	objects = append(objects, readyCatalogObjects()...)
	objects = append(objects, extra...)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	return &Server{k8sClient: c, apiReader: c, scheme: scheme}, c, credActorCtx("alice-id", "Alice")
}

func catalogEntry(catalog *platform.SecurityCatalog, kind platform.SecurityCatalogKind, name string) *platform.SecurityCatalogEntry {
	for _, entry := range catalog.GetEntries() {
		if entry.GetResource().GetKind() == kind && entry.GetResource().GetName() == name {
			return entry
		}
	}
	return nil
}

func applyReviewedSecurityCatalogInstall(t *testing.T, srv *Server, ctx context.Context, request *platform.SecurityCatalogInstallRequest) (*platform.SecurityCatalogInstallResponse, error) {
	t.Helper()
	dryRun, err := srv.DryRunSecurityCatalogInstall(ctx, request)
	if err != nil {
		return nil, err
	}
	request.PlanRevision = dryRun.GetPlanRevision()
	return srv.ApplySecurityCatalogInstall(ctx, request)
}

func TestListSecurityCatalogDiscoversMarkedResourcesDynamically(t *testing.T) {
	unmarked := &triggersv1alpha1.SecurityRanker{
		ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "system"},
		Spec:       triggersv1alpha1.SecurityRankerSpec{Rules: []string{"private"}},
	}
	nonSecuritySkill := readyCatalogSkill("grafana")
	delete(nonSecuritySkill.Annotations, securitySkillBundleAnnotation)
	srv, c, ctx := newSecurityCatalogServer(t, unmarked, nonSecuritySkill)

	catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ListSecurityCatalog() error = %v", err)
	}
	if !catalog.GetReady() || catalog.GetRevision() == "" || len(catalog.GetEntries()) != 6 {
		t.Fatalf("catalog = %+v", catalog)
	}
	for _, ref := range []*platform.SecurityCatalogRef{
		{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, Name: "review-skill"},
		{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_WORKFLOW, Name: "review-workflow"},
		{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, Name: "immunefi-v2-3"},
		{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POST_SCRIPT, Name: "verify-script"},
		{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POLICY_PACK, Name: "bounty-policy"},
		{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_PROGRAM, Name: "dynamic-program"},
	} {
		entry := catalogEntry(catalog, ref.Kind, ref.Name)
		if entry == nil || entry.GetTitle() == "" || entry.GetDescription() == "" || !entry.GetReady() || entry.GetInstallState() != platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_NOT_INSTALLED {
			t.Errorf("entry %s/%s = %+v", ref.Kind, ref.Name, entry)
		}
	}
	if catalogEntry(catalog, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, "private") != nil || catalogEntry(catalog, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, "grafana") != nil {
		t.Fatal("catalog included an unmarked resource")
	}

	added := &triggersv1alpha1.SecurityRanker{
		ObjectMeta: bootstrapMeta("new-ranker"),
		Spec:       triggersv1alpha1.SecurityRankerSpec{Description: "New ranker", Rules: []string{"new"}},
	}
	added.Generation = 1
	added.Status = readySecurityLibraryStatus(1)
	if err := c.Create(ctx, added); err != nil {
		t.Fatal(err)
	}
	updated, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GetRevision() == catalog.GetRevision() || catalogEntry(updated, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, "new-ranker") == nil {
		t.Fatalf("dynamic addition did not change catalog: old=%s new=%s", catalog.GetRevision(), updated.GetRevision())
	}
	if err := c.Delete(ctx, added); err != nil {
		t.Fatal(err)
	}
	removed, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if catalogEntry(removed, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, "new-ranker") != nil {
		t.Fatal("dynamic removal remained in catalog")
	}
}

func TestSecurityCatalogProgramDependencyClosureAndApplyOrder(t *testing.T) {
	srv, c, ctx := newSecurityCatalogServer(t)
	catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	request := &platform.SecurityCatalogInstallRequest{
		CatalogRevision: catalog.GetRevision(),
		Resources: []*platform.SecurityCatalogRef{{
			Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_PROGRAM, Name: "dynamic-program",
		}},
	}
	dryRun, err := srv.DryRunSecurityCatalogInstall(ctx, request)
	if err != nil {
		t.Fatalf("DryRunSecurityCatalogInstall() error = %v", err)
	}
	want := []securityCatalogKey{
		{platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, "review-skill"},
		{platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_WORKFLOW, "review-workflow"},
		{platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, "immunefi-v2-3"},
		{platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POST_SCRIPT, "verify-script"},
		{platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POLICY_PACK, "bounty-policy"},
		{platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_PROGRAM, "dynamic-program"},
	}
	if dryRun.GetApplied() || len(dryRun.GetResults()) != len(want) {
		t.Fatalf("dry run = %+v", dryRun)
	}
	for i, result := range dryRun.GetResults() {
		got := securityCatalogKey{result.GetEntry().GetResource().GetKind(), result.GetEntry().GetResource().GetName()}
		if got != want[i] || result.GetAction() != "create" {
			t.Errorf("dry run result %d = %v %q, want %v create", i, got, result.GetAction(), want[i])
		}
	}
	applied, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, request)
	if err != nil {
		t.Fatalf("ApplySecurityCatalogInstall() error = %v", err)
	}
	if !applied.GetApplied() {
		t.Fatal("apply response was not marked applied")
	}
	personalNamespace := deriveUserNamespaceName("Alice", "alice-id")
	checks := []client.Object{
		&platformv1alpha1.Skill{}, &triggersv1alpha1.SecurityWorkflow{}, &triggersv1alpha1.SecurityRanker{},
		&triggersv1alpha1.SecurityPostScript{}, &triggersv1alpha1.SecurityPolicyPack{}, &triggersv1alpha1.SecurityProgram{},
	}
	for i, object := range checks {
		if err := c.Get(ctx, client.ObjectKey{Namespace: personalNamespace, Name: want[i].name}, object); err != nil {
			t.Errorf("dependency %v was not installed in caller namespace: %v", want[i], err)
		}
		if err := c.Get(ctx, client.ObjectKey{Namespace: "other-user", Name: want[i].name}, object.DeepCopyObject().(client.Object)); err == nil {
			t.Errorf("dependency %v was installed in an unrelated namespace", want[i])
		}
	}

	catalog, err = srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	request.CatalogRevision = catalog.GetRevision()
	repeated, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range repeated.GetResults() {
		if result.GetAction() != "unchanged" {
			t.Errorf("idempotent apply action = %q, want unchanged", result.GetAction())
		}
	}
	if repeated.GetApplied() {
		t.Fatal("idempotent apply was marked applied without a mutation")
	}
}

func TestSecurityCatalogApplyReportsPartialFailuresAndContinuesIndependentBranches(t *testing.T) {
	srv, c, ctx := newSecurityCatalogServer(t)
	srv.k8sClient = failingCatalogCreateClient{Client: c, name: "review-skill"}
	catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, &platform.SecurityCatalogInstallRequest{
		CatalogRevision: catalog.GetRevision(),
		Resources: []*platform.SecurityCatalogRef{
			{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_WORKFLOW, Name: "review-workflow"},
			{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, Name: "immunefi-v2-3"},
		},
	})
	if err != nil {
		t.Fatalf("ApplySecurityCatalogInstall() returned a partial-failure RPC error: %v", err)
	}
	if len(response.GetResults()) != 3 || response.GetResults()[0].GetAction() != "failed" || response.GetResults()[1].GetAction() != "blocked" || response.GetResults()[2].GetAction() != "created" {
		t.Fatalf("partial failure results = %+v", response.GetResults())
	}
	if !response.GetApplied() {
		t.Fatal("successful independent mutation was not reflected in applied")
	}
	namespace := deriveUserNamespaceName("Alice", "alice-id")
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "immunefi-v2-3"}, &triggersv1alpha1.SecurityRanker{}); err != nil {
		t.Fatalf("independent ranker was not created: %v", err)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "review-workflow"}, &triggersv1alpha1.SecurityWorkflow{}); err == nil {
		t.Fatal("workflow was created after its required Skill failed")
	}
}

func TestSecurityCatalogApplyAdoptsLegacyCopyAndClaimsExistingResources(t *testing.T) {
	namespace := deriveUserNamespaceName("Alice", "alice-id")
	skillSource := readyCatalogSkill("review-skill")
	legacySkill := catalogDestinationObject(skillSource, namespace).(*platformv1alpha1.Skill)
	legacySkill.Annotations[bootstrapSpecHashAnnotation] = "stale-hash"

	var programSource *triggersv1alpha1.SecurityProgram
	for _, object := range readyCatalogObjects() {
		if program, ok := object.(*triggersv1alpha1.SecurityProgram); ok {
			programSource = program
			break
		}
	}
	existingProgram := catalogDestinationObject(programSource, namespace).(*triggersv1alpha1.SecurityProgram)
	programHash, err := bootstrapSpecHash(existingProgram)
	if err != nil {
		t.Fatal(err)
	}
	existingProgram.Annotations[bootstrapSpecHashAnnotation] = programHash

	srv, c, ctx := newSecurityCatalogServer(t, legacySkill, existingProgram)
	owners := newMockStateStore()
	srv.stateStore = owners
	catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, &platform.SecurityCatalogInstallRequest{
		CatalogRevision: catalog.GetRevision(),
		Resources: []*platform.SecurityCatalogRef{
			{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, Name: legacySkill.Name},
			{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_PROGRAM, Name: existingProgram.Name},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.GetApplied() {
		t.Fatal("adoption and ownership mutations were not reflected in applied")
	}
	if response.GetResults()[0].GetAction() != "adopted" {
		t.Fatalf("legacy Skill action = %q, want adopted", response.GetResults()[0].GetAction())
	}
	gotSkill := &platformv1alpha1.Skill{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(legacySkill), gotSkill); err != nil {
		t.Fatal(err)
	}
	if gotSkill.Annotations[bootstrapSpecHashAnnotation] == "" || gotSkill.Annotations[bootstrapSpecHashAnnotation] == "stale-hash" {
		t.Fatalf("legacy Skill hash was not adopted: %q", gotSkill.Annotations[bootstrapSpecHashAnnotation])
	}
	for resourceType, name := range map[string]string{skillResourceType: legacySkill.Name, securityProgramResourceType: existingProgram.Name} {
		owner, err := owners.GetResourceOwner(ctx, resourceType, name, namespace)
		if err != nil || owner == nil || owner.OwnerID != "alice-id" {
			t.Errorf("owner for %s %s = %+v, %v", resourceType, name, owner, err)
		}
	}

	source := &platformv1alpha1.Skill{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "system", Name: legacySkill.Name}, source); err != nil {
		t.Fatal(err)
	}
	source.Spec.Description = "updated after adoption"
	source.Generation++
	source.Status = platformv1alpha1.SkillStatus{Phase: "Ready", ObservedGeneration: source.Generation, Resolved: &platformv1alpha1.SkillResolved{Name: source.Name, Description: source.Spec.Description, Instructions: "updated"}}
	if err := c.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	catalog, err = srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, &platform.SecurityCatalogInstallRequest{CatalogRevision: catalog.GetRevision(), Resources: []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, Name: source.Name}}})
	if err != nil || refreshed.GetResults()[0].GetAction() != "refreshed" {
		t.Fatalf("refresh adopted Skill = %+v, %v", refreshed, err)
	}
}

func TestSecurityCatalogApplyDoesNotOverwriteAnotherOwner(t *testing.T) {
	namespace := deriveUserNamespaceName("Alice", "alice-id")
	source := readyCatalogSkill("review-skill")
	existing := catalogDestinationObject(source, namespace).(*platformv1alpha1.Skill)
	hash, err := bootstrapSpecHash(existing)
	if err != nil {
		t.Fatal(err)
	}
	existing.Annotations[bootstrapSpecHashAnnotation] = hash
	srv, _, ctx := newSecurityCatalogServer(t, existing)
	owners := newMockStateStore()
	if err := owners.SetResourceOwner(ctx, skillResourceType, existing.Name, namespace, "bob-id"); err != nil {
		t.Fatal(err)
	}
	srv.stateStore = owners
	catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, &platform.SecurityCatalogInstallRequest{CatalogRevision: catalog.GetRevision(), Resources: []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, Name: existing.Name}}})
	if err != nil {
		t.Fatalf("ownership conflict returned RPC error: %v", err)
	}
	if response.GetApplied() || response.GetResults()[0].GetAction() != "failed" {
		t.Fatalf("ownership conflict response = %+v", response)
	}
	owner, err := owners.GetResourceOwner(ctx, skillResourceType, existing.Name, namespace)
	if err != nil || owner == nil || owner.OwnerID != "bob-id" {
		t.Fatalf("existing owner was overwritten: %+v, %v", owner, err)
	}
}

func TestSecurityCatalogInstallStatesRefreshAndProtectConflicts(t *testing.T) {
	conflict := &triggersv1alpha1.SecurityPostScript{
		ObjectMeta: metav1.ObjectMeta{Name: "verify-script", Namespace: deriveUserNamespaceName("Alice", "alice-id")},
		Spec:       triggersv1alpha1.SecurityPostScriptSpec{Description: "Verify findings", Prompt: "Verify"},
	}
	srv, c, ctx := newSecurityCatalogServer(t, conflict)
	catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	entry := catalogEntry(catalog, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POST_SCRIPT, "verify-script")
	if entry.GetInstallState() != platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_CONFLICT {
		t.Fatalf("same-name unrelated state = %s", entry.GetInstallState())
	}
	response, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, &platform.SecurityCatalogInstallRequest{
		CatalogRevision: catalog.GetRevision(),
		Resources:       []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_POST_SCRIPT, Name: "verify-script"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResults()[0].GetAction() != "blocked" {
		t.Fatalf("conflict apply = %+v", response.GetResults()[0])
	}
	preserved := &triggersv1alpha1.SecurityPostScript{}
	if err := c.Get(ctx, client.ObjectKeyFromObject(conflict), preserved); err != nil || preserved.Annotations[bootstrapSourceAnnotation] != "" {
		t.Fatalf("conflicting object changed: %+v, %v", preserved.Spec, err)
	}

	catalog, err = srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, &platform.SecurityCatalogInstallRequest{
		CatalogRevision: catalog.GetRevision(),
		Resources:       []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, Name: "immunefi-v2-3"}},
	}); err != nil {
		t.Fatal(err)
	}
	source := &triggersv1alpha1.SecurityRanker{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "system", Name: "immunefi-v2-3"}, source); err != nil {
		t.Fatal(err)
	}
	source.Spec.Rules = []string{"updated rules"}
	source.Generation++
	source.Status = readySecurityLibraryStatus(source.Generation)
	if err := c.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	catalog, err = srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	entry = catalogEntry(catalog, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, source.Name)
	if entry.GetInstallState() != platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_UPDATE_AVAILABLE {
		t.Fatalf("untouched older catalog state = %s", entry.GetInstallState())
	}
	if _, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, &platform.SecurityCatalogInstallRequest{
		CatalogRevision: catalog.GetRevision(),
		Resources:       []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, Name: source.Name}},
	}); err != nil {
		t.Fatal(err)
	}
	installed := &triggersv1alpha1.SecurityRanker{}
	key := client.ObjectKey{Namespace: deriveUserNamespaceName("Alice", "alice-id"), Name: source.Name}
	if err := c.Get(ctx, key, installed); err != nil || installed.Spec.Rules[0] != "updated rules" {
		t.Fatalf("catalog refresh = %+v, %v", installed.Spec, err)
	}
	installed.Spec.Rules = []string{"my rules"}
	if err := c.Update(ctx, installed); err != nil {
		t.Fatal(err)
	}
	source.Spec.Rules = []string{"third version"}
	source.Generation++
	source.Status = readySecurityLibraryStatus(source.Generation)
	if err := c.Update(ctx, source); err != nil {
		t.Fatal(err)
	}
	catalog, err = srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	entry = catalogEntry(catalog, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, source.Name)
	if entry.GetInstallState() != platform.SecurityCatalogInstallState_SECURITY_CATALOG_INSTALL_STATE_MODIFIED {
		t.Fatalf("modified catalog state = %s", entry.GetInstallState())
	}
}

func TestSecurityCatalogApplyRejectsDestinationAndOwnershipRaces(t *testing.T) {
	namespace := deriveUserNamespaceName("Alice", "alice-id")
	for _, test := range []struct {
		name   string
		mutate func(context.Context, client.Client, *triggersv1alpha1.SecurityRanker) error
	}{
		{
			name: "deletion",
			mutate: func(ctx context.Context, c client.Client, current *triggersv1alpha1.SecurityRanker) error {
				return c.Delete(ctx, current)
			},
		},
		{
			name: "modification",
			mutate: func(ctx context.Context, c client.Client, current *triggersv1alpha1.SecurityRanker) error {
				current.Spec.Rules = []string{"changed after review"}
				return c.Update(ctx, current)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var source *triggersv1alpha1.SecurityRanker
			for _, object := range readyCatalogObjects() {
				if ranker, ok := object.(*triggersv1alpha1.SecurityRanker); ok {
					source = ranker
					break
				}
			}
			existing := catalogDestinationObject(source, namespace).(*triggersv1alpha1.SecurityRanker)
			hash, err := bootstrapSpecHash(existing)
			if err != nil {
				t.Fatal(err)
			}
			existing.Annotations[bootstrapSpecHashAnnotation] = hash
			srv, c, ctx := newSecurityCatalogServer(t, existing)
			catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
			if err != nil {
				t.Fatal(err)
			}
			request := &platform.SecurityCatalogInstallRequest{
				CatalogRevision: catalog.GetRevision(),
				Resources:       []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_RANKER, Name: existing.Name}},
			}
			dryRun, err := srv.DryRunSecurityCatalogInstall(ctx, request)
			if err != nil || dryRun.GetPlanRevision() == "" {
				t.Fatalf("dry run = %+v, %v", dryRun, err)
			}
			if err := test.mutate(ctx, c, existing); err != nil {
				t.Fatal(err)
			}
			request.PlanRevision = dryRun.GetPlanRevision()
			if _, err := srv.ApplySecurityCatalogInstall(ctx, request); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("apply after %s error = %v, want FailedPrecondition", test.name, err)
			}
		})
	}

	source := readyCatalogSkill("review-skill")
	existing := catalogDestinationObject(source, namespace).(*platformv1alpha1.Skill)
	hash, err := bootstrapSpecHash(existing)
	if err != nil {
		t.Fatal(err)
	}
	existing.Annotations[bootstrapSpecHashAnnotation] = hash
	srv, _, ctx := newSecurityCatalogServer(t, existing)
	owners := newMockStateStore()
	srv.stateStore = owners
	catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	request := &platform.SecurityCatalogInstallRequest{
		CatalogRevision: catalog.GetRevision(),
		Resources:       []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, Name: existing.Name}},
	}
	dryRun, err := srv.DryRunSecurityCatalogInstall(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := owners.SetResourceOwner(ctx, skillResourceType, existing.Name, namespace, "bob-id"); err != nil {
		t.Fatal(err)
	}
	request.PlanRevision = dryRun.GetPlanRevision()
	if _, err := srv.ApplySecurityCatalogInstall(ctx, request); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("apply after ownership change error = %v, want FailedPrecondition", err)
	}

	request.PlanRevision = ""
	if _, err := srv.ApplySecurityCatalogInstall(ctx, request); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("apply without reviewed plan error = %v, want FailedPrecondition", err)
	}
}

func TestSecurityCatalogRollsBackExistingMutationWhenOwnershipClaimFails(t *testing.T) {
	namespace := deriveUserNamespaceName("Alice", "alice-id")
	for _, test := range []struct {
		name    string
		prepare func(*platformv1alpha1.Skill)
	}{
		{
			name: "adopted",
			prepare: func(existing *platformv1alpha1.Skill) {
				existing.Annotations[bootstrapSpecHashAnnotation] = "stale-hash"
			},
		},
		{
			name: "refreshed",
			prepare: func(existing *platformv1alpha1.Skill) {
				existing.Spec.Description = "older catalog description"
				hash, err := bootstrapSpecHash(existing)
				if err != nil {
					t.Fatal(err)
				}
				existing.Annotations[bootstrapSpecHashAnnotation] = hash
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := readyCatalogSkill("review-skill")
			existing := catalogDestinationObject(source, namespace).(*platformv1alpha1.Skill)
			test.prepare(existing)
			beforeHash, err := bootstrapSpecHash(existing)
			if err != nil {
				t.Fatal(err)
			}
			beforeRecordedHash := existing.Annotations[bootstrapSpecHashAnnotation]
			srv, c, ctx := newSecurityCatalogServer(t, existing)
			owners := newMockStateStore()
			owners.setResourceOwnerErr = errors.New("injected ownership persistence failure")
			srv.stateStore = owners
			catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
			if err != nil {
				t.Fatal(err)
			}
			request := &platform.SecurityCatalogInstallRequest{
				CatalogRevision: catalog.GetRevision(),
				Resources:       []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, Name: existing.Name}},
			}
			dryRun, err := srv.DryRunSecurityCatalogInstall(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			request.PlanRevision = dryRun.GetPlanRevision()
			response, err := srv.ApplySecurityCatalogInstall(ctx, request)
			if err != nil {
				t.Fatalf("partial apply returned RPC error: %v", err)
			}
			if response.GetApplied() || response.GetResults()[0].GetAction() != "failed" {
				t.Fatalf("ownership failure response = %+v", response)
			}
			got := &platformv1alpha1.Skill{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(existing), got); err != nil {
				t.Fatal(err)
			}
			gotHash, err := bootstrapSpecHash(got)
			if err != nil {
				t.Fatal(err)
			}
			if gotHash != beforeHash || got.Annotations[bootstrapSpecHashAnnotation] != beforeRecordedHash {
				t.Fatalf("existing resource was not restored: spec hash %q, recorded hash %q; want %q, %q", gotHash, got.Annotations[bootstrapSpecHashAnnotation], beforeHash, beforeRecordedHash)
			}
		})
	}
}

func TestSecurityCatalogRejectsStaleRevisionAndBlocksMissingDependencies(t *testing.T) {
	srv, c, ctx := newSecurityCatalogServer(t)
	catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, &platform.SecurityCatalogInstallRequest{CatalogRevision: catalog.GetRevision()}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty resource selection error = %v, want InvalidArgument", err)
	}
	added := &triggersv1alpha1.SecurityRanker{ObjectMeta: bootstrapMeta("later"), Spec: triggersv1alpha1.SecurityRankerSpec{Description: "Later", Rules: []string{"later"}}}
	added.Generation = 1
	added.Status = readySecurityLibraryStatus(1)
	if err := c.Create(ctx, added); err != nil {
		t.Fatal(err)
	}
	_, err = srv.DryRunSecurityCatalogInstall(ctx, &platform.SecurityCatalogInstallRequest{
		CatalogRevision: catalog.GetRevision(),
		Resources:       []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_PROGRAM, Name: "dynamic-program"}},
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("stale revision error = %v, want FailedPrecondition", err)
	}

	workflow := &triggersv1alpha1.SecurityWorkflow{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "system", Name: "review-workflow"}, workflow); err != nil {
		t.Fatal(err)
	}
	workflow.Spec.Tasks[0].SkillRefs = []platformv1alpha1.NamedRef{{Name: "missing-skill"}}
	workflow.Generation++
	workflow.Status = readySecurityLibraryStatus(workflow.Generation)
	if err := c.Update(ctx, workflow); err != nil {
		t.Fatal(err)
	}
	catalog, err = srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	entry := catalogEntry(catalog, platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_WORKFLOW, workflow.Name)
	if entry.GetReady() || len(entry.GetDependencies()) != 1 || !entry.GetDependencies()[0].GetRequired() {
		t.Fatalf("workflow missing dependency metadata = %+v", entry)
	}
	response, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, &platform.SecurityCatalogInstallRequest{
		CatalogRevision: catalog.GetRevision(),
		Resources:       []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_WORKFLOW, Name: workflow.Name}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := response.GetResults()[0].GetAction(); got != "blocked" {
		t.Fatalf("missing dependency action = %q, want blocked", got)
	}
}

func TestSecurityCatalogReadinessAndAuthentication(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "system")
	scheme := testProjectScheme(t)
	objects := readyCatalogObjects()
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	srv := &Server{k8sClient: c, apiReader: c, scheme: scheme}
	ctx := credActorCtx("alice-id", "Alice")
	catalog, err := srv.ListSecurityCatalog(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.GetReady() {
		t.Fatal("catalog was ready without the bootstrap readiness marker")
	}
	response, err := applyReviewedSecurityCatalogInstall(t, srv, ctx, &platform.SecurityCatalogInstallRequest{
		CatalogRevision: catalog.GetRevision(),
		Resources:       []*platform.SecurityCatalogRef{{Kind: platform.SecurityCatalogKind_SECURITY_CATALOG_KIND_SKILL, Name: "review-skill"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResults()[0].GetAction() != "blocked" {
		t.Fatalf("unready catalog apply = %+v", response)
	}
	if err := c.Get(ctx, client.ObjectKey{Namespace: deriveUserNamespaceName("Alice", "alice-id"), Name: "review-skill"}, &platformv1alpha1.Skill{}); err == nil {
		t.Fatal("unready catalog installed a resource")
	}
	if _, err := srv.ListSecurityCatalog(context.Background(), &emptypb.Empty{}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated catalog error = %v, want Unauthenticated", err)
	}
}
