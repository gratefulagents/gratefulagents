package triggers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func runtimeProfileRef(name string) *platformv1alpha1.NamedRef {
	return &platformv1alpha1.NamedRef{Name: name}
}

func securityTestPolicyPack(namespace string) *triggersv1alpha1.SecurityPolicyPack {
	return &triggersv1alpha1.SecurityPolicyPack{
		ObjectMeta: metav1.ObjectMeta{Name: "org-policy", Namespace: namespace, Generation: 7},
		Spec: triggersv1alpha1.SecurityPolicyPackSpec{
			Description:    "org-wide floors",
			MinSeverity:    "low",
			FailOnSeverity: "high",
		},
	}
}

// TestApplySecurityPolicyPackPrecedence pins the per-field precedence and
// enforcement semantics: pack-only values default, scan overrides win when
// the field is not enforced, and enforced relaxations are rejected.
func TestApplySecurityPolicyPackPrecedence(t *testing.T) {
	workflow := []triggersv1alpha1.SecurityScanTask{
		{Name: "inj", Objective: "o", Category: "injection"},
		{Name: "auth", Objective: "o", Category: "auth"},
	}
	tests := []struct {
		name          string
		spec          triggersv1alpha1.SecurityScanSpec
		pack          triggersv1alpha1.SecurityPolicyPackSpec
		check         func(t *testing.T, out triggersv1alpha1.SecurityScanSpec)
		wantViolation string
	}{
		{
			name: "minSeverity pack-only default",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{MinSeverity: "medium"},
			check: func(t *testing.T, out triggersv1alpha1.SecurityScanSpec) {
				if out.MinSeverity != "medium" {
					t.Fatalf("MinSeverity = %q, want pack default medium", out.MinSeverity)
				}
			},
		},
		{
			name: "minSeverity scan override allowed when not enforced",
			spec: triggersv1alpha1.SecurityScanSpec{MinSeverity: "high"},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{MinSeverity: "low"},
			check: func(t *testing.T, out triggersv1alpha1.SecurityScanSpec) {
				if out.MinSeverity != "high" {
					t.Fatalf("MinSeverity = %q, want scan override high", out.MinSeverity)
				}
			},
		},
		{
			name: "minSeverity enforced raise rejected",
			spec: triggersv1alpha1.SecurityScanSpec{MinSeverity: "high"},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				MinSeverity: "low",
				Enforced:    []string{triggersv1alpha1.SecurityPolicyFieldMinSeverity},
			},
			wantViolation: "minSeverity",
		},
		{
			name: "minSeverity enforced lower is allowed",
			spec: triggersv1alpha1.SecurityScanSpec{MinSeverity: "info"},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				MinSeverity: "medium",
				Enforced:    []string{triggersv1alpha1.SecurityPolicyFieldMinSeverity},
			},
			check: func(t *testing.T, out triggersv1alpha1.SecurityScanSpec) {
				if out.MinSeverity != "info" {
					t.Fatalf("MinSeverity = %q, want info", out.MinSeverity)
				}
			},
		},
		{
			name: "failOnSeverity pack-only default",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{FailOnSeverity: "high"},
			check: func(t *testing.T, out triggersv1alpha1.SecurityScanSpec) {
				if out.FailOnSeverity != "high" {
					t.Fatalf("FailOnSeverity = %q, want pack default high", out.FailOnSeverity)
				}
			},
		},
		{
			name: "failOnSeverity scan override allowed when not enforced",
			spec: triggersv1alpha1.SecurityScanSpec{FailOnSeverity: "critical"},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{FailOnSeverity: "high"},
			check: func(t *testing.T, out triggersv1alpha1.SecurityScanSpec) {
				if out.FailOnSeverity != "critical" {
					t.Fatalf("FailOnSeverity = %q, want scan override critical", out.FailOnSeverity)
				}
			},
		},
		{
			name: "failOnSeverity enforced weakening rejected",
			spec: triggersv1alpha1.SecurityScanSpec{FailOnSeverity: "critical"},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				FailOnSeverity: "medium",
				Enforced:       []string{triggersv1alpha1.SecurityPolicyFieldFailOnSeverity},
			},
			wantViolation: "failOnSeverity",
		},
		{
			name: "failOnSeverity enforced empty scan takes pack default",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				FailOnSeverity: "medium",
				Enforced:       []string{triggersv1alpha1.SecurityPolicyFieldFailOnSeverity},
			},
			check: func(t *testing.T, out triggersv1alpha1.SecurityScanSpec) {
				if out.FailOnSeverity != "medium" {
					t.Fatalf("FailOnSeverity = %q, want pack default medium", out.FailOnSeverity)
				}
			},
		},
		{
			name: "failOnSeverity enforced stricter is allowed",
			spec: triggersv1alpha1.SecurityScanSpec{FailOnSeverity: "low"},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				FailOnSeverity: "high",
				Enforced:       []string{triggersv1alpha1.SecurityPolicyFieldFailOnSeverity},
			},
			check: func(t *testing.T, out triggersv1alpha1.SecurityScanSpec) {
				if out.FailOnSeverity != "low" {
					t.Fatalf("FailOnSeverity = %q, want low", out.FailOnSeverity)
				}
			},
		},
		{
			name: "dedupe pack-only default",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				Dedupe: &triggersv1alpha1.SecurityScanDedupe{SimilarityThresholdPermille: 900},
			},
			check: func(t *testing.T, out triggersv1alpha1.SecurityScanSpec) {
				if out.DedupeSimilarityThresholdPermille() != 900 {
					t.Fatalf("dedupe threshold = %d, want pack default 900", out.DedupeSimilarityThresholdPermille())
				}
			},
		},
		{
			name: "dedupe scan override allowed when not enforced",
			spec: triggersv1alpha1.SecurityScanSpec{
				Dedupe: &triggersv1alpha1.SecurityScanDedupe{Enabled: new(bool)},
			},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				Dedupe: &triggersv1alpha1.SecurityScanDedupe{SimilarityThresholdPermille: 900},
			},
			check: func(t *testing.T, out triggersv1alpha1.SecurityScanSpec) {
				if out.DedupeEnabled() {
					t.Fatal("DedupeEnabled() = true, want scan override false")
				}
			},
		},
		{
			name: "dedupe enforced disable rejected",
			spec: triggersv1alpha1.SecurityScanSpec{
				Dedupe: &triggersv1alpha1.SecurityScanDedupe{Enabled: new(bool)},
			},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				Enforced: []string{triggersv1alpha1.SecurityPolicyFieldDedupe},
			},
			wantViolation: "dedupe is disabled",
		},
		{
			name: "dedupe enforced looser threshold rejected",
			spec: triggersv1alpha1.SecurityScanSpec{
				Dedupe: &triggersv1alpha1.SecurityScanDedupe{SimilarityThresholdPermille: 500},
			},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				Dedupe:   &triggersv1alpha1.SecurityScanDedupe{SimilarityThresholdPermille: 900},
				Enforced: []string{triggersv1alpha1.SecurityPolicyFieldDedupe},
			},
			wantViolation: "similarityThresholdPermille 500 is looser",
		},
		{
			name: "dedupe enforced tighter threshold allowed",
			spec: triggersv1alpha1.SecurityScanSpec{
				Dedupe: &triggersv1alpha1.SecurityScanDedupe{SimilarityThresholdPermille: 950},
			},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				Dedupe:   &triggersv1alpha1.SecurityScanDedupe{SimilarityThresholdPermille: 900},
				Enforced: []string{triggersv1alpha1.SecurityPolicyFieldDedupe},
			},
			check: func(t *testing.T, out triggersv1alpha1.SecurityScanSpec) {
				if out.DedupeSimilarityThresholdPermille() != 950 {
					t.Fatalf("dedupe threshold = %d, want 950", out.DedupeSimilarityThresholdPermille())
				}
			},
		},
		{
			name: "requiredCategories not enforced is not checked",
			spec: triggersv1alpha1.SecurityScanSpec{Workflow: workflow},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{RequiredCategories: []string{"supply-chain"}},
		},
		{
			name: "requiredCategories enforced covered is allowed",
			spec: triggersv1alpha1.SecurityScanSpec{Workflow: workflow},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				RequiredCategories: []string{"injection", "auth"},
				Enforced:           []string{triggersv1alpha1.SecurityPolicyFieldRequiredCategories},
			},
		},
		{
			name: "requiredCategories enforced missing rejected",
			spec: triggersv1alpha1.SecurityScanSpec{Workflow: workflow},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				RequiredCategories: []string{"injection", "supply-chain"},
				Enforced:           []string{triggersv1alpha1.SecurityPolicyFieldRequiredCategories},
			},
			wantViolation: "supply-chain",
		},
		{
			name: "allowedRuntimeProfiles not enforced is not checked",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{AllowedRuntimeProfiles: []string{"locked-down"}},
		},
		{
			name: "allowedRuntimeProfiles enforced listed profile allowed",
			spec: triggersv1alpha1.SecurityScanSpec{Defaults: triggersv1alpha1.AgentRunDefaults{
				RuntimeProfileRef: runtimeProfileRef("locked-down"),
			}},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				AllowedRuntimeProfiles: []string{"locked-down"},
				Enforced:               []string{triggersv1alpha1.SecurityPolicyFieldAllowedRuntimeProfiles},
			},
		},
		{
			name: "allowedRuntimeProfiles enforced other profile rejected",
			spec: triggersv1alpha1.SecurityScanSpec{Defaults: triggersv1alpha1.AgentRunDefaults{
				RuntimeProfileRef: runtimeProfileRef("wide-open"),
			}},
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				AllowedRuntimeProfiles: []string{"locked-down"},
				Enforced:               []string{triggersv1alpha1.SecurityPolicyFieldAllowedRuntimeProfiles},
			},
			wantViolation: `runtime profile "wide-open"`,
		},
		{
			name: "allowedRuntimeProfiles enforced missing profile rejected",
			pack: triggersv1alpha1.SecurityPolicyPackSpec{
				AllowedRuntimeProfiles: []string{"locked-down"},
				Enforced:               []string{triggersv1alpha1.SecurityPolicyFieldAllowedRuntimeProfiles},
			},
			wantViolation: `runtime profile ""`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pack := &triggersv1alpha1.SecurityPolicyPack{
				ObjectMeta: metav1.ObjectMeta{Name: "org-policy", Namespace: "default"},
				Spec:       tt.pack,
			}
			out, violations := applySecurityPolicyPack(tt.spec, pack)
			if tt.wantViolation != "" {
				if len(violations) == 0 || !strings.Contains(strings.Join(violations, "; "), tt.wantViolation) {
					t.Fatalf("violations = %v, want one containing %q", violations, tt.wantViolation)
				}
				return
			}
			if len(violations) != 0 {
				t.Fatalf("violations = %v, want none", violations)
			}
			if tt.check != nil {
				tt.check(t, out)
			}
		})
	}
}

