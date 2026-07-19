package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// Transcript snapshot persistence: after each successful turn the loop's
// in-memory session transcript (the runner's exact post-run conversation
// state — messages, tool calls/outputs, reasoning, compaction summaries) is
// serialized and upserted as ONE bounded row per session. A restarted pod
// rehydrates it and resumes with full context instead of the lossy 8-message
// durable tail.
//
// DB growth is bounded by construction:
//   - one upserted row per run, never an append-only log;
//   - the transcript itself is token-bounded by the SDK's mid-run
//     compaction, so the snapshot inherits that ceiling;
//   - image payloads (the only content not bounded by tokens, and
//     incompressible) are stripped;
//   - the gzipped blob is capped at transcriptSnapshotMaxBytes — on breach
//     the snapshot is cleared and resume falls back to the durable tail;
//   - the row dies with the session (ON DELETE CASCADE) and is cleared on
//     external context clears and interrupted runs.

const (
	transcriptSnapshotVersion = 1

	// transcriptSnapshotMaxBytesDefault caps the compressed snapshot size.
	// A compaction-bounded transcript gzips to a few hundred KB; anything
	// near this cap is pathological and falls back to the durable tail.
	transcriptSnapshotMaxBytesDefault = 4 << 20 // 4 MiB

	transcriptImagePlaceholder = "[image attachment omitted from restart snapshot]"
)

// transcriptSnapshotMaxBytes returns the compressed-size cap, overridable via
// TRANSCRIPT_SNAPSHOT_MAX_BYTES. Zero or negative disables persistence.
func transcriptSnapshotMaxBytes() int {
	if raw := os.Getenv("TRANSCRIPT_SNAPSHOT_MAX_BYTES"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			return parsed
		}
		log.Printf("WARN: invalid TRANSCRIPT_SNAPSHOT_MAX_BYTES %q — using default", raw)
	}
	return transcriptSnapshotMaxBytesDefault
}

// transcriptSnapshot is the versioned durable envelope. The watermark fields
// mirror the loop's in-memory transcript bookkeeping so a rehydrated
// transcript folds out-of-band durable messages exactly like a live one.
type transcriptSnapshot struct {
	Version int `json:"version"`
	// FloorMessageID is the durable history floor the transcript was built
	// against. A different floor at load time means the context was cleared
	// while the pod was down — the snapshot is stale and discarded.
	FloorMessageID int64 `json:"floor_message_id"`
	// SeenMessageID is the highest durable message ID already represented in
	// the transcript (loop watermark transcriptSeenMessageID).
	SeenMessageID int64 `json:"seen_message_id"`
	// SelfAssistantMessageID is the loop's own durable append of the last
	// turn's assistant reply, excluded from the out-of-band fold.
	SelfAssistantMessageID int64 `json:"self_assistant_message_id"`
	// PendingUserMessageID, when non-zero, marks a snapshot flushed mid-turn
	// (pod termination, turn-budget exhaustion): the durable user message
	// that started the interrupted turn, whose prompt is already inside
	// Items together with the partial work it triggered. The resume cursor
	// only advances on assistant replies, so a restarted pod receives that
	// message again — the loop must open the resumed turn with a
	// continuation instruction instead of replaying the prompt verbatim.
	PendingUserMessageID int64              `json:"pending_user_message_id,omitempty"`
	Items                []persistedRunItem `json:"items"`
}

