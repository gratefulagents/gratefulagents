package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

const (
	reportPlatformBugToolName = "report_platform_bug"
	platformBugRepository     = "gratefulagents/gratefulagents"
)

type reportPlatformBugTool struct {
	maintainerToolBase
	runner prReviewRunner
}

type reportPlatformBugInput struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type platformBugSearchResult struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

func (t *reportPlatformBugTool) Name() string { return reportPlatformBugToolName }
func (t *reportPlatformBugTool) Description() string {
	return "Search for and, when not already reported, create a Grateful Agents platform/tooling bug in gratefulagents/gratefulagents. This server-enforced target must never be used for user-repository bugs or ordinary project failures."
}
func (t *reportPlatformBugTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","minLength":8,"maxLength":160},"body":{"type":"string","minLength":20,"maxLength":12000,"description":"Redacted report with expected/actual behavior, reproduction, affected tool/controller, impact, and workaround."}},"required":["title","body"]}`)
}
func (t *reportPlatformBugTool) IsReadOnly() bool                      { return false }
func (t *reportPlatformBugTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *reportPlatformBugTool) NeedsApproval() bool                   { return false }
func (t *reportPlatformBugTool) TimeoutSeconds() int                   { return 90 }

func (t *reportPlatformBugTool) Execute(ctx context.Context, raw json.RawMessage, workDir string) (Result, error) {
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	var in reportPlatformBugInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	title, body := strings.TrimSpace(in.Title), strings.TrimSpace(in.Body)
	if len(title) < 8 || len(title) > 160 || len(body) < 20 || len(body) > 12000 {
		return Result{Content: "title or body is outside the allowed length", IsError: true}, nil
	}
	search, err := t.runner.RunGH(ctx, workDir, "issue", "list", "--repo", platformBugRepository, "--state", "all", "--search", title+" in:title", "--json", "number,title,url", "--limit", "20")
	if err != nil {
		return Result{Content: "upstream platform bug reporting is unavailable during deduplication: " + err.Error() + "; submit a blocked maintainer report with the ready-to-file draft", IsError: true}, nil
	}
	var existing []platformBugSearchResult
	if err := json.Unmarshal([]byte(search), &existing); err != nil {
		return Result{Content: "could not decode upstream issue search: " + err.Error(), IsError: true}, nil
	}
	for _, issue := range existing {
		if strings.EqualFold(strings.TrimSpace(issue.Title), title) {
			payload, _ := json.Marshal(map[string]any{"created": false, "duplicate": true, "number": issue.Number, "url": issue.URL, "repository": platformBugRepository})
			return Result{Content: string(payload)}, nil
		}
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: "authorization changed before upstream issue creation: " + err.Error(), IsError: true}, nil
	}
	body += "\n\n" + githubAppAuthorizationFooter
	created, err := t.runner.RunGHWithInput(ctx, workDir, body, "issue", "create", "--repo", platformBugRepository, "--title", title, "--body-file", "-")
	if err != nil {
		return Result{Content: "upstream platform bug reporting is unavailable: " + err.Error() + "; submit a blocked maintainer report with the ready-to-file draft", IsError: true}, nil
	}
	payload, _ := json.Marshal(map[string]any{"created": true, "duplicate": false, "url": strings.TrimSpace(created), "repository": platformBugRepository})
	return Result{Content: string(payload)}, nil
}
