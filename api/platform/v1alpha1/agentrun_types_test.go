package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRoleInstructionRoutingDeepCopy(t *testing.T) {
	role := &RoleInstruction{Spec: RoleInstructionSpec{
		ModelsByProvider: map[string]string{"openai": "gpt-5.6-sol"}, ReasoningLevel: ReasoningMax,
	}}
	clone := role.DeepCopy()
	clone.Spec.ModelsByProvider["openai"] = "gpt-5.6-terra"
	if got := role.Spec.ModelsByProvider["openai"]; got != "gpt-5.6-sol" {
		t.Fatalf("DeepCopy shared provider model map: original = %q", got)
	}
	if clone.Spec.ReasoningLevel != ReasoningMax {
		t.Fatalf("DeepCopy reasoning = %q, want max", clone.Spec.ReasoningLevel)
	}
}

func TestAgentRunRoleModelOverridesDeepCopy(t *testing.T) {
	run := &AgentRun{Spec: AgentRunSpec{RoleModelOverrides: []AgentRunRoleModelOverride{{
		Role: "explore", ModelsByProvider: map[string]string{"openai": "gpt-5.6-terra"},
	}}}}
	clone := run.DeepCopy()
	clone.Spec.RoleModelOverrides[0].ModelsByProvider["openai"] = "gpt-5.4-mini"
	if got := run.Spec.RoleModelOverrides[0].ModelsByProvider["openai"]; got != "gpt-5.6-terra" {
		t.Fatalf("DeepCopy shared role model map: original = %q", got)
	}
}

func TestAgentRunTeamModeJSONShape(t *testing.T) {
	run := AgentRun{
		Spec: AgentRunSpec{
			WorkflowMode:  WorkflowModeChat,
			ExecutionMode: ExecutionModeTeam,
			Trigger:       TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:    RepositoryContext{URL: "https://github.com/example/repo.git"},
			AuthMode:      AgentRunAuthModeOAuth,
			Secrets:       &AgentRunSecrets{OpenAIOAuthSecret: "openai-oauth"},
			Team: &AgentRunTeamSpec{
				Steps: []AgentRunTeamStep{
					{
						Name: "parallel-implementers",
						Type: TeamStepTypeParallel,
						Tasks: []AgentRunTeamTask{
							{
								Name:              "worker-a",
								Role:              "executor",
								Objective:         "Implement schema freeze",
								RuntimeProfileRef: &NamedRef{Name: "team-runtime"},
								DependsOn:         []string{"schema-freeze"},
								MaxRetries:        2,
								ArtifactContract:  "patch/diff",
							},
						},
					},
				},
				DelegationPolicy: &AgentRunDelegationPolicy{
					MaxChildren: 4,
					MaxDepth:    1,
					ParentOnly:  true,
				},
				CompletionPolicy: &AgentRunCompletionPolicy{
					RequireApproval: true,
				},
			},
		},
		Status: AgentRunStatus{
			TeamSummary: &AgentRunTeamSummary{
				CurrentStepIndex: 0,
				CurrentStep:      "parallel-implementers",
				ApprovalState:    "pending",
				TotalChildren:    2,
				RunningChildren:  1,
				PendingChildren:  1,
			},
			Children: []AgentRunChildStatus{
				{
					Name:      "worker-a",
					Namespace: "default",
					Step:      "parallel-implementers",
					Role:      "executor",
					Phase:     AgentRunPhaseRunning,
				},
			},
		},
	}

	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	jsonText := string(raw)
	for _, fragment := range []string{
		`"executionMode":"team"`,
		`"authMode":"oauth"`,
		`"secrets":{"openaiOAuthSecret":"openai-oauth"}`,
		`"team":{"steps":[{"name":"parallel-implementers","type":"parallel"`,
		`"delegationPolicy":{"maxChildren":4,"maxDepth":1,"parentOnly":true}`,
		`"teamSummary":{"currentStep":"parallel-implementers"`,
		`"children":[{"name":"worker-a","namespace":"default","step":"parallel-implementers","role":"executor","phase":"Running"}]`,
	} {
		if !strings.Contains(jsonText, fragment) {
			t.Fatalf("expected marshaled JSON to contain %q, got %s", fragment, jsonText)
		}
	}
}

