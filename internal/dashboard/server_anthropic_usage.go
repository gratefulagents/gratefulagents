package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
	oauth "github.com/gratefulagents/sdk/pkg/agentsdk/providers/oauth"
)

const (
	anthropicUsageURL     = "https://api.anthropic.com/api/oauth/usage"
	anthropicOAuthBeta    = "oauth-2025-04-20"
	anthropicUsageTimeout = 10 * time.Second
)

type anthropicUsageResponse struct {
	FiveHour       *anthropicUsageWindow `json:"five_hour"`
	SevenDay       *anthropicUsageWindow `json:"seven_day"`
	SevenDayOpus   *anthropicUsageWindow `json:"seven_day_opus"`
	SevenDaySonnet *anthropicUsageWindow `json:"seven_day_sonnet"`
	ExtraUsage     *anthropicExtraUsage  `json:"extra_usage"`
}

type anthropicUsageWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type anthropicExtraUsage struct {
	IsEnabled    bool     `json:"is_enabled"`
	MonthlyLimit *float64 `json:"monthly_limit"`
	UsedCredits  *float64 `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
}

type anthropicUsageAuthError struct {
	statusCode int
}

func (e *anthropicUsageAuthError) Error() string {
	if e.statusCode == 0 {
		return "Anthropic OAuth credential is missing an access token"
	}
	return fmt.Sprintf("Claude usage endpoint rejected the OAuth credential with status %d", e.statusCode)
}

// GetMyAnthropicUsage reads account metadata and allowance windows through the
// calling user's saved Claude OAuth credential. OAuth material never leaves
// the server.
func (s *Server) GetMyAnthropicUsage(ctx context.Context, _ *platform.GetMyAnthropicUsageRequest) (*platform.MyAnthropicUsage, error) {
	actor, err := providerOAuthActor(ctx)
	if err != nil {
		return nil, err
	}
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	out := &platform.MyAnthropicUsage{FetchedAtUnix: time.Now().UTC().Unix()}
	secret, err := s.readSecret(ctx, namespace, userCredentialSecretName("anthropic"))
	if k8serrors.IsNotFound(err) {
		return out, nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("saved Anthropic credential could not be read"))
	}

	authJSON := secret.Data[userCredOAuthJSONKey]
	if len(authJSON) == 0 {
		return out, nil
	}
	if provider := oauthMaterialProvider(secret); provider != "" && provider != "anthropic" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("saved Anthropic OAuth credential has the wrong provider"))
	}
	auth, err := oauth.ParseAnthropicAuthJSON(authJSON)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("saved Anthropic OAuth credential is invalid"))
	}
	out.AnthropicOauthPresent = true
	out.AccountEmail = strings.TrimSpace(auth.Email)
	out.AccountUuid = strings.TrimSpace(auth.AccountUUID)
	if !auth.ExpiresAt.IsZero() {
		out.CredentialExpiresAtUnix = auth.ExpiresAt.Unix()
	}
	if !auth.LastRefresh.IsZero() {
		out.CredentialLastRefreshedAtUnix = auth.LastRefresh.Unix()
	}

	usage, err := s.fetchAnthropicUsage(ctx, auth.AccessToken)
	if err != nil {
		var authErr *anthropicUsageAuthError
		if errors.As(err, &authErr) {
			out.ReconnectRequired = true
			out.Warnings = append(out.Warnings, "Claude rejected this OAuth credential. Connect Anthropic again under Credentials.")
		} else {
			out.Warnings = append(out.Warnings, "Claude allowance data is temporarily unavailable.")
		}
		return out, nil
	}
	out.UsageAvailable = true
	out.Limits = anthropicUsageLimits(usage)
	if usage.ExtraUsage != nil {
		out.ExtraUsageAvailable = true
		out.ExtraUsageEnabled = usage.ExtraUsage.IsEnabled
		out.ExtraUsageMonthlyLimitUsdCents = usage.ExtraUsage.MonthlyLimit
		out.ExtraUsageUsedCreditsUsdCents = usage.ExtraUsage.UsedCredits
		out.ExtraUsageUtilization = usage.ExtraUsage.Utilization
	}
	return out, nil
}

func (s *Server) fetchAnthropicUsage(ctx context.Context, accessToken string) (*anthropicUsageResponse, error) {
	token := strings.TrimSpace(accessToken)
	if token == "" {
		return nil, &anthropicUsageAuthError{}
	}
	requestCtx, cancel := context.WithTimeout(ctx, anthropicUsageTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, anthropicUsageURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("anthropic-beta", anthropicOAuthBeta)
	request.Header.Set("User-Agent", "gratefulagents")

	httpClient := *s.providerOAuthClient()
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return nil, &anthropicUsageAuthError{statusCode: response.StatusCode}
		}
		return nil, fmt.Errorf("Claude usage endpoint returned %s", response.Status)
	}
	var usage anthropicUsageResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&usage); err != nil {
		return nil, fmt.Errorf("decode Claude usage response: %w", err)
	}
	return &usage, nil
}

func anthropicUsageLimits(usage *anthropicUsageResponse) []*platform.AnthropicUsageLimit {
	if usage == nil {
		return nil
	}
	windows := []struct {
		label  string
		window *anthropicUsageWindow
	}{
		{label: "5 hour", window: usage.FiveHour},
		{label: "Weekly", window: usage.SevenDay},
		{label: "Weekly Opus", window: usage.SevenDayOpus},
		{label: "Weekly Sonnet", window: usage.SevenDaySonnet},
	}
	out := make([]*platform.AnthropicUsageLimit, 0, len(windows))
	for _, item := range windows {
		if item.window == nil {
			continue
		}
		limit := &platform.AnthropicUsageLimit{
			Label:       item.label,
			UsedPercent: clampPercent(item.window.Utilization),
		}
		if reset, err := time.Parse(time.RFC3339, strings.TrimSpace(item.window.ResetsAt)); err == nil {
			limit.ResetAtUnix = reset.Unix()
		}
		out = append(out, limit)
	}
	return out
}
