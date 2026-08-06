/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func int32Ptr(v int32) *int32 { return &v }

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
		{Name: "a", Objective: "objective a"},
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
