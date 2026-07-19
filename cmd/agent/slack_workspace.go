package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	triggerctrl "github.com/gratefulagents/gratefulagents/internal/controller/triggers"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// slackMemberSyncInterval is how often the workspace connector re-lists member
// SlackAgents to pick up joins, config changes, and departures.
const slackMemberSyncInterval = 30 * time.Second

// slackOnboardHintInterval rate-limits the "create your agent" hint an unmapped
// Slack user receives when they talk to the shared bot.
const slackOnboardHintInterval = time.Hour

// slackWorkspaceOnboardHint is sent to workspace users who talk to the shared
// bot before binding a SlackAgent to the workspace.
const slackWorkspaceOnboardHint = "Hi! I'm this workspace's agent, but you don't have an agent profile yet. " +
	"Create one in the dashboard (Slack → Join workspace app) and I'll pick it up within a minute."

// slackWorkspaceConfig is resolved from the workspace connector pod's
// environment, injected by the SlackWorkspace controller.
type slackWorkspaceConfig struct {
	WorkspaceName string
	Namespace     string
	BotToken      string
	AppToken      string
	TeamID        string // optional pin; enforced when set
	HealthAddr    string
	BatchWindow   time.Duration
}

func loadSlackWorkspaceConfig() (slackWorkspaceConfig, error) {
	cfg := slackWorkspaceConfig{
		WorkspaceName: strings.TrimSpace(os.Getenv("SLACK_WORKSPACE_NAME")),
		Namespace:     strings.TrimSpace(os.Getenv("POD_NAMESPACE")),
		BotToken:      strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN")),
		AppToken:      strings.TrimSpace(os.Getenv("SLACK_APP_TOKEN")),
		TeamID:        strings.TrimSpace(os.Getenv("SLACK_TEAM_ID")),
		HealthAddr:    strings.TrimSpace(os.Getenv("SLACK_HEALTH_ADDR")),
		BatchWindow:   slackBatchWindow("SLACK_BATCH_WINDOW_MS"),
	}
	if cfg.HealthAddr == "" {
		cfg.HealthAddr = ":8080"
	}
	if cfg.AppToken == "" {
		return cfg, errors.New("SLACK_APP_TOKEN (xapp-) is required for Socket Mode")
	}
	if cfg.BotToken == "" {
		return cfg, errors.New("SLACK_BOT_TOKEN (xoxb-) is required")
	}
	return cfg, nil
}

// runSlackWorkspace is the shared-app connector: one Socket Mode connection for
// the whole Slack workspace, routing each event to the sending user's member
// SlackAgent (their orchestrator, namespace, and credentials).
func runSlackWorkspace() error {
	cfg, err := loadSlackWorkspaceConfig()
	if err != nil {
		log.Printf("ERROR: slack workspace connector config: %v", err)
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	webClient, err := internalslack.New(internalslack.Tokens{BotToken: cfg.BotToken, AppToken: cfg.AppToken})
	if err != nil {
		log.Printf("ERROR: building slack client: %v", err)
		return err
	}

	botIdentity, err := webClient.AuthTestBot(ctx)
	if err != nil {
		log.Printf("ERROR: validating bot token: %v", err)
		return err
	}
	if cfg.TeamID != "" && botIdentity.TeamID != cfg.TeamID {
		log.Printf("ERROR: slack workspace %s is pinned to team %s but tokens belong to team %s",
			cfg.WorkspaceName, cfg.TeamID, botIdentity.TeamID)
		return errors.New("slack workspace team mismatch")
	}
	log.Printf("slack workspace connector %s/%s authenticated as bot user=%s team=%s",
		cfg.Namespace, cfg.WorkspaceName, botIdentity.UserID, botIdentity.TeamID)

	backend := &workspaceSlackBackend{
		cfg:         cfg,
		web:         webClient,
		botUserID:   botIdentity.UserID,
		teamID:      firstNonEmpty(cfg.TeamID, botIdentity.TeamID),
		members:     map[string]*workspaceMember{},
		onboardedAt: map[string]time.Time{},
	}

	// Member handling needs Postgres + the CRD client; without them the socket
	// and health endpoints still run so the Deployment stays diagnosable.
	if deps, derr := newSlackDeps(ctx); derr != nil {
		log.Printf("WARN: slack workspace %s: member handling disabled: %v", cfg.WorkspaceName, derr)
	} else {
		backend.deps = deps
		startSlackEventPruner(ctx, deps.queries)
		defer func() { _ = deps.store.Close() }()
		backend.syncMembers(ctx)
		go backend.memberSyncLoop(ctx)
	}

	conn := &slackConnector{
		name:       cfg.WorkspaceName,
		healthAddr: cfg.HealthAddr,
		botToken:   cfg.BotToken,
		appToken:   cfg.AppToken,
		backend:    backend,
	}

	healthSrv := conn.startHealthServer()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = healthSrv.Shutdown(shutdownCtx)
	}()

	conn.run(ctx)
	return nil
}

