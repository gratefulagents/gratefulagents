package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	triggerctrl "github.com/gratefulagents/gratefulagents/internal/controller/triggers"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	"github.com/gratefulagents/gratefulagents/internal/store"
	pgstore "github.com/gratefulagents/gratefulagents/internal/store/postgres"
	"github.com/gratefulagents/gratefulagents/internal/store/postgres/sqlc"
	"github.com/jackc/pgx/v5/pgxpool"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const slackTriggerKind = "SlackAgent"

// Labels the connector stamps on the AgentRuns it creates, used to identify
// slack runs and their kind at the worker.
const (
	slackAgentLabel = "triggers.gratefulagents.dev/slack-agent"
	slackKindLabel  = "triggers.gratefulagents.dev/slack-kind"
)

// roleAssistant is the conversation-message role for agent replies.
const roleAssistant = "assistant"

const (
	// slackKindCommand is the conversation kind for owner commands, stamped on
	// runs via the slack-kind label.
	slackKindCommand = "command"
	// slackModeName is the bundled default mode for Slack-triggered runs.
	slackModeName = "slack"
)

// Draft lifecycle statuses persisted in slack_drafts.
const (
	slackDraftPending      = "pending"
	slackDraftSending      = "sending"
	slackDraftRegenerating = "regenerating"
	slackDraftSent         = "sent"
	slackDraftDismissed    = "dismissed"
)

// slackDraftKindChannelReply is the slack_drafts.kind for the agent's own reply
// into a public conversation (channel thread, group DM), posted as the bot on
// approval. Rows with other kinds are legacy artifacts of the removed inbox
// monitoring feature and are only ever resolved, never delivered.
const slackDraftKindChannelReply = "channel_reply"

const (
	slackSavedGitHubSecretName = "usercred-github"
	slackSavedGitHubTokenKey   = "token"
)

// slackOrchestrator turns routed owner commands into AgentRuns and streams the
// agent's replies back into the Slack thread. One AgentRun is keyed per Slack
// thread: the first message creates it, follow-ups wake it.
type slackOrchestrator struct {
	web       *internalslack.Client
	crdClient client.Client
	store     store.StateStore
	agentName string
	namespace string
	// storeKey scopes this agent's rows in the shared Slack Postgres tables
	// (slack_threads, slack_events). Dedicated connectors use the bare agent
	// name (backward compatible); workspace members use namespace/name so two
	// same-named agents in different namespaces never share conversations.
	storeKey string
	defaults triggersv1alpha1.AgentRunDefaults // startup fallback only
	// triggerOwner carries the SlackAgent metadata used to resolve Project
	// provenance for Project-generated runtime adapters.
	triggerOwner client.Object

	// tokensSecret names the Slack tokens Secret referenced by the SlackAgent;
	// it is passed to child runs so agent-side Slack read tools can authenticate.
	tokensSecret string

	// ownerUserID and botDMChannelID are the resolved Slack identities used to
	// deliver approval cards to the owner's control DM. Set after the connector
	// resolves them at startup.
	ownerUserID    string
	dmMu           sync.RWMutex
	botDMChannelID string

	// turnMu serializes every input for a run through reply/draft completion.
	// Conversation queues serialize submission, while regeneration uses this
	// same gate so it cannot capture another turn's assistant output.
	turnMu    sync.Mutex
	turnGates map[string]*sync.Mutex
	queries   *sqlc.Queries

	// sessionIdle is how long a conversation's AgentRun is reused for follow-ups
	// before a fresh run is started.
	sessionIdle time.Duration

	// batchWindow coalesces a rapid burst of messages in one conversation into a
	// single turn. convQueues holds the per-conversation serialized worker so a
	// conversation never processes two turns concurrently (which would race).
	batchWindow time.Duration
	convMu      sync.Mutex
	convQueues  map[string]*convQueue

	// assistantCtx maps an assistant-pane conversation (channel|thread) to the
	// channel the user is currently viewing, so "this channel" requests resolve.
	assistantMu  sync.Mutex
	assistantCtx map[string]string
}

