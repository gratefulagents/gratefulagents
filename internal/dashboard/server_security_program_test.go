package dashboard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func testSecurityProgramResource(namespace string) *platform.SecurityProgramResource {
	return &platform.SecurityProgramResource{
		Namespace:   namespace,
		Name:        "acme-bounty",
		Provider:    "HackerOne",
		DisplayName: "Acme Bug Bounty",
		ProgramUrl:  "https://hackerone.com/acme",
		ScopePolicy: "Only acme/widget production code is in scope.",
		VerifiedAt:  timestamppb.New(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)),
		ScanTargets: []*platform.SecurityProgramScanTarget{
			{
				RepositoryUrl:   "https://github.com/acme/widget",
				BaseBranch:      "main",
				WorkflowRef:     "blockchain-protocol-audit",
				PolicyPackRef:   "bug-bounty",
				ScanName:        "acme-widget",
				DisplayName:     "Acme Widget",
				Priority:        3,
				ParameterValues: map[string]string{"project_root": "contracts"},
				Featured:        true,
			},
			{
				TargetUrl:     "https://app.acme.example",
				WorkflowRef:   "web-app-full-assessment",
				PolicyPackRef: "bug-bounty",
				ScanName:      "acme-web",
				DisplayName:   "Acme Web",
				Priority:      4,
			},
		},
	}
}

func TestSecurityProgramCRUDAndReferenceGuard(t *testing.T) {
	srv, c := newCronTestServer(t)
	ms := newMockStateStore()
	srv.stateStore = ms
	ctx := projectActorCtx()
	ns := testUserNS()

	created, err := srv.CreateSecurityProgram(ctx, &platform.CreateSecurityProgramRequest{Program: testSecurityProgramResource("")})
	if err != nil {
		t.Fatalf("CreateSecurityProgram() error = %v", err)
	}
	if created.Namespace != ns || created.Name != "acme-bounty" || created.Provider != "HackerOne" {
		t.Fatalf("created = %+v", created)
	}
	if len(created.ScanTargets) != 2 || created.ScanTargets[0].RepositoryUrl != "https://github.com/acme/widget" || created.ScanTargets[0].BaseBranch != "main" ||
		created.ScanTargets[0].WorkflowRef != "blockchain-protocol-audit" || created.ScanTargets[0].PolicyPackRef != "bug-bounty" ||
		created.ScanTargets[0].ScanName != "acme-widget" || created.ScanTargets[0].DisplayName != "Acme Widget" ||
		created.ScanTargets[0].Priority != 3 || !created.ScanTargets[0].Featured ||
		created.ScanTargets[1].TargetUrl != "https://app.acme.example" || created.ScanTargets[1].RepositoryUrl != "" || created.ScanTargets[1].BaseBranch != "" {
		t.Fatalf("created scan targets = %+v", created.ScanTargets)
	}
	owner, err := ms.GetResourceOwner(context.Background(), securityProgramResourceType, created.Name, created.Namespace)
	if err != nil || owner == nil || owner.OwnerID != testProjectSubject {
		t.Fatalf("SecurityProgram owner = %+v, %v", owner, err)
	}
	cr := &triggersv1alpha1.SecurityProgram{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-bounty"}, cr); err != nil {
		t.Fatalf("Get(SecurityProgram) error = %v", err)
	}
	if cr.Spec.ProgramURL != "https://hackerone.com/acme" || cr.Spec.VerifiedAt.IsZero() {
		t.Fatalf("spec = %+v", cr.Spec)
	}
	if len(cr.Spec.ScanTargets) != 2 || cr.Spec.ScanTargets[0].RepositoryURL != "https://github.com/acme/widget" || cr.Spec.ScanTargets[0].BaseBranch != "main" ||
		cr.Spec.ScanTargets[0].Priority != 3 || !cr.Spec.ScanTargets[0].Featured ||
		cr.Spec.ScanTargets[1].TargetURL != "https://app.acme.example" || cr.Spec.ScanTargets[1].RepositoryURL != "" {
		t.Fatalf("stored scan targets = %+v", cr.Spec.ScanTargets)
	}

	update := testSecurityProgramResource("")
	update.DisplayName = "Acme Public Bounty"
	updated, err := srv.UpdateSecurityProgram(ctx, &platform.UpdateSecurityProgramRequest{Program: update})
	if err != nil {
		t.Fatalf("UpdateSecurityProgram() error = %v", err)
	}
	if updated.DisplayName != "Acme Public Bounty" {
		t.Fatalf("updated = %+v", updated)
	}
	list, err := srv.ListSecurityPrograms(ctx, &platform.ListSecurityProgramsRequest{})
	if err != nil || len(list.Programs) != 1 {
		t.Fatalf("ListSecurityPrograms() = %+v, %v", list, err)
	}

	scan := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL:            "https://github.com/acme/widget.git",
			SecurityProgramRef: &triggersv1alpha1.SecurityResourceRef{Name: "acme-bounty"},
		},
	}
	if err := c.Create(context.Background(), scan); err != nil {
		t.Fatalf("Create(SecurityScan) error = %v", err)
	}
	if _, err := srv.DeleteSecurityProgram(ctx, &platform.DeleteSecurityProgramRequest{Name: "acme-bounty"}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("DeleteSecurityProgram(referenced) error = %v, want FailedPrecondition", err)
	}
	got, err := srv.GetSecurityProgram(ctx, &platform.GetSecurityProgramRequest{Name: "acme-bounty"})
	if err != nil || got.UsageCount != 1 || len(got.ReferencingScans) != 1 || got.ReferencingScans[0] != "nightly" {
		t.Fatalf("GetSecurityProgram() = %+v, %v", got, err)
	}
	if err := c.Delete(context.Background(), scan); err != nil {
		t.Fatalf("Delete(SecurityScan) error = %v", err)
	}
	if _, err := srv.DeleteSecurityProgram(ctx, &platform.DeleteSecurityProgramRequest{Name: "acme-bounty"}); err != nil {
		t.Fatalf("DeleteSecurityProgram() error = %v", err)
	}
	if _, err := srv.GetSecurityProgram(ctx, &platform.GetSecurityProgramRequest{Name: "acme-bounty"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetSecurityProgram after delete = %v, want NotFound", err)
	}
}