func TestAgentRunOverseerJSONShape(t *testing.T) {
	lastVerdictTime := metav1.NewTime(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	run := AgentRun{
		Spec: AgentRunSpec{
			Trigger:    TriggerRef{Kind: "Task", Name: "payments"},
			Repository: RepositoryContext{URL: "https://github.com/example/repo.git"},
			Overseer: &AgentRunOverseerSpec{
				ModeRef:          &ModeRef{Name: "overseer", Version: "v1"},
				Model:            "gpt-5",
				Authority:        AgentRunOverseerAuthorityEnforce,
				IntervalMinutes:  15,
				MaxInterventions: 3,
			},
		},
		Status: AgentRunStatus{
			OverseerSummary: &AgentRunOverseerStatus{
				RunName:                  "payments-overseer",
				State:                    "running",
				CheckpointsHandled:       7,
				InterventionsUsed:        2,
				CompletionRejectionsUsed: 1,
				LastVerdict:              OverseerVerdictSteer,
				LastSummary:              "Verification evidence is incomplete.",
				LastVerdictTime:          &lastVerdictTime,
			},
		},
	}

	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	jsonText := string(raw)
	for _, fragment := range []string{
		`"overseer":{"modeRef":{"name":"overseer","version":"v1"},"model":"gpt-5","authority":"enforce","intervalMinutes":15,"maxInterventions":3}`,
		`"overseerSummary":{"runName":"payments-overseer","state":"running","checkpointsHandled":7,"interventionsUsed":2,"completionRejectionsUsed":1,"lastVerdict":"steer","lastSummary":"Verification evidence is incomplete.","lastVerdictTime":"2026-01-02T03:04:05Z"}`,
	} {
		if !strings.Contains(jsonText, fragment) {
			t.Fatalf("expected marshaled JSON to contain %q, got %s", fragment, jsonText)
		}
	}
}

func TestOverseerConstants(t *testing.T) {
	constants := map[string]string{
		"authority observe":         string(AgentRunOverseerAuthorityObserve),
		"authority advise":          string(AgentRunOverseerAuthorityAdvise),
		"authority enforce":         string(AgentRunOverseerAuthorityEnforce),
		"verdict annotation":        OverseerVerdictAnnotation,
		"guidance annotation":       OverseerGuidanceAnnotation,
		"summary annotation":        OverseerSummaryAnnotation,
		"input response annotation": OverseerInputResponseAnnotation,
		"detaching annotation":      OverseerDetachingAnnotation,
		"verdict all clear":         OverseerVerdictAllClear,
		"verdict steer":             OverseerVerdictSteer,
		"verdict reject completion": OverseerVerdictRejectCompletion,
		"verdict resolve input":     OverseerVerdictResolveInput,
		"verdict escalate":          OverseerVerdictEscalate,
	}
	want := map[string]string{
		"authority observe":         "observe",
		"authority advise":          "advise",
		"authority enforce":         "enforce",
		"verdict annotation":        "platform.gratefulagents.dev/overseer-verdict",
		"guidance annotation":       "platform.gratefulagents.dev/overseer-guidance",
		"summary annotation":        "platform.gratefulagents.dev/overseer-summary",
		"input response annotation": "platform.gratefulagents.dev/overseer-input-response",
		"detaching annotation":      "platform.gratefulagents.dev/overseer-detaching",
		"verdict all clear":         "all_clear",
		"verdict steer":             "steer",
		"verdict reject completion": "reject_completion",
		"verdict resolve input":     "resolve_input",
		"verdict escalate":          "escalate",
	}
	for name, got := range constants {
		if got != want[name] {
			t.Errorf("%s = %q, want %q", name, got, want[name])
		}
	}
}

func TestNormalizeGitRemoteWrites(t *testing.T) {
	for _, tc := range []struct {
		in   GitRemoteWrites
		want GitRemoteWrites
	}{
		{"", GitRemoteWritesEnabled},
		{GitRemoteWritesEnabled, GitRemoteWritesEnabled},
		{GitRemoteWritesDisabled, GitRemoteWritesDisabled},
		{"unexpected", GitRemoteWritesDisabled},
	} {
		if got := NormalizeGitRemoteWrites(tc.in); got != tc.want {
			t.Errorf("NormalizeGitRemoteWrites(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMostRestrictivePermissionMode(t *testing.T) {
	cases := []struct {
		a, b, want PermissionMode
	}{
		{"", "", ""},
		{PermissionModeReadOnly, "", PermissionModeReadOnly},
		{"", PermissionModeReadOnly, PermissionModeReadOnly},
		{PermissionModeDangerFullAccess, PermissionModeReadOnly, PermissionModeReadOnly},
		{PermissionModeReadOnly, PermissionModeDangerFullAccess, PermissionModeReadOnly},
		{PermissionModeWorkspaceWrite, PermissionModeDangerFullAccess, PermissionModeWorkspaceWrite},
		{PermissionModeDangerFullAccess, PermissionModeDangerFullAccess, PermissionModeDangerFullAccess},
	}
	for _, tc := range cases {
		if got := MostRestrictivePermissionMode(tc.a, tc.b); got != tc.want {
			t.Errorf("MostRestrictivePermissionMode(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}