// newSlackOrchestrator wires the in-cluster CRD client and Postgres state store
// the connector needs to create runs and read their results. It reads the
// owning SlackAgent's defaults (model/provider/credentials) for created runs.
func newSlackOrchestrator(
	ctx context.Context,
	cfg slackConnectorConfig,
	web *internalslack.Client,
) (*slackOrchestrator, error) {
	deps, err := newSlackDeps(ctx)
	if err != nil {
		return nil, err
	}

	agent := &triggersv1alpha1.SlackAgent{}
	if err := deps.crdClient.Get(ctx, client.ObjectKey{Namespace: cfg.Namespace, Name: cfg.AgentName}, agent); err != nil {
		return nil, fmt.Errorf("getting SlackAgent %s/%s: %w", cfg.Namespace, cfg.AgentName, err)
	}

	return newSlackOrchestratorFromDeps(deps, web, slackOrchestratorParams{
		AgentName:    cfg.AgentName,
		Namespace:    cfg.Namespace,
		Defaults:     agent.Spec.Defaults,
		TriggerOwner: agent.DeepCopy(),
		TokensSecret: strings.TrimSpace(agent.Spec.TokensSecret),
		SessionIdle:  cfg.SessionIdle,
		BatchWindow:  cfg.BatchWindow,
	}), nil
}

// slackDeps bundles the process-wide dependencies orchestrators share: the
// in-cluster CRD client and the Postgres-backed state store. Workspace-mode
// connectors create one slackDeps and many orchestrators (one per member).
type slackDeps struct {
	crdClient client.Client
	store     store.StateStore
	queries   *sqlc.Queries
}

func newSlackDeps(ctx context.Context) (*slackDeps, error) {
	crdClient, err := buildSlackCRDClient()
	if err != nil {
		return nil, fmt.Errorf("building CRD client: %w", err)
	}

	dsn := strings.TrimSpace(databaseURL())
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL is required for the slack connector")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connecting to Postgres: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging Postgres: %w", err)
	}

	return &slackDeps{
		crdClient: crdClient,
		store:     pgstore.NewFromPool(pool),
		queries:   sqlc.New(pool),
	}, nil
}

// startSlackEventPruner bounds growth of Slack's durable dedupe table.
func startSlackEventPruner(ctx context.Context, queries *sqlc.Queries) {
	if queries == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pruneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := queries.PruneSlackEvents(pruneCtx, time.Now().Add(-7*24*time.Hour))
				cancel()
				if err != nil {
					log.Printf("WARN: pruning Slack event dedupe rows: %v", err)
				}
			}
		}
	}()
}

// slackOrchestratorParams is the per-agent configuration an orchestrator serves.
type slackOrchestratorParams struct {
	AgentName string
	Namespace string
	// StoreKey overrides the slack_agent key used in Postgres; defaults to
	// AgentName when empty.
	StoreKey       string
	Defaults       triggersv1alpha1.AgentRunDefaults
	TriggerOwner   client.Object
	TokensSecret   string
	SessionIdle    time.Duration
	BatchWindow    time.Duration
	OwnerUserID    string
	BotDMChannelID string
}

// newSlackOrchestratorFromDeps builds an orchestrator for one SlackAgent on top
// of shared process dependencies.
func newSlackOrchestratorFromDeps(
	deps *slackDeps, web *internalslack.Client, p slackOrchestratorParams,
) *slackOrchestrator {
	sessionIdle := p.SessionIdle
	if sessionIdle <= 0 {
		sessionIdle = defaultSlackSessionIdle
	}
	batchWindow := p.BatchWindow
	if batchWindow <= 0 {
		batchWindow = defaultSlackBatchWindow
	}
	storeKey := p.StoreKey
	if storeKey == "" {
		storeKey = p.AgentName
	}
	return &slackOrchestrator{
		web:            web,
		crdClient:      deps.crdClient,
		store:          deps.store,
		agentName:      p.AgentName,
		namespace:      p.Namespace,
		storeKey:       storeKey,
		defaults:       p.Defaults,
		triggerOwner:   p.TriggerOwner,
		tokensSecret:   strings.TrimSpace(p.TokensSecret),
		ownerUserID:    p.OwnerUserID,
		botDMChannelID: p.BotDMChannelID,
		turnGates:      map[string]*sync.Mutex{},
		queries:        deps.queries,
		sessionIdle:    sessionIdle,
		batchWindow:    batchWindow,
		convQueues:     map[string]*convQueue{},
		assistantCtx:   map[string]string{},
	}
}