func TestSecurityProgramLegacyScanTargetDefaultsToMain(t *testing.T) {
	srv, _ := newCronTestServer(t)
	resource := testSecurityProgramResource("")
	resource.ScanTarget = resource.ScanTargets[0]
	resource.ScanTargets = nil
	resource.ScanTarget.BaseBranch = ""

	created, err := srv.CreateSecurityProgram(projectActorCtx(), &platform.CreateSecurityProgramRequest{Program: resource})
	if err != nil {
		t.Fatalf("CreateSecurityProgram() error = %v", err)
	}
	if created.ScanTarget == nil || created.ScanTarget.BaseBranch != "main" {
		t.Fatalf("scan target = %+v, want base branch main", created.ScanTarget)
	}
}

func TestSecurityProgramOwnershipFailureRollsBack(t *testing.T) {
	srv, c := newCronTestServer(t)
	ms := newMockStateStore()
	ms.setResourceOwnerErr = errors.New("ownership unavailable")
	srv.stateStore = ms

	_, err := srv.CreateSecurityProgram(projectActorCtx(), &platform.CreateSecurityProgramRequest{Program: testSecurityProgramResource("")})
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("CreateSecurityProgram() error = %v, want Internal", err)
	}
	got := &triggersv1alpha1.SecurityProgram{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: testUserNS(), Name: "acme-bounty"}, got); err == nil {
		t.Fatal("unowned SecurityProgram remained after ownership failure")
	}
}

