package store

import (
	"encoding/json"
	"testing"
)

func policyEvent(actor, check, disposition string) SecurityFindingEvent {
	detail, _ := json.Marshal(map[string]string{"execution_id": "exec-1", "policy_check": check, "policy_disposition": disposition})
	return SecurityFindingEvent{EventType: "policy_disposition", Actor: actor, Detail: detail}
}

func TestUnreproducibleEnvIsValidOnlyForBountyAndReproduction(t *testing.T) {
	for check, want := range map[string]bool{"bounty": true, "reproduction": true, "scope": false, "prior_art": false, "": false} {
		if got := ValidSecurityFindingPolicyDecision(check, SecurityFindingPolicyDispositionUnreproducibleEnv); got != want {
			t.Errorf("ValidSecurityFindingPolicyDecision(%q, unreproducible_env) = %v, want %v", check, got, want)
		}
	}
	for _, disposition := range []string{"reproduced", "not_reproduced", "not_ready"} {
		if !ValidSecurityFindingPolicyDecision("reproduction", disposition) {
			t.Errorf("reproduction check rejects %q", disposition)
		}
	}
	if ValidSecurityFindingPolicyDecision("reproduction", "accepted") {
		t.Error("reproduction check must not accept the bounty verdict")
	}
}

// An environment that could not run the PoC blocks packaging like not_ready,
// leaves the technical status alone, and is never a definitive exclusion.
func TestUnreproducibleEnvBlocksPackagingWithoutExcluding(t *testing.T) {
	for _, check := range []string{"bounty", "reproduction"} {
		events := []SecurityFindingEvent{policyEvent("secscan-run", check, SecurityFindingPolicyDispositionUnreproducibleEnv)}
		if got := SecurityFindingBlockingPolicyDisposition(events, "exec-1"); got != SecurityFindingPolicyDispositionUnreproducibleEnv {
			t.Errorf("%s: blocking disposition = %q, want unreproducible_env", check, got)
		}
		if got := SecurityFindingDefinitivePreBountyExclusion(events, "exec-1"); got != "" {
			t.Errorf("%s: definitive exclusion = %q, want none for an inconclusive environment", check, got)
		}
		if got := SecurityFindingBlockingPolicyDisposition(events, "exec-2"); got != "" {
			t.Errorf("%s: another execution is blocked by %q", check, got)
		}
	}

	// A newer reproduction verdict supersedes the environment block; a newer
	// bounty acceptance supersedes everything.
	superseded := []SecurityFindingEvent{
		policyEvent("secscan-run-r2", "reproduction", "reproduced"),
		policyEvent("secscan-run", "reproduction", SecurityFindingPolicyDispositionUnreproducibleEnv),
	}
	if got := SecurityFindingBlockingPolicyDisposition(superseded, "exec-1"); got != "" {
		t.Errorf("superseded environment block still blocks with %q", got)
	}
	accepted := []SecurityFindingEvent{
		policyEvent("secscan-gate", "bounty", "accepted"),
		policyEvent("secscan-run", "reproduction", SecurityFindingPolicyDispositionUnreproducibleEnv),
	}
	if got := SecurityFindingBlockingPolicyDisposition(accepted, "exec-1"); got != "" {
		t.Errorf("bounty acceptance did not supersede the environment block: %q", got)
	}
	stale := []SecurityFindingEvent{
		policyEvent("secscan-run", "reproduction", SecurityFindingPolicyDispositionUnreproducibleEnv),
		policyEvent("secscan-run-old", "reproduction", "reproduced"),
	}
	if got := SecurityFindingBlockingPolicyDisposition(stale, "exec-1"); got != SecurityFindingPolicyDispositionUnreproducibleEnv {
		t.Errorf("newest reproduction decision must win, got %q", got)
	}
}

func TestSecurityFindingRetryablePolicyDisposition(t *testing.T) {
	events := []SecurityFindingEvent{
		policyEvent("other-run", "bounty", "accepted"),
		policyEvent("this-run", "reproduction", SecurityFindingPolicyDispositionUnreproducibleEnv),
		policyEvent("this-run", "scope", "scope_eligible"),
	}
	if got := SecurityFindingRetryablePolicyDisposition(events, "exec-1", "this-run"); got != SecurityFindingPolicyDispositionUnreproducibleEnv {
		t.Errorf("retryable disposition = %q, want unreproducible_env from this run's newest decision", got)
	}
	if got := SecurityFindingRetryablePolicyDisposition(events, "exec-1", "other-run"); got != "" {
		t.Errorf("a real verdict is retryable: %q", got)
	}
	if got := SecurityFindingRetryablePolicyDisposition(events, "exec-2", "this-run"); got != "" {
		t.Errorf("another execution's events leaked: %q", got)
	}
	verdictAfter := []SecurityFindingEvent{
		policyEvent("this-run", "reproduction", "not_reproduced"),
		policyEvent("this-run", "reproduction", "not_ready"),
	}
	if got := SecurityFindingRetryablePolicyDisposition(verdictAfter, "exec-1", "this-run"); got != "" {
		t.Errorf("a newer verdict from the same run must end the retry: %q", got)
	}
	if got := SecurityFindingRetryablePolicyDisposition([]SecurityFindingEvent{policyEvent("this-run", "bounty", "not_ready")}, "exec-1", "this-run"); got != "not_ready" {
		t.Errorf("not_ready = %q, want retryable", got)
	}
}
