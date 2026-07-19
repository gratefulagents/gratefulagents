package main

import (
	"context"
	"log"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// handleAppHome publishes the static App Home tab for the given Slack user:
// the owner-configured header and info line (from spec.appHome, read live so
// dashboard edits apply without a restart), or built-in defaults. The tab
// intentionally carries no agent state: connection status, configuration,
// and held replies are dashboard-only, because any workspace member who opens
// the app sees its Home tab. Publishing the placeholder also replaces the
// richer view older builds may have left behind for this user.
func (o *slackOrchestrator) handleAppHome(ctx context.Context, userID string) {
	if userID == "" {
		return
	}
	var header, text string
	if o.crdClient != nil {
		agent := &triggersv1alpha1.SlackAgent{}
		if err := o.crdClient.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: o.agentName}, agent); err != nil {
			log.Printf("slack connector %s: app home: reading agent: %v", o.agentName, err)
		} else if ah := agent.Spec.AppHome; ah != nil {
			header, text = ah.Header, ah.Text
		}
	}
	if err := o.web.PublishHomeView(ctx, userID, internalslack.BuildHomePlaceholderView(o.agentName, header, text)...); err != nil {
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
