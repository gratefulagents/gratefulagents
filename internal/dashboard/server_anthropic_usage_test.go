package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetMyAnthropicUsageReadsCurrentOAuthAccount(t *testing.T) {
	scheme := testProjectScheme(t)
	expiresAt := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	refreshedAt := expiresAt.Add(-8 * time.Hour)
	authJSON, err := oauth.MarshalAnthropicAuthJSON(oauth.AnthropicAuth{
		AccessToken: "access-token", RefreshToken: "refresh-token", Email: "claude@example.com",
		AccountUUID: "account-123", ExpiresAt: expiresAt, LastRefresh: refreshedAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	transport := providerOAuthRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != anthropicUsageURL {
			t.Fatalf("URL = %q", req.URL.String())
		}
		if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := req.Header.Get("anthropic-beta"); got != anthropicOAuthBeta {
			t.Fatalf("anthropic-beta = %q", got)
		}
		body := `{"five_hour":{"utilization":42.5,"resets_at":"2026-07-25T08:00:00Z"},"seven_day":{"utilization":68,"resets_at":"2026-07-28T08:00:00Z"},"seven_day_opus":{"utilization":11,"resets_at":"2026-07-29T08:00:00Z"},"seven_day_sonnet":null,"extra_usage":{"is_enabled":true,"monthly_limit":5000,"used_credits":1250,"utilization":25}}`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})

	k8sClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: k8sClient, scheme: scheme, providerOAuthHTTP: &http.Client{Transport: transport}}
	ctx := context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: "user-usage", Name: "Usage User", Role: "owner"})
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{AnthropicOauthJson: string(authJSON)}); err != nil {
		t.Fatalf("UpdateMyCredentials() error = %v", err)
	}

	got, err := srv.GetMyAnthropicUsage(ctx, &platform.GetMyAnthropicUsageRequest{})
	if err != nil {
		t.Fatalf("GetMyAnthropicUsage() error = %v", err)
	}
	if !got.AnthropicOauthPresent || !got.UsageAvailable || !got.ExtraUsageAvailable || !got.ExtraUsageEnabled {
		t.Fatalf("availability = %#v", got)
	}
	if got.AccountEmail != "claude@example.com" || got.AccountUuid != "account-123" {
		t.Fatalf("account fields = %#v", got)
	}
	if got.CredentialExpiresAtUnix != expiresAt.Unix() || got.CredentialLastRefreshedAtUnix != refreshedAt.Unix() {
		t.Fatalf("credential dates = %#v", got)
	}
	if len(got.Limits) != 3 || got.Limits[0].Label != "5 hour" || got.Limits[0].UsedPercent != 42.5 {
		t.Fatalf("limits = %#v", got.Limits)
	}
	if got.ExtraUsageMonthlyLimitUsdCents == nil || *got.ExtraUsageMonthlyLimitUsdCents != 5000 || got.ExtraUsageUsedCreditsUsdCents == nil || *got.ExtraUsageUsedCreditsUsdCents != 1250 {
		t.Fatalf("extra usage = %#v", got)
	}
}

func TestGetMyAnthropicUsageWithoutOAuthReturnsDisconnectedState(t *testing.T) {
	scheme := testProjectScheme(t)
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme}
	ctx := credActorCtx("user-without-anthropic", "No OAuth")
	got, err := srv.GetMyAnthropicUsage(ctx, &platform.GetMyAnthropicUsageRequest{})
	if err != nil {
		t.Fatalf("GetMyAnthropicUsage() error = %v", err)
	}
	if got.AnthropicOauthPresent || got.FetchedAtUnix == 0 {
		t.Fatalf("response = %#v", got)
	}
}

func TestGetMyAnthropicUsageKeepsCredentialMetadataWhenUsageUnavailable(t *testing.T) {
	scheme := testProjectScheme(t)
	authJSON, _ := json.Marshal(map[string]any{"access_token": "access-token", "email": "claude@example.com", "type": "claude"})
	transport := providerOAuthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme, providerOAuthHTTP: &http.Client{Transport: transport}}
	ctx := credActorCtx("user-anthropic-unavailable", "Usage User")
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{AnthropicOauthJson: string(authJSON)}); err != nil {
		t.Fatal(err)
	}
	got, err := srv.GetMyAnthropicUsage(ctx, &platform.GetMyAnthropicUsageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.AnthropicOauthPresent || got.UsageAvailable || got.ReconnectRequired || got.AccountEmail != "claude@example.com" || len(got.Warnings) != 1 {
		t.Fatalf("response = %#v", got)
	}
}

func TestGetMyAnthropicUsageRequiresReconnectWhenOAuthIsRejected(t *testing.T) {
	scheme := testProjectScheme(t)
	authJSON, _ := json.Marshal(map[string]any{"access_token": "revoked-token", "email": "claude@example.com", "type": "claude"})
	transport := providerOAuthRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("rejected"))}, nil
	})
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme, providerOAuthHTTP: &http.Client{Transport: transport}}
	ctx := credActorCtx("user-anthropic-revoked", "Usage User")
	if _, err := srv.UpdateMyCredentials(ctx, &platform.UpdateMyCredentialsRequest{AnthropicOauthJson: string(authJSON)}); err != nil {
		t.Fatal(err)
	}
	got, err := srv.GetMyAnthropicUsage(ctx, &platform.GetMyAnthropicUsageRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.AnthropicOauthPresent || !got.ReconnectRequired || got.UsageAvailable || len(got.Warnings) != 1 {
		t.Fatalf("response = %#v", got)
	}
}