// persistedRunItem is a stable, forward-compatible DTO for agent.RunItem.
// Type is a string (not the SDK's iota) so snapshots survive SDK enum
// reordering; unknown types at load discard the whole snapshot.
type persistedRunItem struct {
	Type string `json:"type"`
	// Agent, when non-nil, marks an assistant-side item; its value is the
	// producing agent's name. Only the name survives persistence — replay
	// role detection needs presence + name, nothing else from agent.Agent.
	Agent         *string                  `json:"agent,omitempty"`
	Message       *agent.MessageOutput     `json:"message,omitempty"`
	ToolCall      *agent.ToolCallData      `json:"tool_call,omitempty"`
	ToolOutput    *agent.ToolOutputData    `json:"tool_output,omitempty"`
	HandoffCall   *agent.HandoffCallData   `json:"handoff_call,omitempty"`
	HandoffOutput *agent.HandoffOutputData `json:"handoff_output,omitempty"`
	Reasoning     *agent.ReasoningData     `json:"reasoning,omitempty"`
	Compaction    *agent.CompactionData    `json:"compaction,omitempty"`
	ToolApproval  *agent.ToolApprovalData  `json:"tool_approval,omitempty"`
}

const (
	persistedTypeMessage       = "message"
	persistedTypeToolCall      = "tool_call"
	persistedTypeToolOutput    = "tool_output"
	persistedTypeHandoffCall   = "handoff_call"
	persistedTypeHandoffOutput = "handoff_output"
	persistedTypeReasoning     = "reasoning"
	persistedTypeToolApproval  = "tool_approval"
	persistedTypeCompaction    = "compaction"
)

var runItemTypeToPersisted = map[agent.RunItemType]string{
	agent.RunItemMessage:       persistedTypeMessage,
	agent.RunItemToolCall:      persistedTypeToolCall,
	agent.RunItemToolOutput:    persistedTypeToolOutput,
	agent.RunItemHandoffCall:   persistedTypeHandoffCall,
	agent.RunItemHandoffOutput: persistedTypeHandoffOutput,
	agent.RunItemReasoning:     persistedTypeReasoning,
	agent.RunItemToolApproval:  persistedTypeToolApproval,
	agent.RunItemCompaction:    persistedTypeCompaction,
}

var persistedTypeToRunItem = map[string]agent.RunItemType{
	persistedTypeMessage:       agent.RunItemMessage,
	persistedTypeToolCall:      agent.RunItemToolCall,
	persistedTypeToolOutput:    agent.RunItemToolOutput,
	persistedTypeHandoffCall:   agent.RunItemHandoffCall,
	persistedTypeHandoffOutput: agent.RunItemHandoffOutput,
	persistedTypeReasoning:     agent.RunItemReasoning,
	persistedTypeToolApproval:  agent.RunItemToolApproval,
	persistedTypeCompaction:    agent.RunItemCompaction,
}

// persistedItemsFromRun converts run items to the persistence DTO, stripping
// image payloads (they are token-unbounded, incompressible, and the durable
// tail never replayed them either). Returns ok=false when an item type is
// unknown to this build — the caller must skip persisting rather than store
// a silently lossy transcript.
func persistedItemsFromRun(items []agent.RunItem) ([]persistedRunItem, bool) {
	out := make([]persistedRunItem, 0, len(items))
	for _, item := range items {
		typeName, ok := runItemTypeToPersisted[item.Type]
		if !ok {
			return nil, false
		}
		persisted := persistedRunItem{
			Type:          typeName,
			ToolCall:      item.ToolCall,
			ToolOutput:    item.ToolOutput,
			HandoffCall:   item.HandoffCall,
			HandoffOutput: item.HandoffOutput,
			Reasoning:     item.Reasoning,
			Compaction:    item.Compaction,
			ToolApproval:  item.ToolApproval,
		}
		if item.Agent != nil {
			name := item.Agent.Name
			persisted.Agent = &name
		}
		if item.Message != nil {
			msg := agent.MessageOutput{Text: item.Message.Text}
			if len(item.Message.Images) > 0 && msg.Text == "" {
				msg.Text = transcriptImagePlaceholder
			}
			persisted.Message = &msg
		}
		out = append(out, persisted)
	}
	return out, true
}

