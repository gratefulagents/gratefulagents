package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	slackgo "github.com/slack-go/slack"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// operationResult sends a private, mrkdwn-formatted outcome to the acting
// user's DM with the app. Home controls have no message to reply in, so the DM
// is the one surface every actor is guaranteed to have.
func (o *slackOrchestrator) operationResult(ctx context.Context, userID, text string) {
	channel, err := o.web.OpenIMWithUser(ctx, userID)
	if err == nil {
		_, err = o.web.PostMessageAsBot(ctx, channel, text, "")
	}
	if err != nil {
		log.Printf("slack connector %s: operational result: %v", o.agentName, err)
	}
}

// operationalKey binds a Home form to this agent: it rides in the modal's
// private_metadata and must match on submission.
func (o *slackOrchestrator) operationalKey() string {
	return o.namespace + "/" + o.agentName
}

// operationalDefaults reads the agent's live run defaults for the New Run form
// and its submission. Unlike currentDefaults it does not fall back to the
// startup snapshot: a form built from stale configuration could offer a
// repository the agent no longer has.
func (o *slackOrchestrator) operationalDefaults(
	ctx context.Context,
) (triggersv1alpha1.AgentRunDefaults, error) {
	agent := &triggersv1alpha1.SlackAgent{}
	if err := o.crdClient.Get(
		ctx,
		client.ObjectKey{Namespace: o.namespace, Name: o.agentName},
		agent,
	); err != nil {
		return triggersv1alpha1.AgentRunDefaults{}, errors.New(
			"unable to read this agent's repository configuration; try again in a moment",
		)
	}
	defaults := agent.Spec.Defaults
	defaults.WorkflowMode = platformv1alpha1.WorkflowModeAuto
	if defaults.ModeRef == nil {
		defaults.ModeRef = &platformv1alpha1.ModeRef{Name: slackModeName}
	}
	return defaults, nil
}

// handleOperationalAction handles the Home tab buttons. Every click is
// re-authorized against the live owner/commander list; an unauthorized click
// is ignored without any visible response.
func (o *slackOrchestrator) handleOperationalAction(
	ctx context.Context,
	callback slackgo.InteractionCallback,
) {
	if !o.mayStop(callback.User.ID) || len(callback.ActionCallback.BlockActions) == 0 {
		return
	}
	action := callback.ActionCallback.BlockActions[0]
	switch action.ActionID {
	case internalslack.ActionRunRefresh:
		o.handleAppHome(ctx, callback.User.ID)
	case internalslack.ActionRunStart:
		o.openRunStartModal(ctx, callback)
	case internalslack.ActionRunResume:
		o.openRunResumeModal(ctx, callback, action.Value)
	case internalslack.ActionRunStop:
		o.stopRunFromHome(ctx, callback.User.ID, action.Value)
	}
}

// openRunStartModal opens the New Run form over the live configuration.
func (o *slackOrchestrator) openRunStartModal(ctx context.Context, callback slackgo.InteractionCallback) {
	user := callback.User.ID
	defaults, err := o.operationalDefaults(ctx)
	if err != nil {
		o.operationResult(ctx, user, ":warning: "+capitalize(err.Error())+".")
		return
	}
	repos := configuredRepositories(defaults)
	switch {
	case len(repos) == 0:
		o.operationResult(ctx, user, ":warning: No repository is configured for this agent. "+
			"Add one on its Project in the dashboard, then open Home again.")
		return
	case len(repos) > internalslack.RepositorySelectLimit:
		o.operationResult(ctx, user, fmt.Sprintf(
			":warning: New Run can list at most %d repositories (Slack's menu limit) and %d are configured. "+
				"Trim the Project's additional repositories, then open Home again.",
			internalslack.RepositorySelectLimit, len(repos)))
		return
	}
	o.openOperationalModal(ctx, callback, internalslack.BuildRunStartModal(o.operationalKey(), repos, defaults.BaseBranch))
}

