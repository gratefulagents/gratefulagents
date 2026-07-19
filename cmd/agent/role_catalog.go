package main

import (
	"context"
	"sort"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// defaultRoleToolAccess carries the built-in tool-access defaults for known
// role names, applied when a RoleInstruction CRD omits spec.toolAccess.
// (Formerly sdkmode.AgentCatalog, removed from the SDK in v0.0.36.)
var defaultRoleToolAccess = map[string]string{
	"explore":               "read-only",
	"analyst":               "analysis",
	"planner":               "analysis",
	"architect":             "read-only",
	"debugger":              "analysis",
	"executor":              "execution",
	"team-executor":         "execution",
	"style-reviewer":        "read-only",
	"quality-reviewer":      "read-only",
	"api-reviewer":          "read-only",
	"security-reviewer":     "read-only",
	"performance-reviewer":  "read-only",
	"code-reviewer":         "read-only",
	"dependency-expert":     "analysis",
	"test-engineer":         "execution",
	"quality-strategist":    "analysis",
	"build-fixer":           "execution",
	"designer":              "execution",
	"writer":                "execution",
	"qa-tester":             "execution",
	"git-master":            "execution",
	"code-simplifier":       "execution",
	"researcher":            "analysis",
	"product-manager":       "analysis",
	"ux-researcher":         "analysis",
	"information-architect": "analysis",
	"product-analyst":       "analysis",
	"critic":                "read-only",
	"vision":                "read-only",
}

type resolvedRoleCatalog struct {
	Roles           agent.RoleCatalog
	ReasoningLevels map[string]string
}

func parentModelSettingsForTurn(base, overrides agent.ModelSettings) agent.ModelSettings {
	settings := base.Merge(overrides)
	if strings.TrimSpace(settings.ReasoningEffort) == "" {
		settings = settings.Merge(agent.ModeRoutingSettings("max", ""))
	}
	return settings
}

// loadRoleCatalog maps operator RoleInstruction CRDs into the SDK-native role
// catalog contract and resolves each role's model for the active provider. A
// run-scoped user override wins only for its matching role and provider.
func loadRoleCatalog(ctx context.Context, c client.Client, provider string, userOverrides []platformv1alpha1.AgentRunRoleModelOverride) (resolvedRoleCatalog, error) {
	list := &platformv1alpha1.RoleInstructionList{}
	if err := c.List(ctx, list); err != nil {
		return resolvedRoleCatalog{}, err
	}
	userModels := make(map[string]map[string]string, len(userOverrides))
	for _, override := range userOverrides {
		role := strings.TrimSpace(override.Role)
		if role != "" {
			userModels[role] = override.ModelsByProvider
		}
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Name < list.Items[j].Name })
	resolved := resolvedRoleCatalog{
		Roles:           make(agent.RoleCatalog, 0, len(list.Items)),
		ReasoningLevels: make(map[string]string, len(list.Items)),
	}
	for _, ri := range list.Items {
		toolAccess := ri.Spec.ToolAccess
		if toolAccess == "" {
			toolAccess = defaultRoleToolAccess[ri.Name]
		}
		resolved.Roles = append(resolved.Roles, agent.RoleSpec{
			Name:          ri.Name,
			Description:   ri.Spec.Description,
			Instructions:  ri.Spec.Instructions,
			ToolAccess:    toolAccess,
			ModelOverride: roleModelForProviderWithOverride(ri.Spec, provider, userModels[ri.Name]),
		})
		if level := strings.TrimSpace(string(ri.Spec.ReasoningLevel)); level != "" {
			resolved.ReasoningLevels[ri.Name] = level
		}
	}
	return resolved, nil
}

// roleModelForProvider applies the platform role-model precedence contract:
// provider-specific model, then empty (which tells specialist construction to
// inherit the parent run's model). A model for one provider must never leak
// into another provider's run.
func roleModelForProvider(spec platformv1alpha1.RoleInstructionSpec, provider string) string {
	return roleModelForProviderWithOverride(spec, provider, nil)
}

// roleModelForProviderWithOverride adds a run-scoped personal provider model
// ahead of the platform defaults. Personal overrides are deliberately
// provider-specific so switching providers cannot carry an incompatible model.
func roleModelForProviderWithOverride(spec platformv1alpha1.RoleInstructionSpec, provider string, userModels map[string]string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if model := roleProviderModel(userModels, provider); model != "" {
		return providerQualifiedRoleModel(provider, model)
	}
	if model := roleProviderModel(spec.ModelsByProvider, provider); model != "" {
		return providerQualifiedRoleModel(provider, model)
	}
	return ""
}

