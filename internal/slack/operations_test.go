package slack

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"
)

func TestBuildRunStartModal(t *testing.T) {
	repo := "https://github.com/acme/" + strings.Repeat("long-name", 15)
	view := BuildRunStartModal("ns/me", []string{repo, "https://github.com/acme/two"}, "release/v2")
	if view.CallbackID != CallbackRunStart || view.PrivateMetadata != "ns/me" {
		t.Fatalf("wrong binding: %+v", view)
	}
	repository := inputBlock(t, view, BlockRunRepository)
	branch := inputBlock(t, view, BlockRunBranch)
	task := inputBlock(t, view, BlockRunTask)
	selection := repository.Element.(*slackgo.SelectBlockElement)
	if len(selection.Options) != 2 || selection.Options[0].Value != RepositoryOptionValue(repo) {
		t.Fatalf("invalid repository options: %+v", selection.Options)
	}
	for _, option := range selection.Options {
		if n := len([]rune(option.Text.Text)); n > optionTextMaxRunes {
			t.Fatalf("option label %d runes exceeds Slack's %d", n, optionTextMaxRunes)
		}
		if len(option.Value) > 150 {
			t.Fatal("option value exceeds Slack's 150-character limit")
		}
	}
	if selection.Options[1].Text.Text != "acme/two" {
		t.Fatalf("option label = %q, want owner/repo", selection.Options[1].Text.Text)
	}
	if selection.InitialOption == nil || selection.InitialOption.Value != selection.Options[0].Value {
		t.Fatal("primary repository should be preselected")
	}
	branchInput := branch.Element.(*slackgo.PlainTextInputBlockElement)
	if branchInput.InitialValue != "release/v2" || branchInput.MaxLength != 255 || !branch.Optional || branch.Hint == nil {
		t.Fatalf("invalid branch field: %+v", branch)
	}
	taskInput := task.Element.(*slackgo.PlainTextInputBlockElement)
	if task.Optional || !taskInput.Multiline || taskInput.Placeholder == nil || task.Hint == nil {
		t.Fatal("task must be a required multiline input with guidance")
	}
}

func TestBuildRunStartModalDisambiguatesRepeatedNames(t *testing.T) {
	repos := []string{"https://github.com/acme/app", "https://git.example.com/acme/app"}
	view := BuildRunStartModal("ns/me", repos, "")
	selection := inputBlock(t, view, BlockRunRepository).Element.(*slackgo.SelectBlockElement)
	for i, repo := range repos {
		if selection.Options[i].Text.Text != repo {
			t.Fatalf("ambiguous owner/repo should fall back to the URL, got %q", selection.Options[i].Text.Text)
		}
	}
}

func TestBuildRunStartModalWithoutRepositoriesDoesNotPanic(t *testing.T) {
	view := BuildRunStartModal("ns/me", nil, "main")
	if len(view.Blocks.BlockSet) == 0 {
		t.Fatal("expected a view")
	}
}

func TestBuildRunResumeModalDoesNotRetarget(t *testing.T) {
	run := HomeRun{Name: "run-one", Phase: "Paused", Repository: "https://github.com/acme/one", BaseBranch: "main"}
	view := BuildRunResumeModal("ns/me/run-one", run)
	if view.CallbackID != CallbackRunResume || view.PrivateMetadata != "ns/me/run-one" {
		t.Fatal("wrong run binding")
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), BlockRunRepository) || strings.Contains(string(raw), BlockRunBranch) {
		t.Fatal("resume must not retarget a run")
	}
	for _, want := range []string{"run-one", "Paused", "acme/one", "`main`"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("resume form should summarize the run; missing %q", want)
		}
	}
	var input *slackgo.InputBlock
	for _, block := range view.Blocks.BlockSet {
		if candidate, ok := block.(*slackgo.InputBlock); ok {
			input = candidate
		}
	}
	if input == nil || input.BlockID != BlockRunTask || !input.Optional {
		t.Fatal("resume instructions should be a single optional input")
	}
}

func homeOperationsFixture() HomeOperations {
	return HomeOperations{
		Repositories:     []string{"https://github.com/acme/one", "https://github.com/acme/two.git"},
		BaseBranch:       "main",
		ShowApprovals:    true,
		PendingApprovals: 2,
		Runs: []HomeRun{
			{
				Name:       "run-one",
				Phase:      "Running",
				Repository: "https://github.com/acme/one",
				BaseBranch: "main",
				StartedAt:  time.Date(2026, 10, 5, 14, 3, 0, 0, time.UTC),
				PullRequests: []HomePullRequest{{
					URL:       "https://github.com/acme/one/pull/12",
					Title:     "Fix <login> & retry",
					Lifecycle: "open",
					Checks:    "failure",
				}},
				CanStop:   true,
				CanResume: true,
			},
			{Name: "run-two", Phase: "Succeeded", Repository: "https://github.com/acme/two", CanResume: true},
		},
	}
}