// openRunResumeModal opens the Resume form for one of this agent's runs.
func (o *slackOrchestrator) openRunResumeModal(ctx context.Context, callback slackgo.InteractionCallback, name string) {
	run, err := o.operationalRun(ctx, callback.User.ID, name)
	if err != nil {
		o.operationResult(ctx, callback.User.ID, ":warning: "+capitalize(err.Error())+".")
		return
	}
	modal := internalslack.BuildRunResumeModal(o.operationalKey()+"/"+run.Name, homeRunFor(run, nil))
	o.openOperationalModal(ctx, callback, modal)
}

func (o *slackOrchestrator) openOperationalModal(
	ctx context.Context,
	callback slackgo.InteractionCallback,
	modal slackgo.ModalViewRequest,
) {
	if err := o.web.OpenModal(ctx, callback.TriggerID, modal); err != nil {
		log.Printf("slack connector %s: opening %s modal: %v", o.agentName, modal.CallbackID, err)
		o.operationResult(ctx, callback.User.ID, ":warning: I couldn't open the form. Refresh Home and try again.")
	}
}

// stopRunFromHome interrupts a running turn the same way Slack's native stop
// button and the dashboard do: the stop travels through the session, so the
// conversation stays resumable. A run that is not mid-turn has nothing to stop.
func (o *slackOrchestrator) stopRunFromHome(ctx context.Context, user, name string) {
	run, err := o.operationalRun(ctx, user, name)
	if err != nil {
		o.operationResult(ctx, user, ":warning: "+capitalize(err.Error())+".")
		return
	}
	if run.Status.Phase != platformv1alpha1.AgentRunPhaseRunning {
		o.operationResult(ctx, user, fmt.Sprintf(
			":information_source: No running turn to stop — `%s` is %s.",
			run.Name, strings.ToLower(internalslack.PhaseLabel(string(run.Status.Phase)))))
		return
	}
	if err := o.interruptRun(ctx, run.Name, user); err != nil {
		log.Printf("slack connector %s: stopping %s from Home: %v", o.agentName, run.Name, err)
		o.operationResult(ctx, user, ":warning: I couldn't stop `"+run.Name+"`. Try again from Home.")
		return
	}
	o.signalStop(run.Name)
	o.operationResult(ctx, user, ":octagonal_sign: Stop requested for `"+run.Name+"`. "+
		"The current turn is being interrupted; the conversation stays resumable from Home.")
	o.handleAppHome(ctx, user)
}

// handleOperationalSubmission handles the New Run and Resume forms. The Socket
// Mode ack already ran validateOperationalSubmission, so a field rejection here
// means the configuration changed in between; it is reported by DM.
func (o *slackOrchestrator) handleOperationalSubmission(
	ctx context.Context,
	callback slackgo.InteractionCallback,
) {
	if !o.mayStop(callback.User.ID) || callback.View.State == nil {
		return
	}
	switch callback.View.CallbackID {
	case internalslack.CallbackRunStart:
		o.startRunFromForm(ctx, callback)
	case internalslack.CallbackRunResume:
		o.resumeRunFromForm(ctx, callback)
	}
}

// validateOperationalSubmission runs synchronously before the Socket Mode ack
// so field errors render inline in the open modal instead of arriving by DM
// after it closed. It costs one CR read; a read failure returns no errors and
// leaves the report to the asynchronous handler.
func (o *slackOrchestrator) validateOperationalSubmission(
	ctx context.Context,
	callback slackgo.InteractionCallback,
) map[string]string {
	if callback.View.CallbackID != internalslack.CallbackRunStart ||
		callback.View.PrivateMetadata != o.operationalKey() ||
		callback.View.State == nil ||
		!o.mayStop(callback.User.ID) {
		return nil
	}
	_, fieldErrors, err := o.resolveRunStartForm(ctx, callback)
	if err != nil {
		return nil
	}
	return fieldErrors
}

// runStartForm is a New Run submission resolved against live configuration.
type runStartForm struct {
	defaults triggersv1alpha1.AgentRunDefaults
	repo     string
	branch   string
	task     string
}

