package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PostWebhookMessage posts a plain-text message to a Slack incoming webhook
// URL. It is used by notification paths that have a webhook URL but no bot
// token.
func PostWebhookMessage(ctx context.Context, httpClient *http.Client, webhookURL, text string) error {
	if !strings.HasPrefix(webhookURL, "https://") {
		return fmt.Errorf("slack webhook URL must use https")
	}
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("encoding slack webhook payload: %w", err)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building slack webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("posting slack webhook message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack webhook returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
