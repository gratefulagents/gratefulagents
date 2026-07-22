package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"k8s.io/apimachinery/pkg/watch"
)

type maintainerSemanticCursor struct {
	Version   int              `json:"version"`
	Sequences map[string]int64 `json:"projection_sequences"`
}

type maintainerSemanticWaitOutput struct {
	Changed           bool                          `json:"changed"`
	TimedOut          bool                          `json:"timed_out"`
	ElapsedSeconds    int                           `json:"elapsed_seconds"`
	MigrationMode     string                        `json:"migration_mode"`
	WorkItems         []maintainerRepoWorkItemEvent `json:"work_item_changes"`
	ReconnectRequired bool                          `json:"reconnect_required,omitempty"`
	WatchError        string                        `json:"watch_error,omitempty"`
	Cursor            string                        `json:"cursor"`
}

// executeSemanticWorkItemWait implements waiter v2. The durable source of truth
// is each MaintainerWorkItem's controller-owned ProjectionSequence; the cursor
// only acknowledges those persisted sequences and contains no model-computed
// signatures. A list and watch from the list resourceVersion closes the race.
func (t *waitForRepoEventsTool) executeSemanticWorkItemWait(ctx context.Context, input json.RawMessage) (Result, error) {
	var in waitForRepoEventsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	timeout := in.TimeoutSeconds
	if timeout == 0 {
		timeout = defaultRepoEventsTimeout
	}
	if timeout < minRepoEventsTimeout || timeout > maxRepoEventsTimeout {
		return Result{Content: "timeout_seconds must be between 30 and 21600", IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	previous := maintainerSemanticCursor{Version: 2, Sequences: map[string]int64{}}
	cursorProvided := strings.TrimSpace(in.Cursor) != ""
	if cursorProvided {
		decoded, err := decodeMaintainerSemanticCursor(in.Cursor)
		if err != nil {
			return Result{Content: "invalid semantic cursor: " + err.Error(), IsError: true}, nil
		}
		previous = decoded
	}
	snapshot, watcher, err := t.workItemSnapshotAndWatch(ctx)
	if err != nil {
		return Result{Content: "failed to establish semantic work-item snapshot/watch: " + err.Error(), IsError: true}, nil
	}
	defer watcher.Stop()
	current := semanticSequences(snapshot.workItems)
	changes := semanticSnapshotChanges(previous.Sequences, snapshot.workItems, !cursorProvided)
	if len(changes) > 0 || !cursorProvided {
		return semanticWaitResult(changes, current, true, false, time.Time{})
	}
	started := time.Now()
	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-timer.C:
			return semanticWaitResult(nil, current, false, true, started)
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return semanticWatchReconnectResult(current, started, "semantic work-item watch closed")
			}
			if event.Type == watch.Error {
				return semanticWatchReconnectResult(current, started, "semantic work-item watch reported an error")
			}
			item, ok := event.Object.(*triggersv1alpha1.MaintainerWorkItem)
			if !ok || item.Spec.RepositoryRef.Name != t.repositoryName {
				continue
			}
			if event.Type == watch.Deleted {
				if _, known := current[item.Name]; !known {
					continue
				}
				delete(current, item.Name)
				change := maintainerWorkItemEvent(item)
				change.Removed = true
				return semanticWaitResult([]maintainerRepoWorkItemEvent{change}, current, true, false, started)
			}
			if sequence, known := current[item.Name]; known && sequence == item.Status.ProjectionSequence {
				continue
			}
			current[item.Name] = item.Status.ProjectionSequence
			return semanticWaitResult([]maintainerRepoWorkItemEvent{maintainerWorkItemEvent(item)}, current, true, false, started)
		}
	}
}

func semanticSequences(items map[string]maintainerRepoWorkItemEvent) map[string]int64 {
	sequences := make(map[string]int64, len(items))
	for name, item := range items {
		sequences[name] = item.ProjectionSequence
	}
	return sequences
}

func semanticSnapshotChanges(previous map[string]int64, current map[string]maintainerRepoWorkItemEvent, includeAll bool) []maintainerRepoWorkItemEvent {
	changes := make([]maintainerRepoWorkItemEvent, 0)
	for name, item := range current {
		sequence, known := previous[name]
		if includeAll || !known || sequence != item.ProjectionSequence {
			changes = append(changes, item)
		}
	}
	for name, sequence := range previous {
		if _, known := current[name]; known {
			continue
		}
		changes = append(changes, maintainerRepoWorkItemEvent{Name: name, ProjectionSequence: sequence, Removed: true})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	return changes
}

func semanticWatchReconnectResult(sequences map[string]int64, started time.Time, watchError string) (Result, error) {
	result, err := semanticWaitResult(nil, sequences, false, false, started)
	if err != nil {
		return Result{}, err
	}
	var output maintainerSemanticWaitOutput
	if err := json.Unmarshal([]byte(result.Content), &output); err != nil {
		return Result{}, err
	}
	output.ReconnectRequired = true
	output.WatchError = watchError
	encoded, err := json.Marshal(output)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

func semanticWaitResult(changes []maintainerRepoWorkItemEvent, sequences map[string]int64, changed, timedOut bool, started time.Time) (Result, error) {
	cursor, err := encodeMaintainerSemanticCursor(maintainerSemanticCursor{Version: 2, Sequences: sequences})
	if err != nil {
		return Result{}, err
	}
	elapsed := 0
	if !started.IsZero() {
		elapsed = int(time.Since(started).Seconds())
	}
	output := maintainerSemanticWaitOutput{Changed: changed, TimedOut: timedOut, ElapsedSeconds: elapsed, MigrationMode: string(triggersv1alpha1.MaintainerWorkItemCutoverController), WorkItems: changes, Cursor: cursor}
	encoded, err := json.Marshal(output)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

func encodeMaintainerSemanticCursor(cursor maintainerSemanticCursor) (string, error) {
	if cursor.Sequences == nil {
		cursor.Sequences = map[string]int64{}
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeMaintainerSemanticCursor(value string) (maintainerSemanticCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return maintainerSemanticCursor{}, err
	}
	var cursor maintainerSemanticCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return maintainerSemanticCursor{}, err
	}
	if cursor.Version != 2 || cursor.Sequences == nil {
		return maintainerSemanticCursor{}, fmt.Errorf("unsupported waiter cursor version")
	}
	for name, sequence := range cursor.Sequences {
		if strings.TrimSpace(name) == "" || sequence < 0 {
			return maintainerSemanticCursor{}, fmt.Errorf("invalid projection sequence cursor")
		}
	}
	return cursor, nil
}
