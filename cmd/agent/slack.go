package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

// slackConnectorConfig is resolved from the connector pod's environment. The
// SlackAgent controller injects these (tokens from the referenced Secret, name
// + namespace identifying the owning CR).
type slackConnectorConfig struct {
	AgentName   string
	Namespace   string
	BotToken    string
	UserToken   string
	AppToken    string
	HealthAddr  string
	SlackUserID string
	TeamID      string
	Commanders  []string
	SessionIdle time.Duration
	BatchWindow time.Duration
}

// defaultSlackSessionIdle is the fallback conversation idle window: after this
// much inactivity a new incoming message starts a fresh AgentRun instead of
// resuming the previous one, bounding a conversation's context and cost.
const defaultSlackSessionIdle = 12 * time.Hour

// defaultSlackBatchWindow is how long the connector waits for more messages in a
// conversation before answering, so a rapid burst ("what's the time in japan" /
// "and in usa") is handled as one turn with one reply instead of racing.
const defaultSlackBatchWindow = 4 * time.Second

// slackAppContextChangedEvent bridges the Agent messaging experience event
// until slack-go exposes a typed equivalent. Registering it keeps Socket Mode
// from rejecting the otherwise unknown event before the connector can ACK it.
type slackAppContextChangedEvent struct {
	Type string `json:"type"`
}

func init() {
	eventType := slackevents.EventsAPIType("app_context_changed")
	if _, supported := slackevents.EventsAPIInnerEventMapping[eventType]; !supported {
		slackevents.EventsAPIInnerEventMapping[eventType] = slackAppContextChangedEvent{}
	}
}

func loadSlackConnectorConfig() (slackConnectorConfig, error) {
	cfg := slackConnectorConfig{
		AgentName:   strings.TrimSpace(os.Getenv("SLACK_AGENT_NAME")),
		Namespace:   strings.TrimSpace(os.Getenv("POD_NAMESPACE")),
		BotToken:    strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN")),
		UserToken:   strings.TrimSpace(os.Getenv("SLACK_USER_TOKEN")),
		AppToken:    strings.TrimSpace(os.Getenv("SLACK_APP_TOKEN")),
		SlackUserID: strings.TrimSpace(os.Getenv("SLACK_USER_ID")),
		TeamID:      strings.TrimSpace(os.Getenv("SLACK_TEAM_ID")),
		HealthAddr:  strings.TrimSpace(os.Getenv("SLACK_HEALTH_ADDR")),
		Commanders:  slackEnvList("SLACK_COMMANDERS"),
		SessionIdle: slackSessionIdle("SLACK_SESSION_IDLE_MINUTES"),
		BatchWindow: slackBatchWindow("SLACK_BATCH_WINDOW_MS"),
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

// slackSessionIdle parses a minutes env var into a duration, falling back to
// defaultSlackSessionIdle when unset or non-positive.
func slackSessionIdle(name string) time.Duration {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return time.Duration(v) * time.Minute
	}
	return defaultSlackSessionIdle
}

// slackBatchWindow parses a milliseconds env var into a duration, falling back to
// defaultSlackBatchWindow when unset or non-positive.
func slackBatchWindow(name string) time.Duration {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name))); err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return defaultSlackBatchWindow
}

