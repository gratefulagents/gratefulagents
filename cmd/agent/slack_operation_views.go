package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	slackgo "github.com/slack-go/slack"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (o *slackOrchestrator) operationResult(ctx context.Context, userID, text string) {
	channel, err := o.web.OpenIMWithUser(ctx, userID)
	if err == nil {
		_, err = o.web.PostMessageAsBot(ctx, channel, text, "")
	}
	if err != nil {
		log.Printf("slack connector %s: operational result: %v", o.agentName, err)
	}
}

func (o *slackOrchestrator) operationalDefaults(
	ctx context.Context,
) (triggersv1alpha1.AgentRunDefaults, error) {
	agent := &triggersv1alpha1.SlackAgent{}
	if err := o.crdClient.Get(
		ctx,
		client.ObjectKey{Namespace: o.namespace, Name: o.agentName},
		agent,
	); err != nil {
		return triggersv1alpha1.AgentRunDefaults{}, fmt.Errorf(
			"unable to read repository configuration; try again",
		)
	}
	defaults := agent.Spec.Defaults
	defaults.WorkflowMode = platformv1alpha1.WorkflowModeAuto
	if defaults.ModeRef == nil {
		defaults.ModeRef = &platformv1alpha1.ModeRef{Name: slackModeName}
	}
	return defaults, nil
}

func (o *slackOrchestrator) handleOperationalAction(
	ctx context.Context,
	callback slackgo.InteractionCallback,
) {
	if !o.mayStop(callback.User.ID) || len(callback.ActionCallback.BlockActions) == 0 {
		return
	}
	action := callback.ActionCallback.BlockActions[0]
	var modal slackgo.ModalViewRequest
	key := o.namespace + "/" + o.agentName
	switch action.ActionID {
	case "slack_ops_refresh":
		o.handleAppHome(ctx, callback.User.ID)
		return
	case "slack_ops_start":
		defaults, err := o.operationalDefaults(ctx)
		if err != nil {
			o.operationResult(ctx, callback.User.ID, err.Error())
			return
		}
		repos := []string{}
		seen := map[string]bool{}
		for _, repo := range append([]string{defaults.RepoURL}, defaults.AdditionalRepos...) {
			if repo != "" && !seen[repo] {
				repos = append(repos, repo)
				seen[repo] = true
			}
		}
		if len(repos) == 0 || len(repos) > 100 {
			o.operationResult(
				ctx,
				callback.User.ID,
				"New Run needs between 1 and 100 configured repositories. "+
					"Update the Project repository settings, then reopen Home.",
			)
			return
		}
		modal = internalslack.BuildRunStartModal(key, repos, defaults.BaseBranch)
	case "slack_ops_resume":
		run, err := o.operationalRun(ctx, callback.User.ID, action.Value)
		if err != nil {
			o.operationResult(ctx, callback.User.ID, err.Error())
			return
		}
		modal = internalslack.BuildRunResumeModal(key + "/" + run.Name)
	case "slack_ops_stop":
		run, err := o.operationalRun(ctx, callback.User.ID, action.Value)
		if err != nil {
			o.operationResult(ctx, callback.User.ID, err.Error())
			return
		}
		if run.Status.Phase != platformv1alpha1.AgentRunPhaseRunning {
			o.operationResult(ctx, callback.User.ID, "No running turn to stop for "+run.Name+".")
			return
		}
		sess, err := o.store.GetSessionByRun(ctx, run.Name, o.namespace)
		if err != nil {
			o.operationResult(ctx, callback.User.ID, "Unable to find the run session.")
			return
		}
		if err := sessionclient.RequestInterrupt(
			ctx,
			o.store,
			sess.ID,
			"slack:"+callback.User.ID,
		); err != nil {
			o.operationResult(ctx, callback.User.ID, "Stop failed. Try again from Home.")
			return
		}
		o.signalStop(run.Name)
		o.operationResult(
			ctx,
			callback.User.ID,
			"Stop requested for "+run.Name+". The conversation remains resumable.",
		)
		o.handleAppHome(ctx, callback.User.ID)
		return
	default:
		return
	}
	if err := o.web.OpenModal(ctx, callback.TriggerID, modal); err != nil {
		o.operationResult(ctx, callback.User.ID, "Unable to open the form. Refresh Home and try again.")
	}
}