// workspaceMember is one SlackAgent served by the shared connector.
type workspaceMember struct {
	namespace  string
	name       string
	userID     string // owner's Slack user ID
	commanders []string
	generation int64
	orch       *slackOrchestrator
	// botDM is the member<->bot control DM, resolved lazily.
	botDM  string
	ctx    context.Context
	cancel context.CancelFunc
}

// workspaceSlackBackend routes shared-app events to member orchestrators by the
// acting Slack user.
type workspaceSlackBackend struct {
	cfg       slackWorkspaceConfig
	web       *internalslack.Client
	deps      *slackDeps
	botUserID string
	teamID    string

	mu      sync.Mutex
	members map[string]*workspaceMember // keyed by owner Slack user ID
	// onboardedAt rate-limits onboarding hints per unmapped user.
	onboardedAt map[string]time.Time
}

// allowTeam drops events from any Slack team other than the pinned/resolved
// one: belt-and-braces on top of the app being non-distributed.
func (b *workspaceSlackBackend) allowTeam(teamID string) bool {
	return teamID == "" || b.teamID == "" || teamID == b.teamID
}

// memberSyncLoop periodically refreshes the member registry.
func (b *workspaceSlackBackend) memberSyncLoop(ctx context.Context) {
	ticker := time.NewTicker(slackMemberSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.syncMembers(ctx)
		}
	}
}

// syncMembers rebuilds the Slack-user → member index from SlackAgents whose
// workspaceRef points at this workspace. Existing orchestrators are kept unless
// the agent's spec generation changed (conversation coalescing state lives in
// the orchestrator, so avoid rebuilding it needlessly).
func (b *workspaceSlackBackend) syncMembers(ctx context.Context) {
	if b.deps == nil {
		return
	}
	agents := &triggersv1alpha1.SlackAgentList{}
	if err := b.deps.crdClient.List(ctx, agents); err != nil {
		log.Printf("WARN: slack workspace %s: listing member SlackAgents: %v", b.cfg.WorkspaceName, err)
		return
	}

	next := map[string]*workspaceMember{}
	for i := range agents.Items {
		agent := &agents.Items[i]
		ns, name := agent.ResolvedWorkspaceRef()
		if ns != b.cfg.Namespace || name != b.cfg.WorkspaceName {
			continue
		}
		if agent.Spec.Suspend {
			continue
		}
		userID := strings.TrimSpace(agent.Spec.SlackUserID)
		if userID == "" {
			log.Printf("WARN: slack workspace %s: member %s/%s has no slackUserId; skipping",
				b.cfg.WorkspaceName, agent.Namespace, agent.Name)
			continue
		}
		if existing, taken := next[userID]; taken {
			log.Printf("WARN: slack workspace %s: %s/%s and %s/%s both claim Slack user %s; keeping %s/%s",
				b.cfg.WorkspaceName, existing.namespace, existing.name, agent.Namespace, agent.Name, userID,
				existing.namespace, existing.name)
			continue
		}
		next[userID] = b.buildMember(ctx, agent, userID)
	}

	b.mu.Lock()
	previous := b.members
	b.members = next
	b.mu.Unlock()
	for userID, member := range previous {
		if next[userID] != member && member.cancel != nil {
			member.cancel()
		}
	}
}

