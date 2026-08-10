/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"fmt"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func int32Ptr(v int32) *int32 { return new(v) }

func requireFieldError(t *testing.T, errs []SecurityWorkflowFieldError, field, fragment string) {
	t.Helper()
	for _, err := range errs {
		if err.Field == field && strings.Contains(err.Message, fragment) {
			return
		}
	}
	t.Fatalf("expected error on %q containing %q, got %v", field, fragment, errs)
}

func TestValidateSecurityWorkflowTasksExecutionFieldsHappyPath(t *testing.T) {
	tasks := []SecurityScanTask{
		{
			Name:         "recon",
			Objective:    "map the attack surface",
			OutputSchema: `{"type":"array","items":{"type":"object"}}`,
			MaxRetries:   int32Ptr(3),
			Timeout:      metav1.Duration{Duration: 30 * time.Minute},
			MaxTurns:     40,
			MaxCostUSD:   "2.50",
			Tools:        &SecurityScanTaskTools{Allowed: []string{"grep", "read_file"}, Denied: []string{"Bash"}},
		},
		{
			Name:         "hunt",
			Objective:    "investigate {{item.path}} using {{tasks.recon.output}}",
			DependsOn:    []string{"recon"},
			ForEach:      "recon",
			MaxInstances: 20,
			SkillRefs:    []platformv1alpha1.NamedRef{{Name: "web-app-hunting"}},
		},
		{
			Name:      "ensemble",
			Objective: "review results of {{tasks.recon}}",
			DependsOn: []string{"recon"},
			Repeats:   3,
		},
	}
	if errs := ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateSecurityWorkflowTasksExecutionFieldFailures(t *testing.T) {
	base := func() []SecurityScanTask {
		return []SecurityScanTask{
			{Name: "a", Objective: "objective a", OutputSchema: `{"type":"array"}`},
			{Name: "b", Objective: "objective b", DependsOn: []string{"a"}},
		}
	}

	tests := []struct {
		name     string
		mutate   func(tasks []SecurityScanTask) []SecurityScanTask
		field    string
		fragment string
	}{
		{
			name:     "maxRetries too large",
			mutate:   func(ts []SecurityScanTask) []SecurityScanTask { ts[0].MaxRetries = int32Ptr(11); return ts },
			field:    "tasks[0].maxRetries",
			fragment: "between 0 and 10",
		},
		{
			name:     "maxRetries negative",
			mutate:   func(ts []SecurityScanTask) []SecurityScanTask { ts[0].MaxRetries = int32Ptr(-1); return ts },
			field:    "tasks[0].maxRetries",
			fragment: "between 0 and 10",
		},
		{
			name:     "maxInstances too large",
			mutate:   func(ts []SecurityScanTask) []SecurityScanTask { ts[0].MaxInstances = 51; return ts },
			field:    "tasks[0].maxInstances",
			fragment: "between 0 and 50",
		},
		{
			name:     "targetRuns too large",
			mutate:   func(ts []SecurityScanTask) []SecurityScanTask { ts[0].TargetRuns = 51; return ts },
			field:    "tasks[0].targetRuns",
			fragment: "between 0 and 50",
		},
		{
			name:     "targetRuns without forEach",
			mutate:   func(ts []SecurityScanTask) []SecurityScanTask { ts[0].TargetRuns = 4; return ts },
			field:    "tasks[0].targetRuns",
			fragment: "only with forEach",
		},
		{
			name:     "repeats too large",
			mutate:   func(ts []SecurityScanTask) []SecurityScanTask { ts[0].Repeats = 6; return ts },
			field:    "tasks[0].repeats",
			fragment: "between 0 and 5",
		},
		{
			name:     "maxCostUSD not decimal",
			mutate:   func(ts []SecurityScanTask) []SecurityScanTask { ts[0].MaxCostUSD = "$5"; return ts },
			field:    "tasks[0].maxCostUSD",
			fragment: "invalid cost",
		},
		{
			name: "negative timeout",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[0].Timeout = metav1.Duration{Duration: -time.Minute}
				return ts
			},
			field:    "tasks[0].timeout",
			fragment: "must not be negative",
		},
		{
			name: "empty tool name",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[0].Tools = &SecurityScanTaskTools{Allowed: []string{""}}
				return ts
			},
			field:    "tasks[0].tools.allowed",
			fragment: "empty tool name",
		},
		{
			name: "tool name with whitespace",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[0].Tools = &SecurityScanTaskTools{Denied: []string{"bad tool"}}
				return ts
			},
			field:    "tasks[0].tools.denied",
			fragment: "whitespace",
		},
		{
			name: "duplicate tool name",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[0].Tools = &SecurityScanTaskTools{Allowed: []string{"grep", "grep"}}
				return ts
			},
			field:    "tasks[0].tools.allowed",
			fragment: "twice",
		},
		{
			name: "invalid skill ref name",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[0].SkillRefs = []platformv1alpha1.NamedRef{{Name: "Bad Skill"}}
				return ts
			},
			field:    "tasks[0].skillRefs[0].name",
			fragment: "invalid Skill name",
		},
		{
			name:     "outputSchema not JSON",
			mutate:   func(ts []SecurityScanTask) []SecurityScanTask { ts[0].OutputSchema = "not-json"; return ts },
			field:    "tasks[0].outputSchema",
			fragment: "JSON object",
		},
		{
			name:     "outputSchema JSON array",
			mutate:   func(ts []SecurityScanTask) []SecurityScanTask { ts[0].OutputSchema = `["a"]`; return ts },
			field:    "tasks[0].outputSchema",
			fragment: "JSON object",
		},
		{
			name:     "forEach unknown task",
			mutate:   func(ts []SecurityScanTask) []SecurityScanTask { ts[1].ForEach = "ghost"; return ts },
			field:    "tasks[1].forEach",
			fragment: "unknown task",
		},
		{
			name: "forEach not in dependsOn",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[1].DependsOn = nil
				ts[1].ForEach = "a"
				return ts
			},
			field:    "tasks[1].forEach",
			fragment: "add it there",
		},
		{
			name: "forEach task without outputSchema",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[0].OutputSchema = ""
				ts[1].ForEach = "a"
				return ts
			},
			field:    "tasks[1].forEach",
			fragment: "must declare outputSchema",
		},
		{
			name: "forEach combined with repeats",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[1].ForEach = "a"
				ts[1].Repeats = 2
				return ts
			},
			field:    "tasks[1].repeats",
			fragment: "cannot combine forEach with repeats",
		},
		{
			name: "targetRuns combined with maxInstances",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[1].ForEach = "a"
				ts[1].TargetRuns = 4
				ts[1].MaxInstances = 20
				return ts
			},
			field:    "tasks[1].targetRuns",
			fragment: "cannot combine targetRuns with maxInstances",
		},
		{
			name: "targetRuns combined with repeats",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[1].ForEach = "a"
				ts[1].TargetRuns = 4
				ts[1].Repeats = 2
				return ts
			},
			field:    "tasks[1].targetRuns",
			fragment: "cannot combine targetRuns with repeats",
		},
		{
			name: "targetRuns output must be an array",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[1].ForEach = "a"
				ts[1].TargetRuns = 4
				ts[1].OutputSchema = `{"type":"object"}`
				return ts
			},
			field:    "tasks[1].outputSchema",
			fragment: `must declare "type":"array"`,
		},
		{
			name: "tasks reference outside dependsOn",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[0].Objective = "use {{tasks.b.output}}"
				return ts
			},
			field:    "tasks[0].objective",
			fragment: "does not list \"b\" in dependsOn",
		},
		{
			name: "item reference without forEach",
			mutate: func(ts []SecurityScanTask) []SecurityScanTask {
				ts[1].Objective = "inspect {{item.path}}"
				return ts
			},
			field:    "tasks[1].objective",
			fragment: "does not set forEach",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateSecurityWorkflowTasks(tc.mutate(base()))
			requireFieldError(t, errs, tc.field, tc.fragment)
		})
	}
}

