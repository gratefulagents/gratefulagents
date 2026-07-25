package tools

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maintainerSemanticCursorAnnotation = "platform.gratefulagents.dev/maintainer-semantic-cursor"
	maintainerSemanticLatestHandle     = "latest"
	maintainerSemanticOpaquePrefix     = "mh2_"
	maintainerSemanticHandleLifetime   = 30 * 24 * time.Hour
)

type maintainerSemanticCursor struct {
	Version    int              `json:"version"`
	Sequences  map[string]int64 `json:"projection_sequences"`
	Identities map[string]int32 `json:"work_item_identities,omitempty"`
}

// maintainerSemanticCursorState is persisted on the maintainer AgentRun. The
// owner reference and these immutable UIDs make reconstruction safe without
// introducing process-local cursor continuity.
type maintainerSemanticCursorState struct {
	Version       int                      `json:"version"`
	RunUID        types.UID                `json:"run_uid"`
	RepositoryUID types.UID                `json:"repository_uid"`
	Handle        string                   `json:"handle"`
	ExpiresAt     time.Time                `json:"expires_at"`
	Cursor        maintainerSemanticCursor `json:"cursor"`
}

type maintainerSemanticWaitOutput struct {
	Changed           bool                          `json:"changed"`
	TimedOut          bool                          `json:"timed_out"`
	ElapsedSeconds    int                           `json:"elapsed_seconds"`
	MigrationMode     string                        `json:"migration_mode"`
	WorkItems         []maintainerRepoWorkItemEvent `json:"work_item_changes"`
	ReconnectRequired bool                          `json:"reconnect_required,omitempty"`
	WatchError        string                        `json:"watch_error,omitempty"`
	CursorHandle      string                        `json:"cursor_handle"`
	Cursor            string                        `json:"cursor"` // Deprecated: encoded v2 compatibility cursor.
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
	repository, err := t.repository(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	run, err := t.currentRunForRepository(ctx, repository)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	previous := maintainerSemanticCursor{Version: 2, Sequences: map[string]int64{}}
	cursorValue := strings.TrimSpace(in.Cursor)
	cursorProvided := cursorValue != ""
	expectedState := ""
	if run.Annotations != nil {
		expectedState = run.Annotations[maintainerSemanticCursorAnnotation]
	}
	if cursorProvided {
		switch {
		case cursorValue == maintainerSemanticLatestHandle || strings.HasPrefix(cursorValue, maintainerSemanticOpaquePrefix):
			state, resolveErr := resolveMaintainerSemanticCursorState(run, repository.UID, cursorValue, time.Now())
			if resolveErr != nil {
				return Result{Content: resolveErr.Error(), IsError: true}, nil
			}
			previous = state.Cursor
		default:
			decoded, decodeErr := decodeMaintainerSemanticCursor(cursorValue)
			if decodeErr != nil {
				return Result{Content: "invalid semantic cursor: " + decodeErr.Error(), IsError: true}, nil
			}
			previous = decoded
			// Compatibility cursors may initialize latest, but must not overwrite a
			// checkpoint that another concurrent wait advances.
			if run.Annotations != nil && strings.TrimSpace(run.Annotations[maintainerSemanticCursorAnnotation]) != "" {
				_, resolveErr := resolveMaintainerSemanticCursorState(run, repository.UID, maintainerSemanticLatestHandle, time.Now())
				if resolveErr != nil {
					return Result{Content: resolveErr.Error(), IsError: true}, nil
				}
			}
		}
	}
	snapshot, watcher, err := t.workItemSnapshotAndWatch(ctx)
	if err != nil {
		return Result{Content: "failed to establish semantic work-item snapshot/watch: " + err.Error(), IsError: true}, nil
	}
	defer watcher.Stop()
	if err := validateSemanticWorkItemIdentities(t.repositoryName, snapshot.workItems); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := validateSemanticCursorIdentities(t.repositoryName, previous, snapshot.workItems); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	current := semanticSequences(snapshot.workItems)
	identities := semanticIdentities(snapshot.workItems)
	changes := semanticSnapshotChanges(previous.Sequences, snapshot.workItems, !cursorProvided)
	if len(changes) > 0 || !cursorProvided {
		return t.semanticWaitResult(ctx, run.UID, repository.UID, expectedState, changes, current, identities, true, false, time.Time{})
	}
	started := time.Now()
	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-timer.C:
			return t.semanticWaitResult(ctx, run.UID, repository.UID, expectedState, nil, current, identities, false, true, started)
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return t.semanticWatchReconnectResult(ctx, run.UID, repository.UID, expectedState, current, identities, started, "semantic work-item watch closed")
			}
			if event.Type == watch.Error {
				return t.semanticWatchReconnectResult(ctx, run.UID, repository.UID, expectedState, current, identities, started, "semantic work-item watch reported an error")
			}
			item, ok := event.Object.(*triggersv1alpha1.MaintainerWorkItem)
			if !ok || item.Spec.RepositoryRef.Name != t.repositoryName {
				continue
			}
			if item.Name != triggersv1alpha1.MaintainerWorkItemName(t.repositoryName, item.Spec.IssueNumber) {
				return Result{Content: fmt.Sprintf("non-canonical cursor identity: work item %q", item.Name), IsError: true}, nil
			}
			if event.Type == watch.Deleted {
				if _, known := current[item.Name]; !known {
					continue
				}
				delete(current, item.Name)
				delete(identities, item.Name)
				change := maintainerWorkItemEvent(item)
				change.Removed = true
				return t.semanticWaitResult(ctx, run.UID, repository.UID, expectedState, []maintainerRepoWorkItemEvent{change}, current, identities, true, false, started)
			}
			if sequence, known := current[item.Name]; known && sequence == item.Status.ProjectionSequence {
				continue
			}
			current[item.Name] = item.Status.ProjectionSequence
			identities[item.Name] = item.Spec.IssueNumber
			return t.semanticWaitResult(ctx, run.UID, repository.UID, expectedState, []maintainerRepoWorkItemEvent{maintainerWorkItemEvent(item)}, current, identities, true, false, started)
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

func semanticIdentities(items map[string]maintainerRepoWorkItemEvent) map[string]int32 {
	identities := make(map[string]int32, len(items))
	for name, item := range items {
		identities[name] = item.IssueNumber
	}
	return identities
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

func (t *waitForRepoEventsTool) semanticWatchReconnectResult(ctx context.Context, runUID, repositoryUID types.UID, expectedState string, sequences map[string]int64, identities map[string]int32, started time.Time, watchError string) (Result, error) {
	result, err := t.semanticWaitResult(ctx, runUID, repositoryUID, expectedState, nil, sequences, identities, false, false, started)
	if err != nil || result.IsError {
		return result, err
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

func (t *waitForRepoEventsTool) semanticWaitResult(ctx context.Context, runUID, repositoryUID types.UID, expectedState string, changes []maintainerRepoWorkItemEvent, sequences map[string]int64, identities map[string]int32, changed, timedOut bool, started time.Time) (Result, error) {
	semanticCursor := maintainerSemanticCursor{Version: 2, Sequences: sequences, Identities: identities}
	cursor, err := encodeMaintainerSemanticCursor(semanticCursor)
	if err != nil {
		return Result{}, err
	}
	if err := t.persistMaintainerSemanticCursorState(ctx, runUID, repositoryUID, expectedState, semanticCursor); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	elapsed := 0
	if !started.IsZero() {
		elapsed = int(time.Since(started).Seconds())
	}
	output := maintainerSemanticWaitOutput{Changed: changed, TimedOut: timedOut, ElapsedSeconds: elapsed, MigrationMode: string(triggersv1alpha1.MaintainerWorkItemCutoverController), WorkItems: changes, CursorHandle: maintainerSemanticLatestHandle, Cursor: cursor}
	encoded, err := json.Marshal(output)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

func resolveMaintainerSemanticCursorState(run *platformv1alpha1.AgentRun, repositoryUID types.UID, handle string, now time.Time) (maintainerSemanticCursorState, error) {
	if run == nil || run.Annotations == nil || strings.TrimSpace(run.Annotations[maintainerSemanticCursorAnnotation]) == "" {
		return maintainerSemanticCursorState{}, fmt.Errorf("unknown semantic cursor handle: no latest cursor is established; call once without cursor")
	}
	var state maintainerSemanticCursorState
	if err := json.Unmarshal([]byte(run.Annotations[maintainerSemanticCursorAnnotation]), &state); err != nil || state.Version != 1 {
		return maintainerSemanticCursorState{}, fmt.Errorf("malformed stored semantic cursor state")
	}
	storedHandle, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(state.Handle, maintainerSemanticOpaquePrefix))
	if !strings.HasPrefix(state.Handle, maintainerSemanticOpaquePrefix) || err != nil || len(storedHandle) != 16 || state.Cursor.Version != 2 || state.Cursor.Sequences == nil {
		return maintainerSemanticCursorState{}, fmt.Errorf("malformed stored semantic cursor state")
	}
	for name, sequence := range state.Cursor.Sequences {
		if strings.TrimSpace(name) == "" || sequence < 0 {
			return maintainerSemanticCursorState{}, fmt.Errorf("malformed stored semantic cursor state")
		}
		if issueNumber, ok := state.Cursor.Identities[name]; ok && issueNumber <= 0 {
			return maintainerSemanticCursorState{}, fmt.Errorf("malformed stored semantic cursor state")
		}
	}
	for name := range state.Cursor.Identities {
		if _, ok := state.Cursor.Sequences[name]; !ok {
			return maintainerSemanticCursorState{}, fmt.Errorf("malformed stored semantic cursor state")
		}
	}
	if state.RunUID != run.UID || state.RepositoryUID != repositoryUID {
		return maintainerSemanticCursorState{}, fmt.Errorf("cross-boundary semantic cursor handle")
	}
	if !state.ExpiresAt.After(now) {
		return maintainerSemanticCursorState{}, fmt.Errorf("expired semantic cursor handle; call once without cursor")
	}
	if handle != maintainerSemanticLatestHandle {
		if !strings.HasPrefix(handle, maintainerSemanticOpaquePrefix) {
			return maintainerSemanticCursorState{}, fmt.Errorf("unknown semantic cursor handle")
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handle, maintainerSemanticOpaquePrefix))
		if err != nil || len(raw) != 16 {
			return maintainerSemanticCursorState{}, fmt.Errorf("malformed semantic cursor handle")
		}
		if handle != state.Handle {
			return maintainerSemanticCursorState{}, fmt.Errorf("stale semantic cursor handle; use %q", maintainerSemanticLatestHandle)
		}
	}
	return state, nil
}

func (t *waitForRepoEventsTool) persistMaintainerSemanticCursorState(ctx context.Context, runUID, repositoryUID types.UID, expectedState string, cursor maintainerSemanticCursor) error {
	handleBytes := make([]byte, 16)
	if _, err := rand.Read(handleBytes); err != nil {
		return fmt.Errorf("failed to create semantic cursor handle: %w", err)
	}
	state := maintainerSemanticCursorState{
		Version: 1, RunUID: runUID, RepositoryUID: repositoryUID,
		Handle:    maintainerSemanticOpaquePrefix + base64.RawURLEncoding.EncodeToString(handleBytes),
		ExpiresAt: time.Now().Add(maintainerSemanticHandleLifetime), Cursor: cursor,
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to encode semantic cursor state: %w", err)
	}
	key := client.ObjectKey{Name: t.currentRunName, Namespace: t.currentRunNamespace}
	repositoryKey := client.ObjectKey{Name: t.repositoryName, Namespace: t.repositoryNamespace}
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		repository := &triggersv1alpha1.GitHubRepository{}
		if getErr := t.k8sClient.Get(ctx, repositoryKey, repository); getErr != nil {
			return getErr
		}
		if repository.UID != repositoryUID || !repository.DeletionTimestamp.IsZero() {
			return fmt.Errorf("cross-boundary semantic cursor state: maintained repository UID changed")
		}
		run := &platformv1alpha1.AgentRun{}
		if getErr := t.k8sClient.Get(ctx, key, run); getErr != nil {
			return getErr
		}
		if run.UID != runUID || !maintainerFleetRunOwnedByRepository(run, repository) {
			return fmt.Errorf("cross-boundary semantic cursor state: maintainer AgentRun ownership changed")
		}
		currentState := ""
		if run.Annotations != nil {
			currentState = run.Annotations[maintainerSemanticCursorAnnotation]
		}
		if currentState != expectedState {
			return fmt.Errorf("stale semantic cursor handle: another wait already advanced latest")
		}
		before := run.DeepCopy()
		if run.Annotations == nil {
			run.Annotations = map[string]string{}
		}
		run.Annotations[maintainerSemanticCursorAnnotation] = string(encoded)
		return t.k8sClient.Patch(ctx, run, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
	})
	if err != nil {
		return fmt.Errorf("failed to atomically persist latest semantic cursor: %w", err)
	}
	return nil
}

func validateSemanticWorkItemIdentities(repositoryName string, items map[string]maintainerRepoWorkItemEvent) error {
	for name, item := range items {
		if name != triggersv1alpha1.MaintainerWorkItemName(repositoryName, item.IssueNumber) {
			return fmt.Errorf("non-canonical cursor identity: work item %q", name)
		}
	}
	return nil
}

func validateSemanticCursorIdentities(repositoryName string, cursor maintainerSemanticCursor, current map[string]maintainerRepoWorkItemEvent) error {
	for name := range cursor.Sequences {
		if _, ok := current[name]; ok {
			continue
		}
		if issueNumber, ok := cursor.Identities[name]; ok {
			if issueNumber > 0 && triggersv1alpha1.MaintainerWorkItemName(repositoryName, issueNumber) == name {
				continue
			}
			return fmt.Errorf("non-canonical cursor identity: %q", name)
		}
		canonical := false
		for _, part := range strings.Split(name, "-") {
			issueNumber, err := strconv.ParseInt(part, 10, 32)
			if err == nil && issueNumber > 0 && triggersv1alpha1.MaintainerWorkItemName(repositoryName, int32(issueNumber)) == name {
				canonical = true
				break
			}
		}
		if !canonical {
			return fmt.Errorf("non-canonical cursor identity: %q", name)
		}
	}
	return nil
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