// resolveRunStartForm checks a New Run submission against the live
// configuration: the repository must still be configured, the branch must be a
// valid ref name, and the task must not be blank. Field errors are keyed by
// input block ID so they can be rendered inline; err reports a configuration
// read failure.
func (o *slackOrchestrator) resolveRunStartForm(
	ctx context.Context,
	callback slackgo.InteractionCallback,
) (runStartForm, map[string]string, error) {
	defaults, err := o.operationalDefaults(ctx)
	if err != nil {
		return runStartForm{}, nil, err
	}
	form := runStartForm{
		branch: operationalInput(callback, internalslack.BlockRunBranch),
		task:   operationalInput(callback, internalslack.BlockRunTask),
	}
	fieldErrors := map[string]string{}
	selection := operationalSelection(callback, internalslack.BlockRunRepository)
	for _, candidate := range configuredRepositories(defaults) {
		if internalslack.RepositoryOptionValue(candidate) == selection {
			form.repo = candidate
			break
		}
	}
	if form.repo == "" {
		fieldErrors[internalslack.BlockRunRepository] =
			"This repository is no longer configured. Close and open New Run again."
	}
	if form.branch != "" {
		if err := validateBranchName(form.branch); err != nil {
			fieldErrors[internalslack.BlockRunBranch] = capitalize(err.Error()) + "."
		}
	}
	if form.task == "" {
		fieldErrors[internalslack.BlockRunTask] = "Describe what the agent should do."
	}
	if len(fieldErrors) > 0 {
		return form, fieldErrors, nil
	}
	form.defaults, err = slackSelectedDefaults(defaults, form.repo, form.branch)
	if err != nil {
		return form, map[string]string{internalslack.BlockRunRepository: capitalize(err.Error()) + "."}, nil
	}
	return form, nil, nil
}

// startRunFromForm creates a fresh run for a New Run submission, confirms it in
// the actor's DM, and streams the first reply there. It never retargets an
// existing conversation.
func (o *slackOrchestrator) startRunFromForm(ctx context.Context, callback slackgo.InteractionCallback) {
	user := callback.User.ID
	if callback.View.PrivateMetadata != o.operationalKey() {
		return
	}
	form, fieldErrors, err := o.resolveRunStartForm(ctx, callback)
	if err != nil {
		o.operationResult(ctx, user, ":warning: "+capitalize(err.Error())+".")
		return
	}
	if len(fieldErrors) > 0 {
		o.operationResult(ctx, user, ":warning: I couldn't start that run. "+
			strings.Join(sortedValues(fieldErrors), " ")+" Open New Run again.")
		return
	}
	if o.queries != nil && !o.claimEvent(ctx, "view", callback.View.ID) {
		return
	}
	channel, err := o.web.OpenIMWithUser(ctx, user)
	if err != nil {
		log.Printf("slack connector %s: opening DM for %s: %v", o.agentName, user, err)
		return
	}
	name := "slack-" + uuid.NewString()
	d := internalslack.Decision{
		ChannelID:   channel,
		ChannelType: "im",
		UserID:      user,
		Text:        form.task,
	}
	if err := o.createRunWithDefaults(ctx, name, d, form.task, form.defaults); err != nil {
		log.Printf("slack connector %s: starting %s from Home: %v", o.agentName, name, err)
		o.operationResult(ctx, user, ":warning: I couldn't start the run: "+internalslack.EscapeMrkdwn(err.Error()))
		return
	}
	gate := o.turnGate(name)
	gate.Lock()
	defer gate.Unlock()
	base := strings.TrimSpace(form.defaults.BaseBranch)
	if base == "" {
		base = "repository default"
	}
	_, _ = o.web.PostMessageAsBot(ctx, channel, fmt.Sprintf(
		":rocket: Started `%s` in `%s` on `%s`.\n%s\nI'll post the result here. Stop or resume it from Home.",
		name, internalslack.RepositoryLabel(form.defaults.RepoURL), base, blockQuote(form.task)), "")
	o.handleAppHome(ctx, user)
	o.streamReplies(ctx, replyWatch{
		runName:     name,
		channelID:   channel,
		requester:   user,
		channelType: "im",
		command:     form.task,
	})
}

