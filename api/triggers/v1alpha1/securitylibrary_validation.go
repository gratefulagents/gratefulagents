/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// securityWorkflowTaskRefPattern matches {{tasks.<name>...}} references in a
// task objective template.
var securityWorkflowTaskRefPattern = regexp.MustCompile(`\{\{\s*tasks\.([a-zA-Z0-9-]+)`)

// securityWorkflowTaskFieldRefPattern matches {{tasks.<name>.output.<field>}}
// single-field references in a task objective template.
var securityWorkflowTaskFieldRefPattern = regexp.MustCompile(`\{\{\s*tasks\.([a-zA-Z0-9-]+)\.output\.`)

// securityWorkflowTaskOutputRefPattern matches {{tasks.<name>.output}} and
// {{tasks.<name>.output.<field>}} references in a task objective template.
var securityWorkflowTaskOutputRefPattern = regexp.MustCompile(`\{\{\s*tasks\.([a-zA-Z0-9-]+)\.output`)

// securityWorkflowItemRefPattern matches singular {{item}} and
// {{item.<field>}} references without also matching {{items}}.
var securityWorkflowItemRefPattern = regexp.MustCompile(`\{\{\s*item(?:\s*\}\}|\.)`)

// securityWorkflowItemsRefPattern matches the complete chunk input reference.
var securityWorkflowItemsRefPattern = regexp.MustCompile(`\{\{\s*items\s*\}\}`)

// securityWorkflowRangeRefPattern matches chunk range references.
var securityWorkflowRangeRefPattern = regexp.MustCompile(`\{\{\s*range\.(?:start|end)\s*\}\}`)

// securityWorkflowParameterNamePattern matches valid workflow parameter
// names, referenced as {{params.<name>}}.
var securityWorkflowParameterNamePattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