// runItemsFromPersisted rebuilds run items for replay. Agent identity is
// restored as a name-only reference (same shape BuildConversationTail uses).
// Returns ok=false on unknown item types (snapshot written by a newer build).
func runItemsFromPersisted(items []persistedRunItem) ([]agent.RunItem, bool) {
	out := make([]agent.RunItem, 0, len(items))
	for _, persisted := range items {
		itemType, ok := persistedTypeToRunItem[persisted.Type]
		if !ok {
			return nil, false
		}
		item := agent.RunItem{
			Type:          itemType,
			Message:       persisted.Message,
			ToolCall:      persisted.ToolCall,
			ToolOutput:    persisted.ToolOutput,
			HandoffCall:   persisted.HandoffCall,
			HandoffOutput: persisted.HandoffOutput,
			Reasoning:     persisted.Reasoning,
			Compaction:    persisted.Compaction,
			ToolApproval:  persisted.ToolApproval,
		}
		if persisted.Agent != nil {
			item.Agent = &agent.Agent{Name: *persisted.Agent}
		}
		out = append(out, item)
	}
	return out, true
}

// encodeTranscriptSnapshot serializes the envelope as gzipped JSON.
func encodeTranscriptSnapshot(snap transcriptSnapshot) ([]byte, error) {
	raw, err := json.Marshal(snap)
	if err != nil {
		return nil, fmt.Errorf("marshaling transcript snapshot: %w", err)
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(raw); err != nil {
		return nil, fmt.Errorf("compressing transcript snapshot: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("compressing transcript snapshot: %w", err)
	}
	return buf.Bytes(), nil
}

func decodeTranscriptSnapshot(data []byte) (*transcriptSnapshot, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decompressing transcript snapshot: %w", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("decompressing transcript snapshot: %w", err)
	}
	var snap transcriptSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("unmarshaling transcript snapshot: %w", err)
	}
	if snap.Version != transcriptSnapshotVersion {
		return nil, fmt.Errorf("unsupported transcript snapshot version %d", snap.Version)
	}
	return &snap, nil
}

// persistTranscriptSnapshot durably upserts the post-turn transcript.
// Best-effort: failures log a warning and never fail the turn — the resume
// path falls back to the durable tail exactly as before this feature.
// An empty transcript (interrupted run reset) clears the stored snapshot, and
// so does an oversized one: at turn end the previously stored row is stale —
// user prompts answered since it was written are neither folded on restore
// (the out-of-band fold skips user messages) nor re-delivered (the resume
// cursor advances past them on this turn's assistant reply) — so keeping it
// would silently drop them from the restored context.
func persistTranscriptSnapshot(ctx context.Context, sc *sessionclient.Client, items []agent.RunItem, floorMessageID, seenMessageID, selfAssistantMessageID int64) {
	if len(items) == 0 {
		if err := sc.ClearTranscriptBlob(ctx); err != nil {
			log.Printf("WARN: failed to clear transcript snapshot: %v", err)
		}
		return
	}
	writeTranscriptSnapshot(ctx, sc, items, transcriptSnapshot{
		Version:                transcriptSnapshotVersion,
		FloorMessageID:         floorMessageID,
		SeenMessageID:          seenMessageID,
		SelfAssistantMessageID: selfAssistantMessageID,
	}, true)
}

