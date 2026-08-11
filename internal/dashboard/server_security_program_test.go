package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
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
	}
}

func TestSecurityProgramCRUDAndReferenceGuard(t *testing.T) {
	srv, c := newCronTestServer(t)
	ctx := projectActorCtx()
	ns := testUserNS()

	created, err := srv.CreateSecurityProgram(ctx, &platform.CreateSecurityProgramRequest{Program: testSecurityProgramResource("")})
	if err != nil {
		t.Fatalf("CreateSecurityProgram() error = %v", err)
	}
	if created.Namespace != ns || created.Name != "acme-bounty" || created.Provider != "HackerOne" {
		t.Fatalf("created = %+v", created)
	}
	cr := &triggersv1alpha1.SecurityProgram{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-bounty"}, cr); err != nil {
		t.Fatalf("Get(SecurityProgram) error = %v", err)
	}
	if cr.Spec.ProgramURL != "https://hackerone.com/acme" || cr.Spec.VerifiedAt.IsZero() {
		t.Fatalf("spec = %+v", cr.Spec)
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