// buildMember reuses the previous orchestrator when the agent spec is
// unchanged, otherwise constructs a fresh one from the agent's current spec.
func (b *workspaceSlackBackend) buildMember(parentCtx context.Context, agent *triggersv1alpha1.SlackAgent, userID string) *workspaceMember {
	b.mu.Lock()
	prev := b.members[userID]
	b.mu.Unlock()
	if prev != nil && prev.namespace == agent.Namespace && prev.name == agent.Name && prev.generation == agent.Generation {
		return prev
	}

	sessionIdle := defaultSlackSessionIdle
	if m := agent.Spec.SessionIdleMinutes; m != nil && *m > 0 {
		sessionIdle = time.Duration(*m) * time.Minute
	}
	memberCtx, cancel := context.WithCancel(parentCtx)
	orch := newSlackOrchestratorFromDeps(b.deps, b.web, slackOrchestratorParams{
		AgentName: agent.Name,
		Namespace: agent.Namespace,
		// Namespace-qualify Postgres rows: member agent names are only unique
		// per namespace, and the shared tables key by slack_agent.
		StoreKey:     agent.Namespace + "/" + agent.Name,
		Defaults:     agent.Spec.Defaults,
		TriggerOwner: agent.DeepCopy(),
		// Child runs authenticate agent-side Slack read tools with the shared
		// bot token, synced into the member namespace by the workspace
		// controller.
		TokensSecret: triggerctrl.SlackWorkspaceBotSecretName(b.cfg.WorkspaceName),
		SessionIdle:  sessionIdle,
		BatchWindow:  b.cfg.BatchWindow,
		OwnerUserID:  userID,
	})
	member := &workspaceMember{
		namespace:  agent.Namespace,
		name:       agent.Name,
		userID:     userID,
		commanders: agent.Spec.Commanders,
		generation: agent.Generation,
		orch:       orch,
		ctx:        memberCtx,
		cancel:     cancel,
	}
	if prev != nil {
		member.botDM = prev.botDM
		orch.setBotDMChannelID(prev.botDM)
	}
	return member
}

func (b *workspaceSlackBackend) memberByUser(userID string) *workspaceMember {
	if strings.TrimSpace(userID) == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.members[userID]
}

// memberForCommander returns the single member whose commanders list contains
// userID. Ambiguous (multiple members) resolves to nil: with one shared bot
// there is no way to know whose agent a non-member meant.
func (b *workspaceSlackBackend) memberForCommander(userID string) *workspaceMember {
	if strings.TrimSpace(userID) == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var found *workspaceMember
	for _, m := range b.members {
		for _, c := range m.commanders {
			if strings.TrimSpace(c) == userID {
				if found != nil {
					return nil
				}
				found = m
				break
			}
		}
	}
	return found
}

// routerConfigFor adapts a member to the single-agent router: every DM to the
// bot routes as a command, gated on the sender being the member.
func (b *workspaceSlackBackend) routerConfigFor(ctx context.Context, m *workspaceMember) internalslack.RouterConfig {
	return internalslack.RouterConfig{
		OwnerUserID:    m.userID,
		BotUserID:      b.botUserID,
		BotDMChannelID: b.memberBotDM(ctx, m),
		Commanders:     m.commanders,
	}
}

// memberBotDM lazily resolves and caches the member's control DM with the bot,
// propagating it onto the member's orchestrator (used by channel-reply
// approval cards and command replies).
func (b *workspaceSlackBackend) memberBotDM(ctx context.Context, m *workspaceMember) string {
	b.mu.Lock()
	dm := m.botDM
	b.mu.Unlock()
	if dm != "" {
		return dm
	}
	dm, err := b.web.OpenIMWithUser(ctx, m.userID)
	if err != nil {
		log.Printf("WARN: slack workspace %s: resolving DM for %s: %v", b.cfg.WorkspaceName, m.userID, err)
		return ""
	}
	b.mu.Lock()
	m.botDM = dm
	if m.orch != nil {
		m.orch.setBotDMChannelID(dm)
	}
	b.mu.Unlock()
	return dm
}

