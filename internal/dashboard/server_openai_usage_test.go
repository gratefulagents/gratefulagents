package dashboard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetMyOpenAIUsageReadsCurrentOAuthAccount(t *testing.T) {
	scheme := testProjectScheme(t)

	claims, _ := json.Marshal(map[string]any{
		"email":                       "oauth@example.com",
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "account-123"},
	})
	idToken := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	authJSON, _ := json.Marshal(map[string]any{
		"tokens": map[string]string{
			"id_token": idToken, "access_token": "access-token", "refresh_token": "refresh-token", "account_id": "account-123",
		},
	})

	var mu sync.Mutex
	seen := map[string]bool{}
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := req.Header.Get("ChatGPT-Account-Id"); got != "account-123" {
			t.Fatalf("ChatGPT-Account-Id = %q", got)
		}
		mu.Lock()
		seen[req.URL.Path] = true
		mu.Unlock()
		var body string
		switch req.URL.Path {
		case "/backend-api/wham/usage":
			body = `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":42,"limit_window_seconds":18000,"reset_at":1893456000},"secondary_window":{"used_percent":9,"limit_window_seconds":604800,"reset_at":1893888000}},"credits":{"has_credits":true,"unlimited":false,"balance":"12.50"},"rate_limit_reset_credits":{"available_count":2}}`
		case "/backend-api/wham/rate-limit-reset-credits":
			body = `{"available_count":2,"credits":[{"id":"credit-late","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-01-01T00:00:00Z","expires_at":"2026-03-01T00:00:00Z","title":"Full reset","description":"Reset your current usage limits."},{"id":"credit-redeemed","reset_type":"codex_rate_limits","status":"redeemed","granted_at":"2026-01-01T00:00:00Z"},{"id":"credit-soon","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-01-02T00:00:00Z","expires_at":"2026-02-01T00:00:00Z","title":null,"description":null}]}`
		case "/backend-api/wham/profiles/me":
			body = fmt.Sprintf(`{"stats":{"lifetime_tokens":10000,"peak_daily_tokens":1200,"current_streak_days":3,"longest_streak_days":8,"longest_running_turn_sec":3900,"daily_usage_buckets":[{"start_date":%q,"tokens":700}]}}`, time.Now().UTC().Format("2006-01-02"))
		default:
			t.Fatalf("unexpected path %q", req.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: k8sClient, scheme: scheme, providerOAuthHTTP: &http.Client{Transport: transport}}
	ctx := context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: "user-usage", Name: "Usage User", Role: "owner"})
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		OpenaiOauthJson: string(authJSON), OpenaiAccountId: "account-123",
	}); err != nil {
		t.Fatalf("UpdateMyCredentials() error = %v", err)
	}

	got, err := srv.GetMyOpenAIUsage(ctx, &platform.GetMyOpenAIUsageRequest{})
	if err != nil {
		t.Fatalf("GetMyOpenAIUsage() error = %v", err)
	}
	if !got.OpenaiOauthPresent || !got.AccountStatusAvailable || !got.TokenActivityAvailable {
		t.Fatalf("availability = %#v", got)
	}
	if got.AccountEmail != "oauth@example.com" || got.PlanType != "pro" || got.Credits != "12.50" {
		t.Fatalf("account fields = %#v", got)
	}
	if got.LifetimeTokens == nil || *got.LifetimeTokens != 10000 || got.Last_30DaysTokens != 700 {
		t.Fatalf("token activity = %#v", got)
	}
	if len(got.Limits) != 2 || got.Limits[0].Label != "5 hour" || got.Limits[0].UsedPercent != 42 {
		t.Fatalf("limits = %#v", got.Limits)
	}
	resets := got.RateLimitResetCredits
	if resets == nil || resets.AvailableCount != 2 || !resets.DetailsAvailable {
		t.Fatalf("reset credits = %#v", resets)
	}
	if len(resets.Credits) != 2 || resets.Credits[0].Id != "credit-soon" || resets.Credits[1].Id != "credit-late" {
		t.Fatalf("reset credit rows = %#v", resets.Credits)
	}
	if resets.Credits[0].ExpiresAtUnix != time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).Unix() || resets.Credits[0].Title != "" {
		t.Fatalf("soonest reset credit = %#v", resets.Credits[0])
	}
	if resets.Credits[1].Title != "Full reset" || resets.Credits[1].Description != "Reset your current usage limits." {
		t.Fatalf("labelled reset credit = %#v", resets.Credits[1])
	}
	mu.Lock()
	seenUsage := seen["/backend-api/wham/usage"]
	seenProfile := seen["/backend-api/wham/profiles/me"]
	seenResets := seen["/backend-api/wham/rate-limit-reset-credits"]
	mu.Unlock()
	if !seenUsage || !seenProfile || !seenResets {
		t.Fatalf("seen paths = %#v", seen)
	}
}

