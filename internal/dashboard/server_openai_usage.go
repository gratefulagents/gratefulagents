package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	sdkopenai "github.com/gratefulagents/sdk/pkg/agentsdk/providers/openai"
)

const (
	chatGPTBackendBaseURL = "https://chatgpt.com/backend-api"
	openAIUsageLookback   = 30 * 24 * time.Hour
	openAIAccountTimeout  = 10 * time.Second
)

type openAIAccountClient struct {
	httpClient *http.Client
	session    *sdkopenai.AuthSession
	email      string
}

type openAIRateLimitStatus struct {
	PlanType             string                      `json:"plan_type"`
	RateLimit            *openAIRateLimitDetails     `json:"rate_limit"`
	Credits              *openAICreditStatus         `json:"credits"`
	SpendControl         *openAISpendControlStatus   `json:"spend_control"`
	AdditionalRateLimits []openAIAdditionalRateLimit `json:"additional_rate_limits"`
}

type openAIRateLimitDetails struct {
	PrimaryWindow   *openAIRateLimitWindow `json:"primary_window"`
	SecondaryWindow *openAIRateLimitWindow `json:"secondary_window"`
}

type openAIRateLimitWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type openAICreditStatus struct {
	HasCredits bool    `json:"has_credits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance"`
}

type openAISpendControlStatus struct {
	Reached         bool                        `json:"reached"`
	IndividualLimit *openAIIndividualSpendLimit `json:"individual_limit"`
}

type openAIIndividualSpendLimit struct {
	Limit       string  `json:"limit"`
	Used        string  `json:"used"`
	UsedPercent float64 `json:"used_percent"`
	ResetAt     int64   `json:"reset_at"`
}

type openAIAdditionalRateLimit struct {
	LimitName      string                  `json:"limit_name"`
	MeteredFeature string                  `json:"metered_feature"`
	RateLimit      *openAIRateLimitDetails `json:"rate_limit"`
}

type openAITokenUsageProfile struct {
	Stats openAITokenUsageStats `json:"stats"`
}

type openAITokenUsageStats struct {
	LifetimeTokens        *int64                        `json:"lifetime_tokens"`
	PeakDailyTokens       *int64                        `json:"peak_daily_tokens"`
	LongestRunningTurnSec *int64                        `json:"longest_running_turn_sec"`
	CurrentStreakDays     *int64                        `json:"current_streak_days"`
	LongestStreakDays     *int64                        `json:"longest_streak_days"`
	DailyUsageBuckets     []openAITokenUsageDailyBucket `json:"daily_usage_buckets"`
}

type openAITokenUsageDailyBucket struct {
	StartDate string `json:"start_date"`
	Tokens    int64  `json:"tokens"`
}

// GetMyOpenAIUsage reads account-level data through the calling user's saved
// OpenAI OAuth credential, then combines it with platform-observed model usage.
// OAuth material never leaves the server.
func (s *Server) GetMyOpenAIUsage(ctx context.Context, _ *platform.GetMyOpenAIUsageRequest) (*platform.MyOpenAIUsage, error) {
	actor, err := providerOAuthActor(ctx)
	if err != nil {
		return nil, err
	}
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	out := &platform.MyOpenAIUsage{LookbackDays: 30, FetchedAtUnix: now.Unix()}
	secret, err := s.readSecret(ctx, namespace, userCredentialSecretName("openai"))
	if k8serrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("read saved OpenAI credential: %w", err))
	}
	if len(secret.Data[userCredOAuthJSONKey]) == 0 {
		return out, nil
	}
	account, err := s.savedOpenAIAccountClient(secret)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("load saved OpenAI OAuth credential: %w", err))
	}
	out.OpenaiOauthPresent = true
	out.AccountEmail = account.email

	var limits openAIRateLimitStatus
	var profile openAITokenUsageProfile
	var limitsErr, profileErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		limitsErr = account.getJSON(ctx, "/wham/usage", &limits)
	}()
	go func() {
		defer wg.Done()
		profileErr = account.getJSON(ctx, "/wham/profiles/me", &profile)
	}()
	wg.Wait()

	if limitsErr == nil {
		out.AccountStatusAvailable = true
		out.PlanType = limits.PlanType
		out.Limits = openAIUsageLimits(&limits)
		out.Credits = openAICreditsLabel(&limits)
	} else {
		out.Warnings = append(out.Warnings, "ChatGPT quota data is temporarily unavailable.")
	}
	if profileErr == nil {
		out.TokenActivityAvailable = true
		out.LifetimeTokens = profile.Stats.LifetimeTokens
		out.PeakDailyTokens = profile.Stats.PeakDailyTokens
		out.CurrentStreakDays = profile.Stats.CurrentStreakDays
		out.LongestStreakDays = profile.Stats.LongestStreakDays
		out.LongestRunningTurnSeconds = profile.Stats.LongestRunningTurnSec
		out.Last_30DaysTokens = profileTokensLast30Days(&profile, now)
	} else {
		out.Warnings = append(out.Warnings, "ChatGPT token activity is temporarily unavailable.")
	}

	models, err := s.openAIModelUsageLast30Days(ctx, namespace, userCredentialSecretName("openai"), now)
	if err != nil {
		out.Warnings = append(out.Warnings, "Per-model platform telemetry is temporarily unavailable.")
	} else {
		out.TelemetryAvailable = true
		out.Models = models
	}
	return out, nil
}

