package main

import (
	"strings"
	"testing"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

type stubLoadedSkills struct {
	instructions string
}

func (s *stubLoadedSkills) LoadedInstructions() string { return s.instructions }

func TestAttachLoadedSkillInstructionsUsesProgressiveDisclosure(t *testing.T) {
	source := &stubLoadedSkills{}
	target := &agent.Agent{Instructions: "base instructions"}
	attachLoadedSkillInstructions(target, source)

	if got := target.GetInstructions(&agent.RunContext{}); got != "base instructions" {
		t.Fatalf("instructions before load = %q", got)
	}

	source.instructions = "# Loaded skill guidance\n\n## Skill: pdf\nUse pdfplumber."
	got := target.GetInstructions(&agent.RunContext{})
	if !strings.Contains(got, "base instructions") || !strings.Contains(got, "Use pdfplumber.") {
		t.Fatalf("instructions after load = %q", got)
	}
}

func TestAttachLoadedSkillInstructionsSkipsTypedNilSource(t *testing.T) {
	var source *stubLoadedSkills
	target := &agent.Agent{Instructions: "base instructions"}

	attachLoadedSkillInstructions(target, source)

	if target.InstructionsFn != nil {
		t.Fatal("InstructionsFn was installed for a typed nil source")
	}
	if got := target.GetInstructions(&agent.RunContext{}); got != "base instructions" {
		t.Fatalf("instructions = %q", got)
	}
}

func TestAttachLoadedSkillInstructionsPreservesDynamicBase(t *testing.T) {
	source := &stubLoadedSkills{instructions: "loaded guidance"}
	target := &agent.Agent{
		Instructions: "unused static instructions",
		InstructionsFn: func(_ *agent.RunContext, _ *agent.Agent) string {
			return "dynamic base instructions"
		},
	}
	attachLoadedSkillInstructions(target, source)

	got := target.GetInstructions(&agent.RunContext{})
	if got != "dynamic base instructions\n\nloaded guidance" {
		t.Fatalf("wrapped dynamic instructions = %q", got)
	}
}