func TestValidateSecurityWorkflowTasksTemplateWhitespaceTolerated(t *testing.T) {
	tasks := []SecurityScanTask{
		{Name: "a", Objective: "objective a", OutputSchema: `{"type":"object"}`},
		{Name: "b", Objective: "use {{ tasks.a.output }}", DependsOn: []string{"a"}},
	}
	if errs := ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	tasks[1].DependsOn = nil
	requireFieldError(t, ValidateSecurityWorkflowTasks(tasks), "tasks[1].objective", "dependsOn")
}

func TestValidateSecurityWorkflowTasksDoesNotValidateParams(t *testing.T) {
	tasks := []SecurityScanTask{{Name: "a", Objective: "scan {{params.target_area}} carefully"}}
	if errs := ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		t.Fatalf("expected no errors for params references, got %v", errs)
	}
}

func TestValidateSecurityWorkflowTasksEnforcesTaskCap(t *testing.T) {
	tasks := make([]SecurityScanTask, 0, MaxSecurityWorkflowTasks+1)
	for i := range MaxSecurityWorkflowTasks + 1 {
		tasks = append(tasks, SecurityScanTask{Name: fmt.Sprintf("t%d", i), Objective: "inspect"})
	}
	requireFieldError(t, ValidateSecurityWorkflowTasks(tasks), "tasks", "at most 64 tasks")
	if errs := ValidateSecurityWorkflowTasks(tasks[:MaxSecurityWorkflowTasks]); len(errs) != 0 {
		t.Fatalf("expected %d tasks to validate, got %v", MaxSecurityWorkflowTasks, errs)
	}
}