func (o *slackOrchestrator) setBotDMChannelID(channelID string) {
	o.dmMu.Lock()
	o.botDMChannelID = channelID
	o.dmMu.Unlock()
}

func (o *slackOrchestrator) botDMChannel() string {
	o.dmMu.RLock()
	defer o.dmMu.RUnlock()
	return o.botDMChannelID
}

// setAssistantContext records the channel the user is viewing next to an
// assistant-pane thread; empty viewedChannel clears it.
func (o *slackOrchestrator) setAssistantContext(channelID, threadTS, viewedChannel string) {
	key := channelID + "|" + threadTS
	o.assistantMu.Lock()
	defer o.assistantMu.Unlock()
	if viewedChannel == "" {
		delete(o.assistantCtx, key)
		return
	}
	o.assistantCtx[key] = viewedChannel
}

// assistantContext returns the channel the user is viewing for an assistant
// conversation, or "" when unknown.
func (o *slackOrchestrator) assistantContext(channelID, threadTS string) string {
	o.assistantMu.Lock()
	defer o.assistantMu.Unlock()
	return o.assistantCtx[channelID+"|"+threadTS]
}

// buildSlackCRDClient builds a controller-runtime client whose scheme knows both
// platform (AgentRun) and triggers (SlackAgent) types.
func buildSlackCRDClient() (client.Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("getting in-cluster config: %w", err)
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		platformv1alpha1.AddToScheme,
		triggersv1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return nil, fmt.Errorf("registering scheme: %w", err)
		}
	}
	return client.New(cfg, client.Options{Scheme: scheme})
}

// turnGate returns the shared in-process serialization lock for one AgentRun.
func (o *slackOrchestrator) turnGate(runName string) *sync.Mutex {
	o.turnMu.Lock()
	defer o.turnMu.Unlock()
	gate := o.turnGates[runName]
	if gate == nil {
		gate = &sync.Mutex{}
		o.turnGates[runName] = gate
	}
	return gate
}

// handleCommand creates or wakes the thread's AgentRun and waits through reply
// or draft delivery before the conversation worker accepts another turn. It
// records the baseline assistant message so reused runs post only new output.
func (o *slackOrchestrator) handleCommand(ctx context.Context, d internalslack.Decision) {
	if strings.TrimSpace(d.Text) == "" && len(d.Files) == 0 {
		return
	}
	if channel := projectSlackTriggerChannel(o.triggerOwner); channel != "" && !strings.EqualFold(strings.TrimPrefix(channel, "#"), strings.TrimPrefix(d.ChannelID, "#")) {
		return
	}
	turnText := o.commandTurnText(ctx, d)

	runName, reused := o.resolveConversationRun(ctx, d)
	gate := o.turnGate(runName)
	gate.Lock()
	defer gate.Unlock()
	baseline := o.maxAssistantMessageID(ctx, runName)
	if reused {
		if err := orchestration.WakeAgentRun(ctx, o.crdClient, o.store, o.namespace, runName, turnText); err != nil {
			o.postErr(ctx, d, "I couldn't resume the run", err)
			return
		}
	} else {
		if err := o.createRun(ctx, runName, d, turnText); err != nil {
			o.postErr(ctx, d, "I couldn't start a run", err)
			return
		}
	}
	o.recordConversationActivity(ctx, d, runName, slackKindCommand)

	// Best-effort feedback: an assistant-pane "is thinking…" status (auto-clears
	// when the first reply posts) plus an :eyes: reaction for channel mentions.
	_ = o.web.SetAssistantStatus(ctx, d.ChannelID, d.ThreadTS, "is thinking…")
	if d.MessageTS != "" {
		_ = o.web.AddReactionAsBot(ctx, d.ChannelID, d.MessageTS, "eyes")
	}
	o.streamReplies(ctx, replyWatch{
		runName:     runName,
		channelID:   d.ChannelID,
		threadTS:    d.ThreadTS,
		messageTS:   d.MessageTS,
		requester:   d.UserID,
		command:     d.Text,
		channelType: d.ChannelType,
		baseline:    baseline,
		gate:        o.replyNeedsApproval(ctx, d),
	})
}

