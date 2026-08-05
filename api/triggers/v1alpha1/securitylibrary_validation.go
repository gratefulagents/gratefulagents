/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// SecurityWorkflowFieldError is one structured validation failure for a
// security workflow, ranker, or post-script, addressed to the offending
// field.
type SecurityWorkflowFieldError struct {
	// Field is a JSON-ish path such as "tasks[2].dependsOn".
	Field string
	// Message explains the failure.
	Message string
}

func (e SecurityWorkflowFieldError) Error() string {
	if e.Field == "" {
		return e.Message
	}
	return e.Field + ": " + e.Message
}

// ValidateSecurityWorkflowTasks validates a security workflow task list the
// same way for the dashboard, the SecurityScan controller, and the library
// reconcilers: at least one task; unique DNS-1123 label names; non-empty
// objectives; non-negative maxFindings; DNS-1123 role names; models without
// whitespace; dependsOn entries that resolve to other tasks; and an acyclic
// dependency graph.
func ValidateSecurityWorkflowTasks(tasks []SecurityScanTask) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if len(tasks) == 0 {
		add("tasks", "a workflow needs at least one task")
		return errs
	}

	names := make(map[string]bool, len(tasks))
	for i, task := range tasks {
		field := fmt.Sprintf("tasks[%d]", i)
		name := task.Name
		if problems := validation.IsDNS1123Label(name); len(problems) != 0 {
			add(field+".name", "invalid task name %q (want a DNS-1123 label)", name)
		} else if names[name] {
			add(field+".name", "duplicate task name %q", name)
		}
		names[name] = true
		if strings.TrimSpace(task.Objective) == "" {
			add(field+".objective", "task %q needs an objective", name)
		}
		if task.MaxFindings < 0 {
			add(field+".maxFindings", "task %q maxFindings must not be negative", name)
		}
		if role := task.Role; role != "" {
			if problems := validation.IsDNS1123Subdomain(role); len(problems) != 0 {
				add(field+".role", "invalid role %q (want a RoleInstruction name)", role)
			}
		}
		if model := task.Model; model != strings.TrimSpace(model) || strings.ContainsAny(model, " \t\n") {
			add(field+".model", "invalid model %q (must not contain whitespace)", model)
		}
	}

	for i, task := range tasks {
		field := fmt.Sprintf("tasks[%d].dependsOn", i)
		seen := make(map[string]bool, len(task.DependsOn))
		for _, dep := range task.DependsOn {
			switch {
			case !names[dep]:
				add(field, "task %q depends on unknown task %q", task.Name, dep)
			case dep == task.Name:
				add(field, "task %q cannot depend on itself", task.Name)
			case seen[dep]:
				add(field, "task %q lists dependency %q twice", task.Name, dep)
			}
			seen[dep] = true
		}
	}
	if len(errs) != 0 {
		return errs
	}

	if cycle := securityWorkflowCycle(tasks); len(cycle) != 0 {
		add("tasks", "dependency cycle: %s", strings.Join(cycle, " -> "))
	}
	return errs
}

// securityWorkflowCycle returns one dependency cycle in the task graph, or
// nil when the graph is acyclic. The walk is deterministic (task order, then
// dependsOn order).
func securityWorkflowCycle(tasks []SecurityScanTask) []string {
	deps := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		deps[task.Name] = task.DependsOn
	}
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(tasks))
	var stack []string
	var visit func(name string) []string
	visit = func(name string) []string {
		state[name] = visiting
		stack = append(stack, name)
		for _, dep := range deps[name] {
			switch state[dep] {
			case visiting:
				start := 0
				for i, entry := range stack {
					if entry == dep {
						start = i
						break
					}
				}
				return append(append([]string(nil), stack[start:]...), dep)
			case unvisited:
				if cycle := visit(dep); cycle != nil {
					return cycle
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return nil
	}
	for _, task := range tasks {
		if state[task.Name] == unvisited {
			if cycle := visit(task.Name); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

// ValidateSecurityRankerRules validates reusable ranker rules: at least one
// non-blank rule line.
func ValidateSecurityRankerRules(rules []string) []SecurityWorkflowFieldError {
	nonBlank := 0
	for _, rule := range rules {
		if strings.TrimSpace(rule) != "" {
			nonBlank++
		}
	}
	if nonBlank == 0 {
		return []SecurityWorkflowFieldError{{Field: "rules", Message: "at least one non-empty rule line is required"}}
	}
	return nil
}

// ValidateSecurityPostScriptSpec validates a reusable post-script spec.
func ValidateSecurityPostScriptSpec(spec SecurityPostScriptSpec) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	if strings.TrimSpace(spec.Prompt) == "" {
		errs = append(errs, SecurityWorkflowFieldError{Field: "prompt", Message: "a prompt is required"})
	}
	switch spec.RunOn {
	case "", "all", "confirmed", "high-and-above":
	default:
		errs = append(errs, SecurityWorkflowFieldError{
			Field:   "runOn",
			Message: fmt.Sprintf("invalid runOn %q (want all, confirmed, or high-and-above)", spec.RunOn),
		})
	}
	return errs
}