func (s *Server) savedOpenAIAccountClient(secret *corev1.Secret) (*openAIAccountClient, error) {
	if provider := oauthMaterialProvider(secret); provider != "" && provider != "openai" {
		return nil, fmt.Errorf("saved credential contains %s OAuth material", provider)
	}
	authJSON := secret.Data[userCredOAuthJSONKey]
	if len(authJSON) == 0 {
		return nil, fmt.Errorf("saved credential does not contain OpenAI OAuth")
	}
	session, err := sdkopenai.NewOAuthAuthSessionFromSecretData(authJSON, strings.TrimSpace(string(secret.Data[userCredAccountIDKey])))
	if err != nil {
		return nil, fmt.Errorf("invalid OpenAI OAuth credential: %w", err)
	}
	// The central OAuth refresher persists rotations. Account reads use the
	// current access token without mutating an ephemeral dashboard copy.
	session.DisableRefresh()
	return &openAIAccountClient{
		httpClient: s.providerOAuthClient(),
		session:    session,
		email:      openAIOAuthEmail(authJSON),
	}, nil
}

func openAIOAuthEmail(authJSON []byte) string {
	var auth struct {
		Tokens struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(authJSON, &auth) != nil {
		return ""
	}
	email, _ := providerOAuthJWTClaims(auth.Tokens.IDToken)["email"].(string)
	return strings.TrimSpace(email)
}

func (c *openAIAccountClient) getJSON(ctx context.Context, path string, out any) error {
	requestCtx, cancel := context.WithTimeout(ctx, openAIAccountTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, chatGPTBackendBaseURL+path, nil)
	if err != nil {
		return err
	}
	headers, err := c.session.RequestHeaders(requestCtx)
	if err != nil {
		return err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	request.Header.Set("User-Agent", "gratefulagents")
	// Never forward OAuth-bearing account requests through redirects. The two
	// account endpoints are fixed, and a redirect is safer treated as failure.
	httpClient := *c.httpClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return fmt.Errorf("ChatGPT account endpoint returned %s", response.Status)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("decode ChatGPT account response: %w", err)
	}
	return nil
}

func openAIUsageLimits(status *openAIRateLimitStatus) []*platform.OpenAIUsageLimit {
	if status == nil {
		return nil
	}
	var out []*platform.OpenAIUsageLimit
	appendWindows := func(prefix string, limits *openAIRateLimitDetails) {
		if limits == nil {
			return
		}
		if limits.PrimaryWindow != nil {
			out = append(out, openAIUsageLimit(prefix, limits.PrimaryWindow, false))
		}
		if limits.SecondaryWindow != nil {
			out = append(out, openAIUsageLimit(prefix, limits.SecondaryWindow, true))
		}
	}
	appendWindows("", status.RateLimit)
	for _, additional := range status.AdditionalRateLimits {
		prefix := strings.TrimSpace(additional.LimitName)
		if prefix == "" {
			prefix = strings.TrimSpace(additional.MeteredFeature)
		}
		appendWindows(prefix, additional.RateLimit)
	}
	if limit := status.SpendControl; limit != nil && limit.IndividualLimit != nil {
		individual := limit.IndividualLimit
		out = append(out, &platform.OpenAIUsageLimit{
			Label: "Monthly credit limit", UsedPercent: clampPercent(individual.UsedPercent),
			ResetAtUnix: individual.ResetAt,
			Details:     strings.TrimSpace(individual.Used) + " of " + strings.TrimSpace(individual.Limit) + " credits used",
		})
	}
	return out
}

func openAIUsageLimit(prefix string, window *openAIRateLimitWindow, secondary bool) *platform.OpenAIUsageLimit {
	label := rateLimitDurationLabel(window.LimitWindowSeconds)
	if label == "" {
		if secondary {
			label = "Secondary"
		} else {
			label = "Primary"
		}
	}
	if prefix != "" && !strings.EqualFold(prefix, "codex") {
		label = prefix + " " + strings.ToLower(label)
	}
	return &platform.OpenAIUsageLimit{
		Label: label, UsedPercent: clampPercent(window.UsedPercent), ResetAtUnix: window.ResetAt,
	}
}

func rateLimitDurationLabel(seconds int64) string {
	const tolerance = int64(10 * time.Minute / time.Second)
	for _, target := range []struct {
		seconds int64
		label   string
	}{
		{5 * 60 * 60, "5 hour"},
		{24 * 60 * 60, "Daily"},
		{7 * 24 * 60 * 60, "Weekly"},
		{30 * 24 * 60 * 60, "Monthly"},
		{365 * 24 * 60 * 60, "Annual"},
	} {
		delta := seconds - target.seconds
		if delta < 0 {
			delta = -delta
		}
		if delta <= tolerance {
			return target.label
		}
	}
	return ""
}

func openAICreditsLabel(status *openAIRateLimitStatus) string {
	if status == nil || status.Credits == nil {
		return ""
	}
	if status.Credits.Unlimited {
		return "Unlimited"
	}
	if status.Credits.Balance != nil && strings.TrimSpace(*status.Credits.Balance) != "" {
		return strings.TrimSpace(*status.Credits.Balance)
	}
	if status.Credits.HasCredits {
		return "Available"
	}
	return "None"
}

func (s *Server) openAIModelUsageLast30Days(ctx context.Context, namespace, oauthSecret string, now time.Time) ([]*platform.OpenAIModelUsage, error) {
	analytics, ok := s.stateStore.(observabilityStore)
	if !ok {
		return nil, fmt.Errorf("historical telemetry is unavailable")
	}
	visible, err := s.ListAgentRuns(ctx, &platform.ListAgentRunsRequest{Namespace: namespace})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(visible.Runs))
	for _, run := range visible.Runs {
		if protoRunUsesOpenAIOAuthSecret(run, oauthSecret) {
			names = append(names, run.GetName())
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	overview, err := analytics.GetObservabilityOverview(ctx, store.ObservabilityQuery{
		Namespace: namespace, Start: now.Add(-openAIUsageLookback), End: now,
		BucketSeconds: int64((24 * time.Hour) / time.Second), AgentRunNames: names,
	})
	if err != nil {
		return nil, err
	}
	if overview.Completeness.ActivityTruncated {
		return nil, fmt.Errorf("model telemetry is incomplete because the activity limit was reached")
	}
	models := make([]*platform.OpenAIModelUsage, 0, len(overview.Models))
	for _, model := range overview.Models {
		name := strings.ToLower(strings.TrimSpace(model.Name))
		if !strings.HasPrefix(name, "openai/") && (strings.Contains(name, "/") || !strings.HasPrefix(name, "gpt-")) {
			continue
		}
		models = append(models, &platform.OpenAIModelUsage{
			Model: model.Name, InputTokens: model.InputTokens, OutputTokens: model.OutputTokens,
			EstimatedCostUsd: model.CostUSD, CostKnown: model.CostUSD > 0,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		left := models[i].InputTokens + models[i].OutputTokens
		right := models[j].InputTokens + models[j].OutputTokens
		if left == right {
			return models[i].Model < models[j].Model
		}
		return left > right
	})
	return models, nil
}

func protoRunUsesOpenAIOAuthSecret(run *platform.AgentRun, secretName string) bool {
	if run == nil || secretName == "" || !strings.EqualFold(run.GetAuthMode(), "oauth") {
		return false
	}
	if run.GetOpenaiOauthSecret() == secretName {
		return true
	}
	for _, ref := range run.GetProviderOauthSecrets() {
		if strings.EqualFold(strings.TrimSpace(ref.GetProvider()), "openai") && strings.TrimSpace(ref.GetSecretName()) == secretName {
			return true
		}
	}
	return false
}

func profileTokensLast30Days(profile *openAITokenUsageProfile, now time.Time) int64 {
	if profile == nil {
		return 0
	}
	utc := now.UTC()
	end := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -29)
	var total int64
	for _, bucket := range profile.Stats.DailyUsageBuckets {
		day, err := time.Parse("2006-01-02", bucket.StartDate)
		if err == nil && !day.Before(start) && !day.After(end) && bucket.Tokens > 0 {
			total += bucket.Tokens
		}
	}
	return total
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}