func projectSlackTriggerChannel(owner client.Object) string {
	if owner == nil || owner.GetAnnotations()["triggers.gratefulagents.dev/generated-runtime"] != "true" {
		return ""
	}
	channel := strings.TrimSpace(owner.GetAnnotations()["triggers.gratefulagents.dev/project-trigger-channel"])
	// Slack events carry channel IDs. A human-readable channel name remains
	// useful configuration/display metadata but cannot be compared here without
	// an additional Slack API lookup.
	if strings.HasPrefix(channel, "C") || strings.HasPrefix(channel, "G") || strings.HasPrefix(channel, "D") {
		return channel
	}
	return ""
}

// replyWatch describes one turn's reply-streaming job: which run to watch,
// where the reply goes, and whether it must be held for the owner's approval.
type replyWatch struct {
	runName     string
	channelID   string
	threadTS    string
	messageTS   string // triggering message ts, for reaction bookkeeping
	requester   string // Slack user ID who asked (shown on the approval card)
	command     string // the triggering message text (shown on the approval card)
	channelType string // used to keep public failure details private
	baseline    int64  // newest assistant message id before this turn
	gate        bool   // hold the reply for owner approval instead of posting
}

// replyNeedsApproval reports whether this conversation's replies must be held
// for the owner's approval: only public surfaces (anything beyond a 1:1 DM),
// and only while the SlackAgent's channelReplyMode is not "auto". The mode is
// read live from the CR so dashboard changes apply to the next message without
// a connector restart; a read failure holds the reply (fail safe).
func (o *slackOrchestrator) replyNeedsApproval(ctx context.Context, d internalslack.Decision) bool {
	if !isPublicSurface(d.ChannelType) {
		return false
	}
	agent := &triggersv1alpha1.SlackAgent{}
	if err := o.crdClient.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: o.agentName}, agent); err != nil {
		log.Printf("slack connector %s: reading channelReplyMode (holding reply for approval): %v", o.agentName, err)
		return true
	}
	return agent.Spec.ChannelReplyMode != triggersv1alpha1.SlackChannelReplyAuto
}

// isPublicSurface reports whether a conversation is visible beyond a 1:1 DM
// with the bot: channels, private channels/groups, and group DMs all are.
func isPublicSurface(channelType string) bool {
	return channelType != "im"
}

// commandTurnText assembles the text for this turn: the owner's message plus,
// when known, the channel they are viewing and the contents of small text
// attachments. Agent view supplies point-in-time app_context on the message;
// the stored assistant-thread context is a compatibility fallback.
func (o *slackOrchestrator) commandTurnText(ctx context.Context, d internalslack.Decision) string {
	text := d.Text
	viewed := d.ContextChannelID
	if viewed == "" {
		viewed = o.assistantContext(d.ChannelID, d.ThreadTS)
	}
	if viewed != "" {
		text += "\n\n(Context: the user is currently viewing Slack channel <#" + viewed + ">. " +
			"Requests like \"this channel\" refer to it.)"
	}
	if attachments := o.describeFiles(ctx, d.Files); attachments != "" {
		text += "\n\n" + attachments
	}
	return text
}

func (o *slackOrchestrator) createRun(
	ctx context.Context, runName string, d internalslack.Decision, seedText string,
) error {
	// Resolve defaults first: the agent may carry its own GitHub token secret
	// (Defaults.Secrets.GithubToken), which beats the owner's saved token.
	defaults := o.currentDefaults(ctx)
	gitHubSecret := strings.TrimSpace(defaults.Secrets.GithubToken)
	if gitHubSecret == "" {
		gitHubSecret = slackSavedGitHubSecretName
	}
	if err := o.requireGitHubTokenSecret(ctx, gitHubSecret); err != nil {
		return err
	}
	annotations := map[string]string{
		"triggers.gratefulagents.dev/slack-channel": d.ChannelID,
		"triggers.gratefulagents.dev/slack-thread":  d.ThreadTS,
	}
	_, _, err := triggerctrl.CreateTriggerRun(ctx, o.crdClient, o.store, triggerctrl.TriggerRunSpec{
		RunName:            runName,
		Namespace:          o.namespace,
		TriggerKind:        slackTriggerKind,
		TriggerName:        o.agentName,
		ExternalID:         d.ChannelID + ":" + d.ThreadTS,
		ExternalIdentifier: d.ThreadTS,
		SeedMessage:        seedText,
		Defaults:           defaults,
		OwnerRef:           o.triggerOwner,
		OwnerID:            o.runOwnerID(ctx),
		Labels: map[string]string{
			slackAgentLabel: o.agentName,
			slackKindLabel:  slackKindCommand,
		},
		Annotations: annotations,
		Context: &platformv1alpha1.AgentRunContext{
			ProjectRef: &platformv1alpha1.ProjectRef{Kind: slackTriggerKind, Name: o.agentName},
		},
		GitHubTokenSecret: gitHubSecret,
		SlackTokensSecret: o.tokensSecret,
		SeedLogPrefix:     "slack",
	})
	return err
}