// slackEnvList parses a comma-separated env var into a trimmed, non-empty slice.
func slackEnvList(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	var out []string
	for part := range strings.SplitSeq(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// runSlack is the entrypoint for `agent slack`. It serves two modes: the
// dedicated per-user connector (one Slack app per SlackAgent) and, when
// SLACK_WORKSPACE_NAME is set, the shared workspace connector (one Slack app
// serving every member SlackAgent). Socket Mode load-balances events across an
// app's open sockets, so each app's events must be consumed by exactly one
// process — which is why a shared app gets one connector that routes per user.
func runSlack() error {
	if strings.TrimSpace(os.Getenv("SLACK_WORKSPACE_NAME")) != "" {
		return runSlackWorkspace()
	}
	cfg, err := loadSlackConnectorConfig()
	if err != nil {
		log.Printf("ERROR: slack connector config: %v", err)
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	webClient, err := internalslack.New(internalslack.Tokens{
		BotToken:  cfg.BotToken,
		UserToken: cfg.UserToken,
		AppToken:  cfg.AppToken,
	})
	if err != nil {
		log.Printf("ERROR: building slack client: %v", err)
		return err
	}

	botIdentity, err := webClient.AuthTestBot(ctx)
	if err != nil {
		log.Printf("ERROR: validating bot token: %v", err)
		return err
	}
	if botIdentity.TeamID == "" {
		return fmt.Errorf("slack bot auth.test returned no team ID")
	}
	if cfg.TeamID != "" && botIdentity.TeamID != cfg.TeamID {
		return fmt.Errorf(
			"slack connector is pinned to team %s but bot token belongs to team %s",
			cfg.TeamID,
			botIdentity.TeamID,
		)
	}
	log.Printf("slack connector %s/%s authenticated as bot user=%s team=%s",
		cfg.Namespace, cfg.AgentName, botIdentity.UserID, botIdentity.TeamID)

	backend := &dedicatedSlackBackend{
		cfg: cfg, web: webClient, botUserID: botIdentity.UserID,
		ownerUserID: cfg.SlackUserID, teamID: botIdentity.TeamID,
	}

	// A user token is optional (it powers slack_search and owner resolution).
	// When supplied it must identify the same workspace and, when explicitly
	// configured, the same owner; mixed credentials fail closed at startup.
	if webClient.HasUser() {
		userIdentity, uerr := webClient.AuthTestUser(ctx)
		if uerr != nil {
			return fmt.Errorf("validating slack user token: %w", uerr)
		}
		if userIdentity.TeamID != backend.teamID {
			return fmt.Errorf(
				"slack bot token belongs to team %s but user token belongs to team %s",
				backend.teamID,
				userIdentity.TeamID,
			)
		}
		if backend.ownerUserID != "" && userIdentity.UserID != backend.ownerUserID {
			return fmt.Errorf(
				"slack user token belongs to %s but configured owner is %s",
				userIdentity.UserID,
				backend.ownerUserID,
			)
		}
		backend.ownerUserID = userIdentity.UserID
		log.Printf("slack connector owner user=%s", backend.ownerUserID)
	}

	// Resolve the owner<->bot control DM so the router can tell owner commands
	// apart from other conversations. Best-effort: command routing still gates
	// on the sender being the owner without it.
	if backend.ownerUserID != "" {
		if dm, derr := webClient.OpenIMWithUser(ctx, backend.ownerUserID); derr != nil {
			log.Printf("WARN: slack connector %s: could not resolve bot DM channel: %v", cfg.AgentName, derr)
		} else {
			backend.botDMChannelID = dm
		}
	}

	// The orchestrator turns owner commands into AgentRuns and streams replies
	// back. It needs Postgres + the CRD client; if unavailable, the connector
	// still runs (events are logged) so the socket and health stay up.
	if orch, oerr := newSlackOrchestrator(ctx, cfg, webClient); oerr != nil {
		log.Printf("WARN: slack connector %s: command handling disabled: %v", cfg.AgentName, oerr)
	} else {
		orch.ownerUserID = backend.ownerUserID
		orch.setBotDMChannelID(backend.botDMChannelID)
		backend.orch = orch
		startSlackEventPruner(ctx, orch.queries)
		defer func() { _ = orch.store.Close() }()
	}

	conn := &slackConnector{
		name:       cfg.AgentName,
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

// slackBackend routes decoded Slack events to the owning orchestrator(s). The
// dedicated backend serves a single SlackAgent; the workspace backend serves
// every member of a shared SlackWorkspace app.
type slackBackend interface {
	// handleMessage classifies and acts on a normalized inbound message.
	handleMessage(ctx context.Context, msg internalslack.InboundMessage)
	// handleInteraction dispatches a Block Kit interaction to its owner.
	handleInteraction(ctx context.Context, callback slackgo.InteractionCallback)
	// handleAssistantStarted greets a user who opened the assistant pane.
	handleAssistantStarted(ctx context.Context, e *slackevents.AssistantThreadStartedEvent)
	// handleAssistantContextChanged tracks the channel a user is viewing.
	handleAssistantContextChanged(thread slackevents.AssistantThread)
	// handleAppHome refreshes the App Home tab for the given user.
	handleAppHome(ctx context.Context, userID string)
	// allowTeam reports whether events from this Slack team may be processed.
	allowTeam(teamID string) bool
}

// slackConnector owns the Socket Mode connection lifecycle for one Slack app
// and forwards decoded events to its backend.
type slackConnector struct {
	name       string
	healthAddr string
	botToken   string
	appToken   string
	connected  atomic.Bool
	backend    slackBackend
	eventSlots chan struct{}
}

// startHealthServer exposes /healthz (always 200 once the process is up) and
// /readyz (200 only while the Socket Mode connection is live) so the connector
// Deployment can use them as liveness/readiness probes.
func (c *slackConnector) startHealthServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if c.connected.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "socket not connected", http.StatusServiceUnavailable)
	})
	srv := &http.Server{Addr: c.healthAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("WARN: slack connector health server: %v", err)
		}
	}()
	return srv
}

