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
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maintainerSemanticCursorDataKey  = "state.json"
	maintainerSemanticLatestHandle   = "latest"
	maintainerSemanticOpaquePrefix   = "mh2_"
	maintainerSemanticHandleLifetime = 30 * 24 * time.Hour

	maintainerSemanticWatchReconnectAttempts = 5
	maintainerSemanticWatchReconnectBackoff  = 200 * time.Millisecond
)

type maintainerSemanticCursor struct {
	Version    int              `json:"version"`
	Sequences  map[string]int64 `json:"projection_sequences"`
	Identities map[string]int32 `json:"work_item_identities,omitempty"`
}

// maintainerSemanticCursorState is stored compactly in an AgentRun-owned
// Secret. Each work-item name appears once so large repositories remain well
// below Kubernetes' object-size limit.
type maintainerSemanticCursorState struct {
	Version       int                             `json:"v"`
	RunUID        types.UID                       `json:"r"`
	RepositoryUID types.UID                       `json:"p"`
	Handle        string                          `json:"h"`
	ExpiresAt     time.Time                       `json:"e"`
	Entries       []maintainerSemanticCursorEntry `json:"i"`
}

type maintainerSemanticCursorEntry struct {
	Name        string `json:"n"`
	Sequence    int64  `json:"s"`
	IssueNumber int32  `json:"i"`
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
	checkpoint, err := t.semanticCursorCheckpoint(ctx, run.UID, repository.UID)
	if err != nil {
		return Result{Content: "failed to read semantic cursor checkpoint: " + err.Error(), IsError: true}, nil
	}
	cursorValue := strings.TrimSpace(in.Cursor)
	cursorProvided := cursorValue != ""
	previous, err := resolveSemanticWaitCursor(checkpoint, run, repository.UID, cursorValue, time.Now())
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	expectedState := ""
	if checkpoint != nil {
		expectedState = checkpoint.ResourceVersion
	}
	snapshot, watcher, err := t.workItemSnapshotAndWatch(ctx)
	if err != nil {
		return Result{Content: "failed to establish semantic work-item snapshot/watch: " + err.Error(), IsError: true}, nil
	}
	defer func() { watcher.Stop() }()
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
	waitCtx, cancelWait := context.WithDeadline(ctx, started.Add(time.Duration(timeout)*time.Second))
	defer cancelWait()
	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return t.semanticWaitResult(ctx, run.UID, repository.UID, expectedState, nil, current, identities, false, true, started)
		case event, ok := <-watcher.ResultChan():
			if !ok || event.Type == watch.Error {
				watchError := "semantic work-item watch closed"
				if ok {
					watchError = "semantic work-item watch reported an error"
				}
				watcher.Stop()
				fresh, reconnected, err := t.reconnectWorkItemSnapshotAndWatch(waitCtx)
				if err != nil {
					if ctx.Err() != nil {
						return Result{}, ctx.Err()
					}
					if waitCtx.Err() != nil {
						return t.semanticWaitResult(ctx, run.UID, repository.UID, expectedState, nil, current, identities, false, true, started)
					}
					return t.semanticWatchReconnectResult(ctx, run.UID, repository.UID, expectedState, current, identities, started, watchError+": "+err.Error())
				}
				watcher = reconnected
				if err := validateSemanticWorkItemIdentities(t.repositoryName, fresh.workItems); err != nil {
					return Result{Content: err.Error(), IsError: true}, nil
				}
				changes := semanticSnapshotChanges(current, fresh.workItems, false)
				current = semanticSequences(fresh.workItems)
				identities = semanticIdentities(fresh.workItems)
				if len(changes) > 0 {
					return t.semanticWaitResult(ctx, run.UID, repository.UID, expectedState, changes, current, identities, true, false, started)
				}
				continue
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

// reconnectWorkItemSnapshotAndWatch re-lists and re-watches after the API
// server closed the watch. Idle closes are routine, so they are absorbed here
// instead of surfacing as a reconnect_required turn for the model.
func (t *waitForRepoEventsTool) reconnectWorkItemSnapshotAndWatch(ctx context.Context) (maintainerRepoEventsSnapshot, watch.Interface, error) {
	var lastErr error
	for attempt := 0; attempt < maintainerSemanticWatchReconnectAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return maintainerRepoEventsSnapshot{}, nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * maintainerSemanticWatchReconnectBackoff):
			}
		}
		snapshot, watcher, err := t.workItemSnapshotAndWatch(ctx)
		if err == nil {
			return snapshot, watcher, nil
		}
		lastErr = err
	}
	return maintainerRepoEventsSnapshot{}, nil, fmt.Errorf("reconnect failed after %d attempts: %w", maintainerSemanticWatchReconnectAttempts, lastErr)
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
	if err := t.persistMaintainerSemanticCursorState(ctx, runUID, repositoryUID, expectedState, semanticCursor); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	elapsed := 0
	if !started.IsZero() {
		elapsed = int(time.Since(started).Seconds())
	}
	output := maintainerSemanticWaitOutput{Changed: changed, TimedOut: timedOut, ElapsedSeconds: elapsed, MigrationMode: string(triggersv1alpha1.MaintainerWorkItemCutoverController), WorkItems: changes, CursorHandle: maintainerSemanticLatestHandle}
	encoded, err := json.Marshal(output)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

