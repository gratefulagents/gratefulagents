package main

import (
	"context"
	"log"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	"github.com/gratefulagents/gratefulagents/internal/store/postgres/sqlc"
	slackgo "github.com/slack-go/slack"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// handleAppHome publishes the App Home tab for a Slack user. Everyone gets the
// owner-configured header and info line (spec.appHome, read live so dashboard
// edits apply without a restart). The owner and configured commanders also get
// the operational panel: New Run / Refresh, the configured repositories, the
// owner's approval backlog, and one card per run with Stop / Resume controls.
// Publishing only the placeholder for anyone else also clears the richer view
// a user may have had before their access was removed.
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

// slackMention renders a Slack user mention, or an empty string when the user
// ID is unknown.
func slackMention(userID string) string {
	if userID == "" {
		return ""
	}
	return "<@" + userID + ">"
}

// operationalHomeBlocks gathers the state behind the Home panel for an
// authorized user and renders it. The approval backlog is owner-only and scoped
// to this agent and namespace; a failed read of runs or monitors degrades to an
// in-view note rather than an empty tab.
func (o *slackOrchestrator) operationalHomeBlocks(ctx context.Context, userID string) []slackgo.Block {
	defaults := o.currentDefaults(ctx)
	ops := internalslack.HomeOperations{
		Repositories: configuredRepositories(defaults),
		BaseBranch:   defaults.BaseBranch,
	}
	if o.queries != nil && userID == o.ownerUserID {
		drafts, err := o.queries.ListSlackDraftsByAgent(ctx, sqlc.ListSlackDraftsByAgentParams{
			Namespace:  o.namespace,
			SlackAgent: o.slackStoreKey(),
			Status:     slackDraftPending,
			MaxRows:    internalslack.HomeApprovalCountCap + 1,
		})
		if err != nil {
			log.Printf("slack connector %s: app home: counting pending approvals: %v", o.agentName, err)
		} else {
			ops.ShowApprovals = true
			ops.PendingApprovals = len(drafts)
		}
	}
	runs, err := o.operationalRuns(ctx)
	if err != nil {
		log.Printf("slack connector %s: app home: listing runs: %v", o.agentName, err)
		ops.RunsUnavailable = true
		return internalslack.BuildHomeOperationsBlocks(ops)
	}
	var byRun map[string][]triggersv1alpha1.PullRequestMonitor
	if len(runs) > 0 {
		monitors := &triggersv1alpha1.PullRequestMonitorList{}
		if err := o.crdClient.List(ctx, monitors, client.InNamespace(o.namespace)); err != nil {
			log.Printf("slack connector %s: app home: listing PR monitors: %v", o.agentName, err)
			ops.PullRequestsUnavailable = true
		} else {
			byRun = monitorsByRun(monitors.Items)
		}
	}
	ops.Runs = make([]internalslack.HomeRun, 0, len(runs))
	for i := range runs {
		ops.Runs = append(ops.Runs, homeRunFor(&runs[i], byRun[runs[i].Name]))
	}
	return internalslack.BuildHomeOperationsBlocks(ops)
}
