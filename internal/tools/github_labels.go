package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const defaultGitHubLabelColor = "BFD4F2"

type githubLabel struct {
	Name string `json:"name"`
}

// ensureGitHubLabels creates any requested labels that do not already exist in
// the selected repository. GitHub label names are case-insensitive.
func ensureGitHubLabels(ctx context.Context, runner prReviewRunner, workDir string, labels []string) error {
	labels = nonBlankUniqueFold(labels)
	if len(labels) == 0 {
		return nil
	}

	out, err := runner.RunGH(ctx, workDir, "label", "list", "--limit", "1000", "--json", "name")
	if err != nil {
		return fmt.Errorf("list repository labels: %w\n%s", err, out)
	}
	var existing []githubLabel
	if err := json.Unmarshal([]byte(out), &existing); err != nil {
		return fmt.Errorf("parse repository labels: %w", err)
	}
	existingNames := make(map[string]struct{}, len(existing))
	for _, label := range existing {
		existingNames[strings.ToLower(strings.TrimSpace(label.Name))] = struct{}{}
	}

	for _, label := range labels {
		key := strings.ToLower(label)
		if _, ok := existingNames[key]; ok {
			continue
		}
		createOut, createErr := runner.RunGH(ctx, workDir, "label", "create", label, "--color", defaultGitHubLabelColor)
		if createErr != nil && !strings.Contains(strings.ToLower(createOut), "already exists") {
			return fmt.Errorf("create repository label %q: %w\n%s", label, createErr, createOut)
		}
		existingNames[key] = struct{}{}
	}
	return nil
}

func nonBlankUniqueFold(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}
