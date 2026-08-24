package store

import (
	"encoding/json"
	"testing"
)

func TestValidSecurityVariantSweepCompletionEvidenceAcceptsStructuredEvidence(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"string lists", `{"searched_scope":["handlers"],"methods":["grep"],"evidence":["auth.go:10"],"summary":"no siblings"}`, true},
		{"structured lists", `{"searched_scope":["auth.go"],"methods":[{"name":"source review"}],"evidence":[{"path":"auth.go","result":"guarded"}],"summary":"no siblings"}`, true},
		{"structured scope", `{"searched_scope":{"paths":["auth.go"],"patterns":["authorize"]},"methods":[{"name":"source review"}],"evidence":[{"path":"auth.go"}],"summary":"no siblings"}`, true},
		{"empty evidence", `{"searched_scope":["auth.go"],"methods":["grep"],"evidence":[],"summary":"no siblings"}`, false},
		{"null evidence", `{"searched_scope":["auth.go"],"methods":["grep"],"evidence":null,"summary":"no siblings"}`, false},
		{"null list item", `{"searched_scope":["auth.go"],"methods":[null],"evidence":["line 1"],"summary":"no siblings"}`, false},
		{"empty object item", `{"searched_scope":[{}],"methods":["grep"],"evidence":["line 1"],"summary":"no siblings"}`, false},
		{"blank item", `{"searched_scope":["auth.go"],"methods":["  "],"evidence":["line 1"],"summary":"no siblings"}`, false},
		{"scalar evidence", `{"searched_scope":["auth.go"],"methods":["grep"],"evidence":"line 1","summary":"no siblings"}`, false},
		{"empty summary", `{"searched_scope":["auth.go"],"methods":["grep"],"evidence":["line 1"],"summary":" "}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ValidSecurityVariantSweepCompletionEvidence(json.RawMessage(test.raw)); got != test.want {
				t.Fatalf("ValidSecurityVariantSweepCompletionEvidence() = %v, want %v", got, test.want)
			}
		})
	}
}