const (
	// MaxSecurityWorkflowTasks caps the task list (mirrors the MaxItems=64
	// CRD marker on SecurityScan spec.workflow and SecurityWorkflow
	// spec.tasks).
	MaxSecurityWorkflowTasks = 64

	// MaxSecurityWorkflowPlannedInstances caps the total number of task
	// instances a workflow may plan (ensemble repeats plus forEach fan-out
	// ceilings). It bounds status.lastExecution so the SecurityScan object
	// stays well below the etcd object-size limit; the deterministic engine
	// enforces the same ceiling at fan-out expansion time.
	MaxSecurityWorkflowPlannedInstances = 200
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
// reconcilers: at least one task and at most MaxSecurityWorkflowTasks; unique
// DNS-1123 label names; non-empty objectives; non-negative maxFindings;
// DNS-1123 role names; models without whitespace; per-task execution settings
// (retries, timeout, cost, tools, outputSchema, forEach fan-out, repeats)
// within bounds; objective template references that resolve to declared
// dependencies and, for {{tasks.NAME.output}}, to tasks that declare an
// outputSchema; dependsOn entries that resolve to other tasks; a total
// planned-instance budget of MaxSecurityWorkflowPlannedInstances; and an
// acyclic dependency graph.
func ValidateSecurityWorkflowTasks(tasks []SecurityScanTask) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	if len(tasks) == 0 {
		add("tasks", "a workflow needs at least one task")
		return errs
	}
	if len(tasks) > MaxSecurityWorkflowTasks {
		add("tasks", "a workflow may hold at most %d tasks, got %d", MaxSecurityWorkflowTasks, len(tasks))
		return errs
	}

	names := make(map[string]bool, len(tasks))
	byName := make(map[string]SecurityScanTask, len(tasks))
	for i, task := range tasks {
		field := fmt.Sprintf("tasks[%d]", i)
		name := task.Name
		if problems := validation.IsDNS1123Label(name); len(problems) != 0 {
			add(field+".name", "invalid task name %q (want a DNS-1123 label)", name)
		} else if names[name] {
			add(field+".name", "duplicate task name %q", name)
		}
		names[name] = true
		byName[name] = task
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
		if task.MaxRetries != nil && (*task.MaxRetries < 0 || *task.MaxRetries > 10) {
			add(field+".maxRetries", "task %q maxRetries must be between 0 and 10", name)
		}
		if task.MaxInstances < 0 || task.MaxInstances > 50 {
			add(field+".maxInstances", "task %q maxInstances must be between 0 and 50", name)
		}
		if task.TargetRuns < 0 || task.TargetRuns > 50 {
			add(field+".targetRuns", "task %q targetRuns must be between 0 and 50", name)
		}
		if task.Repeats < 0 || task.Repeats > 5 {
			add(field+".repeats", "task %q repeats must be between 0 and 5", name)
		}
		if !securityBudgetCostPattern.MatchString(task.MaxCostUSD) {
			add(field+".maxCostUSD", "invalid cost %q (want a plain decimal like \"5\" or \"2.50\")", task.MaxCostUSD)
		}
		if task.Timeout.Duration < 0 {
			add(field+".timeout", "task %q timeout must not be negative", name)
		}
		if task.Tools != nil {
			errs = append(errs, validateSecurityWorkflowToolList(field+".tools.allowed", name, task.Tools.Allowed)...)
			errs = append(errs, validateSecurityWorkflowToolList(field+".tools.denied", name, task.Tools.Denied)...)
		}
		if schema := strings.TrimSpace(task.OutputSchema); schema != "" {
			var object map[string]any
			if err := json.Unmarshal([]byte(schema), &object); err != nil || object == nil {
				add(field+".outputSchema", "task %q outputSchema must be a JSON object (a JSON Schema in object form)", name)
			}
		}
	}

	for i, task := range tasks {
		field := fmt.Sprintf("tasks[%d]", i)
		depSet := make(map[string]bool, len(task.DependsOn))
		for _, dep := range task.DependsOn {
			switch {
			case !names[dep]:
				add(field+".dependsOn", "task %q depends on unknown task %q", task.Name, dep)
			case dep == task.Name:
				add(field+".dependsOn", "task %q cannot depend on itself", task.Name)
			case depSet[dep]:
				add(field+".dependsOn", "task %q lists dependency %q twice", task.Name, dep)
			}
			depSet[dep] = true
		}

		if task.ForEach != "" {
			ref, known := byName[task.ForEach]
			switch {
			case !known:
				add(field+".forEach", "task %q forEach references unknown task %q", task.Name, task.ForEach)
			case !depSet[task.ForEach] || task.ForEach == task.Name:
				add(field+".forEach", "task %q forEach task %q must also be listed in dependsOn; add it there", task.Name, task.ForEach)
			case ref.ForEach != "" || ref.Repeats > 1:
				add(field+".forEach", "task %q forEach task %q is itself multi-instance (forEach or repeats); fan-out sources must be single-instance tasks", task.Name, task.ForEach)
			case strings.TrimSpace(ref.OutputSchema) == "":
				add(field+".forEach", "task %q forEach task %q must declare outputSchema", task.Name, task.ForEach)
			case !securityWorkflowSchemaAllowsArray(ref.OutputSchema):
				add(field+".forEach", "task %q forEach task %q outputSchema must declare \"type\":\"array\" (the fan-out source must publish a JSON array of records)", task.Name, task.ForEach)
			}
			if task.Repeats > 1 {
				add(field+".repeats", "task %q cannot combine forEach with repeats", task.Name)
			}
			if task.TargetRuns > 0 && task.MaxInstances > 0 {
				add(field+".targetRuns", "task %q cannot combine targetRuns with maxInstances", task.Name)
			}
			if task.TargetRuns > 0 && task.Repeats > 1 {
				add(field+".targetRuns", "task %q cannot combine targetRuns with repeats", task.Name)
			}
			if task.TargetRuns > 0 && securityWorkflowItemRefPattern.MatchString(task.Objective) {
				add(field+".objective", "task %q uses targetRuns and must reference {{items}}, not {{item}}", task.Name)
			}
			if task.TargetRuns > 0 && strings.TrimSpace(task.OutputSchema) != "" && !securityWorkflowSchemaAllowsArray(task.OutputSchema) {
				add(field+".outputSchema", "task %q uses targetRuns and outputSchema must declare \"type\":\"array\"", task.Name)
			}
			if task.TargetRuns == 0 && (securityWorkflowItemsRefPattern.MatchString(task.Objective) || securityWorkflowRangeRefPattern.MatchString(task.Objective)) {
				add(field+".objective", "task %q references chunk context but does not set targetRuns", task.Name)
			}
		} else {
			if securityWorkflowItemRefPattern.MatchString(task.Objective) || securityWorkflowItemsRefPattern.MatchString(task.Objective) || securityWorkflowRangeRefPattern.MatchString(task.Objective) {
				add(field+".objective", "task %q references forEach input context but does not set forEach", task.Name)
			}
			if task.TargetRuns > 0 {
				add(field+".targetRuns", "task %q may set targetRuns only with forEach", task.Name)
			}
		}

		referenced := make(map[string]bool)
		for _, match := range securityWorkflowTaskRefPattern.FindAllStringSubmatch(task.Objective, -1) {
			ref := match[1]
			if referenced[ref] {
				continue
			}
			referenced[ref] = true
			if !depSet[ref] || ref == task.Name {
				add(field+".objective", "task %q references {{tasks.%s}} but does not list %q in dependsOn", task.Name, ref, ref)
			}
		}
		// Only a task that declares an outputSchema is given the
		// submit_task_output tool, so a reference to a schema-less task's
		// output can never resolve and fails the dependent task at launch.
		outputReferenced := make(map[string]bool)
		for _, match := range securityWorkflowTaskOutputRefPattern.FindAllStringSubmatch(task.Objective, -1) {
			ref := match[1]
			if outputReferenced[ref] {
				continue
			}
			outputReferenced[ref] = true
			src, known := byName[ref]
			if known && ref != task.Name && strings.TrimSpace(src.OutputSchema) == "" {
				add(field+".objective", "task %q references {{tasks.%s.output}} but task %q declares no outputSchema and therefore never publishes structured output; add an outputSchema to %q or stop interpolating its output", task.Name, ref, ref, ref)
			}
		}
		// A multi-instance task's aggregated output is a JSON array of the
		// per-instance outputs, so a single-field access can never resolve.
		fieldReferenced := make(map[string]bool)
		for _, match := range securityWorkflowTaskFieldRefPattern.FindAllStringSubmatch(task.Objective, -1) {
			ref := match[1]
			if fieldReferenced[ref] {
				continue
			}
			fieldReferenced[ref] = true
			src, known := byName[ref]
			if known && (src.ForEach != "" || src.Repeats > 1) {
				add(field+".objective", "task %q references {{tasks.%s.output.<field>}} but task %q is multi-instance (forEach or repeats) and its output is a JSON array of instance outputs; use {{tasks.%s.output}}", task.Name, ref, ref, ref)
			}
		}
	}
	if len(errs) != 0 {
		return errs
	}

	// Planned-instance budget: what planSecurityScanExecution would expand
	// (ensemble repeats now, forEach fan-outs up to targetRuns or the legacy
	// maxInstances cap later) must fit the execution-entry ceiling.
	planned := 0
	for _, task := range tasks {
		instances := task.EffectiveRepeats()
		if task.ForEach != "" && task.EffectiveTargetRuns() > instances {
			instances = task.EffectiveTargetRuns()
		}
		planned += int(instances)
	}
	if planned > MaxSecurityWorkflowPlannedInstances {
		add("tasks", "the workflow plans up to %d task instances (repeats plus forEach maxInstances); at most %d are allowed, lower repeats or maxInstances", planned, MaxSecurityWorkflowPlannedInstances)
		return errs
	}

	if cycle := securityWorkflowCycle(tasks); len(cycle) != 0 {
		add("tasks", "dependency cycle: %s", strings.Join(cycle, " -> "))
	}
	return errs
}