func TestGetMyOpenAIUsageFallsBackToUsageResetCreditSummary(t *testing.T) {
	scheme := testProjectScheme(t)
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/wham/rate-limit-reset-credits"):
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		case strings.HasSuffix(req.URL.Path, "/wham/usage"):
			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"plan_type":"plus","rate_limit_reset_credits":{"available_count":1}}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"stats":{}}`))}, nil
	})
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme, providerOAuthHTTP: &http.Client{Transport: transport}}
	ctx := credActorCtx("user-reset-summary", "Reset Summary")
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		OpenaiOauthJson: testOpenAIOAuthJSON(t, "account-reset"), OpenaiAccountId: "account-reset",
	}); err != nil {
		t.Fatalf("UpdateMyCredentials() error = %v", err)
	}

	got, err := srv.GetMyOpenAIUsage(ctx, &platform.GetMyOpenAIUsageRequest{})
	if err != nil {
		t.Fatalf("GetMyOpenAIUsage() error = %v", err)
	}
	resets := got.RateLimitResetCredits
	if resets == nil || resets.AvailableCount != 1 || resets.DetailsAvailable || len(resets.Credits) != 0 {
		t.Fatalf("reset credits = %#v", resets)
	}
	if len(got.Warnings) != 0 {
		t.Fatalf("warnings = %#v", got.Warnings)
	}
}

func TestConsumeMyOpenAIRateLimitResetCreditPostsRedeemRequest(t *testing.T) {
	scheme := testProjectScheme(t)
	var mu sync.Mutex
	var bodies []string
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/backend-api/wham/rate-limit-reset-credits/consume" {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := req.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		raw, _ := io.ReadAll(req.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		count := len(bodies)
		mu.Unlock()
		body := `{"code":"reset","windows_reset":2}`
		if count > 1 {
			body = `{"code":"already_redeemed"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme, providerOAuthHTTP: &http.Client{Transport: transport}}
	ctx := credActorCtx("user-reset-consume", "Reset Consumer")
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		OpenaiOauthJson: testOpenAIOAuthJSON(t, "account-reset"), OpenaiAccountId: "account-reset",
	}); err != nil {
		t.Fatalf("UpdateMyCredentials() error = %v", err)
	}

	got, err := srv.ConsumeMyOpenAIRateLimitResetCredit(ctx, &platform.ConsumeMyOpenAIRateLimitResetCreditRequest{IdempotencyKey: "attempt-1", CreditId: "credit-soon"})
	if err != nil {
		t.Fatalf("ConsumeMyOpenAIRateLimitResetCredit() error = %v", err)
	}
	if got.Outcome != platform.CodexRateLimitResetOutcome_CODEX_RATE_LIMIT_RESET_OUTCOME_RESET || got.WindowsReset != 2 {
		t.Fatalf("outcome = %#v", got)
	}
	retry, err := srv.ConsumeMyOpenAIRateLimitResetCredit(ctx, &platform.ConsumeMyOpenAIRateLimitResetCreditRequest{IdempotencyKey: "attempt-1"})
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if retry.Outcome != platform.CodexRateLimitResetOutcome_CODEX_RATE_LIMIT_RESET_OUTCOME_ALREADY_REDEEMED {
		t.Fatalf("retry outcome = %#v", retry)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 || bodies[0] != `{"redeem_request_id":"attempt-1","credit_id":"credit-soon"}` || bodies[1] != `{"redeem_request_id":"attempt-1"}` {
		t.Fatalf("request bodies = %#v", bodies)
	}
}