// slackAgentResourceType is the collaboration-store resource type under which
// the dashboard records a SlackAgent's owner, mirroring the dashboard's
// slackResourceType constant.
const slackAgentResourceType = "slackagent"

// runOwnerID resolves the platform user who owns this SlackAgent so the runs
// the connector creates are owned — and therefore manageable (stop, delete) —
// by that user in the dashboard. Best-effort: an unowned agent or a store
// error yields "" and the run is simply created without an owner.
func (o *slackOrchestrator) runOwnerID(ctx context.Context) string {
	if o.store == nil {
		return ""
	}
	ownership, err := o.store.GetResourceOwner(ctx, slackAgentResourceType, o.agentName, o.namespace)
	if err != nil || ownership == nil {
		if err != nil {
			log.Printf("slack connector %s: resolving SlackAgent owner: %v", o.agentName, err)
		}
		return ""
	}
	return ownership.OwnerID
}

// requireGitHubTokenSecret verifies the GitHub token secret a new run would
// mount exists and holds a token, so a conversation fails with a clear message
// instead of a broken run.
func (o *slackOrchestrator) requireGitHubTokenSecret(ctx context.Context, secretName string) error {
	secret := &corev1.Secret{}
	err := o.crdClient.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: secretName}, secret)
	if err == nil && strings.TrimSpace(string(secret.Data[slackSavedGitHubTokenKey])) != "" {
		return nil
	}
	if secretName != slackSavedGitHubSecretName {
		return fmt.Errorf("this agent's GitHub token secret %q is missing or empty; set it again in the agent's settings or clear it to use your saved token", secretName)
	}
	return fmt.Errorf("no saved GitHub token; add it in Settings")
}

// currentDefaults reads the SlackAgent's live Spec.Defaults so configuration
// changes made in the dashboard (model, provider, credentials) take effect on
// the next message without restarting the connector pod. Falls back to the
// startup snapshot if the CR can't be read. WorkflowMode is forced to auto so
// Slack runs share the same finish-gated pacing contract as every other ingress.
// The bundled Slack mode supplies its user-facing delivery contract by default;
// an explicit modeRef remains an operator-controlled override.
func (o *slackOrchestrator) currentDefaults(ctx context.Context) triggersv1alpha1.AgentRunDefaults {
	defaults := o.defaults
	agent := &triggersv1alpha1.SlackAgent{}
	if err := o.crdClient.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: o.agentName}, agent); err != nil {
		log.Printf("slack connector %s: using cached defaults (read SlackAgent failed: %v)", o.agentName, err)
	} else {
		defaults = agent.Spec.Defaults
	}
	defaults.WorkflowMode = platformv1alpha1.WorkflowModeAuto
	if defaults.ModeRef == nil {
		defaults.ModeRef = &platformv1alpha1.ModeRef{Name: slackModeName}
	}
	return defaults
}

