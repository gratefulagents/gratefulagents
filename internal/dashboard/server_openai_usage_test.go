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
			body = `{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":42,"limit_window_seconds":18000,"reset_at":1893456000},"secondary_window":{"used_percent":9,"limit_window_seconds":604800,"reset_at":1893888000}},"credits":{"has_credits":true,"unlimited":false,"balance":"12.50"}}`
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
	mu.Lock()
	seenUsage := seen["/backend-api/wham/usage"]
	seenProfile := seen["/backend-api/wham/profiles/me"]
	mu.Unlock()
	if !seenUsage || !seenProfile {
		t.Fatalf("seen paths = %#v", seen)
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