// persistInFlightTranscriptSnapshot durably upserts a MID-TURN transcript
// (pod-termination flush, turn-budget exhaustion). It differs from the
// turn-end persist in two ways, both valid for the same reason — no
// assistant reply has been durably appended since the previous snapshot was
// written, so that row is still a coherent (just older) resume state and the
// in-flight prompt is re-delivered to a restarted pod by the resume cursor:
//   - pendingUserMessageID records the durable user message whose prompt is
//     already inside this transcript, so the restarted loop opens the
//     resumed turn with a continuation instruction instead of replaying the
//     prompt verbatim after the preserved partial progress;
//   - an oversized payload keeps the previously stored snapshot instead of
//     clearing it — persisting nothing degrades to the last completed turn's
//     full context, clearing would drop to the lossy durable tail.
func persistInFlightTranscriptSnapshot(ctx context.Context, sc *sessionclient.Client, items []agent.RunItem, floorMessageID, seenMessageID, selfAssistantMessageID, pendingUserMessageID int64) {
	if len(items) == 0 {
		return
	}
	writeTranscriptSnapshot(ctx, sc, items, transcriptSnapshot{
		Version:                transcriptSnapshotVersion,
		FloorMessageID:         floorMessageID,
		SeenMessageID:          seenMessageID,
		SelfAssistantMessageID: selfAssistantMessageID,
		PendingUserMessageID:   pendingUserMessageID,
	}, false)
}

// writeTranscriptSnapshot encodes snap with items and stores the single
// snapshot row. clearOnOversize selects the size-cap policy — see the two
// persist entry points above for why turn-end clears and mid-turn keeps.
func writeTranscriptSnapshot(ctx context.Context, sc *sessionclient.Client, items []agent.RunItem, snap transcriptSnapshot, clearOnOversize bool) {
	maxBytes := transcriptSnapshotMaxBytes()
	if maxBytes <= 0 {
		return
	}
	persisted, ok := persistedItemsFromRun(items)
	if !ok {
		log.Printf("WARN: transcript snapshot skipped: transcript contains an item type unknown to the persister")
		return
	}
	snap.Items = persisted
	data, err := encodeTranscriptSnapshot(snap)
	if err != nil {
		log.Printf("WARN: failed to encode transcript snapshot: %v", err)
		return
	}
	if len(data) > maxBytes {
		if !clearOnOversize {
			log.Printf("WARN: transcript snapshot %d bytes exceeds cap %d — keeping the previously stored snapshot", len(data), maxBytes)
			return
		}
		// Oversized snapshots are cleared, not kept stale: resume falls back
		// to the durable tail, and the next post-compaction turn (smaller
		// transcript) persists again.
		log.Printf("WARN: transcript snapshot %d bytes exceeds cap %d — clearing stored snapshot", len(data), maxBytes)
		if err := sc.ClearTranscriptBlob(ctx); err != nil {
			log.Printf("WARN: failed to clear oversized transcript snapshot: %v", err)
		}
		return
	}
	if err := sc.SaveTranscriptBlob(ctx, data, len(persisted)); err != nil {
		log.Printf("WARN: failed to persist transcript snapshot: %v", err)
	}
}

// restoredTranscript carries a rehydrated transcript plus the loop
// watermarks captured when it was persisted.
type restoredTranscript struct {
	Items                  []agent.RunItem
	FloorMessageID         int64
	SeenMessageID          int64
	SelfAssistantMessageID int64
	// PendingUserMessageID is the user message that started the interrupted
	// turn when the snapshot was flushed mid-turn (zero for turn-end
	// snapshots) — see transcriptSnapshot.PendingUserMessageID.
	PendingUserMessageID int64
}