func TestSecurityProgramVisibilityAndAccess(t *testing.T) {
	ns := testUserNS()
	program := &triggersv1alpha1.SecurityProgram{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-bounty", Namespace: ns},
		Spec: triggersv1alpha1.SecurityProgramSpec{
			Provider: "Immunefi", DisplayName: "Acme", ProgramURL: "https://immunefi.com/bug-bounty/acme/scope/",
			ScopePolicy: "Production assets are in scope.", VerifiedAt: metav1.NewTime(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)),
		},
	}
	privateScan := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "private-scan", Namespace: ns},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL: "https://github.com/acme/private.git", SecurityProgramRef: &triggersv1alpha1.SecurityResourceRef{Name: program.Name},
		},
	}
	srv, _ := newCronTestServer(t, program, privateScan)
	ms := newCollaborationStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityProgramResourceType, program.Name, ns, "other-user"); err != nil {
		t.Fatal(err)
	}
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, privateScan.Name, ns, "other-user"); err != nil {
		t.Fatal(err)
	}
	ctx := projectActorCtx()

	list, err := srv.ListSecurityPrograms(ctx, &platform.ListSecurityProgramsRequest{})
	if err != nil || len(list.Programs) != 0 {
		t.Fatalf("non-owner list = %+v, %v", list, err)
	}
	if _, err := srv.GetSecurityProgram(ctx, &platform.GetSecurityProgramRequest{Name: program.Name}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-owner GetSecurityProgram() error = %v, want PermissionDenied", err)
	}
	if _, err := srv.GetSecurityProgram(actorContext("admin", "admin", "", ""), &platform.GetSecurityProgramRequest{Namespace: ns, Name: program.Name}); err != nil {
		t.Fatalf("admin GetSecurityProgram() error = %v", err)
	}

	if _, err := ms.ShareResource(context.Background(), &store.ResourceShare{
		ID: "program-share", ResourceType: securityProgramResourceType, ResourceID: program.Name,
		ResourceNamespace: ns, SharedWithUserID: testProjectSubject, SharedByUserID: "other-user", Permission: "collaborator",
	}); err != nil {
		t.Fatal(err)
	}
	list, err = srv.ListSecurityPrograms(ctx, &platform.ListSecurityProgramsRequest{})
	if err != nil || len(list.Programs) != 1 || list.Programs[0].UsageCount != 0 || len(list.Programs[0].ReferencingScans) != 0 {
		t.Fatalf("shared-user list = %+v, %v", list, err)
	}
	update := testSecurityProgramResource("")
	update.DisplayName = "Shared update"
	updated, err := srv.UpdateSecurityProgram(ctx, &platform.UpdateSecurityProgramRequest{Program: update})
	if err != nil {
		t.Fatalf("shared collaborator UpdateSecurityProgram() error = %v", err)
	}
	if updated.UsageCount != 0 || len(updated.ReferencingScans) != 0 {
		t.Fatalf("update leaked private scan references: %+v", updated)
	}
	_, err = srv.DeleteSecurityProgram(ctx, &platform.DeleteSecurityProgramRequest{Name: program.Name})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || strings.Contains(err.Error(), privateScan.Name) {
		t.Fatalf("DeleteSecurityProgram() leaked private scan name: %v", err)
	}
}

