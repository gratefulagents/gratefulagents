package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxSkillDescriptionLength = 240
	skillNameField            = "name"
)

// SkippedSkill names a referenced skill that load_skill could not offer and
// why, so the agent can log it instead of advertising a skill that fails on
// use.
type SkippedSkill struct {
	Name   string
	Reason string
}

// RegisterLoadSkillTool registers progressive skill loading for the skills
// enabled on a run. Skill summaries are advertised in the tool description;
// full instructions enter model context only when the model calls load_skill.
//
// implicit names are tool-companion skills (for example the computer-use
// guide when the computer_use tool is registered). They are offered without
// being listed in spec.skillRefs, but only when the Skill resource exists in
// the run's namespace, so an uninstalled companion never appears in the menu.
//
// Referenced skills that are missing, Invalid, or in Error without any
// resolved content are left out of the menu and returned as skipped. Skills
// recorded in status.policy.resolvedSkills are re-loaded so a replaced pod
// keeps the guidance the transcript says was loaded.
func RegisterLoadSkillTool(ctx context.Context, registry *Registry, k8sClient client.Client, run *platformv1alpha1.AgentRun, implicit ...string) (*LoadSkillTool, []SkippedSkill) {
	if registry == nil || k8sClient == nil || run == nil {
		return nil, nil
	}

	var skipped []SkippedSkill
	allowed := make(map[string]struct{})
	summaries := make(map[string]string)
	// usable reports whether a fetched skill can serve instructions; the
	// reason explains an exclusion for the run log.
	usable := func(skill *platformv1alpha1.Skill) (bool, string) {
		phase := strings.TrimSpace(skill.Status.Phase)
		switch {
		case strings.EqualFold(phase, "Invalid"):
			return false, "skill is Invalid: " + resolvedConditionMessage(skill)
		case strings.EqualFold(phase, "Error") && skillInstructions(skill) == "":
			return false, "skill has no resolved instructions: " + resolvedConditionMessage(skill)
		}
		return true, ""
	}

	for _, ref := range run.Spec.SkillRefs {
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			continue
		}
		if _, ok := allowed[name]; ok {
			continue
		}
		skill := &platformv1alpha1.Skill{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, skill); err != nil {
			if apierrors.IsNotFound(err) {
				skipped = append(skipped, SkippedSkill{Name: name, Reason: "skill not found in namespace " + run.Namespace})
				continue
			}
			// A transient read failure is not evidence the skill is unusable:
			// keep it offered without a summary rather than silently dropping it.
			allowed[name] = struct{}{}
			continue
		}
		if ok, reason := usable(skill); !ok {
			skipped = append(skipped, SkippedSkill{Name: name, Reason: reason})
			continue
		}
		allowed[name] = struct{}{}
		summaries[name] = skillDescription(skill)
	}
	for _, name := range implicit {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := allowed[name]; ok {
			continue
		}
		skill := &platformv1alpha1.Skill{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, skill); err != nil {
			continue
		}
		if ok, reason := usable(skill); !ok {
			skipped = append(skipped, SkippedSkill{Name: name, Reason: reason})
			continue
		}
		allowed[name] = struct{}{}
		summaries[name] = skillDescription(skill)
	}
	if len(allowed) == 0 {
		return nil, skipped
	}

	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	sort.Strings(names)

	tool := &LoadSkillTool{
		k8sClient: k8sClient,
		namespace: run.Namespace,
		runName:   run.Name,
		allowed:   allowed,
		names:     names,
		summaries: summaries,
		loaded:    make(map[string]string),
	}
	if run.Status.Policy != nil {
		tool.restoreLoaded(ctx, run.Status.Policy.ResolvedSkills)
	}
	registry.Register(tool)
	return tool, skipped
}

// restoreLoaded re-reads skills a previous pod of this run had loaded so the
// guidance survives pod replacement (pause/wake, restart).
func (t *LoadSkillTool) restoreLoaded(ctx context.Context, names []string) {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if _, ok := t.allowed[name]; !ok {
			continue
		}
		skill := &platformv1alpha1.Skill{}
		if err := t.k8sClient.Get(ctx, client.ObjectKey{Namespace: t.namespace, Name: name}, skill); err != nil {
			log.Printf("WARN: previously loaded skill %q could not be restored: %v", name, err)
			continue
		}
		instructions := skillInstructions(skill)
		if instructions == "" {
			log.Printf("WARN: previously loaded skill %q has no resolved instructions; not restored", name)
			continue
		}
		t.mu.Lock()
		t.loaded[name] = skillInstructionsWithProvenance(skill, instructions)
		t.mu.Unlock()
	}
}

func resolvedConditionMessage(skill *platformv1alpha1.Skill) string {
	for _, condition := range skill.Status.Conditions {
		if condition.Type == "Resolved" {
			if message := strings.TrimSpace(condition.Message); message != "" {
				return message
			}
			return condition.Reason
		}
	}
	return "see Skill status"
}

// LoadSkillTool loads one enabled skill's complete instructions on demand.
type LoadSkillTool struct {
	k8sClient client.Client
	namespace string
	runName   string
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
	t.loaded[name] = skillInstructionsWithProvenance(skill, instructions)
	t.mu.Unlock()
	t.persistLoaded(ctx)
	return Result{Content: fmt.Sprintf("Skill %q loaded into the current context.", name)}, nil
}

// LoadedNames returns the sorted names of skills loaded into context.
func (t *LoadSkillTool) LoadedNames() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	names := make([]string, 0, len(t.loaded))
	for name, instructions := range t.loaded {
		if instructions != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// persistLoaded records the loaded skill set on the AgentRun so a replacement
// pod can restore it. The record is advisory: a failed write never fails the
// tool call that already loaded the skill into this pod.
func (t *LoadSkillTool) persistLoaded(ctx context.Context) {
	if t.runName == "" {
		return
	}
	names := t.LoadedNames()
	run := &platformv1alpha1.AgentRun{}
	key := client.ObjectKey{Namespace: t.namespace, Name: t.runName}
	if err := t.k8sClient.Get(ctx, key, run); err != nil {
		log.Printf("WARN: recording loaded skills on AgentRun %s: %v", key, err)
		return
	}
	patch := client.MergeFrom(run.DeepCopy())
	if run.Status.Policy == nil {
		run.Status.Policy = &platformv1alpha1.AgentRunResolvedPolicy{}
	}
	run.Status.Policy.ResolvedSkills = names
	if err := t.k8sClient.Status().Patch(ctx, run, patch); err != nil {
		log.Printf("WARN: recording loaded skills on AgentRun %s: %v", key, err)
	}
}

func skillDescription(skill *platformv1alpha1.Skill) string {
	if skill == nil {
		return ""
	}
	description := strings.TrimSpace(skill.Spec.Description)
	if description == "" && skill.Status.Resolved != nil {
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

func skillInstructionsWithProvenance(skill *platformv1alpha1.Skill, instructions string) string {
	if skill == nil || skill.Spec.Source.Git == nil {
		return instructions
	}
	git := skill.Spec.Source.Git
	var b strings.Builder
	b.WriteString("Upstream source: ")
	b.WriteString(git.URL)
	if git.Ref != "" {
		b.WriteString(" @ ")
		b.WriteString(git.Ref)
	}
	if git.Path != "" {
		b.WriteString(" (path: ")
		b.WriteString(git.Path)
		b.WriteString(")")
	}
	if attribution := strings.TrimSpace(skill.Spec.Description); attribution != "" {
		b.WriteString("\nSkill description: ")
		b.WriteString(attribution)
	}
	b.WriteString("\n\n")
	b.WriteString(instructions)
	return b.String()
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