func TestApplySecurityPolicyPackAppendsDefaultRefs(t *testing.T) {
	spec := triggersv1alpha1.SecurityScanSpec{
		RankerRefs:     []triggersv1alpha1.SecurityResourceRef{{Name: "scan-ranker"}, {Name: "shared"}},
		PostScriptRefs: []triggersv1alpha1.SecurityResourceRef{{Name: "scan-poc"}},
	}
	pack := &triggersv1alpha1.SecurityPolicyPack{
		ObjectMeta: metav1.ObjectMeta{Name: "org-policy", Namespace: "default"},
		Spec: triggersv1alpha1.SecurityPolicyPackSpec{
			DefaultRankerRefs:     []triggersv1alpha1.SecurityResourceRef{{Name: "org-ranker"}, {Name: "shared"}},
			DefaultPostScriptRefs: []triggersv1alpha1.SecurityResourceRef{{Name: "org-poc"}},
		},
	}
	out, violations := applySecurityPolicyPack(spec, pack)
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want none", violations)
	}
	wantRankers := []triggersv1alpha1.SecurityResourceRef{{Name: "scan-ranker"}, {Name: "shared"}, {Name: "org-ranker"}}
	if len(out.RankerRefs) != len(wantRankers) {
		t.Fatalf("RankerRefs = %v, want %v (pack defaults appended, duplicates skipped)", out.RankerRefs, wantRankers)
	}
	for i := range wantRankers {
		if out.RankerRefs[i] != wantRankers[i] {
			t.Fatalf("RankerRefs[%d] = %v, want %v", i, out.RankerRefs[i], wantRankers[i])
		}
	}
	wantScripts := []triggersv1alpha1.SecurityResourceRef{{Name: "scan-poc"}, {Name: "org-poc"}}
	if len(out.PostScriptRefs) != len(wantScripts) || out.PostScriptRefs[1] != wantScripts[1] {
		t.Fatalf("PostScriptRefs = %v, want %v", out.PostScriptRefs, wantScripts)
	}
}