func TestValidateSecurityWorkflowTasksEnforcesPlannedInstanceBudget(t *testing.T) {
	repeated := func(n int) []SecurityScanTask {
		tasks := make([]SecurityScanTask, 0, n)
		for i := range n {
			tasks = append(tasks, SecurityScanTask{Name: fmt.Sprintf("r%d", i), Objective: "inspect", Repeats: 5})
		}
		return tasks
	}
	// 41 tasks x 5 repeats = 205 planned instances > 200.
	requireFieldError(t, ValidateSecurityWorkflowTasks(repeated(41)), "tasks", "task instances")
	// 40 tasks x 5 repeats = exactly 200 planned instances.
	if errs := ValidateSecurityWorkflowTasks(repeated(40)); len(errs) != 0 {
		t.Fatalf("expected exactly-at-budget workflow to validate, got %v", errs)
	}

	// forEach tasks count their maxInstances fan-out ceiling.
	fanned := make([]SecurityScanTask, 0, 5)
	fanned = append(fanned, SecurityScanTask{Name: "src", Objective: "list", OutputSchema: `{"type":"array"}`})
	for i := range 4 {
		fanned = append(fanned, SecurityScanTask{
			Name: fmt.Sprintf("fan%d", i), Objective: "inspect {{item}}",
			DependsOn: []string{"src"}, ForEach: "src", MaxInstances: 50,
		})
	}
	// 1 + 4x50 = 201 planned instances > 200.
	requireFieldError(t, ValidateSecurityWorkflowTasks(fanned), "tasks", "task instances")
	fanned[4].MaxInstances = 49
	if errs := ValidateSecurityWorkflowTasks(fanned); len(errs) != 0 {
		t.Fatalf("expected exactly-at-budget fan-out workflow to validate, got %v", errs)
	}

	// targetRuns replaces the legacy maxInstances ceiling in the budget.
	for i := 1; i < len(fanned); i++ {
		fanned[i].Objective = "inspect {{items}}"
		fanned[i].MaxInstances = 0
		fanned[i].TargetRuns = 50
	}
	requireFieldError(t, ValidateSecurityWorkflowTasks(fanned), "tasks", "task instances")
	fanned[4].TargetRuns = 49
	if errs := ValidateSecurityWorkflowTasks(fanned); len(errs) != 0 {
		t.Fatalf("expected exactly-at-budget targetRuns workflow to validate, got %v", errs)
	}
}

