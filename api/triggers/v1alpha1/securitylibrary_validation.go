/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

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
// DNS-1123 label names; non-empty objectives; DNS-1123 role names; models
// without whitespace; per-task execution settings
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
		for j, ref := range task.SkillRefs {
			if problems := validation.IsDNS1123Subdomain(ref.Name); len(problems) != 0 {
				add(fmt.Sprintf("%s.skillRefs[%d].name", field, j), "invalid Skill name %q (want a DNS-1123 subdomain)", ref.Name)
			}
		}
		if schema := strings.TrimSpace(task.OutputSchema); schema != "" {
			var object map[string]any
			if err := json.Unmarshal([]byte(schema), &object); err != nil || object == nil {
				add(field+".outputSchema", "task %q outputSchema must be a JSON object (a JSON Schema in object form)", name)
			}
		}
		if task.When != nil {
			if strings.TrimSpace(task.When.Task) == "" {
				add(field+".when.task", "task %q condition needs a dependency task", name)
			}
			if strings.TrimSpace(task.When.Path) == "" {
				add(field+".when.path", "task %q condition needs a structured-output path", name)
			}
			var scalar any
			if err := json.Unmarshal([]byte(task.When.Equals), &scalar); err != nil {
				add(field+".when.equals", "task %q condition equals must be a JSON boolean or string", name)
			} else {
				switch scalar.(type) {
				case bool, string:
				default:
					add(field+".when.equals", "task %q condition equals must be a JSON boolean or string", name)
				}
			}
			if output := strings.TrimSpace(task.When.OtherwiseOutput); output != "" {
				if !json.Valid([]byte(output)) {
					add(field+".when.otherwiseOutput", "task %q condition otherwiseOutput must be valid JSON", name)
				} else if schema := strings.TrimSpace(task.OutputSchema); schema != "" {
					if err := ValidateSecurityWorkflowOutput(schema, output); err != nil {
						add(field+".when.otherwiseOutput", "task %q condition otherwiseOutput does not satisfy outputSchema: %v", name, err)
					}
				}
			}
			if strings.TrimSpace(task.OutputSchema) != "" && strings.TrimSpace(task.When.OtherwiseOutput) == "" {
				add(field+".when.otherwiseOutput", "task %q condition needs otherwiseOutput because it declares outputSchema", name)
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
		if task.When != nil {
			ref, known := byName[task.When.Task]
			switch {
			case !known:
				add(field+".when.task", "task %q condition references unknown task %q", task.Name, task.When.Task)
			case !depSet[task.When.Task] || task.When.Task == task.Name:
				add(field+".when.task", "task %q condition task %q must also be listed in dependsOn", task.Name, task.When.Task)
			case ref.ForEach != "" || ref.Repeats > 1:
				add(field+".when.task", "task %q condition task %q is multi-instance (forEach or repeats); condition sources must be single-instance tasks", task.Name, task.When.Task)
			case strings.TrimSpace(ref.OutputSchema) == "":
				add(field+".when.task", "task %q condition task %q must declare outputSchema", task.Name, task.When.Task)
			case !securityWorkflowSchemaAllowsObject(ref.OutputSchema):
				add(field+".when.task", "task %q condition task %q outputSchema must allow an object", task.Name, task.When.Task)
			case !securityWorkflowSchemaAllowsPath(ref.OutputSchema, task.When.Path):
				add(field+".when.path", "task %q condition path %q is incompatible with task %q outputSchema", task.Name, task.When.Path, task.When.Task)
			}
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
		// A regular task must declare outputSchema to receive the
		// submit_task_output tool. targetRuns tasks receive the tool and a
		// generated indexed-envelope schema even without a declared schema.
		outputReferenced := make(map[string]bool)
		for _, match := range securityWorkflowTaskOutputRefPattern.FindAllStringSubmatch(task.Objective, -1) {
			ref := match[1]
			if outputReferenced[ref] {
				continue
			}
			outputReferenced[ref] = true
			src, known := byName[ref]
			if known && ref != task.Name && strings.TrimSpace(src.OutputSchema) == "" && src.TargetRuns <= 0 {
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
	return securityWorkflowSchemaAllowsType(schema, "array")
}

func securityWorkflowSchemaAllowsObject(schema string) bool {
	return securityWorkflowSchemaAllowsType(schema, "object")
}

func securityWorkflowSchemaAllowsType(schema, want string) bool {
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
	return typeName == want
}

// securityWorkflowSchemaAllowsPath rejects paths that contradict concrete
// object properties in a source schema. Loose schemas remain allowed.
func securityWorkflowSchemaAllowsPath(schema, path string) bool {
	var current map[string]any
	if err := json.Unmarshal([]byte(schema), &current); err != nil {
		return true
	}
	remaining := path
	for remaining != "" {
		segment, rest, hasMore := strings.Cut(remaining, ".")
		properties, known := current["properties"].(map[string]any)
		if !known {
			return true
		}
		next, exists := properties[segment].(map[string]any)
		if !exists {
			return false
		}
		if !hasMore {
			if typ, known := next["type"].(string); known && (typ == "object" || typ == "array") {
				return false
			}
			return true
		}
		if typ, known := next["type"].(string); known && typ != "object" {
			return false
		}
		current = next
		remaining = rest
	}
	return false
}

// ValidateSecurityWorkflowOutput checks the same dependency-free JSON Schema
// subset used for AgentRun task output. In addition to structural type,
// property, item, enum, and additional-property checks, the subset supports
// scalar/collection bounds and the composition keywords needed to express
// conditional evidence contracts (const, allOf/anyOf/oneOf, not, and
// if/then/else).
func ValidateSecurityWorkflowOutput(schemaJSON, valueJSON string) error {
	var schema map[string]any
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	var value any
	if err := json.Unmarshal([]byte(valueJSON), &value); err != nil {
		return fmt.Errorf("invalid value: %w", err)
	}
	return validateSecurityWorkflowValue(schema, value, "$")
}

func validateSecurityWorkflowValue(schema map[string]any, value any, path string) error {
	if constant, ok := schema["const"]; ok && !reflect.DeepEqual(constant, value) {
		return fmt.Errorf("%s must equal the schema const value", path)
	}
	if err := validateSecurityWorkflowComposition(schema, value, path); err != nil {
		return err
	}
	if typ, ok := schema["type"].(string); ok {
		valid := false
		switch typ {
		case "object":
			_, valid = value.(map[string]any)
		case "array":
			_, valid = value.([]any)
		case "string":
			_, valid = value.(string)
		case "boolean":
			_, valid = value.(bool)
		case "number":
			_, valid = value.(float64)
		case "integer":
			if number, isNumber := value.(float64); isNumber {
				valid = number == math.Trunc(number)
			}
		case "null":
			valid = value == nil
		default:
			valid = true // unsupported schema types are handled by AgentRun tooling
		}
		if !valid {
			return fmt.Errorf("%s must be %s", path, typ)
		}
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(candidate, value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not one of the enum values", path)
		}
	}
	if number, ok := value.(float64); ok {
		if minimum, exists := schema["minimum"].(float64); exists && number < minimum {
			return fmt.Errorf("%s must be at least %v", path, minimum)
		}
		if maximum, exists := schema["maximum"].(float64); exists && number > maximum {
			return fmt.Errorf("%s must be at most %v", path, maximum)
		}
	}
	if stringValue, ok := value.(string); ok {
		length := float64(utf8.RuneCountInString(stringValue))
		if minimum, exists := schema["minLength"].(float64); exists && length < minimum {
			return fmt.Errorf("%s must contain at least %v characters", path, minimum)
		}
		if maximum, exists := schema["maxLength"].(float64); exists && length > maximum {
			return fmt.Errorf("%s must contain at most %v characters", path, maximum)
		}
	}
	if object, ok := value.(map[string]any); ok {
		if required, ok := schema["required"].([]any); ok {
			for _, raw := range required {
				name, ok := raw.(string)
				if ok {
					if _, exists := object[name]; !exists {
						return fmt.Errorf("%s is missing required property %q", path, name)
					}
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, childValue := range object {
			child, hasSchema := properties[name].(map[string]any)
			if hasSchema {
				if err := validateSecurityWorkflowValue(child, childValue, path+"."+name); err != nil {
					return err
				}
				continue
			}
			if additional, closed := schema["additionalProperties"].(bool); closed && !additional {
				return fmt.Errorf("%s has disallowed additional property %q", path, name)
			}
		}
	}
	if array, ok := value.([]any); ok {
		length := float64(len(array))
		if minimum, exists := schema["minItems"].(float64); exists && length < minimum {
			return fmt.Errorf("%s must contain at least %v items", path, minimum)
		}
		if maximum, exists := schema["maxItems"].(float64); exists && length > maximum {
			return fmt.Errorf("%s must contain at most %v items", path, maximum)
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, item := range array {
				if err := validateSecurityWorkflowValue(items, item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateSecurityWorkflowComposition(schema map[string]any, value any, path string) error {
	if branches, ok := schema["allOf"].([]any); ok {
		for index, raw := range branches {
			branch, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if err := validateSecurityWorkflowValue(branch, value, path); err != nil {
				return fmt.Errorf("%s allOf[%d]: %w", path, index, err)
			}
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		branches, ok := schema[keyword].([]any)
		if !ok {
			continue
		}
		matches := 0
		for _, raw := range branches {
			branch, ok := raw.(map[string]any)
			if ok && validateSecurityWorkflowValue(branch, value, path) == nil {
				matches++
			}
		}
		if keyword == "anyOf" && matches == 0 {
			return fmt.Errorf("%s must satisfy at least one anyOf branch", path)
		}
		if keyword == "oneOf" && matches != 1 {
			return fmt.Errorf("%s must satisfy exactly one oneOf branch (matched %d)", path, matches)
		}
	}
	if disallowed, ok := schema["not"].(map[string]any); ok && validateSecurityWorkflowValue(disallowed, value, path) == nil {
		return fmt.Errorf("%s satisfies a disallowed schema", path)
	}
	if condition, ok := schema["if"].(map[string]any); ok {
		keyword := "else"
		if validateSecurityWorkflowValue(condition, value, path) == nil {
			keyword = "then"
		}
		if branch, ok := schema[keyword].(map[string]any); ok {
			if err := validateSecurityWorkflowValue(branch, value, path); err != nil {
				return fmt.Errorf("%s %s: %w", path, keyword, err)
			}
		}
	}
	return nil
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
	case "", "all", "confirmed", "high-and-above", "high-and-above-actionable", "medium-and-above-actionable", "low-and-above-actionable":
	default:
		errs = append(errs, SecurityWorkflowFieldError{
			Field:   "runOn",
			Message: fmt.Sprintf("invalid runOn %q (want all, confirmed, high-and-above, high-and-above-actionable, medium-and-above-actionable, or low-and-above-actionable)", spec.RunOn),
		})
	}
	return errs
}
