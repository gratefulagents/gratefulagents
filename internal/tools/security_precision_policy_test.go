package tools

import (
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

func TestSecuritySubmissionPrecisionPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		published int32
		precision *store.SecuritySubmissionPrecision
		want      int32
		gate      string
	}{
		{name: "cold start", published: 10, precision: &store.SecuritySubmissionPrecision{Accepted: 1, Rejected: 3}, want: 10, gate: "cold-start-minimum-5"},
		{name: "low signal", published: 10, precision: &store.SecuritySubmissionPrecision{Accepted: 0, Duplicate: 2, Informative: 2, Rejected: 1}, want: 1, gate: "strict-perfect-verification"},
		{name: "medium signal", published: 10, precision: &store.SecuritySubmissionPrecision{Accepted: 1, Duplicate: 2, Rejected: 2}, want: 2, gate: "heightened-independent-reproduction"},
		{name: "healthy signal", published: 10, precision: &store.SecuritySubmissionPrecision{Accepted: 2, Duplicate: 1, Informative: 1, Rejected: 1}, want: 10, gate: "standard-program-evidence"},
		{name: "never raises cap", published: 1, precision: &store.SecuritySubmissionPrecision{Accepted: 2, Duplicate: 3}, want: 1, gate: "standard-program-evidence"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, gate := securitySubmissionPrecisionPolicy(tc.published, tc.precision)
			if got != tc.want || gate != tc.gate {
				t.Fatalf("policy = (%d, %q), want (%d, %q)", got, gate, tc.want, tc.gate)
			}
		})
	}
}