// run connects via Socket Mode and consumes events until the context is
// cancelled. socketmode.RunContext reconnects internally with backoff; if it
// returns due to a fatal error we retry with our own backoff unless shutting
// down.
func (c *slackConnector) run(ctx context.Context) {
	api := slackgo.New(c.botToken, slackgo.OptionAppLevelToken(c.appToken))
	sm := socketmode.New(api)

	go c.consume(ctx, sm)

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := sm.RunContext(ctx)
		if ctx.Err() != nil {
			return
		}
		c.connected.Store(false)
		log.Printf("WARN: slack socket mode loop exited (%v); reconnecting in %s", err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// consume drains the Socket Mode event channel, tracking connection state and
// acking inbound requests. Phase 0 acks and logs; later phases dispatch
// events_api / interactive / slash_commands to handlers.
func (c *slackConnector) consume(ctx context.Context, sm *socketmode.Client) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sm.Events:
			if !ok {
				return
			}
			c.handleSocketEvent(ctx, sm, evt)
		}
	}
}

const slackEventConcurrency = 64

func (c *slackConnector) dispatchAsync(ctx context.Context, fn func()) {
	if c.eventSlots == nil {
		c.eventSlots = make(chan struct{}, slackEventConcurrency)
	}
	select {
	case c.eventSlots <- struct{}{}:
	case <-ctx.Done():
		return
	}
	go func() {
		defer func() { <-c.eventSlots }()
		fn()
	}()
}

func (c *slackConnector) handleSocketEvent(ctx context.Context, sm *socketmode.Client, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnecting:
		log.Printf("slack connector %s connecting…", c.name)
	case socketmode.EventTypeConnected:
		c.connected.Store(true)
		log.Printf("slack connector %s connected", c.name)
	case socketmode.EventTypeHello:
		// Connection handshake completed.
	case socketmode.EventTypeDisconnect:
		c.connected.Store(false)
		log.Printf("slack connector %s disconnected", c.name)
	case socketmode.EventTypeInvalidAuth:
		c.connected.Store(false)
		log.Printf("ERROR: slack connector %s invalid auth (check app/bot tokens)", c.name)
	case socketmode.EventTypeConnectionError, socketmode.EventTypeIncomingError, socketmode.EventTypeErrorWriteFailed, socketmode.EventTypeErrorBadMessage:
		log.Printf("WARN: slack connector %s transport event: %s", c.name, evt.Type)
	case socketmode.EventTypeEventsAPI:
		// Ack first so Slack does not retry; then process off the consume loop so
		// run creation / streaming never blocks event intake.
		if evt.Request != nil {
			if err := sm.Ack(*evt.Request); err != nil {
				log.Printf("WARN: ack %s envelope=%s: %v", evt.Type, evt.Request.EnvelopeID, err)
			}
		}
		c.dispatchAsync(ctx, func() { c.handleEventsAPI(ctx, evt) })
	case socketmode.EventTypeInteractive:
		if evt.Request != nil {
			if err := sm.Ack(*evt.Request); err != nil {
				log.Printf("WARN: ack %s envelope=%s: %v", evt.Type, evt.Request.EnvelopeID, err)
			}
		}
		c.dispatchAsync(ctx, func() { c.handleInteractive(ctx, evt) })
	case socketmode.EventTypeSlashCommand:
		if evt.Request != nil {
			if err := sm.Ack(*evt.Request); err != nil {
				log.Printf("WARN: ack %s envelope=%s: %v", evt.Type, evt.Request.EnvelopeID, err)
			}
		}
		// TODO(phase1-refine): slash-command entrypoint.
		log.Printf("slack connector %s received %s event (handler pending)", c.name, evt.Type)
	default:
		// Ignore other internal event types.
	}
}

