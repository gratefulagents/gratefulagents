package dashboard

import (
	"context"
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

func testSecurityPolicyPackResource(namespace string) *platform.SecurityPolicyPackResource {
	return &platform.SecurityPolicyPackResource{
		Namespace:              namespace,
		Name:                   "org-policy",
		Description:            "org-wide floors",
		RequiredCategories:     []string{"injection"},
		MinSeverity:            "low",
		FailOnSeverity:         "high",
		Dedupe:                 &platform.SecurityScanDedupeConfig{Enabled: true, SimilarityThresholdPermille: 900},
		AllowedRuntimeProfiles: []string{"locked-down"},
		DefaultRankerRefs:      []string{"org-ranker"},
		DefaultPostScriptRefs:  []string{"org-poc"},
		Enforced:               []string{"minSeverity", "failOnSeverity"},
		Retention: &platform.SecurityPolicyPackRetentionConfig{
			ScanDays:       30,
			FindingDays:    365,
			ReportDays:     90,
			EvidenceDays:   14,
			PocDays:        7,
			AuditEventDays: 730,
		},
		Budgets: &platform.SecurityScanBudgetsConfig{
			MaxModelJobs:      16,
			MaxCostUsd:        "5",
			MaxTokens:         100000,
			MaxRuntime:        "1h",
			MaxValidationJobs: 8,
		},
		Suppressions: []*platform.SecurityPolicySuppressionConfig{{
			Name:      "noisy-vendor",
			Reason:    "vendored code",
			Owner:     "appsec",
			Matcher:   &platform.SecuritySuppressionMatcherConfig{PathGlob: "vendor/*"},
			ExpiresAt: timestamppb.New(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
		}},
	}
}

func TestSecurityPolicyPackCRUDLifecycle(t *testing.T) {
	srv, c := newCronTestServer(t)
	ns := testUserNS()
	ctx := projectActorCtx()
	testSecurityPolicyPackCreateAndRead(t, srv, c, ns, ctx)
	testSecurityPolicyPackUpdateListDelete(t, srv, ctx)
}

func testSecurityPolicyPackCreateAndRead(t *testing.T, srv *Server, c client.Client, ns string, ctx context.Context) {
	created, err := srv.CreateSecurityPolicyPack(ctx, &platform.CreateSecurityPolicyPackRequest{PolicyPack: testSecurityPolicyPackResource("")})
	if err != nil {
		t.Fatalf("CreateSecurityPolicyPack() error = %v", err)
	}
	if created.Namespace != ns || created.Name != "org-policy" || len(created.Suppressions) != 1 {
		t.Fatalf("created = %+v", created)
	}

	cr := &triggersv1alpha1.SecurityPolicyPack{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "org-policy"}, cr); err != nil {
		t.Fatalf("Get(SecurityPolicyPack) error = %v", err)
	}
	if cr.Spec.MinSeverity != "low" || cr.Spec.FailOnSeverity != "high" ||
		len(cr.Spec.Enforced) != 2 || cr.Spec.Dedupe == nil || cr.Spec.Dedupe.SimilarityThresholdPermille != 900 ||
		len(cr.Spec.DefaultRankerRefs) != 1 || cr.Spec.DefaultRankerRefs[0].Name != "org-ranker" {
		t.Fatalf("spec = %+v", cr.Spec)
	}
	rule := cr.Spec.Suppressions[0]
	if rule.Name != "noisy-vendor" || rule.Reason != "vendored code" || rule.Owner != "appsec" ||
		rule.Matcher.PathGlob != "vendor/*" || rule.ExpiresAt == nil {
		t.Fatalf("suppression = %+v", rule)
	}
	if cr.Spec.Retention == nil || cr.Spec.Retention.ScanDays != 30 || cr.Spec.Retention.FindingDays != 365 ||
		cr.Spec.Retention.EvidenceDays != 14 || cr.Spec.Retention.PoCDays != 7 || cr.Spec.Retention.AuditEventDays != 730 {
		t.Fatalf("retention = %+v", cr.Spec.Retention)
	}
	if cr.Spec.Budgets == nil || cr.Spec.Budgets.MaxCostUSD != "5" || cr.Spec.Budgets.MaxTokens != 100000 ||
		cr.Spec.Budgets.MaxRuntime.Duration != time.Hour {
		t.Fatalf("budgets = %+v", cr.Spec.Budgets)
	}

}

func testSecurityPolicyPackUpdateListDelete(t *testing.T, srv *Server, ctx context.Context) {
	got, err := srv.GetSecurityPolicyPack(ctx, &platform.GetSecurityPolicyPackRequest{Name: "org-policy"})
	if err != nil {
		t.Fatalf("GetSecurityPolicyPack() error = %v", err)
	}
	if got.UsageCount != 0 || got.Description != "org-wide floors" || got.Suppressions[0].GetMatcher().GetPathGlob() != "vendor/*" {
		t.Fatalf("got = %+v", got)
	}
	if got.GetRetention().GetPocDays() != 7 || got.GetRetention().GetScanDays() != 30 ||
		got.GetBudgets().GetMaxCostUsd() != "5" || got.GetBudgets().GetMaxRuntime() != "1h0m0s" {
		t.Fatalf("retention/budgets round-trip = %+v / %+v", got.GetRetention(), got.GetBudgets())
	}

	update := testSecurityPolicyPackResource("")
	update.Description = "updated"
	update.Enforced = []string{"dedupe"}
	updated, err := srv.UpdateSecurityPolicyPack(ctx, &platform.UpdateSecurityPolicyPackRequest{PolicyPack: update})
	if err != nil {
		t.Fatalf("UpdateSecurityPolicyPack() error = %v", err)
	}
	if updated.Description != "updated" || len(updated.Enforced) != 1 || updated.Enforced[0] != "dedupe" {
		t.Fatalf("updated = %+v", updated)
	}

	list, err := srv.ListSecurityPolicyPacks(ctx, &platform.ListSecurityPolicyPacksRequest{})
	if err != nil {
		t.Fatalf("ListSecurityPolicyPacks() error = %v", err)
	}
	if len(list.PolicyPacks) != 1 || list.PolicyPacks[0].Name != "org-policy" {
		t.Fatalf("list = %+v", list.PolicyPacks)
	}

	if _, err := srv.DeleteSecurityPolicyPack(ctx, &platform.DeleteSecurityPolicyPackRequest{Name: "org-policy"}); err != nil {
		t.Fatalf("DeleteSecurityPolicyPack() error = %v", err)
	}
	if _, err := srv.GetSecurityPolicyPack(ctx, &platform.GetSecurityPolicyPackRequest{Name: "org-policy"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetSecurityPolicyPack after delete = %v, want NotFound", err)
	}
}

func TestCreateSecurityPolicyPackValidationErrors(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := projectActorCtx()

	for name, mutate := range map[string]func(*platform.SecurityPolicyPackResource){
		"invalid minSeverity":    func(p *platform.SecurityPolicyPackResource) { p.MinSeverity = "urgent" },
		"invalid failOnSeverity": func(p *platform.SecurityPolicyPackResource) { p.FailOnSeverity = "sev1" },
		"unknown enforced field": func(p *platform.SecurityPolicyPackResource) { p.Enforced = []string{"notAField"} },
		"dedupe out of range":    func(p *platform.SecurityPolicyPackResource) { p.Dedupe.SimilarityThresholdPermille = 2000 },
		"suppression bad name":   func(p *platform.SecurityPolicyPackResource) { p.Suppressions[0].Name = "Bad Name!" },
		"suppression no reason":  func(p *platform.SecurityPolicyPackResource) { p.Suppressions[0].Reason = " " },
		"suppression no owner":   func(p *platform.SecurityPolicyPackResource) { p.Suppressions[0].Owner = "" },
		"suppression no matcher": func(p *platform.SecurityPolicyPackResource) { p.Suppressions[0].Matcher = nil },
		"invalid resource name":  func(p *platform.SecurityPolicyPackResource) { p.Name = "Not Valid!" },
		"invalid default ref":    func(p *platform.SecurityPolicyPackResource) { p.DefaultRankerRefs = []string{"Not Valid!"} },
		"duplicate default ref":  func(p *platform.SecurityPolicyPackResource) { p.DefaultRankerRefs = []string{"r", "r"} },
		"duplicate rule names": func(p *platform.SecurityPolicyPackResource) {
			p.Suppressions = append(p.Suppressions, p.Suppressions[0])
		},
		"duplicate enforced": func(p *platform.SecurityPolicyPackResource) { p.Enforced = []string{"dedupe", "dedupe"} },
		"retention days above bound": func(p *platform.SecurityPolicyPackResource) {
			p.Retention.FindingDays = 4000
		},
		"negative retention days": func(p *platform.SecurityPolicyPackResource) {
			p.Retention.EvidenceDays = -1
		},
		"invalid budget cost": func(p *platform.SecurityPolicyPackResource) {
			p.Budgets.MaxCostUsd = "five dollars"
		},
		"invalid budget runtime": func(p *platform.SecurityPolicyPackResource) {
			p.Budgets.MaxRuntime = "banana"
		},
		"negative budget tokens": func(p *platform.SecurityPolicyPackResource) {
			p.Budgets.MaxTokens = -5
		},
	} {
		t.Run(name, func(t *testing.T) {
			pack := testSecurityPolicyPackResource("")
			mutate(pack)
			_, err := srv.CreateSecurityPolicyPack(ctx, &platform.CreateSecurityPolicyPackRequest{PolicyPack: pack})
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("CreateSecurityPolicyPack(%s) error = %v, want InvalidArgument", name, err)
			}
		})
	}
}

func TestCreateSecurityPolicyPackAllowsEmptyEnforcedBudgets(t *testing.T) {
	srv, _ := newCronTestServer(t)
	pack := testSecurityPolicyPackResource("")
	pack.Enforced = []string{"budgets"}
	pack.Budgets = nil

	if _, err := srv.CreateSecurityPolicyPack(projectActorCtx(), &platform.CreateSecurityPolicyPackRequest{PolicyPack: pack}); err != nil {
		t.Fatalf("CreateSecurityPolicyPack() error = %v, want legacy empty enforced budgets to remain valid", err)
	}
}

func TestDeleteSecurityPolicyPackBlockedWhileReferenced(t *testing.T) {
	srv, c := newCronTestServer(t)
	ns := testUserNS()
	ctx := projectActorCtx()

	if _, err := srv.CreateSecurityPolicyPack(ctx, &platform.CreateSecurityPolicyPackRequest{PolicyPack: testSecurityPolicyPackResource("")}); err != nil {
		t.Fatalf("CreateSecurityPolicyPack() error = %v", err)
	}
	scan := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL:       "https://github.com/acme/widget.git",
			PolicyPackRef: &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"},
		},
	}
	if err := c.Create(context.Background(), scan); err != nil {
		t.Fatalf("Create(SecurityScan) error = %v", err)
	}

	_, err := srv.DeleteSecurityPolicyPack(ctx, &platform.DeleteSecurityPolicyPackRequest{Name: "org-policy"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("DeleteSecurityPolicyPack(referenced) error = %v, want FailedPrecondition", err)
	}

	got, err := srv.GetSecurityPolicyPack(ctx, &platform.GetSecurityPolicyPackRequest{Name: "org-policy"})
	if err != nil {
		t.Fatalf("GetSecurityPolicyPack() error = %v", err)
	}
	if got.UsageCount != 1 || len(got.ReferencingScans) != 1 || got.ReferencingScans[0] != "nightly" {
		t.Fatalf("usage = %d %v, want the referencing scan reported", got.UsageCount, got.ReferencingScans)
	}

	if err := c.Delete(context.Background(), scan); err != nil {
		t.Fatalf("Delete(SecurityScan) error = %v", err)
	}
	if _, err := srv.DeleteSecurityPolicyPack(ctx, &platform.DeleteSecurityPolicyPackRequest{Name: "org-policy"}); err != nil {
		t.Fatalf("DeleteSecurityPolicyPack(unreferenced) error = %v", err)
	}
}