// streamReplies polls the run and delivers the current turn's reply, then
// returns. The worker persists exactly one assistant message per chat turn (at
// turn end), so that message — not the run phase — is the completion signal: a
// persistent-pod chat run stays Running between turns and never reaches a
// terminal phase, so waiting for terminal would hang until the deadline. The
// deadline is only a safety net for a turn that produces no reply.
//
// Delivery depends on the watch: ungated replies post straight back to the
// thread; gated replies (public surfaces with channelReplyMode require-approval)
// are held as a channel_reply draft and proposed to the owner instead, in which
// case the :eyes: reaction stays until the owner decides.
func (o *slackOrchestrator) streamReplies(ctx context.Context, w replyWatch) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(20 * time.Minute)
	deadlineNotified := false

	lastPosted := w.baseline

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		run := &platformv1alpha1.AgentRun{}
		if err := o.crdClient.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: w.runName}, run); err != nil {
			if apierrors.IsNotFound(err) {
				o.resolveTurnReaction(ctx, w.channelID, w.messageTS, "x")
				return
			}
			continue
		}

		// Delivering the turn's reply means the turn is complete — stop here.
		if w.gate {
			if text := o.collectNewAssistantText(ctx, w.runName, &lastPosted); text != "" {
				if !o.proposeChannelReply(ctx, w, text) {
					o.resolveTurnReaction(ctx, w.channelID, w.messageTS, "x")
				}
				return
			}
		} else if posted := o.postNewAssistantMessages(ctx, w.runName, w.channelID, w.threadTS, &lastPosted); posted > 0 {
			o.resolveTurnReaction(ctx, w.channelID, w.messageTS, "white_check_mark")
			return
		}

		// A terminal phase without any posted reply means the run ended (e.g.
		// failed) before producing one.
		if isTerminalPhase(run.Status.Phase) {
			if run.Status.Phase == platformv1alpha1.AgentRunPhaseFailed && lastPosted == w.baseline {
				// Public surfaces receive no raw provider/tool error details. They
				// may contain source, credentials, or internal infrastructure data.
				_, _ = o.web.PostMessageAsBot(ctx, w.channelID, slackRunFailureMessage(run, isPublicSurface(w.channelType)), w.threadTS)
			}
			o.resolveTurnReaction(ctx, w.channelID, w.messageTS, "x")
			return
		}

		if !deadlineNotified && time.Now().After(deadline) {
			stillWorking := ":hourglass: Still working — I'll keep watching and post the reply when it is ready."
			_, _ = o.web.PostMessageAsBot(ctx, w.channelID, stillWorking, w.threadTS)
			deadlineNotified = true
			ticker.Reset(30 * time.Second)
		}
	}
}

// resolveTurnReaction swaps the :eyes: "working on it" reaction for a final
// outcome emoji. Best-effort: reactions are feedback, not state.
func (o *slackOrchestrator) resolveTurnReaction(ctx context.Context, channelID, messageTS, outcome string) {
	if messageTS == "" {
		return
	}
	_ = o.web.RemoveReactionAsBot(ctx, channelID, messageTS, "eyes")
	_ = o.web.AddReactionAsBot(ctx, channelID, messageTS, outcome)
}

// postNewAssistantMessages posts assistant messages newer than *lastPosted and
// advances the cursor. Returns how many were posted.
func (o *slackOrchestrator) postNewAssistantMessages(
	ctx context.Context,
	runName, channelID, threadTS string,
	lastPosted *int64,
) int {
	sess, err := o.store.GetSessionByRun(ctx, runName, o.namespace)
	if err != nil {
		return 0
	}
	msgs, err := o.store.GetMessages(ctx, sess.ID)
	if err != nil {
		return 0
	}
	posted := 0
	for _, m := range msgs {
		if m.Role != roleAssistant || m.ID <= *lastPosted {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" {
			*lastPosted = m.ID
			continue
		}
		// Agent output is markdown; convert so Slack renders it properly.
		if _, perr := o.web.PostMessageAsBot(ctx, channelID, internalslack.ToMrkdwn(text), threadTS); perr != nil {
			log.Printf("slack connector %s: posting reply for %s: %v", o.agentName, runName, perr)
			break
		}
		*lastPosted = m.ID
		posted++
	}
	return posted
}

