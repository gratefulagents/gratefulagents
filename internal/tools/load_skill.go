package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxSkillDescriptionLength = 240
	skillNameField            = "name"
)

// RegisterLoadSkillTool registers progressive skill loading for the skills
// enabled on a run. Skill summaries are advertised in the tool description;
// full instructions enter model context only when the model calls load_skill.
func RegisterLoadSkillTool(ctx context.Context, registry *Registry, k8sClient client.Client, run *platformv1alpha1.AgentRun) *LoadSkillTool {
	if registry == nil || k8sClient == nil || run == nil || len(run.Spec.SkillRefs) == 0 {
		return nil
	}

	allowed := make(map[string]struct{}, len(run.Spec.SkillRefs))
	for _, ref := range run.Spec.SkillRefs {
		if name := strings.TrimSpace(ref.Name); name != "" {
			allowed[name] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil
	}

	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	sort.Strings(names)

	summaries := make(map[string]string, len(names))
	for _, name := range names {
		skill := &platformv1alpha1.Skill{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, skill); err == nil {
			summaries[name] = skillDescription(skill)
		}
	}

	tool := &LoadSkillTool{
		k8sClient: k8sClient,
		namespace: run.Namespace,
		allowed:   allowed,
		names:     names,
		summaries: summaries,
		loaded:    make(map[string]string),
	}
	registry.Register(tool)
	return tool
}

// LoadSkillTool loads one enabled skill's complete instructions on demand.
type LoadSkillTool struct {
	k8sClient client.Client
	namespace string
	allowed   map[string]struct{}
	names     []string
	summaries map[string]string

	mu     sync.RWMutex
	loaded map[string]string
}

type loadSkillInput struct {
	Name string `json:"name"`
}

func (t *LoadSkillTool) Name() string { return "load_skill" }

func (t *LoadSkillTool) Description() string {
	var b strings.Builder
	b.WriteString("Load an enabled skill's full instructions into the current context. Use this when a skill is relevant to the user's task; skills are not loaded until you call this tool. Available skills:")
	for _, name := range t.names {
		b.WriteString("\n- ")
		b.WriteString(name)
		if description := strings.TrimSpace(t.summaries[name]); description != "" {
			b.WriteString(": ")
			b.WriteString(description)
		}
	}

	return b.String()
}

// LoadedInstructions returns the guidance explicitly loaded through the tool.
// The agent runtime reads this for each model turn and appends it to trusted
// agent instructions, so skill content is neither eager nor treated as an
// untrusted tool-output instruction block.
func (t *LoadSkillTool) LoadedInstructions() string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var b strings.Builder
	for _, name := range t.names {
		if instructions := t.loaded[name]; instructions != "" {
			if b.Len() == 0 {
				b.WriteString("# Loaded skill guidance")
			}
			b.WriteString("\n\n## Skill: ")
			b.WriteString(name)
			b.WriteString("\n")
			b.WriteString(instructions)
		}
	}
	return b.String()
}

func (t *LoadSkillTool) InputSchema() json.RawMessage {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			skillNameField: map[string]any{
				"type":        "string",
				"description": "The enabled skill to load.",
				"enum":        t.names,
			},
		},
		"required":             []string{skillNameField},
		"additionalProperties": false,
	}
	encoded, _ := json.Marshal(schema)
	return encoded
}

func (t *LoadSkillTool) IsReadOnly() bool                      { return true }
func (t *LoadSkillTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *LoadSkillTool) NeedsApproval() bool                   { return false }
func (t *LoadSkillTool) TimeoutSeconds() int                   { return 30 }

func (t *LoadSkillTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in loadSkillInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	name := strings.TrimSpace(in.Name)
	if _, ok := t.allowed[name]; !ok {
		return Result{Content: fmt.Sprintf("skill %q is not enabled for this run", name), IsError: true}, nil
	}

	skill := &platformv1alpha1.Skill{}
	if err := t.k8sClient.Get(ctx, client.ObjectKey{Namespace: t.namespace, Name: name}, skill); err != nil {
		return Result{Content: fmt.Sprintf("failed to load skill %q: %v", name, err), IsError: true}, nil
	}
	instructions := skillInstructions(skill)
	if instructions == "" {
		return Result{Content: fmt.Sprintf("skill %q has no resolved instructions yet", name), IsError: true}, nil
	}

	t.mu.Lock()
	t.loaded[name] = instructions
	t.mu.Unlock()
	return Result{Content: fmt.Sprintf("Skill %q loaded into the current context.", name)}, nil
}

func skillDescription(skill *platformv1alpha1.Skill) string {
	if skill == nil {
		return ""
	}
	description := strings.TrimSpace(skill.Spec.Description)
	if skill.Status.Resolved != nil {
		if resolved := strings.TrimSpace(skill.Status.Resolved.Description); resolved != "" {
			description = resolved
		}
	}
	runes := []rune(description)
	if len(runes) > maxSkillDescriptionLength {
		description = string(runes[:maxSkillDescriptionLength]) + "…"
	}
	return description
}

func skillInstructions(skill *platformv1alpha1.Skill) string {
	if skill == nil || strings.EqualFold(strings.TrimSpace(skill.Status.Phase), "Invalid") {
		return ""
	}
	if skill.Status.Resolved != nil {
		if instructions := strings.TrimSpace(skill.Status.Resolved.Instructions); instructions != "" {
			return instructions
		}
	}
	if skill.Spec.Source.Inline != nil {
		return strings.TrimSpace(skill.Spec.Source.Inline.Instructions)
	}
	return ""
}
