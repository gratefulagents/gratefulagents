package triggers

import (
	"context"
	"testing"
	"time"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// securityProgramRankerCR builds a shipped-style ranker CR for the severity
// table transcriptions the resolver auto-selects.
func securityProgramRankerCR(namespace, name string) *triggersv1alpha1.SecurityRanker {
	return &triggersv1alpha1.SecurityRanker{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: triggersv1alpha1.SecurityRankerSpec{
			Description: "severity table transcription for " + name,
			Rules:       []string{"report the severity level from the " + name + " table by name"},
		},
	}
}

// securityProgramWithSeveritySystem returns a ready SecurityProgram governed
// by the given severity system.
func securityProgramWithSeveritySystem(namespace, system string) *triggersv1alpha1.SecurityProgram {
	program := securityTestProgram(namespace)
	program.Spec.SeveritySystem = system
	program.Status.ContentDigest = securitySpecHash(program.Spec)
	return program
}

// TestResolveSecurityScanProgramRanker pins the rule that severity comes from
// the governing program's own published table: resolving a program with a
// severity system attaches that platform's ranker to the rankers the prompt
// renders, without duplicating an operator's own reference and without
// failing the scan when the shipped ranker CR is not installed.
func TestResolveSecurityScanProgramRanker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		system      string
		scanRankers []triggersv1alpha1.SecurityResourceRef
		objects     func(namespace string) []client.Object
		want        []string
	}{
		{
			name:    "immunefi program resolves its own table",
			system:  string(triggersv1alpha1.SeveritySystemImmunefiV23),
			objects: func(ns string) []client.Object { return []client.Object{securityProgramRankerCR(ns, "immunefi-v2-3")} },
			want:    []string{"immunefi-v2-3"},
		},
		{
			name:    "code4rena program resolves its own table",
			system:  string(triggersv1alpha1.SeveritySystemCode4rena),
			objects: func(ns string) []client.Object { return []client.Object{securityProgramRankerCR(ns, "code4rena")} },
			want:    []string{"code4rena"},
		},
		{
			name:    "sherlock program resolves its own table",
			system:  string(triggersv1alpha1.SeveritySystemSherlock),
			objects: func(ns string) []client.Object { return []client.Object{securityProgramRankerCR(ns, "sherlock")} },
			want:    []string{"sherlock"},
		},
		{
			name:    "program without a severity system changes nothing",
			system:  "",
			objects: func(ns string) []client.Object { return []client.Object{securityProgramRankerCR(ns, "immunefi-v2-3")} },
			want:    nil,
		},
		{
			name:    "severity system without a shipped table changes nothing",
			system:  string(triggersv1alpha1.SeveritySystemCustom),
			objects: func(ns string) []client.Object { return []client.Object{securityProgramRankerCR(ns, "immunefi-v2-3")} },
			want:    nil,
		},
		{
			name:        "already referenced ranker is not duplicated",
			system:      string(triggersv1alpha1.SeveritySystemImmunefiV23),
			scanRankers: []triggersv1alpha1.SecurityResourceRef{{Name: "immunefi-v2-3"}},
			objects:     func(ns string) []client.Object { return []client.Object{securityProgramRankerCR(ns, "immunefi-v2-3")} },
			want:        []string{"immunefi-v2-3"},
		},
		{
			name:        "program ranker is appended after the scan's own rankers",
			system:      string(triggersv1alpha1.SeveritySystemSherlock),
			scanRankers: []triggersv1alpha1.SecurityResourceRef{{Name: "payments-ranker"}},
			objects: func(ns string) []client.Object {
				return []client.Object{securityTestRanker(ns), securityProgramRankerCR(ns, "sherlock")}
			},
			want: []string{"payments-ranker", "sherlock"},
		},
		{
			name:    "missing ranker CR does not fail resolution",
			system:  string(triggersv1alpha1.SeveritySystemImmunefiV23),
			objects: func(string) []client.Object { return nil },
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scan := securityScanTestScan()
			scan.Spec.SecurityProgramRef = &triggersv1alpha1.SecurityResourceRef{Name: "acme-bounty"}
			scan.Spec.RankerRefs = tt.scanRankers

			objects := append([]client.Object{securityProgramWithSeveritySystem(scan.Namespace, tt.system)}, tt.objects(scan.Namespace)...)
			_, k8sClient, _ := newSecurityScanReconciler(t, time.Now(), append(objects, scan)...)

			resolved, err := resolveSecurityScanRefs(context.Background(), k8sClient, scan)
			if err != nil {
				t.Fatalf("resolveSecurityScanRefs() error = %v", err)
			}
			var got []string
			for _, ranker := range resolved.spec.SeverityRankers {
				got = append(got, ranker.Name)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("resolved severity rankers = %v, want %v", got, tt.want)
			}
			for i, name := range tt.want {
				if got[i] != name {
					t.Fatalf("resolved severity rankers = %v, want %v", got, tt.want)
				}
			}
			for _, ranker := range resolved.spec.SeverityRankers {
				if ranker.Rules == "" {
					t.Errorf("resolved ranker %q carries no rules; the prompt would render an empty table", ranker.Name)
				}
			}

			// Re-resolving the same scan must produce the same rankers: the
			// program-derived entry is appended, never accumulated.
			again, err := resolveSecurityScanRefs(context.Background(), k8sClient, scan)
			if err != nil {
				t.Fatalf("second resolveSecurityScanRefs() error = %v", err)
			}
			if len(again.spec.SeverityRankers) != len(resolved.spec.SeverityRankers) {
				t.Fatalf("second resolution rankers = %d, want %d (resolution must be idempotent)",
					len(again.spec.SeverityRankers), len(resolved.spec.SeverityRankers))
			}
		})
	}
}

// TestResolveSecurityScanProgramRankerRecordsProvenance keeps the
// auto-selected ranker in the resolved-refs snapshot, so a run records which
// severity table it was judged against.
func TestResolveSecurityScanProgramRankerRecordsProvenance(t *testing.T) {
	t.Parallel()

	scan := securityScanTestScan()
	scan.Spec.SecurityProgramRef = &triggersv1alpha1.SecurityResourceRef{Name: "acme-bounty"}
	program := securityProgramWithSeveritySystem(scan.Namespace, string(triggersv1alpha1.SeveritySystemImmunefiV23))
	ranker := securityProgramRankerCR(scan.Namespace, "immunefi-v2-3")

	_, k8sClient, _ := newSecurityScanReconciler(t, time.Now(), scan, program, ranker)
	resolved, err := resolveSecurityScanRefs(context.Background(), k8sClient, scan)
	if err != nil {
		t.Fatalf("resolveSecurityScanRefs() error = %v", err)
	}
	found := false
	for _, ref := range resolved.refs {
		if ref.Kind == "SecurityRanker" && ref.Name == "immunefi-v2-3" {
			found = true
			if ref.Hash == "" {
				t.Error("resolved ranker ref carries no content hash")
			}
		}
	}
	if !found {
		t.Fatalf("resolved refs = %+v, want a SecurityRanker ref for immunefi-v2-3", resolved.refs)
	}
}