func resolveSemanticWaitCursor(checkpoint *corev1.Secret, run *platformv1alpha1.AgentRun, repositoryUID types.UID, value string, now time.Time) (maintainerSemanticCursor, error) {
	if value == "" {
		return maintainerSemanticCursor{Version: 2, Sequences: map[string]int64{}}, nil
	}
	if value == maintainerSemanticLatestHandle || strings.HasPrefix(value, maintainerSemanticOpaquePrefix) {
		state, err := resolveMaintainerSemanticCursorState(checkpoint, run, repositoryUID, value, now)
		if err != nil {
			return maintainerSemanticCursor{}, err
		}
		return state.semanticCursor(), nil
	}
	cursor, err := decodeMaintainerSemanticCursor(value)
	if err != nil {
		return maintainerSemanticCursor{}, fmt.Errorf("invalid semantic cursor: %w", err)
	}
	// Compatibility cursors may initialize latest, but must not overwrite a
	// malformed, expired, or cross-boundary checkpoint.
	if checkpoint != nil && len(checkpoint.Data[maintainerSemanticCursorDataKey]) > 0 {
		if _, err := resolveMaintainerSemanticCursorState(checkpoint, run, repositoryUID, maintainerSemanticLatestHandle, now); err != nil {
			return maintainerSemanticCursor{}, err
		}
	}
	return cursor, nil
}

func (t *waitForRepoEventsTool) semanticCursorCheckpoint(ctx context.Context, runUID, repositoryUID types.UID) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: t.currentRunNamespace, Name: triggersv1alpha1.MaintainerSemanticCursorSecretName(runUID, repositoryUID)}
	if err := t.k8sClient.Get(ctx, key, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if !semanticCursorSecretOwnedByRun(secret, runUID) {
		return nil, fmt.Errorf("cross-boundary semantic cursor checkpoint ownership")
	}
	return secret, nil
}

func semanticCursorSecretOwnedByRun(secret *corev1.Secret, runUID types.UID) bool {
	if secret == nil {
		return false
	}
	for _, owner := range secret.OwnerReferences {
		if owner.APIVersion == platformv1alpha1.GroupVersion.String() && owner.Kind == "AgentRun" && owner.UID == runUID {
			return true
		}
	}
	return false
}

