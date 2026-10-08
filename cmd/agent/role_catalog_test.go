package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/agentroles"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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

func TestRoleModelParentPreferenceBypassesPersonalAndPlatformMappings(t *testing.T) {
	spec := platformv1alpha1.RoleInstructionSpec{ModelsByProvider: map[string]string{"openai": "platform-model"}}
	got := roleModelForProviderWithOverride(spec, "openai", map[string]string{"openai": "personal-model"}, true)
	if got != "" {
		t.Fatalf("roleModelForProviderWithOverride() = %q, want parent inheritance", got)
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
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
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

	catalog, err := loadRoleCatalog(context.Background(), c, "openai", []platformv1alpha1.AgentRunRoleModelOverride{{
		Role: "explore", ModelsByProvider: map[string]string{"openai": "gpt-5.6-terra"},
	}})
	if err != nil {
		t.Fatalf("loadRoleCatalog: %v", err)
	}
	if len(catalog.Roles) != 3 || catalog.Roles[0].Name != "analyst" || catalog.Roles[1].Name != "explore" || catalog.Roles[2].Name != agentroles.GeneralName {
		t.Fatalf("catalog order = %#v", catalog.Roles)
	}
	if want := agentroles.SharedInstructions + "\n\nexplore"; catalog.Roles[1].Instructions != want {
		t.Fatalf("explore instructions = %q, want shared base prompt then role prompt", catalog.Roles[1].Instructions)
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

func roleCatalogTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func TestLoadRoleCatalogAlwaysOffersGeneralRole(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(roleCatalogTestScheme(t)).Build()

	catalog, err := loadRoleCatalog(context.Background(), c, "anthropic", nil)
	if err != nil {
		t.Fatalf("loadRoleCatalog: %v", err)
	}
	if len(catalog.Roles) != 1 {
		t.Fatalf("catalog = %#v, want only the built-in general role", catalog.Roles)
	}
	general := catalog.Roles[0]
	if general.Name != agentroles.GeneralName || general.ToolAccess != "full" || general.Description != agentroles.GeneralDescription {
		t.Fatalf("general role = %#v", general)
	}
	if general.ModelOverride != "" {
		t.Fatalf("general model = %q, want parent inheritance", general.ModelOverride)
	}
	if !strings.HasPrefix(general.Instructions, agentroles.SharedInstructions) || !strings.HasSuffix(general.Instructions, agentroles.GeneralInstructions) {
		t.Fatalf("general instructions = %q, want shared base prompt then general prompt", general.Instructions)
	}
	if _, ok := catalog.ReasoningLevels[agentroles.GeneralName]; ok {
		t.Fatal("general role must inherit the parent's reasoning level")
	}
}

func TestLoadRoleCatalogBuiltinGeneralHonorsPersonalModel(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(roleCatalogTestScheme(t)).Build()

	catalog, err := loadRoleCatalog(context.Background(), c, "openai", []platformv1alpha1.AgentRunRoleModelOverride{{
		Role: agentroles.GeneralName, ModelsByProvider: map[string]string{"openai": "gpt-5.6-terra"},
	}})
	if err != nil {
		t.Fatalf("loadRoleCatalog: %v", err)
	}
	if got := catalog.Roles[0].ModelOverride; got != "openai/gpt-5.6-terra" {
		t.Fatalf("general model = %q, want personal preference", got)
	}
}

func TestLoadRoleCatalogUsesGeneralRoleInstructionOverride(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(roleCatalogTestScheme(t)).WithObjects(
		&platformv1alpha1.RoleInstruction{
			ObjectMeta: metav1.ObjectMeta{Name: agentroles.GeneralName},
			Spec: platformv1alpha1.RoleInstructionSpec{
				Description:  "Team general agent",
				Instructions: "Follow the team runbook.",
				ToolAccess:   "execution",
			},
		},
	).Build()

	catalog, err := loadRoleCatalog(context.Background(), c, "openai", nil)
	if err != nil {
		t.Fatalf("loadRoleCatalog: %v", err)
	}
	if len(catalog.Roles) != 1 {
		t.Fatalf("catalog = %#v, want the override to replace the built-in general role", catalog.Roles)
	}
	general := catalog.Roles[0]
	if general.Description != "Team general agent" || general.ToolAccess != "execution" {
		t.Fatalf("general role = %#v, want the RoleInstruction override", general)
	}
	if want := agentroles.SharedInstructions + "\n\nFollow the team runbook."; general.Instructions != want {
		t.Fatalf("general instructions = %q, want %q", general.Instructions, want)
	}
}

func TestLoadRoleCatalogListFailureKeepsGeneralRole(t *testing.T) {
	listErr := errors.New("roleinstructions is forbidden")
	c := fake.NewClientBuilder().WithScheme(roleCatalogTestScheme(t)).WithInterceptorFuncs(interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return listErr
		},
	}).Build()

	catalog, err := loadRoleCatalog(context.Background(), c, "openai", nil)
	if !errors.Is(err, listErr) {
		t.Fatalf("loadRoleCatalog error = %v, want %v", err, listErr)
	}
	if len(catalog.Roles) != 1 || catalog.Roles[0].Name != agentroles.GeneralName {
		t.Fatalf("catalog = %#v, want the built-in general role despite the list failure", catalog.Roles)
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
