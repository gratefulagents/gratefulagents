package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	"github.com/gratefulagents/gratefulagents/internal/store/postgres/sqlc"
	slackgo "github.com/slack-go/slack"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (o *slackOrchestrator) handleAppHome(ctx context.Context, userID string) {
	if userID == "" {
		return
	}
	var header, text string
	if o.crdClient != nil {
		agent := &triggersv1alpha1.SlackAgent{}
		if err := o.crdClient.Get(
			ctx,
			client.ObjectKey{Namespace: o.namespace, Name: o.agentName},
			agent,
		); err != nil {
			log.Printf("slack connector %s: app home: reading agent: %v", o.agentName, err)
		} else if ah := agent.Spec.AppHome; ah != nil {
			header, text = ah.Header, ah.Text
		}
	}
	blocks := internalslack.BuildHomePlaceholderView(o.agentName, header, text)
	if o.mayStop(userID) {
		blocks = append(blocks, o.operationalHomeBlocks(ctx, userID)...)
	}
	if err := o.web.PublishHomeView(ctx, userID, blocks...); err != nil {
		log.Printf("slack connector %s: publishing app home: %v", o.agentName, err)
	}
}

// slackMention wraps a Slack user ID as an <@ID> mention, or "" when empty.
func slackMention(userID string) string {
	if userID == "" {
		return ""
	}
	return "<@" + userID + ">"
}

func slackHomeSection(text string) slackgo.Block {
	if len([]rune(text)) > 2900 {
		text = string([]rune(text)[:2900]) + "…"
	}
	return slackgo.NewSectionBlock(
		slackgo.NewTextBlockObject(slackgo.PlainTextType, text, false, false),
		nil,
		nil,
	)
}

func (o *slackOrchestrator) operationalHomeBlocks(ctx context.Context, userID string) []slackgo.Block {
	blocks := []slackgo.Block{
		slackHomeSection(
			"New Run selects a repository and base branch. Stop interrupts a turn; Resume continues the same run. " +
				"Results and notifications arrive privately in your DM.",
		),
	}
	refresh := slackgo.NewButtonBlockElement(
		"slack_ops_refresh",
		"refresh",
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Refresh", false, false),
	)
	start := slackgo.NewButtonBlockElement(
		"slack_ops_start",
		"start",
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "New Run", false, false),
	)
	blocks = append(blocks, slackgo.NewActionBlock("slack_ops", start, refresh))
	runs, err := o.operationalRuns(ctx)
	if err != nil {
		return append(blocks, slackHomeSection("Run status is temporarily unavailable."))
	}
	if o.queries != nil && userID == o.ownerUserID {
		drafts, err := o.queries.ListSlackDraftsByAgent(ctx, sqlc.ListSlackDraftsByAgentParams{
			Namespace: o.namespace, SlackAgent: o.slackStoreKey(), Status: slackDraftPending, MaxRows: 101,
		})
		if err == nil {
			pending := fmt.Sprint(len(drafts))
			if len(drafts) > 100 {
				pending = "100+"
			}
			blocks = append(
				blocks,
				slackHomeSection(
					"Pending approvals: "+pending+". Review the approval cards in your DM with this app.",
				),
			)
		}
	}
	defaults := o.currentDefaults(ctx)
	blocks = append(
		blocks,
		slackHomeSection(
			"Repositories: "+strings.Join(
				append([]string{defaults.RepoURL}, defaults.AdditionalRepos...),
				", ",
			)+"\nDefault base: "+defaults.BaseBranch,
		),
	)
	if len(runs) == 0 {
		return append(
			blocks,
			slackHomeSection("No runs yet. Select New Run to choose a repository and describe your task."),
		)
	}
	if len(runs) > 10 {
		runs = runs[:10]
	}
	monitors := &triggersv1alpha1.PullRequestMonitorList{}
	monitorErr := o.crdClient.List(ctx, monitors, client.InNamespace(o.namespace))
	if monitorErr != nil {
		blocks = append(blocks, slackHomeSection("PR/check status is temporarily unavailable."))
	}
	for i := range runs {
		run := &runs[i]
		var text strings.Builder
		text.WriteString(slackRunSummary(run))
		for j := range monitors.Items {
			m := &monitors.Items[j]
			if m.Spec.ImplementerRef.Name == run.Name {
				text.WriteString("\n")
				text.WriteString(slackMonitorSummary(m))
			}
		}
		blocks = append(blocks, slackHomeSection(text.String()))
		stop := slackgo.NewButtonBlockElement(
			"slack_ops_stop",
			run.Name,
			slackgo.NewTextBlockObject(slackgo.PlainTextType, "Stop", false, false),
		)
		resume := slackgo.NewButtonBlockElement(
			"slack_ops_resume",
			run.Name,
			slackgo.NewTextBlockObject(slackgo.PlainTextType, "Resume", false, false),
		)
		switch run.Status.Phase {
		case platformv1alpha1.AgentRunPhaseRunning:
			blocks = append(blocks, slackgo.NewActionBlock("ops_"+run.Name, stop, resume))
		case platformv1alpha1.AgentRunPhasePaused,
			platformv1alpha1.AgentRunPhaseQuestion,
			platformv1alpha1.AgentRunPhaseBlocked,
			platformv1alpha1.AgentRunPhaseSucceeded,
			platformv1alpha1.AgentRunPhaseFailed,
			platformv1alpha1.AgentRunPhaseCancelled:
			blocks = append(blocks, slackgo.NewActionBlock("ops_"+run.Name, resume))
		}
	}
	return blocks
}