func TestSecurityScanPolicyViolationCreatesNoRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"}
	scan.Spec.MinSeverity = "critical"
	pack := securityTestPolicyPack(scan.Namespace)
	pack.Spec.Enforced = []string{triggersv1alpha1.SecurityPolicyFieldMinSeverity}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, pack)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want 0: a policy-violating scan must not create a run", len(runs))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, securityScanReasonPolicyViolation)
	cond := findSecurityScanReadyCondition(updated)
	if cond == nil {
		t.Fatal("scan has no Ready condition")
	}
	if !strings.Contains(cond.Message, "org-policy") || !strings.Contains(cond.Message, "minSeverity") {
		t.Fatalf("condition message = %q, want the pack and field named", cond.Message)
	}
}

func TestSecurityScanInvalidPolicyPackFailsClosed(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"}
	pack := securityTestPolicyPack(scan.Namespace)
	pack.Spec.Enforced = []string{"notAField"}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, pack)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want 0: an invalid pack must fail closed", len(runs))
	}
	assertSecurityScanCondition(t, getSecurityScan(t, k8sClient, scan), metav1.ConditionFalse, securityScanReasonPolicyViolation)
}

func TestSecurityScanMissingPolicyPackIsUnresolvedReference(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "missing"}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want 0", len(runs))
	}
	assertSecurityScanCondition(t, getSecurityScan(t, k8sClient, scan), metav1.ConditionFalse, securityScanReasonUnresolvedReference)
}

