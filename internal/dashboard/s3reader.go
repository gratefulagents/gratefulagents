package dashboard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// preserveEventUsageCacheSemantics carries fields introduced by newer SDK
// ContentEvent versions through the current ActivityEntry compatibility shape.
// InputRaw is already the llm_attempt metadata envelope and is the fallback
// source used by usage aggregation for events produced before the protobuf
// schema grows dedicated semantics fields.
func preserveEventUsageCacheSemantics(line []byte, entry *platform.ActivityEntry) {
	if entry == nil || entry.Type != "llm_attempt" {
		return
	}
	var wire struct {
		InputTokensIncludeCache      bool `json:"input_tokens_include_cache"`
		InputTokensIncludeCacheKnown bool `json:"input_tokens_include_cache_known"`
	}
	if json.Unmarshal(line, &wire) != nil || !wire.InputTokensIncludeCacheKnown {
		return
	}
	payload := map[string]any{}
	if raw := strings.TrimSpace(entry.InputRaw); raw != "" {
		if json.Unmarshal([]byte(raw), &payload) != nil {
			return
		}
	}
	payload["input_tokens_include_cache"] = wire.InputTokensIncludeCache
	payload["input_tokens_include_cache_known"] = true
	if raw, err := json.Marshal(payload); err == nil {
		entry.InputRaw = string(raw)
	}
}