func TestConsumeMyOpenAIRateLimitResetCreditRejectsMissingKeyAndCredential(t *testing.T) {
	scheme := testProjectScheme(t)
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected request %s %s", req.Method, req.URL.Path)
		return nil, nil
	})
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme, providerOAuthHTTP: &http.Client{Transport: transport}}
	ctx := credActorCtx("user-reset-missing", "Reset Missing")

	_, err := srv.ConsumeMyOpenAIRateLimitResetCredit(ctx, &platform.ConsumeMyOpenAIRateLimitResetCreditRequest{})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty idempotency key error = %v", err)
	}
	_, err = srv.ConsumeMyOpenAIRateLimitResetCredit(ctx, &platform.ConsumeMyOpenAIRateLimitResetCreditRequest{IdempotencyKey: "attempt-1"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("missing credential error = %v", err)
	}
}

func TestGetMyCopilotUsageIncludesOAuthQuotas(t *testing.T) {
	scheme := testProjectScheme(t)
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/copilot_internal/user" {
			t.Fatalf("unexpected path %q", req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "token github-oauth" {
			t.Fatalf("Authorization = %q", got)
		}
		body := `{"login":"octocat","copilot_plan":"individual_pro","quota_reset_date":"2026-08-01","quota_snapshots":{"premium_interactions":{"entitlement":300,"remaining":225,"overage_count":2,"unlimited":false},"chat":{"entitlement":0,"remaining":0,"unlimited":true}}}`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: k8sClient, scheme: scheme, providerOAuthHTTP: &http.Client{Transport: transport}}
	ctx := credActorCtx("user-copilot-usage", "Copilot User")
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		CopilotOauthJson: `{"oauth_token":"github-oauth","token":"copilot-api-token","expires_at":4070908800}`,
	}); err != nil {
		t.Fatalf("UpdateMyCredentials() error = %v", err)
	}

	got, err := srv.GetMyCopilotUsage(ctx, &platform.GetMyCopilotUsageRequest{})
	if err != nil {
		t.Fatalf("GetMyCopilotUsage() error = %v", err)
	}
	if !got.CopilotOauthPresent || !got.UsageAvailable {
		t.Fatalf("Copilot availability = %#v", got)
	}
	if got.AccountLogin != "octocat" || got.Plan != "individual_pro" || got.QuotaResetDate != "2026-08-01" {
		t.Fatalf("Copilot account fields = %#v", got)
	}
	if len(got.Quotas) != 2 || got.Quotas[1].Name != "premium_interactions" || got.Quotas[1].Remaining != 225 || got.Quotas[1].OverageCount != 2 {
		t.Fatalf("Copilot quotas = %#v", got.Quotas)
	}
}

func TestProviderUsageKeepsCopilotIndependentWhenOpenAICredentialIsInvalid(t *testing.T) {
	scheme := testProjectScheme(t)
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/copilot_internal/user" {
			t.Fatalf("unexpected path %q", req.URL.Path)
		}
		body := `{"login":"octocat","copilot_plan":"individual_pro","quota_snapshots":{"premium_interactions":{"entitlement":300,"remaining":225}}}`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme, providerOAuthHTTP: &http.Client{Transport: transport}}
	ctx := credActorCtx("user-copilot-fallback", "Copilot Fallback")
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		CopilotOauthJson: `{"oauth_token":"github-oauth","token":"copilot-api-token","expires_at":4070908800}`,
	}); err != nil {
		t.Fatalf("UpdateMyCredentials() error = %v", err)
	}
	actor, _ := providerOAuthActor(ctx)
	namespace, _ := srv.ensureUserNamespace(ctx, actor)
	if err := srv.writeCredentialData(ctx, namespace, "openai", map[string][]byte{userCredOAuthJSONKey: []byte(`{"broken":true}`)}); err != nil {
		t.Fatalf("writeCredentialData() error = %v", err)
	}

	got, err := srv.GetMyCopilotUsage(ctx, &platform.GetMyCopilotUsageRequest{})
	if err != nil {
		t.Fatalf("GetMyCopilotUsage() error = %v", err)
	}
	if !got.CopilotOauthPresent || !got.UsageAvailable || got.AccountLogin != "octocat" {
		t.Fatalf("Copilot usage = %#v", got)
	}
	if len(got.Quotas) != 1 || got.Quotas[0].Remaining != 225 {
		t.Fatalf("Copilot quotas = %#v", got.Quotas)
	}
	if _, err := srv.GetMyOpenAIUsage(ctx, &platform.GetMyOpenAIUsageRequest{}); err == nil {
		t.Fatal("GetMyOpenAIUsage() unexpectedly accepted invalid OpenAI credential")
	}
}