// handleInteractive dispatches Block Kit interactions (draft approve/dismiss).
func (c *slackConnector) handleInteractive(ctx context.Context, evt socketmode.Event) {
	callback, ok := evt.Data.(slackgo.InteractionCallback)
	if !ok {
		log.Printf("WARN: slack connector %s: unexpected interactive payload type %T", c.name, evt.Data)
		return
	}
	if !c.backend.allowTeam(callback.Team.ID) {
		log.Printf("WARN: slack connector %s: dropping interaction from foreign team %s", c.name, callback.Team.ID)
		return
	}
	c.backend.handleInteraction(ctx, callback)
}

// handleEventsAPI decodes a Slack Events API envelope and dispatches it. Agent
// view apps use app_home_opened plus message.im/app_context_changed; legacy
// assistant lifecycle events remain supported while existing apps migrate.
func (c *slackConnector) handleEventsAPI(ctx context.Context, evt socketmode.Event) {
	apiEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		log.Printf("WARN: slack connector %s: unexpected events_api payload type %T", c.name, evt.Data)
		return
	}
	if !c.backend.allowTeam(apiEvent.TeamID) {
		log.Printf("WARN: slack connector %s: dropping event from foreign team %s", c.name, apiEvent.TeamID)
		return
	}
	switch inner := apiEvent.InnerEvent.Data.(type) {
	case *slackevents.AssistantThreadStartedEvent:
		c.backend.handleAssistantContextChanged(inner.AssistantThread)
		c.backend.handleAssistantStarted(ctx, inner)
		return
	case *slackevents.AssistantThreadContextChangedEvent:
		// Track the channel the user is viewing so "this channel" style requests
		// resolve to it.
		c.backend.handleAssistantContextChanged(inner.AssistantThread)
		return
	case *slackevents.AppHomeOpenedEvent:
		switch inner.Tab {
		case "home":
			c.backend.handleAppHome(ctx, inner.User)
		case "messages":
			// This is the Agent view signal that the user opened the app DM.
			// Suggested prompts are pinned globally by the app manifest, so no
			// per-open Web API call or repetitive greeting is needed.
		}
		return
	case *slackAppContextChangedEvent:
		// Subscribing enables app_context on message.im events. We consume that
		// point-in-time context below instead of retaining these ambient updates.
		return
	}
	msg, ok := inboundFromInnerEvent(apiEvent.InnerEvent.Data)
	if !ok {
		return // not a message-like event we route
	}
	// Fail closed when the envelope is unavailable: bot-DM gating (owner only)
	// is the conservative default; events from a legacy user-token subscription
	// always carry an authorizations array identifying their subscription.
	msg.ViaBotEvent = true
	if evt.Request != nil {
		msg.ViaBotEvent = slackEventViaBot(evt.Request.Payload)
	}
	if evt.Request != nil {
		msg.ContextChannelID = slackEventContextChannel(evt.Request.Payload)
		if len(msg.Files) == 0 {
			// slack-go's MessageEvent drops the files array (only app_mention carries
			// it), so re-parse the raw envelope for attachments.
			msg.Files = slackEventFiles(evt.Request.Payload)
		}
	}
	c.backend.handleMessage(ctx, msg)
}