// contentEventToActivityEntry translates a ContentEvent into an ActivityEntry proto.
func contentEventToActivityEntry(ev *agent.ContentEvent) *platform.ActivityEntry {
	// A zero/unset Timestamp would yield time.Time{}.Unix() == -62135596800
	// (year 1), which poisons downstream wall-clock math (e.g. "Worked for
	// 17754771h"). Emit 0 instead so consumers can treat it as "unknown".
	var tsUnix int64
	if !ev.Timestamp.IsZero() {
		tsUnix = ev.Timestamp.Unix()
	}
	entry := &platform.ActivityEntry{
		TimestampUnix:  tsUnix,
		Session:        ev.Session,
		Message:        ev.Message,
		Tool:           ev.Tool,
		ToolUseId:      ev.ToolUseID,
		ParentCallId:   ev.ParentCallID,
		IsError:        ev.IsError,
		AgentName:      ev.AgentName,
		InputRaw:       ev.InputRaw,
		Output:         ev.Output,
		ToolDurationMs: ev.ToolDurationMS,
		Phase:          ev.Phase,
		TaskId:         ev.TaskID,
		Step:           ev.Step,
		TokensBefore:   ev.TokensBefore,
		TokensAfter:    ev.TokensAfter,
	}
	if ev.Type == "llm_attempt" {
		entry.Type = "llm_attempt"
		entry.LlmAttemptId = ev.ToolUseID
		entry.LlmAttemptModel = ev.Model
		entry.LlmAttemptProvider = ev.Provider
		entry.StatusCategory = firstNonEmpty(ev.AttemptStatus, ev.Status)
		entry.Reason = firstNonEmpty(ev.FailureKind, ev.Reason)
		entry.DurationMs = ev.AttemptLatencyMs
		entry.RetryAfterMs = ev.RetryAfterMs
		entry.Turn = ev.Turn
		entry.LlmAttemptInputTokens = ev.InputTokens
		entry.LlmAttemptOutputTokens = ev.OutputTokens
		entry.LlmAttemptCacheReadInputTokens = ev.CacheReadInputTokens
		entry.LlmAttemptCacheCreationInputTokens = ev.CacheCreationInputTokens
		entry.LlmAttemptTokensKnown = ev.UsageAvailable || ev.InputTokens > 0 || ev.OutputTokens > 0 || ev.CacheReadInputTokens > 0 || ev.CacheCreationInputTokens > 0
		entry.CostUsd = ev.CostUsd
		entry.CostKnown = ev.CostKnown
		if raw := strings.TrimSpace(ev.InputRaw); raw != "" {
			var payload struct {
				AttemptID                string   `json:"attempt_id"`
				Model                    string   `json:"model"`
				Provider                 string   `json:"provider"`
				InputTokens              int64    `json:"input_tokens"`
				OutputTokens             int64    `json:"output_tokens"`
				CacheReadInputTokens     int64    `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int64    `json:"cache_creation_input_tokens"`
				TokensKnown              *bool    `json:"tokens_known"`
				CostUsd                  *float64 `json:"cost_usd"`
				CostKnown                *bool    `json:"cost_known"`
			}
			if json.Unmarshal([]byte(raw), &payload) == nil {
				if payload.AttemptID != "" {
					entry.LlmAttemptId = payload.AttemptID
				}
				if payload.Model != "" {
					entry.LlmAttemptModel = payload.Model
				}
				entry.LlmAttemptProvider = payload.Provider
				if payload.InputTokens != 0 || payload.OutputTokens != 0 || payload.CacheReadInputTokens != 0 || payload.CacheCreationInputTokens != 0 {
					entry.LlmAttemptInputTokens = payload.InputTokens
					entry.LlmAttemptOutputTokens = payload.OutputTokens
					entry.LlmAttemptCacheReadInputTokens = payload.CacheReadInputTokens
					entry.LlmAttemptCacheCreationInputTokens = payload.CacheCreationInputTokens
				}
				if payload.TokensKnown != nil {
					entry.LlmAttemptTokensKnown = *payload.TokensKnown
				}
				if payload.CostUsd != nil {
					entry.CostUsd = *payload.CostUsd
				}
				if payload.CostKnown != nil {
					entry.CostKnown = *payload.CostKnown
				}
			}
		}
		return entry
	}
	switch ev.Type {
	case "tool_start":
		entry.Type = "tool_use"
	case "tool_end":
		entry.Type = "tool_result"
	case "subagent_status":
		entry.SubagentType = firstNonEmpty(ev.SubagentType, ev.AgentName)
		entry.SubagentModel = ev.SubagentModel
		entry.SubagentPrompt = ev.SubagentPrompt
		entry.SubagentResultText = ev.SubagentResultText
		entry.SubagentToolCount = ev.SubagentToolCount
		entry.SubagentTotalTokens = ev.SubagentTokens
		entry.SubagentDurationMs = ev.SubagentDurationMs
		entry.SubagentCostUsd = ev.SubagentCostUsd
		entry.SubagentCostKnown = ev.SubagentCostKnown
		entry.SubagentNumTurns = ev.SubagentNumTurns
		entry.SubagentStopReason = ev.SubagentStopReason
		entry.SubagentDependsOn = append([]string(nil), ev.SubagentDependsOn...)
		entry.SubagentWaitingOn = append([]string(nil), ev.SubagentWaitingOn...)
		entry.SubagentCurrentStep = ev.SubagentCurrentStep
		entry.SubagentLastTool = ev.SubagentLastTool
		entry.SubagentFilesWritten = int32(ev.SubagentFilesWritten)
		entry.SubagentMessagesReceived = int32(ev.SubagentMessagesReceived)
		entry.SubagentLastParentMessage = ev.SubagentLastParentMessage
		entry.LastToolName = ev.SubagentLastTool
		entry.SubagentStatus = ev.Status
		entry.SubagentDescription = ev.Message
		switch {
		case ev.Status == "started":
			entry.Type = "subagent_started"
		case isTerminalSubagentStatus(ev.Status):
			// Any terminal category (including failure spellings such as
			// timeout or error) closes the task; routing them as progress
			// would leave the task live forever in every view.
			entry.Type = "subagent_completed"
		default:
			entry.Type = "subagent_progress"
		}
	case "session_end":
		entry.Type = "result"
		entry.StatusCategory = ev.Status
		entry.CostUsd = ev.CostUsd
		entry.CostKnown = ev.CostKnown
		entry.InputTokens = ev.InputTokens
		entry.OutputTokens = ev.OutputTokens
		entry.CacheReadInputTokens = ev.CacheReadInputTokens
		entry.CacheCreationInputTokens = ev.CacheCreationInputTokens
		entry.NumTurns = ev.NumTurns
		entry.DurationMs = ev.DurationMs
		entry.StopReason = ev.StopReason
	case "system_init":
		entry.Type = "system_init"
		entry.Model = ev.Model
		entry.PermissionMode = ev.PermissionMode
		entry.Cwd = ev.Cwd
		entry.MaxTurns = ev.MaxTurns
		entry.Tools = ev.Tools
		entry.McpServers = ev.McpServers
	case "hook_decision":
		entry.Type = "hook_decision"
		entry.HookName = ev.HookName
		entry.Decision = ev.Decision
		entry.Reason = ev.Reason
	default:
		entry.Type = ev.Type
	}
	return entry
}

// s3ActivityReader fetches and parses event streams from S3. It does not
// retain parsed entries itself: the built activity-log response is memoized
// per run in Server.activityMemo under a shared byte budget, so a second
// cache here would only pin the same payloads twice. Concurrent fetches of
// the same URL (several tabs opening one finished run) are coalesced so the
// artifact is downloaded and parsed once.
type s3ActivityReader struct {
	client   *s3.Client
	mu       sync.Mutex
	inflight map[string]*s3Fetch
	// fetch, when set, replaces the S3 download (tests).
	fetch func(ctx context.Context, s3URL string) ([]*platform.ActivityEntry, error)
}

// s3Fetch is one in-flight download shared by every caller that asked for
// the same URL while it was running.
type s3Fetch struct {
	done    chan struct{}
	entries []*platform.ActivityEntry
	err     error
}

func newS3ActivityReader() *s3ActivityReader {
	bucket := os.Getenv("S3_BUCKET")
	if bucket == "" {
		return nil
	}

	region := os.Getenv("S3_REGION")
	if region == "" {
		region = "us-east-1"
	}

	opts := s3.Options{
		Region:       region,
		UsePathStyle: true, // Required for Minio.
	}
	if ak, sk := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"); ak != "" && sk != "" {
		opts.Credentials = credentials.NewStaticCredentialsProvider(ak, sk, "")
	}
	if endpoint := os.Getenv("S3_ENDPOINT"); endpoint != "" {
		opts.BaseEndpoint = aws.String(endpoint)
	}

	return &s3ActivityReader{
		client: s3.New(opts),
	}
}

// parseS3URL extracts bucket and key from an s3://bucket/key URL.
func parseS3URL(s3URL string) (bucket, key string, err error) {
	path := strings.TrimPrefix(s3URL, "s3://")
	if path == s3URL {
		return "", "", fmt.Errorf("invalid S3 URL (must start with s3://): %s", s3URL)
	}
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid S3 URL (missing key): %s", s3URL)
	}
	return parts[0], parts[1], nil
}

// FetchEventStream downloads and parses a thin events.jsonl from S3.
// Returns ActivityEntry protos with ContentEvent types mapped to legacy names.
// Concurrent calls for the same URL share one download; the returned slice is
// shared between those callers and must be treated as read-only.
func (r *s3ActivityReader) FetchEventStream(ctx context.Context, s3URL string) ([]*platform.ActivityEntry, error) {
	r.mu.Lock()
	if r.inflight == nil {
		r.inflight = make(map[string]*s3Fetch)
	}
	if f, ok := r.inflight[s3URL]; ok {
		r.mu.Unlock()
		select {
		case <-f.done:
			return f.entries, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &s3Fetch{done: make(chan struct{})}
	r.inflight[s3URL] = f
	r.mu.Unlock()

	// Detach the download from the first caller's context so one tab
	// closing mid-download does not fail every other tab that joined it.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s3FetchTimeout)
	go func() {
		defer cancel()
		f.entries, f.err = r.fetchEventStream(fetchCtx, s3URL)
		r.mu.Lock()
		delete(r.inflight, s3URL)
		r.mu.Unlock()
		close(f.done)
	}()
	select {
	case <-f.done:
		return f.entries, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// s3FetchTimeout bounds one events.jsonl download once it has been detached
// from the requesting caller's context.
const s3FetchTimeout = 2 * time.Minute

func (r *s3ActivityReader) fetchEventStream(ctx context.Context, s3URL string) ([]*platform.ActivityEntry, error) {
	if r.fetch != nil {
		return r.fetch(ctx, s3URL)
	}
	bucket, key, err := parseS3URL(s3URL)
	if err != nil {
		return nil, err
	}

	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("S3 GetObject %s: %w", s3URL, err)
	}
	defer out.Body.Close()

	var entries []*platform.ActivityEntry
	reader := bufio.NewReader(out.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("reading S3 body %s: %w", s3URL, err)
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			if err == io.EOF {
				break
			}
			continue
		}
		var ev agent.ContentEvent
		if jsonErr := json.Unmarshal(line, &ev); jsonErr != nil {
			log.Printf("WARN: skipping malformed event line: %v", jsonErr)
			if err == io.EOF {
				break
			}
			continue
		}
		e := contentEventToActivityEntry(&ev)
		preserveEventUsageCacheSemantics(line, e)
		e.EventId = int64(len(entries) + 1)
		entries = append(entries, e)
		if err == io.EOF {
			break
		}
	}
	return entries, nil
}