func TestProviderUsageKeepsOpenAIIndependentWhenCopilotCredentialIsInvalid(t *testing.T) {
	scheme := testProjectScheme(t)
	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"stats":{}}`
		if strings.HasSuffix(req.URL.Path, "/wham/usage") {
			body = `{"plan_type":"pro"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme, providerOAuthHTTP: &http.Client{Transport: transport}}
	ctx := credActorCtx("user-partial-usage", "Partial User")
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{
		OpenaiOauthJson: testOpenAIOAuthJSON(t, "account-partial"), OpenaiAccountId: "account-partial",
	}); err != nil {
		t.Fatalf("UpdateMyCredentials() error = %v", err)
	}
	actor, _ := providerOAuthActor(ctx)
	namespace, _ := srv.ensureUserNamespace(ctx, actor)
	if err := srv.writeCredentialData(ctx, namespace, "copilot", map[string][]byte{userCredOAuthJSONKey: []byte(`{"broken":true}`)}); err != nil {
		t.Fatalf("writeCredentialData() error = %v", err)
	}

	got, err := srv.GetMyOpenAIUsage(ctx, &platform.GetMyOpenAIUsageRequest{})
	if err != nil {
		t.Fatalf("GetMyOpenAIUsage() error = %v", err)
	}
	if !got.OpenaiOauthPresent || got.PlanType != "pro" {
		t.Fatalf("OpenAI usage = %#v", got)
	}
	if _, err := srv.GetMyCopilotUsage(ctx, &platform.GetMyCopilotUsageRequest{}); err == nil {
		t.Fatal("GetMyCopilotUsage() unexpectedly accepted invalid Copilot credential")
	}
}

func TestGetMyOpenAIUsageWithoutOAuthReturnsDisconnectedState(t *testing.T) {
	scheme := testProjectScheme(t)
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme}
	ctx := credActorCtx("user-without-openai", "No OAuth")
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{OpenaiApiKey: "test-openai-key"}); err != nil {
		t.Fatalf("UpdateMyCredentials() error = %v", err)
	}
	got, err := srv.GetMyOpenAIUsage(ctx, &platform.GetMyOpenAIUsageRequest{})
	if err != nil {
		t.Fatalf("GetMyOpenAIUsage() error = %v", err)
	}
	if got.OpenaiOauthPresent || got.LookbackDays != 30 || got.FetchedAtUnix == 0 {
		t.Fatalf("response = %#v", got)
	}
}

func testOpenAIOAuthJSON(t *testing.T, accountID string) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID}})
	if err != nil {
		t.Fatal(err)
	}
	idToken := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	authJSON, err := json.Marshal(map[string]any{"tokens": map[string]string{
		"id_token": idToken, "access_token": "access-token", "refresh_token": "refresh-token", "account_id": accountID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(authJSON)
}

func TestOpenAIUsageLimitsIncludesSpendControl(t *testing.T) {
	got := openAIUsageLimits(&openAIRateLimitStatus{SpendControl: &openAISpendControlStatus{
		IndividualLimit: &openAIIndividualSpendLimit{Limit: "25000", Used: "8000", UsedPercent: 32, ResetAt: 123},
	}})
	if len(got) != 1 || got[0].Label != "Monthly credit limit" || got[0].Details != "8000 of 25000 credits used" {
		t.Fatalf("limits = %#v", got)
	}
}

func TestProfileTokensLast30DaysRejectsOldFutureAndMalformedBuckets(t *testing.T) {
	profile := &openAITokenUsageProfile{Stats: openAITokenUsageStats{DailyUsageBuckets: []openAITokenUsageDailyBucket{
		{StartDate: "2026-06-22", Tokens: 100},
		{StartDate: "2026-06-23", Tokens: 200},
		{StartDate: "2026-07-22", Tokens: 300},
		{StartDate: "2026-07-23", Tokens: 400},
		{StartDate: "not-a-date", Tokens: 500},
	}}}
	if got := profileTokensLast30Days(profile, time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)); got != 500 {
		t.Fatalf("profileTokensLast30Days() = %d, want 500", got)
	}
}