// collectNewAssistantText gathers assistant messages newer than *cursor (joined
// oldest-first) and advances the cursor past them. Used by gated turns, where
// the reply is held for approval as one draft instead of being posted.
func (o *slackOrchestrator) collectNewAssistantText(ctx context.Context, runName string, cursor *int64) string {
	sess, err := o.store.GetSessionByRun(ctx, runName, o.namespace)
	if err != nil {
		return ""
	}
	msgs, err := o.store.GetMessages(ctx, sess.ID)
	if err != nil {
		return ""
	}
	var parts []string
	for _, m := range msgs {
		if m.Role != roleAssistant || m.ID <= *cursor {
			continue
		}
		*cursor = m.ID
		if text := strings.TrimSpace(m.Content); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// proposeChannelReply holds a public-surface reply for the owner's approval: it
// records a channel_reply draft and posts the approval card to the owner's
// control DM. Nothing reaches the thread until the owner approves. Returns
// false when the approval request could not be delivered (fail closed: the
// reply is never posted unapproved), after leaving a status note in the thread.
func (o *slackOrchestrator) proposeChannelReply(ctx context.Context, w replyWatch, text string) bool {
	warn := func(summary string, err error) bool {
		log.Printf("slack connector %s: %s for %s: %v", o.agentName, summary, w.runName, err)
		_, _ = o.web.PostMessageAsBot(ctx, w.channelID,
			":warning: I have a reply ready, but I couldn't reach my owner for approval.", w.threadTS)
		return false
	}
	botDMChannelID := o.botDMChannel()
	if botDMChannelID == "" {
		return warn("requesting reply approval", fmt.Errorf("owner control DM unresolved"))
	}
	draft, err := o.queries.CreateSlackDraft(ctx, sqlc.CreateSlackDraftParams{
		SlackAgent:   o.agentName,
		Namespace:    o.namespace,
		OwnerSubject: o.ownerUserID,
		ChannelID:    w.channelID,
		ThreadTs:     w.threadTS,
		TargetUser:   w.requester,
		IncomingText: w.command,
		DraftText:    text,
		Status:       slackDraftPending,
		NotifyMsgTs:  "",
		Kind:         slackDraftKindChannelReply,
		OriginMsgTs:  w.messageTS,
		RunName:      w.runName,
	})
	if err != nil {
		return warn("persisting channel-reply draft", err)
	}
	blocks := internalslack.BuildChannelReplyApprovalBlocks(
		draft.ID.String(), "<@"+w.requester+">", w.channelID, w.command, text)
	const fallback = "A reply is ready for your approval."
	ts, err := o.web.PostMessageAsBotBlocks(ctx, botDMChannelID, fallback, "", blocks...)
	if err != nil {
		return warn("posting reply approval prompt", err)
	}
	o.recordNotifyTS(ctx, draft.ID, ts)
	return true
}

// recordNotifyTS stores the control-DM message ts on the draft so later
// interactions (edit modal, regenerate) can refresh that message.
func (o *slackOrchestrator) recordNotifyTS(ctx context.Context, id uuid.UUID, ts string) {
	if ts == "" {
		return
	}
	if err := o.queries.SetSlackDraftNotifyTS(ctx, sqlc.SetSlackDraftNotifyTSParams{ID: id, NotifyMsgTs: ts}); err != nil {
		log.Printf("slack connector %s: recording notify ts: %v", o.agentName, err)
	}
}

func (o *slackOrchestrator) maxAssistantMessageID(ctx context.Context, runName string) int64 {
	sess, err := o.store.GetSessionByRun(ctx, runName, o.namespace)
	if err != nil {
		return 0
	}
	msgs, err := o.store.GetMessages(ctx, sess.ID)
	if err != nil {
		return 0
	}
	var max int64
	for _, m := range msgs {
		if m.Role == roleAssistant && m.ID > max {
			max = m.ID
		}
	}
	return max
}

func (o *slackOrchestrator) postErr(ctx context.Context, d internalslack.Decision, summary string, err error) {
	log.Printf("slack connector %s: %s: %v", o.agentName, summary, err)
	_, _ = o.web.PostMessageAsBot(ctx, d.ChannelID, ":warning: "+summary+".", d.ThreadTS)
}

func isTerminalPhase(phase platformv1alpha1.AgentRunPhase) bool {
	switch phase {
	case platformv1alpha1.AgentRunPhaseSucceeded,
		platformv1alpha1.AgentRunPhaseFailed,
		platformv1alpha1.AgentRunPhaseCancelled:
		return true
	default:
		return false
	}
}

// slackRunFailureMessage returns a generic public failure. Detailed status is
// limited to a 1:1 owner DM because raw errors may contain secrets or source.
func slackRunFailureMessage(run *platformv1alpha1.AgentRun, public bool) string {
	if public {
		return ":warning: The run failed before producing a reply. Open the authenticated dashboard for details."
	}
	detail := strings.TrimSpace(run.Status.LastError)
	if detail == "" {
		return ":warning: The run failed before producing a reply."
	}
	const maxDetail = 1500
	if len(detail) > maxDetail {
		detail = detail[:maxDetail] + "…"
	}
	return ":warning: The run failed before producing a reply:\n```" + detail + "```"
}

func databaseURL() string {
	return strings.TrimSpace(os.Getenv("DATABASE_URL"))
}