func TestSecurityProgramProtoHidesStaleStatus(t *testing.T) {
	program := &triggersv1alpha1.SecurityProgram{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-bounty", Namespace: "tenant", Generation: 2},
		Spec: triggersv1alpha1.SecurityProgramSpec{
			Provider: "Immunefi", DisplayName: "Acme", ProgramURL: "https://immunefi.com/bug-bounty/acme/scope/",
			ScopePolicy: "Production contracts are in scope.", VerifiedAt: metav1.NewTime(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)),
		},
	}
	program.Status.ObservedGeneration = program.Generation
	program.Status.ContentDigest = securityProgramContentDigest(program.Spec)
	program.Status.Conditions = []metav1.Condition{{
		Type: triggersv1alpha1.ConditionSecurityLibraryReady, Status: metav1.ConditionTrue,
		ObservedGeneration: program.Generation, Reason: "Validated",
	}}
	if got := securityProgramToProto(program, nil); got.ConditionReady != "True" || got.ContentDigest == "" {
		t.Fatalf("current status hidden: %+v", got)
	}

	program.Generation++
	if got := securityProgramToProto(program, nil); got.ConditionReady != "" || got.ContentDigest != "" {
		t.Fatalf("stale status exposed after generation change: %+v", got)
	}
}

func TestCreateSecurityProgramValidation(t *testing.T) {
	srv, _ := newCronTestServer(t)
	for name, mutate := range map[string]func(*platform.SecurityProgramResource){
		"missing provider":     func(p *platform.SecurityProgramResource) { p.Provider = " " },
		"missing display name": func(p *platform.SecurityProgramResource) { p.DisplayName = "" },
		"non-https URL":        func(p *platform.SecurityProgramResource) { p.ProgramUrl = "http://example.com/program" },
		"missing scope":        func(p *platform.SecurityProgramResource) { p.ScopePolicy = "\n" },
		"oversized scope": func(p *platform.SecurityProgramResource) {
			p.ScopePolicy = strings.Repeat("x", triggersv1alpha1.MaxSecurityProgramScopePolicyLength+1)
		},
		"missing verified time": func(p *platform.SecurityProgramResource) { p.VerifiedAt = nil },
		"invalid name":          func(p *platform.SecurityProgramResource) { p.Name = "Not Valid!" },
		"invalid repository URL": func(p *platform.SecurityProgramResource) {
			p.ScanTargets[0].RepositoryUrl = "http://github.com/acme/widget"
		},
		"invalid workflow ref": func(p *platform.SecurityProgramResource) { p.ScanTargets[0].WorkflowRef = "Not Valid!" },
		"invalid policy pack ref": func(p *platform.SecurityProgramResource) {
			p.ScanTargets[0].PolicyPackRef = "Not Valid!"
		},
		"invalid scan name": func(p *platform.SecurityProgramResource) { p.ScanTargets[0].ScanName = "Not Valid!" },
		"duplicate scan name": func(p *platform.SecurityProgramResource) {
			p.ScanTargets[1].ScanName = p.ScanTargets[0].ScanName
		},
		"negative priority": func(p *platform.SecurityProgramResource) { p.ScanTargets[0].Priority = -1 },
		"both target forms": func(p *platform.SecurityProgramResource) {
			p.ScanTarget = p.ScanTargets[0]
		},
		"unknown severity system": func(p *platform.SecurityProgramResource) { p.SeveritySystem = "hackerone" },
		"unknown primacy":         func(p *platform.SecurityProgramResource) { p.Primacy = "either" },
		"unknown poc environment": func(p *platform.SecurityProgramResource) { p.PocEnvironment = "testnet" },
		"impact without level": func(p *platform.SecurityProgramResource) {
			p.InScopeImpacts = []*platform.SecurityProgramImpact{{Impact: "Permanent freezing of funds"}}
		},
		"severity outside the program's ladder": func(p *platform.SecurityProgramResource) {
			p.SeveritySystem = string(triggersv1alpha1.SeveritySystemSherlock)
			p.InScopeImpacts = []*platform.SecurityProgramImpact{
				{Impact: "Griefing", Level: "low", AssetType: "Smart Contract"},
			}
		},
		"unidentifiable asset": func(p *platform.SecurityProgramResource) {
			p.Assets = []*platform.SecurityProgramAsset{{DisplayName: "Acme vault"}}
		},
		"known issue without summary": func(p *platform.SecurityProgramResource) {
			p.KnownIssues = []*platform.SecurityProgramKnownIssue{{Source: "prior audit"}}
		},
		"budget period without cap": func(p *platform.SecurityProgramResource) {
			p.SubmissionBudget = &platform.SecurityProgramSubmissionBudget{PeriodDays: 30}
		},
	} {
		t.Run(name, func(t *testing.T) {
			program := testSecurityProgramResource("")
			mutate(program)
			_, err := srv.CreateSecurityProgram(projectActorCtx(), &platform.CreateSecurityProgramRequest{Program: program})
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("CreateSecurityProgram() error = %v, want InvalidArgument", err)
			}
		})
	}
}

