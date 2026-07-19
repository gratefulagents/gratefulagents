package main

import (
	"context"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// skillInstructionsForRun collects the prompt guidance shipped by the Skills a
// run references (inline spec instructions, or the controller-resolved
// SKILL.md body for git-sourced skills) so skills can teach the agent how to
// work well (query discipline, safety rules, runbooks). The blocks ride with
// the mode directive text each turn, mirroring how mode instructions are
// applied. Missing or unresolved skills contribute nothing; failures degrade
// to no guidance rather than blocking the turn.
func skillInstructionsForRun(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) string {
	if c == nil || run == nil || len(run.Spec.SkillRefs) == 0 {
		return ""
	}
	var blocks []string //nolint:prealloc // some skills may resolve to nothing
	for _, ref := range run.Spec.SkillRefs {
		name := strings.TrimSpace(ref.Name)
		if name == "" {
			continue
		}
		skill := &platformv1alpha1.Skill{}
		if err := c.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, skill); err != nil {
			continue
		}
		instr := resolvedSkillInstructions(skill)
		if instr == "" {
			continue
		}
		blocks = append(blocks, "## Skill: "+displaySkillName(skill, name)+"\n"+instr)
	}
	if len(blocks) == 0 {
		return ""
	}
	return "# Attached skill guidance\n\n" +
		"Skills provide run-specific instructions and may attach tools; a skill is not itself callable. " +
		"Follow the guidance below and use only associated tools listed in the environment block.\n\n" +
		strings.Join(blocks, "\n\n")
}

// displaySkillName prefers the controller-resolved name (SKILL.md frontmatter
// for git-sourced skills) over the reference name, so the prompt header
// matches what the skill calls itself.
func displaySkillName(skill *platformv1alpha1.Skill, refName string) string {
	if skill != nil && skill.Status.Resolved != nil {
		if resolved := strings.TrimSpace(skill.Status.Resolved.Name); resolved != "" {
			return resolved
		}
	}
	return refName
}

// resolvedSkillInstructions returns the effective instruction text for a
// skill: the controller-resolved content when available (covers git-sourced
// skills), falling back to inline spec instructions so freshly created inline
// skills work before their first reconcile. Skills the controller marked
// Invalid contribute nothing — the inline fallback must not resurrect content
// that failed validation.
func resolvedSkillInstructions(skill *platformv1alpha1.Skill) string {
	if skill == nil {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(skill.Status.Phase), "Invalid") {
		return ""
	}
	if res := skill.Status.Resolved; res != nil {
		if instr := strings.TrimSpace(res.Instructions); instr != "" {
			return instr
		}
	}
	if inline := skill.Spec.Source.Inline; inline != nil {
		return strings.TrimSpace(inline.Instructions)
	}
	return ""
}