func resolveMaintainerSemanticCursorState(secret *corev1.Secret, run *platformv1alpha1.AgentRun, repositoryUID types.UID, handle string, now time.Time) (maintainerSemanticCursorState, error) {
	if secret == nil || len(secret.Data[maintainerSemanticCursorDataKey]) == 0 {
		return maintainerSemanticCursorState{}, fmt.Errorf("unknown semantic cursor handle: no latest cursor is established; call once without cursor")
	}
	if run == nil || !semanticCursorSecretOwnedByRun(secret, run.UID) {
		return maintainerSemanticCursorState{}, fmt.Errorf("cross-boundary semantic cursor handle")
	}
	var state maintainerSemanticCursorState
	if err := json.Unmarshal(secret.Data[maintainerSemanticCursorDataKey], &state); err != nil || state.Version != 1 {
		return maintainerSemanticCursorState{}, fmt.Errorf("malformed stored semantic cursor state")
	}
	if err := validateMaintainerSemanticCursorState(state); err != nil {
		return maintainerSemanticCursorState{}, err
	}
	if state.RunUID != run.UID || state.RepositoryUID != repositoryUID {
		return maintainerSemanticCursorState{}, fmt.Errorf("cross-boundary semantic cursor handle")
	}
	if !state.ExpiresAt.After(now) {
		return maintainerSemanticCursorState{}, fmt.Errorf("expired semantic cursor handle; call once without cursor")
	}
	if handle != maintainerSemanticLatestHandle {
		if err := validateMaintainerSemanticHandle(handle); err != nil {
			return maintainerSemanticCursorState{}, err
		}
		if handle != state.Handle {
			return maintainerSemanticCursorState{}, fmt.Errorf("stale semantic cursor handle; use %q", maintainerSemanticLatestHandle)
		}
	}
	return state, nil
}

func validateMaintainerSemanticCursorState(state maintainerSemanticCursorState) error {
	if err := validateMaintainerSemanticHandle(state.Handle); err != nil {
		return fmt.Errorf("malformed stored semantic cursor state")
	}
	seen := make(map[string]struct{}, len(state.Entries))
	for _, entry := range state.Entries {
		if strings.TrimSpace(entry.Name) == "" || entry.Sequence < 0 || entry.IssueNumber <= 0 {
			return fmt.Errorf("malformed stored semantic cursor state")
		}
		if _, exists := seen[entry.Name]; exists {
			return fmt.Errorf("malformed stored semantic cursor state")
		}
		seen[entry.Name] = struct{}{}
	}
	return nil
}

func validateMaintainerSemanticHandle(handle string) error {
	if !strings.HasPrefix(handle, maintainerSemanticOpaquePrefix) {
		return fmt.Errorf("unknown semantic cursor handle")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handle, maintainerSemanticOpaquePrefix))
	if err != nil || len(raw) != 16 {
		return fmt.Errorf("malformed semantic cursor handle")
	}
	return nil
}

func (state maintainerSemanticCursorState) semanticCursor() maintainerSemanticCursor {
	cursor := maintainerSemanticCursor{Version: 2, Sequences: make(map[string]int64, len(state.Entries)), Identities: make(map[string]int32, len(state.Entries))}
	for _, entry := range state.Entries {
		cursor.Sequences[entry.Name] = entry.Sequence
		cursor.Identities[entry.Name] = entry.IssueNumber
	}
	return cursor
}

func newMaintainerSemanticCursorState(runUID, repositoryUID types.UID, handle string, expiresAt time.Time, cursor maintainerSemanticCursor) maintainerSemanticCursorState {
	names := make([]string, 0, len(cursor.Sequences))
	for name := range cursor.Sequences {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]maintainerSemanticCursorEntry, 0, len(names))
	for _, name := range names {
		entries = append(entries, maintainerSemanticCursorEntry{Name: name, Sequence: cursor.Sequences[name], IssueNumber: cursor.Identities[name]})
	}
	return maintainerSemanticCursorState{Version: 1, RunUID: runUID, RepositoryUID: repositoryUID, Handle: handle, ExpiresAt: expiresAt, Entries: entries}
}