func TestSecurityProgramProtoRoundTripWithoutScanTarget(t *testing.T) {
	pb := testSecurityProgramResource("")
	pb.ScanTargets = nil
	pb.ScanTarget = nil
	spec, err := securityProgramSpecFromProto(pb)
	if err != nil || spec.ScanTarget != nil || len(spec.ScanTargets) != 0 {
		t.Fatalf("securityProgramSpecFromProto() = %+v, %v", spec, err)
	}
	got := securityProgramToProto(&triggersv1alpha1.SecurityProgram{Spec: spec}, nil)
	if got.ScanTarget != nil || len(got.ScanTargets) != 0 {
		t.Fatalf("securityProgramToProto() scan targets = %+v / %+v", got.ScanTarget, got.ScanTargets)
	}
}

func testSecurityProgramTypedScope(pb *platform.SecurityProgramResource) {
	pb.SeveritySystem = string(triggersv1alpha1.SeveritySystemImmunefiV23)
	pb.Primacy = string(triggersv1alpha1.PrimacyImpact)
	pb.PocRequired = true
	pb.PocEnvironment = string(triggersv1alpha1.PoCEnvironmentMainnetFork)
	pb.KycRequired = true
	pb.InScopeImpacts = []*platform.SecurityProgramImpact{
		{Impact: "Permanent freezing of funds", Level: "critical", AssetType: "Smart Contract"},
		{Impact: "Theft of unclaimed yield", Level: "high", AssetType: "Smart Contract"},
	}
	pb.OutOfScope = []string{"Attacks requiring leaked keys"}
	pb.ProhibitedTesting = []string{"Testing on mainnet or public testnet"}
	pb.Assets = []*platform.SecurityProgramAsset{{
		ChainId: "1", Address: "0xabc", RepositoryUrl: "https://github.com/acme/widget",
		DisplayName: "Acme vault", AddedOn: "2026-07-01",
	}}
	pb.KnownIssues = []*platform.SecurityProgramKnownIssue{{
		Source: "prior audit", Summary: "Acknowledged rounding", Reference: "https://acme.example/audit.pdf",
	}}
	pb.SubmissionBudget = &platform.SecurityProgramSubmissionBudget{
		MaxPerPeriod: 2, PeriodDays: 30, UnrestrictedRequiresReputation: true,
	}
}

