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
		ScanTarget: &platform.SecurityProgramScanTarget{
			RepositoryUrl: "https://github.com/acme/widget",
			WorkflowRef:   "blockchain-protocol-audit",
			PolicyPackRef: "bug-bounty",
			ScanName:      "acme-bounty",
			DisplayName:   "Acme",
			Priority:      3,
			Featured:      true,
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
	if created.ScanTarget == nil || created.ScanTarget.RepositoryUrl != "https://github.com/acme/widget" ||
		created.ScanTarget.WorkflowRef != "blockchain-protocol-audit" || created.ScanTarget.PolicyPackRef != "bug-bounty" ||
		created.ScanTarget.ScanName != "acme-bounty" || created.ScanTarget.DisplayName != "Acme" ||
		created.ScanTarget.Priority != 3 || !created.ScanTarget.Featured {
		t.Fatalf("created scan target = %+v", created.ScanTarget)
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
	if cr.Spec.ScanTarget == nil || cr.Spec.ScanTarget.RepositoryURL != "https://github.com/acme/widget" ||
		cr.Spec.ScanTarget.Priority != 3 || !cr.Spec.ScanTarget.Featured {
		t.Fatalf("stored scan target = %+v", cr.Spec.ScanTarget)
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
			p.ScanTarget.RepositoryUrl = "http://github.com/acme/widget"
		},
		"invalid workflow ref": func(p *platform.SecurityProgramResource) { p.ScanTarget.WorkflowRef = "Not Valid!" },
		"invalid policy pack ref": func(p *platform.SecurityProgramResource) {
			p.ScanTarget.PolicyPackRef = "Not Valid!"
		},
		"invalid scan name": func(p *platform.SecurityProgramResource) { p.ScanTarget.ScanName = "Not Valid!" },
		"negative priority": func(p *platform.SecurityProgramResource) { p.ScanTarget.Priority = -1 },
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
	pb.ScanTarget = nil
	spec, err := securityProgramSpecFromProto(pb)
	if err != nil || spec.ScanTarget != nil {
		t.Fatalf("securityProgramSpecFromProto() = %+v, %v", spec, err)
	}
	got := securityProgramToProto(&triggersv1alpha1.SecurityProgram{Spec: spec}, nil)
	if got.ScanTarget != nil {
		t.Fatalf("securityProgramToProto() scan target = %+v", got.ScanTarget)
	}
}
