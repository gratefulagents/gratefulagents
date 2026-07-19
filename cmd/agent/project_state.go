package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gratefulagents/gratefulagents/internal/agentinfra"
	opprojectstate "github.com/gratefulagents/gratefulagents/internal/projectstate"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkmemory "github.com/gratefulagents/sdk/pkg/agentsdk/memory"
	"github.com/gratefulagents/sdk/pkg/agentsdk/modelsdev"
	sdkprojectstate "github.com/gratefulagents/sdk/pkg/agentsdk/projectstate"
	sdkproviders "github.com/gratefulagents/sdk/pkg/agentsdk/providers"
	sdkopenai "github.com/gratefulagents/sdk/pkg/agentsdk/providers/openai"
)

func envBoolDefault(name string, defaultValue bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if raw == "" {
		return defaultValue
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultValue
	}
}

type openAIModelMetadataResolver = sdkopenai.CompactionMetadataResolver

// newOpenAIModelMetadataResolver builds the lazy ChatGPT-backend /models
// resolver whenever the pod carries OpenAI OAuth material. It deliberately
// does NOT gate on the startup provider or model: the active model can switch
// to an openai-oauth one mid-run (run chat-gf-all-i8fml9 started on
// copilot/claude-fable-5 and switched to openai/gpt-5.5), and a pod that
// skipped the resolver at startup would then resolve the API-key models.dev
// window (1.05M → trigger 945K) for a backend that really serves 272K.
// The resolver is lazy (first Lookup fetches), so building it on runs that
// never route to the OAuth backend costs nothing.
func newOpenAIModelMetadataResolver(cfg runConfig) *openAIModelMetadataResolver {
	if sdkopenai.NormalizeAuthMode(cfg.AuthMode) != sdkopenai.AuthModeOAuth {
		return nil
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = sdkproviders.DefaultCodexBackendBaseURL
	}
	if !sdkopenai.IsChatGPTBackendBaseURL(baseURL) {
		return nil
	}
	session, err := sdkopenai.NewOAuthAuthSessionFromConfig(sdkopenai.OAuthSessionConfig{
		AuthJSONPath:  cfg.OpenAIOAuthPath,
		AccountID:     cfg.OpenAIOAuthAccountID,
		AccountIDPath: cfg.OpenAIOAuthAccountIDPath,
	})
	if err != nil {
		log.Printf("WARN: OpenAI model metadata disabled: %v", err)
		return nil
	}
	return sdkopenai.NewCompactionMetadataResolver(baseURL, session)
}

func usesOpenAIProvider(provider, model string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	prefix, _ := agent.ParseModelPrefix(model)
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	return provider == "openai" || prefix == "openai"
}

// modelRoutesToCodexMetadata reports whether the model resolves its window
// from the ChatGPT-backend /models metadata: an explicit openai/ prefix, or a
// bare name whose live provider is openai. Only meaningful when the resolver
// exists (it is only built for openai-oauth ChatGPT-backend deployments).
func modelRoutesToCodexMetadata(metadata *openAIModelMetadataResolver, provider, model string) bool {
	if metadata == nil {
		return false
	}
	prefix, _ := agent.ParseModelPrefix(model)
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix != "" {
		return prefix == "openai"
	}
	return strings.ToLower(strings.TrimSpace(provider)) == "openai"
}

