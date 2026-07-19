package dashboard

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

type usageTotals struct {
	inputTokens              int64
	outputTokens             int64
	cacheReadInputTokens     int64
	cacheCreationInputTokens int64
	totalTokens              int64
	tokensKnown              bool
}

func (u *usageTotals) add(other usageTotals) {
	u.inputTokens += other.inputTokens
	u.outputTokens += other.outputTokens
	u.cacheReadInputTokens += other.cacheReadInputTokens
	u.cacheCreationInputTokens += other.cacheCreationInputTokens
	u.totalTokens += other.totalTokens
	u.tokensKnown = u.tokensKnown || other.tokensKnown
}

func normalizedUsageIdentity(value string) string {
	return strings.NewReplacer("-", "", "_", "", " ", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(value)))
}

func providerInputIncludesCache(provider string) bool {
	switch normalizedUsageIdentity(provider) {
	case "openai", "copilot", "githubcopilot", "azure", "azureopenai", "openrouter", "xai", "openaicompatible":
		return true
	default:
		return false
	}
}

func modelInputIncludesCache(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{"openai/", "copilot/", "github-copilot/", "azure/", "azure-openai/", "openrouter/", "xai/"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

func entryInputIncludesCache(entry *platform.ActivityEntry) bool {
	if entry == nil {
		return false
	}
	if raw := strings.TrimSpace(entry.InputRaw); raw != "" {
		var payload struct {
			InputTokensIncludeCache      bool `json:"input_tokens_include_cache"`
			InputTokensIncludeCacheKnown bool `json:"input_tokens_include_cache_known"`
		}
		if json.Unmarshal([]byte(raw), &payload) == nil && payload.InputTokensIncludeCacheKnown {
			return payload.InputTokensIncludeCache
		}
	}
	return providerInputIncludesCache(entry.LlmAttemptProvider) || modelInputIncludesCache(entry.LlmAttemptModel)
}

func usageForEntry(entry *platform.ActivityEntry) usageTotals {
	inputTokens := entry.LlmAttemptInputTokens
	outputTokens := entry.LlmAttemptOutputTokens
	cacheReadInputTokens := entry.LlmAttemptCacheReadInputTokens
	cacheCreationInputTokens := entry.LlmAttemptCacheCreationInputTokens
	totalTokens := inputTokens + outputTokens + cacheReadInputTokens + cacheCreationInputTokens
	if entryInputIncludesCache(entry) {
		totalTokens = inputTokens + outputTokens
	}
	return usageTotals{
		inputTokens:              inputTokens,
		outputTokens:             outputTokens,
		cacheReadInputTokens:     cacheReadInputTokens,
		cacheCreationInputTokens: cacheCreationInputTokens,
		totalTokens:              totalTokens,
		tokensKnown:              entry.LlmAttemptTokensKnown,
	}
}

func (u usageTotals) proto() *platform.UsageTotals {
	return &platform.UsageTotals{
		InputTokens:              u.inputTokens,
		OutputTokens:             u.outputTokens,
		CacheReadInputTokens:     u.cacheReadInputTokens,
		CacheCreationInputTokens: u.cacheCreationInputTokens,
		TotalTokens:              u.totalTokens,
		TokensKnown:              u.tokensKnown,
	}
}

type usageAttemptRecord struct {
	attemptID  string
	agentName  string
	taskID     string
	phase      string
	step       string
	model      string
	provider   string
	timestamp  int64
	isSubagent bool
	usage      usageTotals
}

type usageTaskRecord struct {
	taskID      string
	agentName   string
	isTopLevel  bool
	usage       usageTotals
	attempts    []*usageAttemptRecord
	attemptByID map[string]*usageAttemptRecord
}

type usagePhaseRecord struct {
	phase          string
	usage          usageTotals
	tasks          map[string]*usageTaskRecord
	firstTimestamp int64
}

func aggregateUsageFromEntries(entries []*platform.ActivityEntry) *platform.AgentRunUsageResponse {
	resp := &platform.AgentRunUsageResponse{IsAvailable: false, IsComplete: false}
	if len(entries) == 0 {
		return resp
	}

	summary := usageTotals{}
	phases := map[string]*usagePhaseRecord{}
	tasks := map[string]*usageTaskRecord{}
	available := false

	ensureTask := func(taskID, agentName string) *usageTaskRecord {
		bucketID := taskID
		isTopLevel := false
		if bucketID == "" {
			bucketID = "top-level"
			isTopLevel = true
		}
		rec := tasks[bucketID]
		if rec == nil {
			rec = &usageTaskRecord{taskID: bucketID, agentName: agentName, isTopLevel: isTopLevel, attemptByID: map[string]*usageAttemptRecord{}}
			tasks[bucketID] = rec
		}
		if rec.agentName == "" {
			rec.agentName = agentName
		}
		return rec
	}

	ensurePhase := func(phase string, timestamp int64) *usagePhaseRecord {
		if phase == "" {
			return nil
		}
		rec := phases[phase]
		if rec == nil {
			rec = &usagePhaseRecord{
				phase:          phase,
				tasks:          map[string]*usageTaskRecord{},
				firstTimestamp: timestamp,
			}
			phases[phase] = rec
		}
		if rec.firstTimestamp == 0 || (timestamp > 0 && timestamp < rec.firstTimestamp) {
			rec.firstTimestamp = timestamp
		}
		return rec
	}

	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if entry.Type == "llm_attempt" {
			available = true
			phase := strings.TrimSpace(entry.Phase)
			step := strings.TrimSpace(entry.Step)
			usage := usageForEntry(entry)
			summary.add(usage)
			task := ensureTask(entry.TaskId, entry.AgentName)
			task.usage.add(usage)
			if phaseRec := ensurePhase(phase, entry.TimestampUnix); phaseRec != nil {
				phaseRec.usage.add(usage)
				phaseRec.tasks[task.taskID] = task
			}
			attemptID := entry.LlmAttemptId
			if attemptID == "" {
				attemptID = entry.TaskId + ":" + entry.AgentName + ":" + phase
				if phase == "" {
					attemptID = entry.TaskId + ":" + entry.AgentName + ":" + step + ":" + strings.TrimSpace(entry.LlmAttemptModel)
				}
			}
			attempt := task.attemptByID[attemptID]
			if attempt == nil {
				attempt = &usageAttemptRecord{
					attemptID:  attemptID,
					agentName:  entry.AgentName,
					taskID:     task.taskID,
					phase:      phase,
					step:       step,
					model:      entry.LlmAttemptModel,
					provider:   entry.LlmAttemptProvider,
					timestamp:  entry.TimestampUnix,
					isSubagent: !task.isTopLevel,
				}
				task.attemptByID[attemptID] = attempt
				task.attempts = append(task.attempts, attempt)
			}
			attempt.usage.add(usage)
			if attempt.model == "" {
				attempt.model = entry.LlmAttemptModel
			}
			if attempt.provider == "" {
				attempt.provider = entry.LlmAttemptProvider
			}
			if attempt.agentName == "" {
				attempt.agentName = entry.AgentName
			}
			if attempt.phase == "" {
				attempt.phase = phase
			}
			if attempt.step == "" {
				attempt.step = step
			}
			continue
		}
	}

	resp.IsAvailable = available
	resp.Summary = summary.proto()
	for _, e := range entries {
		if e != nil && e.Type == "result" {
			resp.IsComplete = true
			break
		}
	}

	phaseList := make([]*usagePhaseRecord, 0, len(phases))
	for _, phase := range phases {
		phaseList = append(phaseList, phase)
	}
	sort.Slice(phaseList, func(i, j int) bool {
		if phaseList[i].firstTimestamp == phaseList[j].firstTimestamp {
			return phaseList[i].phase < phaseList[j].phase
		}
		return phaseList[i].firstTimestamp < phaseList[j].firstTimestamp
	})
	for _, phase := range phaseList {
		phaseTasks := make([]*platform.UsageTask, 0, len(phase.tasks))
		for _, task := range sortedUsageTasks(phase.tasks) {
			phaseTasks = append(phaseTasks, task)
		}
		resp.Phases = append(resp.Phases, &platform.UsagePhase{
			Phase: phase.phase,
			Usage: phase.usage.proto(),
			Tasks: phaseTasks,
		})
	}

	for _, task := range sortedUsageTasks(tasks) {
		if task.IsTopLevel {
			resp.TopLevelTasks = append(resp.TopLevelTasks, task)
		} else {
			resp.SubagentTasks = append(resp.SubagentTasks, task)
		}
	}
	return resp
}

func sortedUsageTasks(tasks map[string]*usageTaskRecord) []*platform.UsageTask {
	keys := make([]string, 0, len(tasks))
	for k := range tasks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*platform.UsageTask, 0, len(keys))
	for _, k := range keys {
		rec := tasks[k]
		sort.Slice(rec.attempts, func(i, j int) bool {
			if rec.attempts[i].timestamp == rec.attempts[j].timestamp {
				return rec.attempts[i].attemptID < rec.attempts[j].attemptID
			}
			return rec.attempts[i].timestamp < rec.attempts[j].timestamp
		})
		attempts := make([]*platform.UsageAttempt, 0, len(rec.attempts))
		for _, attempt := range rec.attempts {
			attempts = append(attempts, &platform.UsageAttempt{
				AttemptId:     attempt.attemptID,
				AgentName:     attempt.agentName,
				TaskId:        attempt.taskID,
				Phase:         attempt.phase,
				Step:          attempt.step,
				Model:         attempt.model,
				Provider:      attempt.provider,
				Usage:         attempt.usage.proto(),
				TimestampUnix: attempt.timestamp,
				IsSubagent:    attempt.isSubagent,
			})
		}
		out = append(out, &platform.UsageTask{
			TaskId:     rec.taskID,
			AgentName:  rec.agentName,
			IsTopLevel: rec.isTopLevel,
			Usage:      rec.usage.proto(),
			Attempts:   attempts,
		})
	}
	return out
}