func (t *waitForRepoEventsTool) persistMaintainerSemanticCursorState(ctx context.Context, runUID, repositoryUID types.UID, expectedState string, cursor maintainerSemanticCursor) error {
	handleBytes := make([]byte, 16)
	if _, err := rand.Read(handleBytes); err != nil {
		return fmt.Errorf("failed to create semantic cursor handle: %w", err)
	}
	handle := maintainerSemanticOpaquePrefix + base64.RawURLEncoding.EncodeToString(handleBytes)
	state := newMaintainerSemanticCursorState(runUID, repositoryUID, handle, time.Now().Add(maintainerSemanticHandleLifetime), cursor)
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to encode semantic cursor state: %w", err)
	}
	run, err := t.verifySemanticCursorBoundary(ctx, runUID, repositoryUID)
	if err != nil {
		return err
	}
	return t.writeSemanticCursorCheckpoint(ctx, run, repositoryUID, expectedState, encoded)
}

func (t *waitForRepoEventsTool) verifySemanticCursorBoundary(ctx context.Context, runUID, repositoryUID types.UID) (*platformv1alpha1.AgentRun, error) {
	repository, err := t.repository(ctx)
	if err != nil {
		return nil, err
	}
	if repository.UID != repositoryUID {
		return nil, fmt.Errorf("cross-boundary semantic cursor state: maintained repository UID changed")
	}
	run, err := t.currentRunForRepository(ctx, repository)
	if err != nil {
		return nil, err
	}
	if run.UID != runUID {
		return nil, fmt.Errorf("cross-boundary semantic cursor state: maintainer AgentRun UID changed")
	}
	return run, nil
}

func (t *waitForRepoEventsTool) writeSemanticCursorCheckpoint(ctx context.Context, run *platformv1alpha1.AgentRun, repositoryUID types.UID, expectedState string, encoded []byte) error {
	checkpoint, err := t.semanticCursorCheckpoint(ctx, run.UID, repositoryUID)
	if err != nil {
		return fmt.Errorf("failed to read latest semantic cursor checkpoint: %w", err)
	}
	if checkpoint == nil {
		return fmt.Errorf("semantic cursor checkpoint is not provisioned for this maintainer run")
	}
	if expectedState == "" || checkpoint.ResourceVersion != expectedState {
		return fmt.Errorf("stale semantic cursor handle: another wait already advanced latest")
	}
	checkpoint.Data = map[string][]byte{maintainerSemanticCursorDataKey: encoded}
	if err := t.k8sClient.Update(ctx, checkpoint); err != nil {
		if apierrors.IsConflict(err) {
			return fmt.Errorf("stale semantic cursor handle: another wait already advanced latest")
		}
		return fmt.Errorf("failed to update semantic cursor checkpoint: %w", err)
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
		if !legacySemanticCursorIdentityCanonical(repositoryName, name) {
			return fmt.Errorf("non-canonical cursor identity: %q", name)
		}
	}
	return nil
}

func legacySemanticCursorIdentityCanonical(repositoryName, name string) bool {
	const hashLength = 10
	separator := strings.LastIndexByte(name, '-')
	if !strings.HasPrefix(name, "mwi-") || len(name) > 63 || separator <= 4 || len(name)-separator-1 != hashLength || !lowerHex(name[separator+1:]) {
		return false
	}
	for part := range strings.SplitSeq(name[:separator], "-") {
		issueNumber, err := strconv.ParseInt(part, 10, 32)
		if err == nil && issueNumber > 0 && triggersv1alpha1.MaintainerWorkItemName(repositoryName, int32(issueNumber)) == name {
			return true
		}
	}
	probe := triggersv1alpha1.MaintainerWorkItemName(repositoryName, 1)
	probeSeparator := strings.LastIndexByte(probe, '-')
	probeBase := probe[:probeSeparator]
	candidateBase := name[:separator]
	if candidateBase == probeBase {
		return true
	}
	if !strings.HasSuffix(probeBase, "-1") {
		return false
	}
	repositoryBase := strings.TrimSuffix(probeBase, "-1")
	if candidateBase == repositoryBase {
		return true
	}
	partialIssue := strings.TrimPrefix(candidateBase, repositoryBase+"-")
	return partialIssue != candidateBase && partialIssue != "" && decimalDigits(partialIssue)
}

func lowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func decimalDigits(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
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