// A {{tasks.NAME.output}} reference only resolves when NAME declares an
// outputSchema: without one the task never receives submit_task_output, so
// the dependent task would fail at launch instead of at authoring time.
func TestValidateSecurityWorkflowTasksRequiresOutputSchemaForOutputReferences(t *testing.T) {
	tasks := []SecurityScanTask{
		{Name: "recon", Objective: "map the attack surface"},
		{Name: "hunt", Objective: "dig into {{tasks.recon.output}}", DependsOn: []string{"recon"}},
	}
	errs := ValidateSecurityWorkflowTasks(tasks)
	requireFieldError(t, errs, "tasks[1].objective", "outputSchema")
	for _, err := range errs {
		if err.Field != "tasks[1].objective" {
			continue
		}
		if !strings.Contains(err.Message, `"hunt"`) || !strings.Contains(err.Message, `"recon"`) {
			t.Errorf("error must name both tasks, got %q", err.Message)
		}
	}

	tasks[0].OutputSchema = `{"type":"object","properties":{"areas":{"type":"array"}}}`
	if errs := ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		t.Fatalf("expected reference to a schema-declaring task to validate, got %v", errs)
	}

	// A field access on a schema-less task is rejected the same way.
	tasks[0].OutputSchema = ""
	tasks[1].Objective = "dig into {{tasks.recon.output.areas}}"
	requireFieldError(t, ValidateSecurityWorkflowTasks(tasks), "tasks[1].objective", "outputSchema")

	// Referencing the task itself (not its output) needs no schema.
	tasks[1].Objective = "continue the work of {{tasks.recon}}"
	if errs := ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		t.Fatalf("expected a non-output task reference to validate, got %v", errs)
	}
}

func TestValidateSecurityWorkflowTasksAllowsSchemaLessTargetRunsOutputReference(t *testing.T) {
	tasks := []SecurityScanTask{
		{Name: "inventory", Objective: "list", OutputSchema: `{"type":"array"}`},
		{Name: "chunk", Objective: "inspect {{items}}", DependsOn: []string{"inventory"}, ForEach: "inventory", TargetRuns: 2},
		{Name: "join", Objective: "combine {{tasks.chunk.output}}", DependsOn: []string{"chunk"}},
	}
	if errs := ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		t.Fatalf("schema-less targetRuns output reference should validate, got %v", errs)
	}
}

func TestValidateSecurityWorkflowTasksRejectsFieldAccessOnMultiInstanceOutputs(t *testing.T) {
	tasks := []SecurityScanTask{
		{Name: "a", Objective: "objective a", Repeats: 3, OutputSchema: `{"type":"object"}`},
		{Name: "b", Objective: "use {{tasks.a.output.summary}}", DependsOn: []string{"a"}},
	}
	requireFieldError(t, ValidateSecurityWorkflowTasks(tasks), "tasks[1].objective", "multi-instance")
	tasks[1].Objective = "use {{tasks.a.output}}"
	if errs := ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		t.Fatalf("expected whole-output reference to validate, got %v", errs)
	}

	tasks = []SecurityScanTask{
		{Name: "src", Objective: "list", OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{item}}", DependsOn: []string{"src"}, ForEach: "src", OutputSchema: `{"type":"object"}`},
		{Name: "join", Objective: "use {{tasks.fan.output.result}}", DependsOn: []string{"fan"}},
	}
	requireFieldError(t, ValidateSecurityWorkflowTasks(tasks), "tasks[2].objective", "multi-instance")
	tasks[2].Objective = "use {{tasks.fan.output}}"
	if errs := ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		t.Fatalf("expected whole-output fan reference to validate, got %v", errs)
	}
}

