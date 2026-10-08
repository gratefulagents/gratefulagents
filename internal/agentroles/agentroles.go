// Package agentroles defines the built-in sub-agent role contract shared by the
// agent runtime, the manager, and configuration tests.
//
// Sub-agents are defined by capability (tool access, model, reasoning), not by
// persona: the parent's task message carries the task-specific instructions, a
// short shared base prompt carries the rules every sub-agent follows, and a
// role adds only what its capability boundary needs.
package agentroles

import "strings"

// GeneralName is the general-purpose role. The runtime offers it even when no
// RoleInstruction of that name exists, so delegation never requires a persona.
const GeneralName = "general"

// GeneralToolAccess gives the general role every tool the run has.
const GeneralToolAccess = "full"

// GeneralDescription is shown to the parent agent when it picks a sub-agent.
const GeneralDescription = "Full tool access, same model as you. Use for implementation, fixes, test runs, and other multi-step work you can describe in a self-contained message. For parallel work, give each instance its own files."

// GeneralInstructions is the general role's prompt, added after SharedInstructions.
const GeneralInstructions = `Carry the task through to a verified result: make the change, then run the checks that prove it works, such as the build, the relevant tests, or the command that reproduced the problem. If you are blocked, report what you tried and where it stopped instead of guessing.`

// SharedInstructions is the base prompt every sub-agent role receives ahead of
// its own instructions, including roles created by users.
const SharedInstructions = `You are a sub-agent working on a task delegated by another agent. The task message is your assignment; you see the parent's conversation only if it was explicitly shared with you.

- Do what the task asks. Don't add features, refactors, abstractions, or fallbacks it doesn't need, and don't widen scope without saying why.
- If the task is ambiguous, take the most reasonable reading, note the assumption in your report, and keep going.
- Base conclusions on what files, commands, and tool output actually show. Report outcomes faithfully: say plainly what failed, what you could not verify, and what you did not do.
- Other agents may be working in the same workspace. Stay within the files you were given and don't revert changes you didn't make.
- Your final message goes back to the parent agent. Make it a concise report: the answer or what changed (with file paths), how you verified it, and anything still open.`

// WithSharedInstructions prefixes a role's own instructions with the shared
// sub-agent base prompt.
func WithSharedInstructions(instructions string) string {
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return SharedInstructions
	}
	return SharedInstructions + "\n\n" + instructions
}

// RetiredBootstrapNames lists persona roles the chart used to seed. Bootstrap
// defaults are Helm hooks, which Helm never deletes once they leave the chart,
// so the manager removes the seeded copies of these on upgraded clusters.
var RetiredBootstrapNames = []string{
	"analyst",
	"architect",
	"build-fixer",
	"code-reviewer",
	"code-simplifier",
	"critic",
	"debugger",
	"dependency-expert",
	"designer",
	"executor",
	"git-master",
	"planner",
	"researcher",
	"security-reviewer",
	"team-executor",
	"test-engineer",
	"vision",
	"writer",
}