// roleProviderModel prefers the canonical normalized key and otherwise uses a
// stable lexical order for case-insensitive legacy keys. This keeps direct
// Kubernetes/GitOps objects deterministic even if they contain keys that only
// differ by case; dashboard writes always normalize keys before persistence.
func roleProviderModel(models map[string]string, provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || len(models) == 0 {
		return ""
	}
	if model, ok := models[provider]; ok {
		return strings.TrimSpace(model)
	}
	keys := make([]string, 0, len(models))
	for key := range models {
		if strings.EqualFold(strings.TrimSpace(key), provider) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if model := strings.TrimSpace(models[key]); model != "" {
			return model
		}
	}
	return ""
}

// providerQualifiedRoleModel keeps provider selection explicit so role routing
// remains correct when a session switches provider without rebuilding its
// multi-provider runtime. A model selected from ModelsByProvider belongs to the
// map key even when its provider-native ID contains a slash (for example an
// OpenRouter model ID).
func providerQualifiedRoleModel(provider, model string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.TrimSpace(model)
	if model == "" || provider == "" {
		return model
	}
	prefix, _ := agent.ParseModelPrefix(model)
	if strings.EqualFold(strings.TrimSpace(prefix), provider) {
		return model
	}
	return provider + "/" + model
}

// roleCatalogWithoutModelOverrides preserves role membership and reasoning but
// avoids routing a newly selected provider through stale provider-qualified
// models when a catalog refresh fails.
func roleCatalogWithoutModelOverrides(catalog resolvedRoleCatalog) resolvedRoleCatalog {
	out := resolvedRoleCatalog{
		Roles:           make(agent.RoleCatalog, len(catalog.Roles)),
		ReasoningLevels: make(map[string]string, len(catalog.ReasoningLevels)),
	}
	copy(out.Roles, catalog.Roles)
	for i := range out.Roles {
		out.Roles[i].ModelOverride = ""
	}
	for role, level := range catalog.ReasoningLevels {
		out.ReasoningLevels[role] = level
	}
	return out
}

// cloneSpecialistAgents creates an immutable routing snapshot for a turn. A
// spawned async task keeps the clone it selected even if a later turn switches
// providers and reconfigures the session scheduler.
func cloneSpecialistAgents(specialists map[string]*agent.Agent) map[string]*agent.Agent {
	clones := make(map[string]*agent.Agent, len(specialists))
	for name, specialist := range specialists {
		if specialist != nil {
			clones[name] = specialist.Clone()
		}
	}
	return clones
}

// specialistAgentsForRoleCatalog resolves models and reasoning onto per-turn
// clones rather than mutating the persistent runtime catalog, which may still
// be referenced by queued or running async tasks.
func specialistAgentsForRoleCatalog(specialists map[string]*agent.Agent, catalog resolvedRoleCatalog, parentModel string, parentSettings agent.ModelSettings) map[string]*agent.Agent {
	clones := cloneSpecialistAgents(specialists)
	models := make(map[string]string, len(catalog.Roles))
	for _, role := range catalog.Roles {
		models[role.Name] = strings.TrimSpace(role.ModelOverride)
	}
	for name, specialist := range clones {
		model := models[name]
		if model == "" {
			model = strings.TrimSpace(parentModel)
		}
		specialist.Model = model
		specialist.ModelSettings = parentSettings
		if level := strings.TrimSpace(catalog.ReasoningLevels[name]); level != "" {
			specialist.ModelSettings = specialist.ModelSettings.Merge(agent.ModeRoutingSettings(level, ""))
		}
	}
	return clones
}

// handoffsForSpecialists shallow-clones handoff definitions and retargets them
// to the same per-turn specialist snapshot used by the async scheduler.
func handoffsForSpecialists(handoffs []*agent.Handoff, specialists map[string]*agent.Agent) []*agent.Handoff {
	out := make([]*agent.Handoff, 0, len(handoffs))
	for _, handoff := range handoffs {
		if handoff == nil {
			continue
		}
		clone := *handoff
		if handoff.Agent != nil {
			if specialist := specialists[handoff.Agent.Name]; specialist != nil {
				clone.Agent = specialist
			}
		}
		out = append(out, &clone)
	}
	return out
}