// securityWorkflowSchemaAllowsArray reports whether an outputSchema (already
// checked to be a JSON object) can describe a JSON array: a string "type"
// other than "array" cannot, an absent or non-string "type" is allowed (the
// schema may be intentionally loose).
func securityWorkflowSchemaAllowsArray(schema string) bool {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(schema), &object); err != nil || object == nil {
		return true // malformed schemas are rejected by the per-task check
	}
	raw, ok := object["type"]
	if !ok {
		return true
	}
	var typeName string
	if err := json.Unmarshal(raw, &typeName); err != nil {
		return true
	}
	return typeName == "array"
}

// validateSecurityWorkflowToolList validates one tools.allowed/denied list:
// entries must be non-empty, contain no whitespace, and be unique within the
// list.
func validateSecurityWorkflowToolList(field, taskName string, tools []string) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	seen := make(map[string]bool, len(tools))
	for _, tool := range tools {
		switch {
		case strings.TrimSpace(tool) == "":
			add("task %q lists an empty tool name", taskName)
		case strings.ContainsAny(tool, " \t\n"):
			add("task %q tool %q must not contain whitespace", taskName, tool)
		case seen[tool]:
			add("task %q lists tool %q twice", taskName, tool)
		}
		seen[tool] = true
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

// ValidateSecurityWorkflowParameters validates a workflow's declared
// scan-time parameters: unique names matching {{params.<name>}} identifier
// syntax, descriptions of bounded length, and no contradictory
// required-with-default declarations (a required parameter with a default
// would never fail when omitted, so the default would silently win).
func ValidateSecurityWorkflowParameters(params []SecurityWorkflowParameter) []SecurityWorkflowFieldError {
	var errs []SecurityWorkflowFieldError
	add := func(field, format string, args ...any) {
		errs = append(errs, SecurityWorkflowFieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}

	names := make(map[string]bool, len(params))
	for i, param := range params {
		field := fmt.Sprintf("parameters[%d]", i)
		name := param.Name
		switch {
		case len(name) > 63 || !securityWorkflowParameterNamePattern.MatchString(name):
			add(field+".name", "invalid parameter name %q (want an identifier like snake_case, at most 63 characters)", name)
		case names[name]:
			add(field+".name", "duplicate parameter name %q", name)
		}
		names[name] = true
		if len(param.Description) > 512 {
			add(field+".description", "parameter %q description must be at most 512 characters", name)
		}
		if param.Required && param.Default != "" {
			add(field+".default", "parameter %q cannot be required and have a default; drop one of them", name)
		}
	}
	return errs
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
