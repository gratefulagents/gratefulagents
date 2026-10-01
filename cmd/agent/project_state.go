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
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
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

	// Embeddings are optional: recall works on Postgres full-text ranking
	// alone, and fuses pgvector similarity when an OpenAI-compatible
	// embeddings endpoint is configured (vector(1536) column).
	var embedder sdkprojectstate.Embedder
	if apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); apiKey != "" {
		e, err := sdkprojectstate.NewOpenAIEmbedder(sdkprojectstate.OpenAIEmbedderOptions{
			BaseURL: strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
			APIKey:  apiKey,
			ModelID: agentinfra.EnvOrDefault("MEMORY_EMBEDDING_MODEL", "text-embedding-3-small"),
			// Matches the vector(1536) column; text-embedding-3-* honor it.
			Dimensions: 1536,
		})
		if err != nil {
			log.Printf("WARN: memory embeddings disabled: %v", err)
		} else {
			embedder = e
		}
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

	recall := "full-text+semantic recall"
	if embedder == nil {
		recall = "full-text recall (no OPENAI_API_KEY for embeddings)"
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
// together with the briefing rendered right after it. It is injected only when
// the project state store is live (ENABLE_MEMORY + DATABASE_URL), because the
// tools it references are only registered then. Mode templates may point to
// this block, but keep concrete tool guidance here because availability is
// environment-gated rather than mode-gated.
func projectStateGuidance() string {
	return `## Durable Project State
This project keeps durable memories and tasks that outlive this run. The
briefing below was rendered when this run started: the Memory Index lists
preferences and decisions first, then the most-used facts and procedures, by
title within a size budget. It is
re-rendered after context compaction; call prime_context only to recover state
explicitly.

Memory:
- Before investigating something the project may already know, use
  memory_search; open entries with memory_get. Results flagged stale have
  cited files that changed or were not verified recently: re-check the cited
  sources, then memory_verify (still true), memory_save with the id
  (corrected), or memory_delete (wrong).
- Save with memory_save only durable knowledge a future run could not cheaply
  rediscover from the code or docs: user preferences and corrections
  (preference), decisions with their rationale (decision), non-obvious facts
  and gotchas (fact), repeatable how-tos (procedure). Give a short specific
  title, a body under ~1,500 characters, and citations (files or URLs) so the
  memory can be re-verified.
- Never save progress logs, PR changelogs, or test results — PR descriptions
  and run history already hold those. Prefer updating an existing memory (pass
  its id) over adding a near-duplicate; memory_save rejects likely duplicates.
- A background consolidator reviews each substantial turn and may merge or
  prune memories; you do not need to save routine outcomes.

Tasks:
- Use durable tasks only for work that must survive this run: multi-run
  efforts, or work another agent will pick up. Do not mirror single-run steps
  into tasks — an ordinary plan covers those.
- task_create, then task_claim before starting, task_comment for handoff
  notes, task_close when done; task_ready lists unblocked work. Claims you
  still hold are released automatically when this run ends, so leave a
  task_comment with the current state and next action on anything unfinished.

In read-only modes the mutating task and memory tools are unavailable.`
}

// releaseClaimsOnExit reopens tasks this run still holds when the run reaches
// a terminal outcome so claims never outlive their claimant (orphaned
// in_progress tasks otherwise hide ready work from every later run).
// Resumable exits — pod shutdown, replacement, cost-cap pause — return an
// empty status and resume the same run, so claims are kept then. Best-effort.
func releaseClaimsOnExit(runCtx context.Context, store *opprojectstate.Store, runID string, result runResult) {
	status := strings.TrimSpace(result.Status)
	if store == nil || strings.TrimSpace(runID) == "" || status == "" || runCtx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	note := fmt.Sprintf("Claim released automatically: run %s %s without closing this task.", runID, status)
	released, err := store.ReleaseClaims(ctx, runID, note)
	if err != nil {
		log.Printf("WARN: releasing durable task claims for %s: %v", runID, err)
		return
	}
	if len(released) > 0 {
		log.Printf("Released %d durable task claim(s) held by %s", len(released), runID)
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