func TestSecurityPolicyPackCrossNamespaceDeniedForNonAdmins(t *testing.T) {
	srv, _ := newCronTestServer(t)
	ctx := actorContext("mallory", "member", "", "")
	const other = "someone-elses-namespace"

	calls := map[string]func() error{
		"ListSecurityPolicyPacks": func() error {
			_, err := srv.ListSecurityPolicyPacks(ctx, &platform.ListSecurityPolicyPacksRequest{Namespace: other})
			return err
		},
		"GetSecurityPolicyPack": func() error {
			_, err := srv.GetSecurityPolicyPack(ctx, &platform.GetSecurityPolicyPackRequest{Namespace: other, Name: "x"})
			return err
		},
		"CreateSecurityPolicyPack": func() error {
			_, err := srv.CreateSecurityPolicyPack(ctx, &platform.CreateSecurityPolicyPackRequest{PolicyPack: testSecurityPolicyPackResource(other)})
			return err
		},
		"UpdateSecurityPolicyPack": func() error {
			_, err := srv.UpdateSecurityPolicyPack(ctx, &platform.UpdateSecurityPolicyPackRequest{PolicyPack: testSecurityPolicyPackResource(other)})
			return err
		},
		"DeleteSecurityPolicyPack": func() error {
			_, err := srv.DeleteSecurityPolicyPack(ctx, &platform.DeleteSecurityPolicyPackRequest{Namespace: other, Name: "x"})
			return err
		},
	}
	for name, call := range calls {
		if err := call(); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("%s cross-namespace error = %v, want PermissionDenied", name, err)
		}
	}
}