// Typed scope is what downstream gates check, so it has to survive the whole
// dashboard round trip: an editor save must return, store, and re-read exactly
// what the operator transcribed.
func TestSecurityProgramTypedScopeRoundTrip(t *testing.T) {
	srv, c := newCronTestServer(t)
	srv.stateStore = newMockStateStore()
	ctx := projectActorCtx()
	ns := testUserNS()

	program := testSecurityProgramResource("")
	testSecurityProgramTypedScope(program)
	created, err := srv.CreateSecurityProgram(ctx, &platform.CreateSecurityProgramRequest{Program: program})
	if err != nil {
		t.Fatalf("CreateSecurityProgram() error = %v", err)
	}
	assertSecurityProgramTypedScope(t, "create", created)

	got, err := srv.GetSecurityProgram(ctx, &platform.GetSecurityProgramRequest{Name: "acme-bounty"})
	if err != nil {
		t.Fatalf("GetSecurityProgram() error = %v", err)
	}
	assertSecurityProgramTypedScope(t, "get", got)

	update := testSecurityProgramResource("")
	testSecurityProgramTypedScope(update)
	update.DisplayName = "Acme Public Bounty"
	updated, err := srv.UpdateSecurityProgram(ctx, &platform.UpdateSecurityProgramRequest{Program: update})
	if err != nil {
		t.Fatalf("UpdateSecurityProgram() error = %v", err)
	}
	if updated.DisplayName != "Acme Public Bounty" {
		t.Errorf("displayName = %q, want the edit to apply", updated.DisplayName)
	}
	assertSecurityProgramTypedScope(t, "update", updated)

	reread, err := srv.GetSecurityProgram(ctx, &platform.GetSecurityProgramRequest{Name: "acme-bounty"})
	if err != nil {
		t.Fatalf("GetSecurityProgram() after update error = %v", err)
	}
	assertSecurityProgramTypedScope(t, "get after update", reread)

	assertStoredSecurityProgramTypedScope(t, c, ns)
}

// assertStoredSecurityProgramTypedScope checks the CR the API server actually
// persisted, not just what the RPC echoed back.
func assertStoredSecurityProgramTypedScope(t *testing.T, c client.Client, ns string) {
	t.Helper()
	stored := &triggersv1alpha1.SecurityProgram{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-bounty"}, stored); err != nil {
		t.Fatalf("Get(SecurityProgram) error = %v", err)
	}
	if stored.Spec.SeveritySystem != string(triggersv1alpha1.SeveritySystemImmunefiV23) ||
		stored.Spec.Primacy != string(triggersv1alpha1.PrimacyImpact) || !stored.Spec.PoCRequired ||
		stored.Spec.PoCEnvironment != string(triggersv1alpha1.PoCEnvironmentMainnetFork) || !stored.Spec.KYCRequired {
		t.Errorf("stored typed scope flags = %+v", stored.Spec)
	}
	if len(stored.Spec.InScopeImpacts) != 2 || stored.Spec.InScopeImpacts[0].Impact != "Permanent freezing of funds" ||
		stored.Spec.InScopeImpacts[0].Level != "critical" || stored.Spec.InScopeImpacts[0].AssetType != "Smart Contract" {
		t.Errorf("stored inScopeImpacts = %+v", stored.Spec.InScopeImpacts)
	}
	if len(stored.Spec.Assets) != 1 || stored.Spec.Assets[0].ChainID != "1" || stored.Spec.Assets[0].Address != "0xabc" ||
		len(stored.Spec.KnownIssues) != 1 || stored.Spec.KnownIssues[0].Source != "prior audit" {
		t.Errorf("stored assets/knownIssues = %+v / %+v", stored.Spec.Assets, stored.Spec.KnownIssues)
	}
	if stored.Spec.SubmissionBudget == nil || stored.Spec.SubmissionBudget.MaxPerPeriod != 2 ||
		stored.Spec.SubmissionBudget.PeriodDays != 30 || !stored.Spec.SubmissionBudget.UnrestrictedRequiresReputation {
		t.Errorf("stored submissionBudget = %+v", stored.Spec.SubmissionBudget)
	}

}