// newCompactionModelResolver builds the per-model compaction threshold
// resolver used by the runner and sub-agent scheduler (SDK v0.0.38). Sources,
// in order:
//
//  1. models.dev catalog — authoritative for API-style deployments: provider
//     /models limits under-report real windows (e.g. Copilot advertises a
//     200K prompt cap for claude-fable-5 while 1M-context requests succeed).
//  2. OpenAI OAuth /models metadata — openai-oauth deployments are
//     deliberately absent from models.dev (their windows differ from the
//     OpenAI API), so they resolve from the backend's own metadata.
//  3. Static per-model defaults — always resolve, so sub-agents pinned to a
//     different model never inherit the parent model's thresholds.
//
// Returns nil when ops pinned explicit thresholds (or disabled compaction)
// via env, so those overrides always win over catalog lookups.
func newCompactionModelResolver(cfg runConfig, metadata *openAIModelMetadataResolver) agent.CompactionModelResolver {
	if envBoolDefault("ENGG_OPERATOR_DISABLE_CONTEXT_COMPACTION", false) ||
		agentinfra.EnvOrDefault("ENGG_OPERATOR_COMPACTION_TRIGGER_TOKENS", "") != "" ||
		agentinfra.EnvOrDefault("ENGG_OPERATOR_COMPACTION_TARGET_TOKENS", "") != "" {
		return nil
	}
	catalog := modelsdev.NewResolver()
	defaultCatalogProvider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if metadata != nil && defaultCatalogProvider == "openai" {
		// OAuth-backed OpenAI deployment: models.dev has no provider for the
		// ChatGPT backend, so unprefixed lookups must miss the catalog and
		// use its /models metadata below. Other startup providers (copilot,
		// anthropic) keep their catalog default — the metadata resolver now
		// exists on those runs too, ready for mid-run switches to openai.
		defaultCatalogProvider = ""
	}
	return func(ctx context.Context, model string) (int, int, bool) {
		// Bare names inherit the startup provider (mid-run switches always
		// arrive prefixed via liveRuntimeModelAndProvider).
		codexRouted := modelRoutesToCodexMetadata(metadata, strings.TrimSpace(cfg.Provider), model)
		catalogProvider := defaultCatalogProvider
		if prefix, _ := agent.ParseModelPrefix(model); strings.TrimSpace(prefix) != "" {
			p := strings.ToLower(strings.TrimSpace(prefix))
			if metadata != nil && p == "openai" {
				p = "" // routes to the codex backend, not the OpenAI API
			}
			catalogProvider = p
		}
		if catalogProvider != "" && !codexRouted {
			catalogCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			trigger, target, ok := catalog.CompactionThresholds(catalogCtx, catalogProvider, model)
			cancel()
			if ok {
				return trigger, target, true
			}
		}
		if codexRouted {
			lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			meta, ok := metadata.Lookup(lookupCtx, model)
			cancel()
			if ok {
				if trigger, target, ok := sdkopenai.CompactionDefaultsFromModelMetadata(meta); ok {
					return trigger, target, true
				}
			}
		}
		trigger, target := agent.CompactionDefaultsForModel(model)
		return trigger, target, true
	}
}

// resolveCompactionConfig builds the compaction config with priority:
// environment variable > provider metadata > static per-model defaults > hardcoded default.
func resolveCompactionConfig(ctx context.Context, model, provider string, metadata *openAIModelMetadataResolver) agent.CompactionConfig {
	cfg := agent.DefaultCompactionConfig()
	codexRouted := modelRoutesToCodexMetadata(metadata, provider, model)

	// Layer 1: static per-model defaults. For OAuth-routed models, keep the
	// conservative default until the backend metadata resolves — the static
	// gpt-5.x numbers assume the API deployment, above the backend's real
	// window. Models routed elsewhere (e.g. copilot/claude-fable-5 while a
	// OAuth resolver exists for mid-run switches) use their static defaults:
	// the backend metadata can never describe them.
	if model != "" && !codexRouted {
		trigger, target := agent.CompactionDefaultsForModel(model)
		cfg.TriggerTokens = trigger
		cfg.TargetTokens = target
	}

	// Layer 2: backend-reported model metadata for OAuth-routed models.
	if codexRouted {
		if meta, ok := metadata.Lookup(ctx, model); ok {
			if trigger, target, ok := sdkopenai.CompactionDefaultsFromModelMetadata(meta); ok {
				cfg.TriggerTokens = trigger
				cfg.TargetTokens = target
				metadata.LogOnce(
					"applied:"+strings.ToLower(strings.TrimSpace(meta.ID)),
					"Using provider model metadata for compaction: model=%s context_window=%d auto_compact_limit=%d trigger=%d target=%d",
					meta.ID,
					meta.ResolvedContextWindow(),
					meta.AutoCompactTokenLimit,
					trigger,
					target,
				)
			}
		}
	}

	// Layer 3: environment variable overrides (highest priority, for ops).
	if envBoolDefault("ENGG_OPERATOR_DISABLE_CONTEXT_COMPACTION", false) {
		cfg.Enabled = false
	}
	priorTrigger, priorTarget := cfg.TriggerTokens, cfg.TargetTokens
	if v := agentinfra.EnvOrDefault("ENGG_OPERATOR_COMPACTION_TRIGGER_TOKENS", ""); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			cfg.TriggerTokens = parsed
		} else {
			log.Printf("WARN: ignoring invalid ENGG_OPERATOR_COMPACTION_TRIGGER_TOKENS=%q; must be positive", v)
		}
	}
	if v := agentinfra.EnvOrDefault("ENGG_OPERATOR_COMPACTION_TARGET_TOKENS", ""); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			cfg.TargetTokens = parsed
		} else {
			log.Printf("WARN: ignoring invalid ENGG_OPERATOR_COMPACTION_TARGET_TOKENS=%q; must be positive", v)
		}
	}
	if cfg.TargetTokens >= cfg.TriggerTokens {
		log.Printf("WARN: ignoring compaction token overrides: target (%d) must be less than trigger (%d)", cfg.TargetTokens, cfg.TriggerTokens)
		cfg.TriggerTokens, cfg.TargetTokens = priorTrigger, priorTarget
	}
	return cfg
}