// inboundFromInnerEvent normalizes a slack-go inner event into the
// transport-agnostic InboundMessage the router understands. It returns false for
// event types the connector does not route.
func inboundFromInnerEvent(inner any) (internalslack.InboundMessage, bool) {
	switch e := inner.(type) {
	case *slackevents.MessageEvent:
		return internalslack.InboundMessage{
			ChannelType: e.ChannelType,
			ChannelID:   e.Channel,
			UserID:      e.User,
			BotID:       e.BotID,
			Text:        e.Text,
			TS:          e.TimeStamp,
			ThreadTS:    e.ThreadTimeStamp,
			SubType:     e.SubType,
		}, true
	case *slackevents.AppMentionEvent:
		return internalslack.InboundMessage{
			ChannelType:  "channel",
			ChannelID:    e.Channel,
			UserID:       e.User,
			BotID:        e.BotID,
			Text:         e.Text,
			TS:           e.TimeStamp,
			ThreadTS:     e.ThreadTimeStamp,
			IsAppMention: true,
			Files:        filesFromSlack(e.Files),
		}, true
	default:
		return internalslack.InboundMessage{}, false
	}
}

// slackEventViaBot reports whether a raw Events API envelope includes the bot's
// own event subscription (any authorizations[].is_bot) rather than only
// user-token subscriptions a legacy app manifest may still carry. Slack includes
// the authorizations array in every Events API delivery; if it is ever absent
// or unparseable this reports true so DMs with the bot stay gated on the owner
// (fail closed) while legacy user-token events are ignored by the router.
func slackEventViaBot(payload json.RawMessage) bool {
	if len(payload) == 0 {
		return true
	}
	var envelope struct {
		Authorizations []struct {
			IsBot bool `json:"is_bot"`
		} `json:"authorizations"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || len(envelope.Authorizations) == 0 {
		return true
	}
	for _, authorization := range envelope.Authorizations {
		if authorization.IsBot {
			return true
		}
	}
	return false
}

// slackEventFiles extracts file attachments from a raw Events API envelope
// payload (the `event.files` array), used for message events where slack-go's
// typed struct drops them.
func slackEventFiles(payload json.RawMessage) []internalslack.File {
	if len(payload) == 0 {
		return nil
	}
	var envelope struct {
		Event struct {
			Files []slackgo.File `json:"files"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil
	}
	return filesFromSlack(envelope.Event.Files)
}

// slackEventContextChannel returns the most relevant channel from the
// app_context attached to an Agent view message. Slack orders entities by
// relevance and may represent a channel either directly or through a message
// context. Other entity types are intentionally ignored.
func slackEventContextChannel(payload json.RawMessage) string {
	if len(payload) == 0 {
		return ""
	}
	var envelope struct {
		Event struct {
			AppContext struct {
				Entities []struct {
					Type  string          `json:"type"`
					Value json.RawMessage `json:"value"`
				} `json:"entities"`
			} `json:"app_context"`
		} `json:"event"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	for _, entity := range envelope.Event.AppContext.Entities {
		switch entity.Type {
		case "slack#/types/channel_id":
			var channelID string
			if json.Unmarshal(entity.Value, &channelID) == nil && channelID != "" {
				return channelID
			}
		case "slack#/types/message_context":
			var message struct {
				ChannelID string `json:"channel_id"`
			}
			if json.Unmarshal(entity.Value, &message) == nil && message.ChannelID != "" {
				return message.ChannelID
			}
		}
	}
	return ""
}

// filesFromSlack converts slack-go file records to the transport-agnostic view.
func filesFromSlack(files []slackgo.File) []internalslack.File {
	if len(files) == 0 {
		return nil
	}
	out := make([]internalslack.File, 0, len(files))
	for _, f := range files {
		out = append(out, internalslack.File{
			ID:         f.ID,
			Name:       f.Name,
			Mimetype:   f.Mimetype,
			Filetype:   f.Filetype,
			Size:       f.Size,
			URLPrivate: f.URLPrivate,
		})
	}
	return out
}
