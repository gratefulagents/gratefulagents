package main

import (
	"reflect"
	"strings"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

type loadedSkillInstructionSource interface {
	LoadedInstructions() string
}

func isNilLoadedSkillInstructionSource(source loadedSkillInstructionSource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// attachLoadedSkillInstructions keeps skill guidance out of the base prompt
// until load_skill has selected it. InstructionsFn is evaluated for every SDK
// model turn, including the turn immediately after the tool call.
func attachLoadedSkillInstructions(target *agent.Agent, source loadedSkillInstructionSource) {
	if target == nil || isNilLoadedSkillInstructionSource(source) {
		return
	}
	baseInstructions, baseInstructionsFn := target.Instructions, target.InstructionsFn
	target.InstructionsFn = func(runCtx *agent.RunContext, current *agent.Agent) string {
		instructions := baseInstructions
		if baseInstructionsFn != nil {
			instructions = baseInstructionsFn(runCtx, current)
		}
		if loaded := source.LoadedInstructions(); loaded != "" {
			instructions = strings.TrimSpace(instructions + "\n\n" + loaded)
		}
		return instructions
	}
}