func resolveHandoffHistoryConfig() agent.HandoffHistoryConfig {
	enabled := !envBoolDefault("ENGG_OPERATOR_DISABLE_NESTED_HANDOFF_HISTORY", false)
	cfg := agent.HandoffHistoryConfig{
		Enabled:             enabled,
		MaxTokens:           6000,
		TargetTokens:        3000,
		PreserveRecentItems: 8,
		SummaryBulletLimit:  4,
	}
	priorMax, priorTarget := cfg.MaxTokens, cfg.TargetTokens
	if v := agentinfra.EnvOrDefault("ENGG_OPERATOR_HANDOFF_HISTORY_MAX_TOKENS", ""); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			cfg.MaxTokens = parsed
		} else {
			log.Printf("WARN: ignoring invalid ENGG_OPERATOR_HANDOFF_HISTORY_MAX_TOKENS=%q; must be positive", v)
		}
	}
	if v := agentinfra.EnvOrDefault("ENGG_OPERATOR_HANDOFF_HISTORY_TARGET_TOKENS", ""); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			cfg.TargetTokens = parsed
		} else {
			log.Printf("WARN: ignoring invalid ENGG_OPERATOR_HANDOFF_HISTORY_TARGET_TOKENS=%q; must be positive", v)
		}
	}
	if cfg.TargetTokens > cfg.MaxTokens {
		log.Printf("WARN: ignoring handoff history token overrides: target (%d) must not exceed max (%d)", cfg.TargetTokens, cfg.MaxTokens)
		cfg.MaxTokens, cfg.TargetTokens = priorMax, priorTarget
	}
	return cfg
}

// projectStateSetupStatus reports how durable project state was resolved.
type projectStateSetupStatus struct {
	enabled   bool
	projectID string
	message   string
	err       error
}

// setupProjectState builds the Postgres-backed SDK project state store. The
// SDK is the backbone (engine semantics, tools, priming); the operator only
// hooks up persistence. On by default; opt out with ENABLE_MEMORY=false. The
// embedder is optional: without OpenAI auth, recall degrades to lexical.
func setupProjectState(
	ctx context.Context,
	cfg runConfig,
) (*opprojectstate.Store, *pgxpool.Pool, projectStateSetupStatus) {
	if !envFlagEnabled("ENABLE_MEMORY", true) {
		return nil, nil, projectStateSetupStatus{message: "durable project state disabled"}
	}

	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		err := fmt.Errorf("DATABASE_URL not set")
		return nil, nil, projectStateSetupStatus{
			message: "durable project state: DATABASE_URL not set — disabled",
			err:     err,
		}
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, nil, projectStateSetupStatus{
			message: fmt.Sprintf("durable project state: failed to connect to Postgres: %v", err),
			err:     err,
		}
	}

	var embedder sdkmemory.Embedder
	if apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); apiKey != "" {
		authSession := sdkopenai.NewAPIKeyAuthSession(apiKey)
		embedder = sdkmemory.NewOpenAIEmbedder(authSession, strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), "")
	}

	projectID := projectStateID(cfg)
	store, err := opprojectstate.NewStore(opprojectstate.Options{
		Pool:      pool,
		Embedder:  embedder,
		ProjectID: projectID,
		Actor:     cfg.TaskName,
		RunID:     cfg.TaskName,
		WorkDir:   cfg.RepoDir,
	})
	if err != nil {
		pool.Close()
		return nil, nil, projectStateSetupStatus{
			message: fmt.Sprintf("durable project state: failed to initialize store: %v", err),
			err:     err,
		}
	}

	recall := "semantic+lexical recall"
	if embedder == nil {
		recall = "lexical recall (no OPENAI_API_KEY for embeddings)"
	}
	return store, pool, projectStateSetupStatus{
		enabled:   true,
		projectID: projectID,
		message:   fmt.Sprintf("Durable project state enabled: project=%s, %s", projectID, recall),
	}
}

