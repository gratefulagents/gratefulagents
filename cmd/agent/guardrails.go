package main

import (
	"context"
	"errors"
	"fmt"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkguardrails "github.com/gratefulagents/sdk/pkg/agentsdk/guardrails"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// loadCRDGuardrails reads a GuardrailPolicy from the cluster and converts rules to SDK guardrails.
func loadCRDGuardrails(ctx context.Context, c client.Client, ref *platformv1alpha1.NamedRef, namespace string) ([]agent.ToolInputGuardrail, []agent.ToolOutputGuardrail, error) {
	if ref == nil {
		return nil, nil, nil
	}
	if c == nil {
		return nil, nil, errors.New("cannot load referenced GuardrailPolicy without a Kubernetes client")
	}
	if ref.Name == "" {
		return nil, nil, errors.New("GuardrailPolicy reference name is empty")
	}

	var policy platformv1alpha1.GuardrailPolicy
	key := types.NamespacedName{Name: ref.Name, Namespace: namespace}
	if err := c.Get(ctx, key, &policy); err != nil {
		return nil, nil, fmt.Errorf("load GuardrailPolicy %s: %w", key, err)
	}

	rules := make([]agent.GuardrailRule, 0, len(policy.Spec.Rules))
	for _, rule := range policy.Spec.Rules {
		rules = append(rules, agent.GuardrailRule{
			Name:        rule.Name,
			Type:        rule.Type,
			Regex:       rule.Regex,
			ToolPattern: rule.ToolPattern,
			Action:      rule.Action,
			Message:     rule.Message,
		})
	}

	inputGuardrails, outputGuardrails, conversionErrs := sdkguardrails.ToolGuardrailsFromRules(rules)
	if len(conversionErrs) > 0 {
		return nil, nil, fmt.Errorf("convert GuardrailPolicy %s: %w", key, errors.Join(conversionErrs...))
	}
	return inputGuardrails, outputGuardrails, nil
}