// renderBlocks serializes blocks without JSON's HTML escaping so assertions can
// read mrkdwn links and entities literally. Some slack-go block types escape in
// their own MarshalJSON, so the output is round-tripped through generic values
// and re-encoded here with escaping off.
func renderBlocks(t *testing.T, blocks []slackgo.Block) string {
	t.Helper()
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(generic); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestBuildHomeOperationsBlocks(t *testing.T) {
	blocks := BuildHomeOperationsBlocks(homeOperationsFixture())
	view := renderBlocks(t, blocks)
	for _, want := range []string{
		ActionRunStart, ActionRunRefresh, ActionRunStop, ActionRunResume,
		"`acme/one`", "`acme/two`", "`main`",
		"2 replies are waiting for your approval",
		"*run-one*", "Running", "*run-two*", "Succeeded",
		"<https://github.com/acme/one/pull/12|acme/one#12 — Fix &lt;login&gt; &amp; retry>",
		"checks failed",
		"<!date^1791208980^{date_short_pretty} at {time}|2026-10-05 14:03 UTC>",
		"Stop this run?",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("Home view missing %q", want)
		}
	}
	var stopButtons, resumeButtons int
	for _, block := range blocks {
		actions, ok := block.(*slackgo.ActionBlock)
		if !ok {
			continue
		}
		for _, element := range actions.Elements.ElementSet {
			button, ok := element.(*slackgo.ButtonBlockElement)
			if !ok {
				continue
			}
			switch button.ActionID {
			case ActionRunStart:
				if button.Style != slackgo.StylePrimary {
					t.Error("New Run should be the primary action")
				}
			case ActionRunStop:
				stopButtons++
				if button.Style != slackgo.StyleDanger || button.Confirm == nil {
					t.Error("Stop should be a confirmed danger action")
				}
				if button.Value != "run-one" {
					t.Errorf("Stop bound to %q, want run-one", button.Value)
				}
			case ActionRunResume:
				resumeButtons++
			}
		}
	}
	if stopButtons != 1 || resumeButtons != 2 {
		t.Fatalf("stop=%d resume=%d, want 1 and 2", stopButtons, resumeButtons)
	}
	if n := len(blocks); n > 100 {
		t.Fatalf("%d blocks exceeds Slack's Home limit", n)
	}
}

func TestBuildHomeOperationsBlocksStates(t *testing.T) {
	empty := BuildHomeOperationsBlocks(HomeOperations{})
	raw, _ := json.Marshal(empty)
	for _, want := range []string{"No runs yet", "none configured", "repository default"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("empty Home missing %q: %s", want, raw)
		}
	}
	if strings.Contains(string(raw), "approval") {
		t.Error("approval backlog must stay hidden unless requested (owner only)")
	}

	unavailable := BuildHomeOperationsBlocks(HomeOperations{RunsUnavailable: true, Runs: []HomeRun{{Name: "hidden"}}})
	raw, _ = json.Marshal(unavailable)
	if !strings.Contains(string(raw), "temporarily unavailable") || strings.Contains(string(raw), "hidden") {
		t.Errorf("unavailable Home should explain and not render cards: %s", raw)
	}

	many := HomeOperations{}
	for i := range 25 {
		many.Runs = append(many.Runs, HomeRun{Name: "run-" + strings.Repeat("x", i+1), Phase: "Succeeded", CanResume: true})
	}
	blocks := BuildHomeOperationsBlocks(many)
	raw, _ = json.Marshal(blocks)
	if !strings.Contains(string(raw), "showing the 10 most recent of 25") {
		t.Errorf("Home should say it is capped: %s", raw)
	}
	if strings.Count(string(raw), `"`+ActionRunResume+`"`) != homeRunCardLimit {
		t.Errorf("Home should render exactly %d cards", homeRunCardLimit)
	}
	if n := len(blocks); n > 100 {
		t.Fatalf("%d blocks exceeds Slack's Home limit", n)
	}
}

func TestRepositoryAndPullRequestLabels(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/one":      "acme/one",
		"https://github.com/acme/one.git/": "acme/one",
		"git@github.com:acme/one.git":      "acme/one",
		"https://gitlab.example.com/a/b/c": "https://gitlab.example.com/a/b/c",
		"not a url":                        "not a url",
		"":                                 "",
	} {
		if got := RepositoryLabel(in); got != want {
			t.Errorf("RepositoryLabel(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"https://github.com/acme/one/pull/12": "acme/one#12",
		"https://github.com/acme/one":         "https://github.com/acme/one",
	} {
		if got := PullRequestLabel(in); got != want {
			t.Errorf("PullRequestLabel(%q) = %q, want %q", in, got, want)
		}
	}
	if got := EscapeMrkdwn("a<b>&c"); got != "a&lt;b&gt;&amp;c" {
		t.Errorf("EscapeMrkdwn = %q", got)
	}
}

// inputBlock returns the modal's input block with the given ID, failing the
// test when it is absent.
func inputBlock(t *testing.T, view slackgo.ModalViewRequest, blockID string) *slackgo.InputBlock {
	t.Helper()
	for _, block := range view.Blocks.BlockSet {
		if input, ok := block.(*slackgo.InputBlock); ok && input.BlockID == blockID {
			return input
		}
	}
	t.Fatalf("missing %s input block", blockID)
	return nil
}