func TestListSecurityFindingsSuppressedFilterRoundTrip(t *testing.T) {
	sec := newMockSecurityStore()
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	for _, value := range []string{"", "exclude", "include", "only"} {
		if _, err := srv.ListSecurityFindings(ctx, &platform.ListSecurityFindingsRequest{
			Namespace: "default", Suppressed: value,
		}); err != nil {
			t.Fatalf("ListSecurityFindings(suppressed=%q) error = %v", value, err)
		}
		if sec.lastFilter.Suppressed != value {
			t.Fatalf("filter.Suppressed = %q, want %q", sec.lastFilter.Suppressed, value)
		}
	}

	_, err := srv.ListSecurityFindings(ctx, &platform.ListSecurityFindingsRequest{
		Namespace: "default", Suppressed: "bogus",
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("ListSecurityFindings(suppressed=bogus) error = %v, want InvalidArgument", err)
	}
}

func TestSecurityFindingProtoCarriesSuppressionFields(t *testing.T) {
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	suppressedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	rec := &store.SecurityFindingRecord{
		SuppressedBy:         "org-policy/noisy-vendor",
		SuppressedReason:     "vendored code",
		SuppressedOwner:      "appsec",
		SuppressionExpiresAt: &expires,
		SuppressedAt:         &suppressedAt,
	}
	pb := securityFindingProto(rec)
	if pb.GetSuppressedBy() != "org-policy/noisy-vendor" || pb.GetSuppressedReason() != "vendored code" ||
		pb.GetSuppressedOwner() != "appsec" ||
		!pb.GetSuppressionExpiresAt().AsTime().Equal(expires) || !pb.GetSuppressedAt().AsTime().Equal(suppressedAt) {
		t.Fatalf("proto suppression fields = %+v", pb)
	}
}

func TestGetSecurityFindingSummaryIncludeSuppressed(t *testing.T) {
	sec := newMockSecurityStore()
	sec.summary = map[string]int32{"total": 1}
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	if _, err := srv.GetSecurityFindingSummary(ctx, &platform.GetSecurityFindingSummaryRequest{Namespace: "default"}); err != nil {
		t.Fatalf("GetSecurityFindingSummary() error = %v", err)
	}
	if sec.summaryIncludeSuppressed {
		t.Fatal("includeSuppressed = true by default, want false")
	}
	if _, err := srv.GetSecurityFindingSummary(ctx, &platform.GetSecurityFindingSummaryRequest{
		Namespace: "default", IncludeSuppressed: true,
	}); err != nil {
		t.Fatalf("GetSecurityFindingSummary(include) error = %v", err)
	}
	if !sec.summaryIncludeSuppressed {
		t.Fatal("includeSuppressed not passed through to the store")
	}
}

func TestCreateSecurityScanPolicyPackRefRoundTrip(t *testing.T) {
	srv, c := newCronTestServer(t)
	srv.stateStore = newMockStateStore()
	ns := testUserNS()

	spec := fullSecurityScanSpec()
	spec.PolicyPackRef = "org-policy"
	resp, err := srv.CreateSecurityScan(projectActorCtx(), &platform.CreateSecurityScanRequest{
		Name: "nightly-scan",
		Spec: spec,
	})
	if err != nil {
		t.Fatalf("CreateSecurityScan() error = %v", err)
	}
	if resp.GetSpec().GetPolicyPackRef() != "org-policy" {
		t.Fatalf("proto policy_pack_ref = %q, want org-policy", resp.GetSpec().GetPolicyPackRef())
	}
	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly-scan"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if cr.Spec.PolicyPackRef == nil || cr.Spec.PolicyPackRef.Name != "org-policy" {
		t.Fatalf("cr.Spec.PolicyPackRef = %+v, want org-policy", cr.Spec.PolicyPackRef)
	}

	spec.PolicyPackRef = "Not Valid!"
	if _, err := srv.CreateSecurityScan(projectActorCtx(), &platform.CreateSecurityScanRequest{
		Name: "bad-scan", Spec: spec,
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("CreateSecurityScan(bad ref) error = %v, want InvalidArgument", err)
	}
}