// loadTranscriptSnapshot rehydrates the persisted transcript on pod start.
// Returns nil (durable-tail fallback) when no snapshot exists, it fails to
// decode, or the durable history floor moved while the pod was down (the
// context was cleared externally — the snapshot is stale). Stale or
// undecodable snapshots are deleted so they are not retried forever.
func loadTranscriptSnapshot(ctx context.Context, sc *sessionclient.Client, currentFloorMessageID int64) *restoredTranscript {
	data, err := sc.LoadTranscriptBlob(ctx)
	if err != nil {
		log.Printf("WARN: failed to load transcript snapshot: %v", err)
		return nil
	}
	if len(data) == 0 {
		return nil
	}
	discard := func(reason string) {
		log.Printf("Transcript snapshot discarded (%s) — falling back to durable tail", reason)
		if err := sc.ClearTranscriptBlob(ctx); err != nil {
			log.Printf("WARN: failed to clear stale transcript snapshot: %v", err)
		}
	}
	snap, err := decodeTranscriptSnapshot(data)
	if err != nil {
		discard(err.Error())
		return nil
	}
	if snap.FloorMessageID != currentFloorMessageID {
		discard(fmt.Sprintf("history floor moved %d → %d while pod was down", snap.FloorMessageID, currentFloorMessageID))
		return nil
	}
	items, ok := runItemsFromPersisted(snap.Items)
	if !ok {
		discard("snapshot contains an unknown item type")
		return nil
	}
	if len(items) == 0 {
		return nil
	}
	return &restoredTranscript{
		Items:                  items,
		FloorMessageID:         snap.FloorMessageID,
		SeenMessageID:          snap.SeenMessageID,
		SelfAssistantMessageID: snap.SelfAssistantMessageID,
		PendingUserMessageID:   snap.PendingUserMessageID,
	}
}

// podTerminationFlushTimeout bounds the transcript/working-state flush when
// the pod is being terminated. It leaves room for the object-storage workspace
// checkpoint finalizer inside the pod's 60s termination grace.
const podTerminationFlushTimeout = 15 * time.Second

// flushPodTerminationState persists the in-flight turn's accumulated
// conversation before the pod dies (pause/wake pod deletion, node drain).
// The run context is already cancelled, so writes use a detached deadline
// sized to fit the kubelet's termination grace period.
//
// The SDK hands the interrupted turn's run state back as a partial result
// alongside the cancellation error; persisting its transcript here means a
// paused run resumes with the interrupted turn's full context instead of
// amnesia — the same contract the turn-budget-exhausted path honors.
// pendingUserMessageID (the durable user message that started the
// interrupted turn) rides along in the snapshot: the resume cursor
// re-delivers that message to the replacement pod, which must not replay
// its prompt verbatim on top of the preserved partial progress. A nil or
// empty partial (older SDK, cancellation before the first turn completed,
// or an interrupted result with an unresolved approval) degrades to the
// previous behavior: the snapshot from the last completed turn stays
// untouched — as does an oversized partial, which keeps the prior snapshot
// instead of clearing it.
func flushPodTerminationState(sc *sessionclient.Client, result *agent.RunResult, floorMessageID, seenMessageID, selfAssistantMessageID, pendingUserMessageID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), podTerminationFlushTimeout)
	defer cancel()
	if transcript := transcriptAfterRun(result); len(transcript) > 0 {
		if turnSummary := buildAssistantTurnSummary(result.NewItems); turnSummary != "" {
			if err := sc.UpdateWorkingState(ctx, func(state *sessionclient.WorkingState) error {
				state.LastAssistantSummary = turnSummary
				state.RecentTurnSummaries = append(state.RecentTurnSummaries, turnSummary)
				return nil
			}); err != nil {
				log.Printf("WARN: failed to persist working state during pod termination: %v", err)
			}
		}
		persistInFlightTranscriptSnapshot(ctx, sc, transcript, floorMessageID, seenMessageID, selfAssistantMessageID, pendingUserMessageID)
		log.Printf("Pod terminating mid-turn — preserved %d transcript items for resume", len(transcript))
	}
	_ = sc.WriteActivity(ctx, "pod_terminating", "Pod terminating; flushing run state", nil)
}

// podResumeContinuationPrompt opens a resumed turn when a restarted pod
// re-receives the user message that started an interrupted turn (see
// transcriptSnapshot.PendingUserMessageID). The original prompt and the
// partial progress it triggered are already in the replayed transcript, so
// repeating the prompt verbatim would duplicate it after that work.
const podResumeContinuationPrompt = "[SYSTEM] The runtime restarted mid-turn (pod pause/restart). The conversation above already contains the user's request and the partial progress made before the restart. Continue that work from where it left off — do not start over, and do not ask the user to repeat the request."
