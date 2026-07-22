package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

const criticApprovedVerdict = "VERDICT: APPROVED"

// newCriticVerifier builds the autonomous final-answer verifier while carrying
// the parent model-call reliability settings into the nested critic run.
func newCriticVerifier(
	runner *agent.Runner,
	critic *agent.Agent,
	originalTask string,
	retryPolicy *agent.RetryPolicy,
	modelCallTimeout time.Duration,
) func(context.Context, string) (string, error) {
	return func(ctx context.Context, finalText string) (string, error) {
		if runner == nil || critic == nil {
			return "", fmt.Errorf("critic verifier requires a runner and a critic agent")
		}
		criticAgent := critic.Clone()
		if strings.TrimSpace(criticAgent.Instructions) == "" {
			criticAgent.Instructions = agent.DefaultCriticInstructions
		}
		prompt := fmt.Sprintf(`<original_task>
%s
</original_task>

<candidate_final_answer>
%s
</candidate_final_answer>

Review the candidate final answer against the original task. Verify its claims with your tools, then give your verdict.`, originalTask, finalText)

		result, err := runner.Run(ctx, criticAgent, []agent.RunItem{{
			Type:    agent.RunItemMessage,
			Message: &agent.MessageOutput{Text: prompt},
		}}, agent.RunConfig{
			MaxTurns:              12,
			SubAgentMaxTurns:      12,
			ToolOutputDir:         workspaceScratchDir,
			ToolAccessLevel:       agent.ToolAccessLevelReadOnly,
			ForceFinalSummaryTurn: true,
			RetryPolicy:           retryPolicy,
			ModelCallTimeout:      modelCallTimeout,
		})
		if err != nil {
			return "", err
		}
		verdict := result.FinalText()
		if strings.Contains(strings.ToUpper(verdict), criticApprovedVerdict) || strings.TrimSpace(verdict) == "" {
			return "", nil
		}
		return verdict, nil
	}
}
