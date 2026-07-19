package dashboard

import (
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func TestResolveReasoningLevelSupportsMax(t *testing.T) {
	got, err := resolveReasoningLevel(" Max ")
	if err != nil {
		t.Fatalf("resolveReasoningLevel(max): %v", err)
	}
	if got != platformv1alpha1.ReasoningMax {
		t.Fatalf("resolveReasoningLevel(max) = %q, want %q", got, platformv1alpha1.ReasoningMax)
	}
}

func TestResolveReasoningLevelErrorListsSupportedLevels(t *testing.T) {
	_, err := resolveReasoningLevel("extreme")
	if err == nil {
		t.Fatal("resolveReasoningLevel(extreme) unexpectedly succeeded")
	}
	if !strings.HasSuffix(err.Error(), "xhigh, max)") {
		t.Fatalf("error %q does not list max", err)
	}
}