func (o *slackOrchestrator) handleOperationalSubmission(
	ctx context.Context,
	callback slackgo.InteractionCallback,
) {
	if !o.mayStop(callback.User.ID) || callback.View.State == nil {
		return
	}
	key := o.namespace + "/" + o.agentName
	text := strings.TrimSpace(
		callback.View.State.Values[internalslack.BlockRunTask][internalslack.ActionRunInput].Value,
	)
	switch callback.View.CallbackID {
	case internalslack.CallbackRunStart:
		if callback.View.PrivateMetadata != key {
			return
		}
		if text == "" {
			o.operationResult(ctx, callback.User.ID, "A task is required. Open New Run and try again.")
			return
		}
		defaults, err := o.operationalDefaults(ctx)
		if err != nil {
			o.operationResult(ctx, callback.User.ID, err.Error())
			return
		}
		repositoryInput := callback.View.State.Values[internalslack.BlockRunRepository][internalslack.ActionRunInput]
		selection := repositoryInput.SelectedOption.Value
		repo := ""
		for _, candidate := range append([]string{defaults.RepoURL}, defaults.AdditionalRepos...) {
			if candidate != "" && internalslack.RepositoryOptionValue(candidate) == selection {
				repo = candidate
				break
			}
		}
		if repo == "" {
			o.operationResult(
				ctx,
				callback.User.ID,
				"Repository is no longer configured. Open New Run again.",
			)
			return
		}
		branch := strings.TrimSpace(
			callback.View.State.Values[internalslack.BlockRunBranch][internalslack.ActionRunInput].Value,
		)
		defaults, err = slackSelectedDefaults(defaults, repo, branch)
		if err != nil {
			o.operationResult(ctx, callback.User.ID, err.Error()+". Open New Run again.")
			return
		}
		if o.queries != nil && !o.claimEvent(ctx, "view", callback.View.ID) {
			return
		}
		channel, err := o.web.OpenIMWithUser(ctx, callback.User.ID)
		if err != nil {
			o.operationResult(ctx, callback.User.ID, "Unable to open your DM.")
			return
		}
		name := "slack-" + uuid.NewString()
		d := internalslack.Decision{
			ChannelID:   channel,
			ChannelType: "im",
			UserID:      callback.User.ID,
			Text:        text,
		}
		if err := o.createRunWithDefaults(ctx, name, d, text, defaults); err != nil {
			o.operationResult(ctx, callback.User.ID, "Unable to start run: "+err.Error())
			return
		}
		gate := o.turnGate(name)
		gate.Lock()
		defer gate.Unlock()
		o.operationResult(
			ctx,
			callback.User.ID,
			"Started "+name+". Results will arrive here. Use Home to stop or resume this run.",
		)
		o.handleAppHome(ctx, callback.User.ID)
		o.streamReplies(
			ctx,
			replyWatch{
				runName:     name,
				channelID:   channel,
				requester:   callback.User.ID,
				channelType: "im",
				command:     text,
			},
		)
	case internalslack.CallbackRunResume:
		if !strings.HasPrefix(callback.View.PrivateMetadata, key+"/") {
			return
		}
		name := strings.TrimPrefix(callback.View.PrivateMetadata, key+"/")
		run, err := o.operationalRun(ctx, callback.User.ID, name)
		if err != nil {
			o.operationResult(ctx, callback.User.ID, err.Error())
			return
		}
		gate := o.turnGate(run.Name)
		if !gate.TryLock() {
			o.operationResult(
				ctx,
				callback.User.ID,
				"A turn is already in progress. Stop it or wait for its reply before resuming.",
			)
			return
		}
		defer gate.Unlock()
		if o.queries != nil && !o.claimEvent(ctx, "view", callback.View.ID) {
			return
		}
		if text == "" {
			text = "Continue the previous task."
		}
		channel, err := o.web.OpenIMWithUser(ctx, callback.User.ID)
		if err != nil {
			o.operationResult(ctx, callback.User.ID, "Unable to open your DM.")
			return
		}
		baseline := o.maxAssistantMessageID(ctx, run.Name)
		if err := orchestration.WakeAgentRunIdempotent(
			ctx,
			o.crdClient,
			o.store,
			o.namespace,
			run.Name,
			text,
			"slack-resume:"+callback.View.ID,
			platformv1alpha1.AgentRunPhaseRunning,
			platformv1alpha1.AgentRunPhasePaused,
			platformv1alpha1.AgentRunPhaseQuestion,
			platformv1alpha1.AgentRunPhaseBlocked,
			platformv1alpha1.AgentRunPhaseSucceeded,
			platformv1alpha1.AgentRunPhaseFailed,
			platformv1alpha1.AgentRunPhaseCancelled,
		); err != nil {
			o.operationResult(ctx, callback.User.ID, "Unable to resume: "+err.Error())
			return
		}
		o.operationResult(ctx, callback.User.ID, "Resumed "+run.Name+". Results will arrive here.")
		o.handleAppHome(ctx, callback.User.ID)
		o.streamReplies(
			ctx,
			replyWatch{
				runName:     run.Name,
				channelID:   channel,
				requester:   callback.User.ID,
				channelType: "im",
				command:     text,
				baseline:    baseline,
			},
		)
	}
}
