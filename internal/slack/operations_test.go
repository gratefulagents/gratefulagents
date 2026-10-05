package slack

import (
	"encoding/json"
	"strings"
	"testing"

	slackgo "github.com/slack-go/slack"
)

func TestBuildRunStartModal(t *testing.T) {
	repo := "https://github.com/acme/" + strings.Repeat("long-name", 15)
	view := BuildRunStartModal("ns/me", []string{repo, "https://github.com/acme/two"}, "release/v2")
	if view.CallbackID != CallbackRunStart || view.PrivateMetadata != "ns/me" {
		t.Fatalf("wrong binding: %+v", view)
	}
	if len(view.Blocks.BlockSet) != 3 {
		t.Fatal("missing repository, branch or task field")
	}
	selection := view.Blocks.BlockSet[0].(*slackgo.InputBlock).Element.(*slackgo.SelectBlockElement)
	if len(selection.Options) != 2 || selection.Options[0].Value != RepositoryOptionValue(repo) || len([]rune(selection.Options[0].Text.Text)) > 75 {
		t.Fatalf("invalid repository options: %+v", selection.Options)
	}
	if len(selection.Options[0].Value) > 150 || selection.InitialOption.Value != selection.Options[0].Value {
		t.Fatal("invalid initial repository")
	}
	branch := view.Blocks.BlockSet[1].(*slackgo.InputBlock).Element.(*slackgo.PlainTextInputBlockElement)
	if branch.InitialValue != "release/v2" || branch.MaxLength != 255 {
		t.Fatalf("invalid branch field: %+v", branch)
	}
	task := view.Blocks.BlockSet[2].(*slackgo.InputBlock)
	if task.Optional || !task.Element.(*slackgo.PlainTextInputBlockElement).Multiline {
		t.Fatal("task must be required multiline input")
	}
}

func TestBuildRunResumeModalDoesNotRetarget(t *testing.T) {
	view := BuildRunResumeModal("ns/me/run-one")
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
	if len(view.Blocks.BlockSet) != 1 || !view.Blocks.BlockSet[0].(*slackgo.InputBlock).Optional {
		t.Fatal("resume instructions should be optional")
	}
}
