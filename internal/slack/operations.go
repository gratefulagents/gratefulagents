package slack

import (
	"crypto/sha256"
	"fmt"

	slackgo "github.com/slack-go/slack"
)

const (
	CallbackRunStart   = "slack_ops_start"
	CallbackRunResume  = "slack_ops_resume"
	BlockRunRepository = "ops_repository"
	BlockRunBranch     = "ops_branch"
	BlockRunTask       = "ops_task"
	ActionRunInput     = "value"
)

func RepositoryOptionValue(repository string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(repository)))
}

func BuildRunStartModal(agentKey string, repositories []string, branch string) slackgo.ModalViewRequest {
	options := make([]*slackgo.OptionBlockObject, 0, len(repositories))
	for _, repo := range repositories {
		options = append(options, slackgo.NewOptionBlockObject(RepositoryOptionValue(repo), slackgo.NewTextBlockObject(slackgo.PlainTextType, truncateRunes(repo, 75), false, false), nil))
	}
	selection := slackgo.NewOptionsSelectBlockElement(slackgo.OptTypeStatic, slackgo.NewTextBlockObject(slackgo.PlainTextType, "Choose a repository", false, false), ActionRunInput, options...)
	selection.InitialOption = options[0]
	branchInput := slackgo.NewPlainTextInputBlockElement(slackgo.NewTextBlockObject(slackgo.PlainTextType, "Default branch", false, false), ActionRunInput)
	branchInput.InitialValue = branch
	branchInput.MaxLength = 255
	branchBlock := slackgo.NewInputBlock(BlockRunBranch, slackgo.NewTextBlockObject(slackgo.PlainTextType, "Base branch", false, false), nil, branchInput)
	branchBlock.Optional = true
	task := slackgo.NewPlainTextInputBlockElement(slackgo.NewTextBlockObject(slackgo.PlainTextType, "What should the agent do?", false, false), ActionRunInput)
	task.Multiline = true
	task.MaxLength = 3000
	return slackgo.ModalViewRequest{
		Type: slackgo.VTModal, CallbackID: CallbackRunStart, PrivateMetadata: agentKey,
		Title:  slackgo.NewTextBlockObject(slackgo.PlainTextType, "New Run", false, false),
		Submit: slackgo.NewTextBlockObject(slackgo.PlainTextType, "Start", false, false),
		Close:  slackgo.NewTextBlockObject(slackgo.PlainTextType, "Cancel", false, false),
		Blocks: slackgo.Blocks{BlockSet: []slackgo.Block{
			slackgo.NewInputBlock(BlockRunRepository, slackgo.NewTextBlockObject(slackgo.PlainTextType, "Repository", false, false), nil, selection), branchBlock,
			slackgo.NewInputBlock(BlockRunTask, slackgo.NewTextBlockObject(slackgo.PlainTextType, "Task", false, false), nil, task),
		}},
	}
}

func BuildRunResumeModal(runKey string) slackgo.ModalViewRequest {
	task := slackgo.NewPlainTextInputBlockElement(slackgo.NewTextBlockObject(slackgo.PlainTextType, "Continue the previous task", false, false), ActionRunInput)
	task.Multiline = true
	task.MaxLength = 3000
	input := slackgo.NewInputBlock(BlockRunTask, slackgo.NewTextBlockObject(slackgo.PlainTextType, "Instructions (optional)", false, false), nil, task)
	input.Optional = true
	return slackgo.ModalViewRequest{
		Type: slackgo.VTModal, CallbackID: CallbackRunResume, PrivateMetadata: runKey,
		Title:  slackgo.NewTextBlockObject(slackgo.PlainTextType, "Resume Run", false, false),
		Submit: slackgo.NewTextBlockObject(slackgo.PlainTextType, "Resume", false, false),
		Close:  slackgo.NewTextBlockObject(slackgo.PlainTextType, "Cancel", false, false),
		Blocks: slackgo.Blocks{BlockSet: []slackgo.Block{input}},
	}
}