func TestSecurityScanPolicyPackSnapshotRecorded(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"}
	pack := securityTestPolicyPack(scan.Namespace)
	pack.Spec.MinSeverity = "medium"
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, pack)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	// The pack's minSeverity default must reach the run's reporting-policy
	// annotation (enforcement happens before prompt construction).
	if got := runs[0].Annotations[triggersv1alpha1.SecurityScanMinSeverityAnnotation]; got != "medium" {
		t.Fatalf("min-severity annotation = %q, want the pack default medium", got)
	}

	refsJSON := runs[0].Annotations[triggersv1alpha1.SecurityScanResolvedRefsAnnotation]
	if refsJSON == "" {
		t.Fatal("run is missing the resolved-refs annotation")
	}
	var refs []triggersv1alpha1.SecurityScanResolvedRef
	if err := json.Unmarshal([]byte(refsJSON), &refs); err != nil {
		t.Fatalf("resolved-refs annotation is not JSON: %v", err)
	}
	want := resolvedSecurityRef("SecurityPolicyPack", pack.Name, pack.Generation, pack.Spec)
	if len(refs) != 1 || refs[0] != want {
		t.Fatalf("resolved refs = %+v, want [%+v]", refs, want)
	}
	if refs[0].Hash == "" || refs[0].Generation != 7 {
		t.Fatalf("resolved pack ref has empty hash or wrong generation: %+v", refs[0])
	}

	updated := getSecurityScan(t, k8sClient, scan)
	if len(updated.Status.LastResolvedRefs) != 1 || updated.Status.LastResolvedRefs[0] != want {
		t.Fatalf("status.lastResolvedRefs = %+v, want [%+v]", updated.Status.LastResolvedRefs, want)
	}
}

// suppressionSweepStore records suppression sweep calls.
type suppressionSweepStore struct {
	store.SecurityFindingStore
	expiredNamespaces  []string
	appliedNamespace   string
	appliedScan        string
	appliedRules       []store.SecuritySuppressionRule
	revokeCalls        int
	revokedNamespace   string
	revokedScan        string
	revokedRules       []store.SecuritySuppressionRule
	revokedBeforeApply bool
}

func (s *suppressionSweepStore) ExpireSecuritySuppressions(_ context.Context, namespace string) (int32, error) {
	s.expiredNamespaces = append(s.expiredNamespaces, namespace)
	return 0, nil
}

func (s *suppressionSweepStore) ApplySecuritySuppressions(_ context.Context, namespace, scanName string, rules []store.SecuritySuppressionRule) (int32, error) {
	s.appliedNamespace, s.appliedScan = namespace, scanName
	s.appliedRules = append([]store.SecuritySuppressionRule(nil), rules...)
	return int32(len(rules)), nil
}

func (s *suppressionSweepStore) RevokeSecuritySuppressions(_ context.Context, namespace, scanName string, activeRules []store.SecuritySuppressionRule) (int32, error) {
	s.revokeCalls++
	s.revokedNamespace, s.revokedScan = namespace, scanName
	s.revokedRules = append([]store.SecuritySuppressionRule(nil), activeRules...)
	s.revokedBeforeApply = s.appliedRules == nil
	return 0, nil
}

