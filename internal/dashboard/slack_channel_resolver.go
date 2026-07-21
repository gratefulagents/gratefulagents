package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	slackgo "github.com/slack-go/slack"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

type slackConversationLookup func(ctx context.Context, botToken, name string) (string, error)

// resolveSlackTriggerChannel turns a dashboard-friendly #name into the stable
// Slack conversation ID persisted in the Project. Raw IDs and empty scope are
// left unchanged.
func (s *Server) resolveSlackTriggerChannel(ctx context.Context, namespace string, trigger *platform.ProjectTrigger) error {
	if trigger == nil || trigger.GetSlack() == nil {
		return nil
	}
	if strings.TrimSpace(trigger.GetType()) != string(triggersv1alpha1.ProjectTriggerTypeSlack) {
		return nil
	}
	channel := strings.TrimSpace(trigger.GetSlack().GetChannel())
	if !strings.HasPrefix(channel, "#") {
		return nil
	}
	name := strings.TrimSpace(strings.TrimPrefix(channel, "#"))
	if name == "" || strings.ContainsAny(name, "# ") {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid Slack channel name %q", channel))
	}
	connectionName := strings.TrimSpace(trigger.GetSlack().GetConnectionRef())
	if connectionName == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("slack trigger requires connection_ref"))
	}

	connection := &triggersv1alpha1.Connection{}
	if err := s.apiReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: connectionName}, connection); err != nil {
		if k8serrors.IsNotFound(err) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("Slack connection %q not found", connectionName))
		}
		return mapK8sError("get Slack Connection for channel lookup", err)
	}
	if connection.Spec.Type != triggersv1alpha1.ConnectionTypeSlack || connection.Spec.Slack == nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("connection %q is not a Slack connection", connectionName))
	}
	secretName := strings.TrimSpace(connection.Spec.Slack.TokensSecret)
	secret := &corev1.Secret{}
	if err := s.apiReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		if k8serrors.IsNotFound(err) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Slack credentials Secret %q not found", secretName))
		}
		return mapK8sError("get Slack credentials for channel lookup", err)
	}
	botToken := strings.TrimSpace(string(secret.Data[triggersv1alpha1.SlackBotTokenKey]))
	if botToken == "" {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Slack connection %q has no bot token", connectionName))
	}
	lookup := s.slackConversationLookup
	if lookup == nil {
		lookup = lookupSlackConversation
	}
	channelID, err := lookup(ctx, botToken, name)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return connect.NewError(connect.CodeCanceled, err)
		case errors.Is(err, context.DeadlineExceeded):
			return connect.NewError(connect.CodeDeadlineExceeded, err)
		default:
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("resolve Slack channel #%s: %w", name, err))
		}
	}
	trigger.Slack.Channel = channelID
	return nil
}

func lookupSlackConversation(ctx context.Context, botToken, name string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	api := slackgo.New(botToken)
	params := &slackgo.GetConversationsParameters{
		ExcludeArchived: true,
		Limit:           200,
		Types:           []string{"public_channel", "private_channel"},
	}
	for {
		channels, nextCursor, err := api.GetConversationsContext(ctx, params)
		if err != nil {
			return "", fmt.Errorf("Slack conversations.list: %w", err)
		}
		for _, channel := range channels {
			if strings.EqualFold(channel.Name, name) {
				return channel.ID, nil
			}
		}
		if nextCursor == "" {
			break
		}
		params.Cursor = nextCursor
	}
	return "", fmt.Errorf("channel is not visible to the bot")
}