func (b *workspaceSlackBackend) handleMessage(ctx context.Context, msg internalslack.InboundMessage) {
	// Never react to our own bot or any bot/app message: prevents loops.
	if msg.UserID != "" && msg.UserID == b.botUserID {
		return
	}
	if strings.TrimSpace(msg.BotID) != "" {
		return
	}

	member := b.memberByUser(msg.UserID)
	if member == nil && msg.IsAppMention {
		// A non-member @mentioned the shared bot: they may be a designated
		// commander of exactly one member's agent.
		member = b.memberForCommander(msg.UserID)
	}
	if member == nil {
		b.hintOnboarding(ctx, msg)
		return
	}
	decision := internalslack.Route(msg, b.routerConfigFor(ctx, member))
	dispatchSlackDecision(member.ctx, b.web, member.namespace+"/"+member.name, member.orch, decision)
}

// hintOnboarding tells an unmapped user (in their DM with the bot, or on an
// explicit @mention) how to get their own agent, at most once per hour.
func (b *workspaceSlackBackend) hintOnboarding(ctx context.Context, msg internalslack.InboundMessage) {
	if msg.ChannelType != "im" && !msg.IsAppMention {
		return // don't volunteer in channels the user didn't address us in
	}
	if st := strings.TrimSpace(msg.SubType); st != "" && st != "file_share" {
		return // ignore edits/deletes/joins
	}
	b.mu.Lock()
	last, seen := b.onboardedAt[msg.UserID]
	now := time.Now()
	if seen && now.Sub(last) < slackOnboardHintInterval {
		b.mu.Unlock()
		return
	}
	b.onboardedAt[msg.UserID] = now
	b.mu.Unlock()

	var err error
	if msg.ChannelType == "im" {
		_, err = b.web.PostMessageAsBot(ctx, msg.ChannelID, slackWorkspaceOnboardHint, "")
	} else {
		err = b.web.PostEphemeralAsBot(ctx, msg.ChannelID, msg.UserID, slackWorkspaceOnboardHint)
	}
	if err != nil {
		log.Printf("WARN: slack workspace %s: onboarding hint for %s: %v", b.cfg.WorkspaceName, msg.UserID, err)
	}
}

func (b *workspaceSlackBackend) handleInteraction(ctx context.Context, callback slackgo.InteractionCallback) {
	member := b.memberByUser(callback.User.ID)
	if member == nil || member.orch == nil {
		return
	}
	member.orch.handleInteraction(member.ctx, callback)
}

func (b *workspaceSlackBackend) handleAssistantStarted(
	ctx context.Context, e *slackevents.AssistantThreadStartedEvent,
) {
	if member := b.memberByUser(e.AssistantThread.UserID); member != nil {
		const greeting = "Hi! I'm your agent. Ask me to do something, or pick a prompt below."
		postAssistantGreeting(ctx, b.web, b.cfg.WorkspaceName, e, greeting)
		return
	}
	channelID := e.AssistantThread.ChannelID
	threadTS := e.AssistantThread.ThreadTimeStamp
	if channelID == "" || threadTS == "" {
		return
	}
	if _, err := b.web.PostMessageAsBot(ctx, channelID, slackWorkspaceOnboardHint, threadTS); err != nil {
		log.Printf("WARN: slack workspace %s: onboarding greeting: %v", b.cfg.WorkspaceName, err)
	}
}

func (b *workspaceSlackBackend) handleAssistantContextChanged(thread slackevents.AssistantThread) {
	member := b.memberByUser(thread.UserID)
	if member == nil || member.orch == nil || thread.ChannelID == "" || thread.ThreadTimeStamp == "" {
		return
	}
	member.orch.setAssistantContext(thread.ChannelID, thread.ThreadTimeStamp, thread.Context.ChannelID)
}

func (b *workspaceSlackBackend) handleAppHome(ctx context.Context, userID string) {
	member := b.memberByUser(userID)
	if member == nil || member.orch == nil {
		return
	}
	member.orch.handleAppHome(member.ctx, userID)
}
