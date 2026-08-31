package store

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func validResearchArtifact() *SecurityResearchArtifact {
	return &SecurityResearchArtifact{
		RevisionID: uuid.New(), ExecutionID: "execution", TaskName: "investigate", Actor: "run",
		Kind: SecurityResearchArtifactTrace, SchemaVersion: SecurityTaskHandoffVersion,
		Payload: json.RawMessage(`{"steps":["trace"]}`), IdempotencyKey: "trace-1",
		CandidateFingerprints: []string{"fingerprint"},
		CoverageIDs:           map[string][]uuid.UUID{SecurityCoverageAdequatelyTested: {uuid.New()}},
		Conditions:            map[string]json.RawMessage{"ready": json.RawMessage(`true`)},
	}
}

func TestValidateSecurityTaskHandoffBoundsAndDeduplicates(t *testing.T) {
	handoff := SecurityTaskHandoff{
		Version: SecurityTaskHandoffVersion, ArtifactIDs: []uuid.UUID{uuid.New()},
		CandidateFingerprints: []string{"candidate"},
		CoverageIDs:           map[string][]uuid.UUID{SecurityCoverageAdequatelyTested: {uuid.New()}},
	}
	if err := ValidateSecurityTaskHandoff(handoff); err != nil {
		t.Fatalf("valid handoff: %v", err)
	}

	duplicate := handoff
	duplicate.ArtifactIDs = []uuid.UUID{handoff.ArtifactIDs[0], handoff.ArtifactIDs[0]}
	if err := ValidateSecurityTaskHandoff(duplicate); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate handoff error = %v", err)
	}

	overflow := handoff
	overflow.CandidateFingerprints = make([]string, MaxSecurityTaskHandoffReferences+1)
	for i := range overflow.CandidateFingerprints {
		overflow.CandidateFingerprints[i] = uuid.NewString()
	}
	if err := ValidateSecurityTaskHandoff(overflow); err == nil || !strings.Contains(err.Error(), "references") {
		t.Fatalf("overflow handoff error = %v", err)
	}
}

func TestValidateSecurityResearchArtifactBoundsAndScalars(t *testing.T) {
	if err := ValidateSecurityResearchArtifact(validResearchArtifact()); err != nil {
		t.Fatalf("valid artifact: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*SecurityResearchArtifact)
		want   string
	}{
		{name: "payload type", mutate: func(value *SecurityResearchArtifact) { value.Payload = json.RawMessage(`[]`) }, want: "JSON object"},
		{name: "payload bytes", mutate: func(value *SecurityResearchArtifact) {
			value.Payload, _ = json.Marshal(map[string]string{"value": string(bytes.Repeat([]byte("x"), MaxSecurityResearchArtifactPayloadBytes))})
		}, want: "payload must not exceed"},
		{name: "condition object", mutate: func(value *SecurityResearchArtifact) {
			value.Conditions = map[string]json.RawMessage{"not_scalar": json.RawMessage(`{"nested":true}`)}
		}, want: "JSON scalar"},
		{name: "reference ceiling", mutate: func(value *SecurityResearchArtifact) {
			value.CandidateFingerprints = make([]string, MaxSecurityResearchArtifactReferences+1)
			for i := range value.CandidateFingerprints {
				value.CandidateFingerprints[i] = uuid.NewString()
			}
			value.CoverageIDs = nil
		}, want: "references must not exceed"},
		{name: "duplicate coverage", mutate: func(value *SecurityResearchArtifact) {
			id := uuid.New()
			value.CoverageIDs = map[string][]uuid.UUID{SecurityCoverageAdequatelyTested: {id}, SecurityCoverageNotTested: {id}}
		}, want: "unique across verdicts"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validResearchArtifact()
			test.mutate(value)
			if err := ValidateSecurityResearchArtifact(value); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}