// The editor is authoritative: clearing a transcribed field must clear it in
// the CR rather than resurrect the previously stored value, which is what the
// interim preservation guard used to do before the RPC carried these fields.
func TestSecurityProgramTypedScopeCanBeCleared(t *testing.T) {
	srv, c := newCronTestServer(t)
	srv.stateStore = newMockStateStore()
	ctx := projectActorCtx()
	ns := testUserNS()

	program := testSecurityProgramResource("")
	testSecurityProgramTypedScope(program)
	if _, err := srv.CreateSecurityProgram(ctx, &platform.CreateSecurityProgramRequest{Program: program}); err != nil {
		t.Fatalf("CreateSecurityProgram() error = %v", err)
	}
	stored := &triggersv1alpha1.SecurityProgram{}
	cleared := testSecurityProgramResource("")
	if _, err := srv.UpdateSecurityProgram(ctx, &platform.UpdateSecurityProgramRequest{Program: cleared}); err != nil {
		t.Fatalf("UpdateSecurityProgram(cleared) error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-bounty"}, stored); err != nil {
		t.Fatalf("Get(SecurityProgram) after clear error = %v", err)
	}
	if stored.Spec.SeveritySystem != "" || stored.Spec.Primacy != "" || stored.Spec.PoCRequired ||
		stored.Spec.PoCEnvironment != "" || stored.Spec.KYCRequired || len(stored.Spec.InScopeImpacts) != 0 ||
		len(stored.Spec.OutOfScope) != 0 || len(stored.Spec.ProhibitedTesting) != 0 || len(stored.Spec.Assets) != 0 ||
		len(stored.Spec.KnownIssues) != 0 || stored.Spec.SubmissionBudget != nil {
		t.Errorf("cleared typed scope = %+v", stored.Spec)
	}
}

func assertSecurityProgramTypedScope(t *testing.T, stage string, pb *platform.SecurityProgramResource) {
	t.Helper()
	if pb.GetSeveritySystem() != string(triggersv1alpha1.SeveritySystemImmunefiV23) ||
		pb.GetPrimacy() != string(triggersv1alpha1.PrimacyImpact) || !pb.GetPocRequired() ||
		pb.GetPocEnvironment() != string(triggersv1alpha1.PoCEnvironmentMainnetFork) || !pb.GetKycRequired() {
		t.Errorf("%s: typed scope flags = %+v", stage, pb)
	}
	if len(pb.GetInScopeImpacts()) != 2 ||
		pb.GetInScopeImpacts()[0].GetImpact() != "Permanent freezing of funds" ||
		pb.GetInScopeImpacts()[0].GetLevel() != "critical" ||
		pb.GetInScopeImpacts()[0].GetAssetType() != "Smart Contract" ||
		pb.GetInScopeImpacts()[1].GetImpact() != "Theft of unclaimed yield" {
		t.Errorf("%s: inScopeImpacts = %+v", stage, pb.GetInScopeImpacts())
	}
	if len(pb.GetOutOfScope()) != 1 || pb.GetOutOfScope()[0] != "Attacks requiring leaked keys" ||
		len(pb.GetProhibitedTesting()) != 1 || pb.GetProhibitedTesting()[0] != "Testing on mainnet or public testnet" {
		t.Errorf("%s: verbatim lists = %+v / %+v", stage, pb.GetOutOfScope(), pb.GetProhibitedTesting())
	}
	if len(pb.GetAssets()) != 1 || pb.GetAssets()[0].GetChainId() != "1" || pb.GetAssets()[0].GetAddress() != "0xabc" ||
		pb.GetAssets()[0].GetRepositoryUrl() != "https://github.com/acme/widget" ||
		pb.GetAssets()[0].GetDisplayName() != "Acme vault" || pb.GetAssets()[0].GetAddedOn() != "2026-07-01" {
		t.Errorf("%s: assets = %+v", stage, pb.GetAssets())
	}
	if len(pb.GetKnownIssues()) != 1 || pb.GetKnownIssues()[0].GetSource() != "prior audit" ||
		pb.GetKnownIssues()[0].GetSummary() != "Acknowledged rounding" ||
		pb.GetKnownIssues()[0].GetReference() != "https://acme.example/audit.pdf" {
		t.Errorf("%s: knownIssues = %+v", stage, pb.GetKnownIssues())
	}
	budget := pb.GetSubmissionBudget()
	if budget.GetMaxPerPeriod() != 2 || budget.GetPeriodDays() != 30 || !budget.GetUnrestrictedRequiresReputation() {
		t.Errorf("%s: submissionBudget = %+v", stage, budget)
	}
}
