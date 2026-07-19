package main

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestRoleModelForProvider(t *testing.T) {
	tests := []struct {
		name     string
		spec     platformv1alpha1.RoleInstructionSpec
		provider string
		want     string
	}{
		{
			name: "provider override wins",
			spec: platformv1alpha1.RoleInstructionSpec{
				Model: "fallback-model",
				ModelsByProvider: map[string]string{
					"OpenAI": "gpt-5.6-sol",
				},
			},
			provider: " openai ",
			want:     "openai/gpt-5.6-sol",
		},
		{
			name: "provider native slash remains on selected provider",
			spec: platformv1alpha1.RoleInstructionSpec{
				ModelsByProvider: map[string]string{
					"openrouter": "moonshotai/kimi-k2",
				},
			},
			provider: "openrouter",
			want:     "openrouter/moonshotai/kimi-k2",
		},
		{
			name: "already qualified provider override is unchanged",
			spec: platformv1alpha1.RoleInstructionSpec{
				ModelsByProvider: map[string]string{
					"copilot": "copilot/gpt-5.4",
				},
			},
			provider: "copilot",
			want:     "copilot/gpt-5.4",
		},
		{
			name:     "bare generic default does not replace missing provider model",
			spec:     platformv1alpha1.RoleInstructionSpec{Model: "luna"},
			provider: "anthropic",
			want:     "",
		},
		{
			name:     "qualified generic default does not replace missing provider model",
			spec:     platformv1alpha1.RoleInstructionSpec{Model: "anthropic/claude-sonnet-4-6"},
			provider: "openai",
			want:     "",
		},
		{
			name:     "empty role model inherits parent",
			spec:     platformv1alpha1.RoleInstructionSpec{},
			provider: "openai",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := roleModelForProvider(tt.spec, tt.provider); got != tt.want {
				t.Fatalf("roleModelForProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRoleModelForProviderMissingProviderAlwaysInheritsParent(t *testing.T) {
	spec := platformv1alpha1.RoleInstructionSpec{
		Model: "legacy-generic-default",
		ModelsByProvider: map[string]string{
			"openai": "gpt-5.6-sol",
		},
	}
	for _, provider := range []string{"anthropic", "copilot", "gemini", "openrouter", "groq"} {
		t.Run(provider, func(t *testing.T) {
			if got := roleModelForProvider(spec, provider); got != "" {
				t.Fatalf("roleModelForProvider() = %q, want parent inheritance", got)
			}
		})
	}
}

func TestLoadRoleCatalogResolvesProviderModelsAndSortsRoles(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&platformv1alpha1.RoleInstruction{
			ObjectMeta: metav1.ObjectMeta{Name: "explore"},
			Spec: platformv1alpha1.RoleInstructionSpec{
				Instructions:   "explore",
				Model:          "fallback-explore",
				ReasoningLevel: platformv1alpha1.ReasoningLow,
				ModelsByProvider: map[string]string{
					"openai": "gpt-5.6-sol",
				},
			},
		},
		&platformv1alpha1.RoleInstruction{
			ObjectMeta: metav1.ObjectMeta{Name: "analyst"},
			Spec:       platformv1alpha1.RoleInstructionSpec{Instructions: "analyze"},
		},
	).Build()

	catalog, err := loadRoleCatalog(context.Background(), client, "openai", []platformv1alpha1.AgentRunRoleModelOverride{{
		Role: "explore", ModelsByProvider: map[string]string{"openai": "gpt-5.6-terra"},
	}})
	if err != nil {
		t.Fatalf("loadRoleCatalog: %v", err)
	}
	if len(catalog.Roles) != 2 || catalog.Roles[0].Name != "analyst" || catalog.Roles[1].Name != "explore" {
		t.Fatalf("catalog order = %#v", catalog.Roles)
	}
	if catalog.Roles[0].ToolAccess != "analysis" || catalog.Roles[0].ModelOverride != "" {
		t.Fatalf("analyst catalog entry = %#v", catalog.Roles[0])
	}
	if catalog.Roles[1].ToolAccess != "read-only" || catalog.Roles[1].ModelOverride != "openai/gpt-5.6-terra" {
		t.Fatalf("explore catalog entry = %#v", catalog.Roles[1])
	}
	if catalog.ReasoningLevels["explore"] != "low" {
		t.Fatalf("explore reasoning = %q, want low", catalog.ReasoningLevels["explore"])
	}
}

func TestParentModelSettingsDefaultToMaxButRespectExplicitReasoning(t *testing.T) {
	if got := parentModelSettingsForTurn(agent.ModelSettings{}, agent.ModelSettings{}).ReasoningEffort; got != "max" {
		t.Fatalf("default parent reasoning = %q, want max", got)
	}
	explicit := agent.ModeRoutingSettings("high", "")
	if got := parentModelSettingsForTurn(agent.ModelSettings{}, explicit).ReasoningEffort; got != "high" {
		t.Fatalf("explicit parent reasoning = %q, want high", got)
	}
}

func TestSpecialistAgentsForRoleCatalogCreatesImmutableTurnSnapshot(t *testing.T) {
	specialists := map[string]*agent.Agent{
		"analyst": {Name: "analyst", Model: "old-analyst"},
		"explore": {Name: "explore", Model: "old-explore"},
	}
	catalog := resolvedRoleCatalog{
		Roles: agent.RoleCatalog{
			{Name: "analyst", ModelOverride: "openai/gpt-5.6-sol"},
			{Name: "explore"},
		},
		ReasoningLevels: map[string]string{"analyst": "max", "explore": "low"},
	}
	parentSettings := agent.ModeRoutingSettings("max", "")
	turnSpecialists := specialistAgentsForRoleCatalog(specialists, catalog, "openai/gpt-5.5", parentSettings)

	if got := turnSpecialists["analyst"].Model; got != "openai/gpt-5.6-sol" {
		t.Fatalf("turn analyst model = %q, want provider override", got)
	}
	if got := turnSpecialists["explore"].Model; got != "openai/gpt-5.5" {
		t.Fatalf("turn explore model = %q, want parent model", got)
	}
	if got := turnSpecialists["analyst"].ModelSettings.ReasoningEffort; got != "max" {
		t.Fatalf("turn analyst reasoning = %q, want max", got)
	}
	if got := turnSpecialists["explore"].ModelSettings.ReasoningEffort; got != "low" {
		t.Fatalf("turn explore reasoning = %q, want low", got)
	}
	if got := specialists["analyst"].Model; got != "old-analyst" {
		t.Fatalf("persistent analyst model was mutated to %q", got)
	}
	if turnSpecialists["analyst"] == specialists["analyst"] {
		t.Fatal("turn analyst must be a clone, not the persistent agent")
	}

	nextTurn := specialistAgentsForRoleCatalog(specialists, resolvedRoleCatalog{Roles: agent.RoleCatalog{
		{Name: "analyst", ModelOverride: "copilot/gpt-5.4"},
	}}, "copilot/gpt-5.4", parentSettings)
	if got := nextTurn["analyst"].Model; got != "copilot/gpt-5.4" {
		t.Fatalf("next-turn analyst model = %q, want switched provider model", got)
	}
	if got := turnSpecialists["analyst"].Model; got != "openai/gpt-5.6-sol" {
		t.Fatalf("previous turn snapshot changed to %q", got)
	}
}

func TestHandoffsForSpecialistsUsesTurnSnapshot(t *testing.T) {
	originalAgent := &agent.Agent{Name: "analyst", Model: "old-model"}
	original := agent.NewHandoff(originalAgent, agent.WithToolName("transfer_to_analyst"))
	turnAgent := &agent.Agent{Name: "analyst", Model: "openai/gpt-5.6-sol"}

	got := handoffsForSpecialists([]*agent.Handoff{original}, map[string]*agent.Agent{"analyst": turnAgent})

	if len(got) != 1 || got[0].Agent != turnAgent {
		t.Fatalf("turn handoff target = %#v, want turn specialist", got)
	}
	if got[0] == original {
		t.Fatal("turn handoff must be cloned")
	}
	if original.Agent != originalAgent {
		t.Fatal("original handoff target was mutated")
	}
}

func TestRoleProviderModelUsesCanonicalKeyDeterministically(t *testing.T) {
	models := map[string]string{
		"openai": "canonical",
		"OpenAI": "mixed-case",
		"OPENAI": "upper-case",
	}
	for range 100 {
		if got := roleProviderModel(models, "OpenAI"); got != "canonical" {
			t.Fatalf("roleProviderModel() = %q, want canonical", got)
		}
	}

	delete(models, "openai")
	for range 100 {
		if got := roleProviderModel(models, "openai"); got != "upper-case" {
			t.Fatalf("legacy roleProviderModel() = %q, want lexically first key", got)
		}
	}
}