// projectStateID derives a stable project identity from the namespace plus
// repository so durable state is shared across runs of the same project.
func projectStateID(cfg runConfig) string {
	return opprojectstate.ProjectID(cfg.Namespace, cfg.RepoURL)
}

// refreshPrimeContext re-renders the durable project state briefing with a
// short timeout. Best-effort: failures degrade to no briefing.
func refreshPrimeContext(ctx context.Context, store *opprojectstate.Store, actor string) string {
	if store == nil {
		return ""
	}
	primeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	prime, err := store.PrimeContext(primeCtx, sdkprojectstate.PrimeOptions{Actor: actor, ReadyLimit: 8, MemoryLimit: 8})
	if err != nil {
		log.Printf("WARN: project state priming failed: %v", err)
		return ""
	}
	return strings.TrimSpace(prime)
}

// projectStateGuidance is the system prompt block teaching the agent how the
// durable project state surface (task_*, memory_*, prime_context) fits
// together with the automatic per-turn briefing. It is injected only when the
// project state store is live (ENABLE_MEMORY + DATABASE_URL), because the
// tools it references are only registered then. Mode templates may point to
// this block, but keep concrete tool guidance here because availability is
// environment-gated rather than mode-gated.
func projectStateGuidance() string {
	return `## Durable Project State
This project keeps durable tasks and memories that outlive this session and
survive context compaction. The briefing below is refreshed automatically every
turn with active, ready, and blocked tasks plus pinned and recent memories.

Use the tools as a lifecycle, not as a scratchpad:
- Before starting substantial work, read the briefing. Use memory_recall when
  you need older knowledge beyond the surfaced memories, and memory_list or
  memory_stats only when inventory or cleanup is the actual goal. Do not call
  prime_context routinely; use it to rebuild a compact briefing after context
  compaction or when explicitly recovering state.
- For work that must survive this run, use task_create, then task_claim or
  task_update as it progresses, task_comment for durable handoff notes, and
  task_close when complete. Use task_ready to find unblocked work and task_link
  for real dependencies. Do not mirror every conversational step into a task.
- Use memory_remember for reusable decisions, conventions, facts, and gotchas —
  not transient progress or facts already obvious from the repository. Choose
  semantic for facts, procedural for repeatable how-tos, episodic for notable
  events, and pinned only for context that should appear on every future run.
  Prefer the narrowest useful project, task, or file scope and useful tags.
- Use memory_update when durable knowledge changes and memory_delete when it is
  invalid; do not leave conflicting memories for future agents.
- Use the subagent tool for ephemeral in-run delegation. Sub-agent tasks are not
  durable project tasks and do not replace the task lifecycle above.

A session summary is saved automatically when the run ends. In read-only modes,
mutating task and memory tools are filtered out; task inspection plus memory
recall, listing, statistics, and prime tools remain available.`
}

// saveSessionSummaryOnExit persists a compact session summary into durable
// project state when the run reached a terminal status. Best-effort.
func saveSessionSummaryOnExit(store *opprojectstate.Store, sc *sessionclient.Client, runID string, result runResult) {
	if store == nil || sc == nil || result.Status == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	state, err := sc.ReadWorkingState(ctx)
	if err != nil {
		return
	}
	var parts []string
	if state.Goal != "" {
		parts = append(parts, "Goal: "+state.Goal)
	}
	if state.LastAssistantSummary != "" {
		parts = append(parts, state.LastAssistantSummary)
	}
	if len(parts) == 0 {
		return
	}
	summary := fmt.Sprintf("[%s] %s", result.Status, strings.Join(parts, " — "))
	record := sdkprojectstate.SessionSummary{RunID: runID, Summary: summary}
	if _, err := store.SaveSessionSummary(ctx, record); err != nil {
		log.Printf("WARN: failed to save session summary to project state: %v", err)
	}
}

// envFlagEnabled reads a boolean env toggle, returning fallback when unset.
func envFlagEnabled(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return strings.EqualFold(value, "true") || value == "1"
}

// criticVerifierEnabled gates the SDK adversarial final-answer verifier for
// autonomous runs. On by default; opt out with ENABLE_CRITIC_VERIFIER=false.
func criticVerifierEnabled() bool {
	return envFlagEnabled("ENABLE_CRITIC_VERIFIER", true)
}