// resumeRunFromForm wakes one of this agent's runs. Blank instructions resume
// the interrupted work through a message-free wake (the same path the
// dashboard's Retry takes) so the transcript gains no synthetic user message;
// explicit instructions become the run's next turn. The repository and
// checkout are never changed.
func (o *slackOrchestrator) resumeRunFromForm(ctx context.Context, callback slackgo.InteractionCallback) {
	user := callback.User.ID
	prefix := o.operationalKey() + "/"
	if !strings.HasPrefix(callback.View.PrivateMetadata, prefix) {
		return
	}
	run, err := o.operationalRun(ctx, user, strings.TrimPrefix(callback.View.PrivateMetadata, prefix))
	if err != nil {
		o.operationResult(ctx, user, ":warning: "+capitalize(err.Error())+".")
		return
	}
	gate := o.turnGate(run.Name)
	if !gate.TryLock() {
		o.operationResult(ctx, user, fmt.Sprintf(
			":hourglass_flowing_sand: `%s` is mid-turn. Stop it or wait for its reply, then resume.", run.Name))
		return
	}
	defer gate.Unlock()
	if o.queries != nil && !o.claimEvent(ctx, "view", callback.View.ID) {
		return
	}
	channel, err := o.web.OpenIMWithUser(ctx, user)
	if err != nil {
		log.Printf("slack connector %s: opening DM for %s: %v", o.agentName, user, err)
		return
	}
	instructions := operationalInput(callback, internalslack.BlockRunTask)
	baseline := o.maxAssistantMessageID(ctx, run.Name)
	if err := orchestration.WakeAgentRunIdempotent(
		ctx,
		o.crdClient,
		o.store,
		o.namespace,
		run.Name,
		instructions,
		"slack-resume:"+callback.View.ID,
		platformv1alpha1.AgentRunPhaseRunning,
		platformv1alpha1.AgentRunPhasePaused,
		platformv1alpha1.AgentRunPhaseQuestion,
		platformv1alpha1.AgentRunPhaseBlocked,
		platformv1alpha1.AgentRunPhaseSucceeded,
		platformv1alpha1.AgentRunPhaseFailed,
		platformv1alpha1.AgentRunPhaseCancelled,
	); err != nil {
		log.Printf("slack connector %s: resuming %s from Home: %v", o.agentName, run.Name, err)
		o.operationResult(ctx, user, ":warning: I couldn't resume `"+run.Name+"`: "+internalslack.EscapeMrkdwn(err.Error()))
		return
	}
	confirmation := fmt.Sprintf(":arrow_forward: Resumed `%s`, continuing the previous task.", run.Name)
	command := "Continue the previous task."
	if instructions != "" {
		confirmation = fmt.Sprintf(":arrow_forward: Resumed `%s` with your instructions.\n%s",
			run.Name, blockQuote(instructions))
		command = instructions
	}
	_, _ = o.web.PostMessageAsBot(ctx, channel, confirmation+"\nI'll post the result here.", "")
	o.handleAppHome(ctx, user)
	o.streamReplies(ctx, replyWatch{
		runName:     run.Name,
		channelID:   channel,
		requester:   user,
		channelType: "im",
		command:     command,
		baseline:    baseline,
	})
}

// operationalInput reads a trimmed plain-text input from a submitted form.
func operationalInput(callback slackgo.InteractionCallback, blockID string) string {
	if callback.View.State == nil {
		return ""
	}
	return strings.TrimSpace(callback.View.State.Values[blockID][internalslack.ActionRunInput].Value)
}

// operationalSelection reads a static-select value from a submitted form.
func operationalSelection(callback slackgo.InteractionCallback, blockID string) string {
	if callback.View.State == nil {
		return ""
	}
	return callback.View.State.Values[blockID][internalslack.ActionRunInput].SelectedOption.Value
}

// blockQuote renders user text as a short mrkdwn quote.
func blockQuote(text string) string {
	const maxRunes = 300
	text = internalslack.EscapeMrkdwn(strings.TrimSpace(text))
	if r := []rune(text); len(r) > maxRunes {
		text = string(r[:maxRunes-1]) + "…"
	}
	return "> " + strings.ReplaceAll(text, "\n", "\n> ")
}

// sortedValues returns a map's values in key order, for stable messages.
func sortedValues(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, k := range keys {
		values = append(values, m[k])
	}
	return values
}

// capitalize upper-cases the first rune so an error reads as a sentence.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}