func TestValidateSecurityWorkflowTasksRequiresArrayCapableForEachSourceSchema(t *testing.T) {
	tasks := []SecurityScanTask{
		{Name: "src", Objective: "list", OutputSchema: `{"type":"object"}`},
		{Name: "fan", Objective: "inspect {{item}}", DependsOn: []string{"src"}, ForEach: "src"},
	}
	requireFieldError(t, ValidateSecurityWorkflowTasks(tasks), "tasks[1].forEach", `"type":"array"`)
	// A schema without "type" may still describe an array and stays allowed.
	tasks[0].OutputSchema = `{"items":{"type":"object"}}`
	if errs := ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		t.Fatalf("expected type-less source schema to validate, got %v", errs)
	}
	tasks[0].OutputSchema = `{"type":"array"}`
	if errs := ValidateSecurityWorkflowTasks(tasks); len(errs) != 0 {
		t.Fatalf("expected array source schema to validate, got %v", errs)
	}
}

func TestValidateSecurityWorkflowTasksForbidsMultiInstanceForEachSources(t *testing.T) {
	tasks := []SecurityScanTask{
		{Name: "src", Objective: "list", OutputSchema: `{"type":"array"}`},
		{Name: "fan1", Objective: "inspect {{item}}", DependsOn: []string{"src"}, ForEach: "src", OutputSchema: `{"type":"array"}`},
		{Name: "fan2", Objective: "inspect {{item}}", DependsOn: []string{"fan1"}, ForEach: "fan1"},
	}
	requireFieldError(t, ValidateSecurityWorkflowTasks(tasks), "tasks[2].forEach", "single-instance")

	tasks = []SecurityScanTask{
		{Name: "rep", Objective: "list", Repeats: 2, OutputSchema: `{"type":"array"}`},
		{Name: "fan", Objective: "inspect {{item}}", DependsOn: []string{"rep"}, ForEach: "rep"},
	}
	requireFieldError(t, ValidateSecurityWorkflowTasks(tasks), "tasks[1].forEach", "single-instance")
}

func TestValidateSecurityWorkflowParameters(t *testing.T) {
	valid := []SecurityWorkflowParameter{
		{Name: "target_area", Description: "which area to scan", Default: "auth"},
		{Name: "depth", Required: true},
		{Name: "_private"},
	}
	if errs := ValidateSecurityWorkflowParameters(valid); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
	if errs := ValidateSecurityWorkflowParameters(nil); len(errs) != 0 {
		t.Fatalf("expected no errors for nil params, got %v", errs)
	}

	tests := []struct {
		name     string
		params   []SecurityWorkflowParameter
		field    string
		fragment string
	}{
		{
			name:     "invalid name",
			params:   []SecurityWorkflowParameter{{Name: "bad-name"}},
			field:    "parameters[0].name",
			fragment: "invalid parameter name",
		},
		{
			name:     "name starting with digit",
			params:   []SecurityWorkflowParameter{{Name: "1abc"}},
			field:    "parameters[0].name",
			fragment: "invalid parameter name",
		},
		{
			name:     "name too long",
			params:   []SecurityWorkflowParameter{{Name: strings.Repeat("a", 64)}},
			field:    "parameters[0].name",
			fragment: "invalid parameter name",
		},
		{
			name:     "duplicate name",
			params:   []SecurityWorkflowParameter{{Name: "dup"}, {Name: "dup"}},
			field:    "parameters[1].name",
			fragment: "duplicate parameter name",
		},
		{
			name:     "required with default",
			params:   []SecurityWorkflowParameter{{Name: "p", Required: true, Default: "x"}},
			field:    "parameters[0].default",
			fragment: "cannot be required and have a default",
		},
		{
			name:     "description too long",
			params:   []SecurityWorkflowParameter{{Name: "p", Description: strings.Repeat("d", 513)}},
			field:    "parameters[0].description",
			fragment: "at most 512 characters",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requireFieldError(t, ValidateSecurityWorkflowParameters(tc.params), tc.field, tc.fragment)
		})
	}
}