func TestSweepSecuritySuppressionsAppliesPackRules(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"}
	pack := securityTestPolicyPack(scan.Namespace)
	past := metav1.NewTime(now.Add(-time.Hour))
	future := metav1.NewTime(now.Add(time.Hour))
	pack.Spec.Suppressions = []triggersv1alpha1.SecurityPolicySuppression{
		{
			Name: "noisy-vendor", Reason: "vendored code", Owner: "appsec",
			Matcher:   triggersv1alpha1.SecuritySuppressionMatcher{PathGlob: "vendor/*"},
			ExpiresAt: &future,
		},
		{
			Name: "already-expired", Reason: "old", Owner: "appsec",
			Matcher:   triggersv1alpha1.SecuritySuppressionMatcher{Category: "injection"},
			ExpiresAt: &past,
		},
	}
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan, pack)
	findings := &suppressionSweepStore{}
	reconciler.Findings = findings

	reconciler.sweepSecuritySuppressions(context.Background(), scan)

	if len(findings.expiredNamespaces) != 1 || findings.expiredNamespaces[0] != scan.Namespace {
		t.Fatalf("expire sweeps = %v, want [%s]", findings.expiredNamespaces, scan.Namespace)
	}
	if findings.appliedNamespace != scan.Namespace || findings.appliedScan != scan.Name {
		t.Fatalf("applied to %s/%s, want %s/%s", findings.appliedNamespace, findings.appliedScan, scan.Namespace, scan.Name)
	}
	if len(findings.appliedRules) != 1 {
		t.Fatalf("applied rules = %+v, want only the unexpired rule", findings.appliedRules)
	}
	rule := findings.appliedRules[0]
	if rule.ID != "org-policy/noisy-vendor" || rule.Owner != "appsec" || rule.Reason != "vendored code" ||
		rule.Matcher.PathGlob != "vendor/*" || rule.ExpiresAt == nil || !rule.ExpiresAt.Equal(future.Time) {
		t.Fatalf("applied rule = %+v", rule)
	}
	// Revoked suppressions (deleted rules, narrowed matchers) are cleared
	// with the same active rule set, before re-applying, so a finding
	// released by one rule can be re-suppressed by another in one sweep.
	if findings.revokeCalls != 1 || findings.revokedNamespace != scan.Namespace || findings.revokedScan != scan.Name {
		t.Fatalf("revoke calls = %d on %s/%s, want 1 on %s/%s",
			findings.revokeCalls, findings.revokedNamespace, findings.revokedScan, scan.Namespace, scan.Name)
	}
	if len(findings.revokedRules) != 1 || findings.revokedRules[0].ID != "org-policy/noisy-vendor" {
		t.Fatalf("revoked with rules = %+v, want the active rule set", findings.revokedRules)
	}
	if !findings.revokedBeforeApply {
		t.Fatal("revocation must run before the apply pass")
	}
}

func TestSweepSecuritySuppressionsWithoutPackRevokesAll(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan)
	findings := &suppressionSweepStore{}
	reconciler.Findings = findings

	reconciler.sweepSecuritySuppressions(context.Background(), scan)

	if len(findings.expiredNamespaces) != 1 {
		t.Fatalf("expire sweeps = %v, want one", findings.expiredNamespaces)
	}
	if findings.appliedRules != nil {
		t.Fatalf("applied rules = %+v, want none without a pack ref", findings.appliedRules)
	}
	// A scan without a policyPackRef has no active rules: every previously
	// granted suppression must be revoked, not left in place forever.
	if findings.revokeCalls != 1 || findings.revokedNamespace != scan.Namespace || findings.revokedScan != scan.Name {
		t.Fatalf("revoke calls = %d on %s/%s, want 1 on %s/%s",
			findings.revokeCalls, findings.revokedNamespace, findings.revokedScan, scan.Namespace, scan.Name)
	}
	if len(findings.revokedRules) != 0 {
		t.Fatalf("revoked with rules = %+v, want an empty active set", findings.revokedRules)
	}
}

func TestSweepSecuritySuppressionsDeletedPackRevokesAll(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "gone-policy"}
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan)
	findings := &suppressionSweepStore{}
	reconciler.Findings = findings

	reconciler.sweepSecuritySuppressions(context.Background(), scan)

	if findings.revokeCalls != 1 || len(findings.revokedRules) != 0 {
		t.Fatalf("revoke calls = %d with rules %+v, want one call with an empty active set for a deleted pack",
			findings.revokeCalls, findings.revokedRules)
	}
	if findings.appliedRules != nil {
		t.Fatalf("applied rules = %+v, want none for a deleted pack", findings.appliedRules)
	}
}

func TestSweepSecuritySuppressionsInvalidPackKeepsSuppressions(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"}
	pack := securityTestPolicyPack(scan.Namespace)
	pack.Spec.Enforced = []string{"notAField"}
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan, pack)
	findings := &suppressionSweepStore{}
	reconciler.Findings = findings

	reconciler.sweepSecuritySuppressions(context.Background(), scan)

	// An invalid pack's rule intent is unknowable: neither revoke nor apply.
	if findings.revokeCalls != 0 {
		t.Fatalf("revoke calls = %d, want 0 for an invalid pack", findings.revokeCalls)
	}
	if findings.appliedRules != nil {
		t.Fatalf("applied rules = %+v, want none for an invalid pack", findings.appliedRules)
	}
}
