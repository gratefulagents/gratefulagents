package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

const (
	defaultRepoEventsTimeout        = 3600
	minRepoEventsTimeout            = 30
	maxRepoEventsTimeout            = 21600
	defaultBacklogPollInterval      = 60 * time.Second
	defaultFleetEventsPollInterval  = 15 * time.Second
	maintainerRepoEventsResultLimit = 24 * 1024
)

type waitForRepoEventsTool struct {
	maintainerToolBase
	runner              prReviewRunner
	backlogPollInterval time.Duration
	fleetPollInterval   time.Duration
}

type waitForRepoEventsInput struct {
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
}

type maintainerBacklogLabel struct {
	Name string `json:"name"`
}

type maintainerBacklogIssue struct {
	Number    int                      `json:"number"`
	Title     string                   `json:"title"`
	Labels    []maintainerBacklogLabel `json:"labels"`
	UpdatedAt string                   `json:"updatedAt"`
	URL       string                   `json:"url"`
}

type maintainerRepoEventIssue struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	UpdatedAt string   `json:"updated_at"`
	Labels    []string `json:"labels"`
}

type maintainerRepoFleetEvent struct {
	Name            string                         `json:"name"`
	Phase           platformv1alpha1.AgentRunPhase `json:"phase"`
	PullRequestURLs []string                       `json:"pull_request_urls"`
	BlockedReason   string                         `json:"blocked_reason,omitempty"`
	PendingInput    bool                           `json:"pending_input"`
}

type maintainerRepoEventsCursor struct {
	BacklogFingerprint string            `json:"backlog_fingerprint"`
	IssueSignatures    map[string]string `json:"issue_signatures"`
	FleetSignatures    map[string]string `json:"fleet_signatures"`
}

type maintainerRepoEventsSnapshot struct {
	backlogFingerprint string
	issueSignatures    map[string]string
	issues             []maintainerRepoEventIssue
	backlogAvailable   bool
	backlogError       string
	fleetSignatures    map[string]string
	fleet              map[string]maintainerRepoFleetEvent
}

type waitForRepoEventsOutput struct {
	Changed        bool                       `json:"changed"`
	TimedOut       bool                       `json:"timed_out"`
	ElapsedSeconds int                        `json:"elapsed_seconds"`
	BacklogChanged bool                       `json:"backlog_changed"`
	ChangedIssues  []maintainerRepoEventIssue `json:"changed_issues"`
	FleetChanges   []maintainerRepoFleetEvent `json:"fleet_changes"`
	Cursor         string                     `json:"cursor"`
	BacklogError   string                     `json:"backlog_error,omitempty"`
}

func (t *waitForRepoEventsTool) Name() string { return "wait_for_repo_events" }
func (t *waitForRepoEventsTool) Description() string {
	return "Watch the maintained repository. Without a cursor it returns the current open-issue backlog and fleet run state immediately; with the cursor from the previous result it blocks until either changes. Always pass the cursor when waiting."
}
func (t *waitForRepoEventsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"timeout_seconds":{"type":"integer","minimum":30,"maximum":21600},"cursor":{"type":"string"}}}`)
}
func (t *waitForRepoEventsTool) IsReadOnly() bool                      { return true }
func (t *waitForRepoEventsTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *waitForRepoEventsTool) NeedsApproval() bool                   { return false }
func (t *waitForRepoEventsTool) TimeoutSeconds() int                   { return 21660 }

func (t *waitForRepoEventsTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
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
	wd, err := resolveLocalGitRepositoryWorkDir(workDir, "")
	if err != nil {
		return Result{Content: fmt.Sprintf("workspace repository unavailable: %v", err), IsError: true}, nil
	}
	var previous maintainerRepoEventsSnapshot
	cursorProvided := strings.TrimSpace(in.Cursor) != ""
	if cursorProvided {
		cursor, err := decodeMaintainerRepoEventsCursor(in.Cursor)
		if err != nil {
			return Result{Content: fmt.Sprintf("invalid cursor: %v", err), IsError: true}, nil
		}
		previous = snapshotFromMaintainerRepoEventsCursor(cursor)
	}

	current, err := t.repoEventsSnapshot(ctx, wd, true)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if !cursorProvided {
		// First call of an episode: return the current backlog and fleet state
		// immediately instead of arming a delta wait. A "wait for change from
		// now" snapshot silently swallows everything that happened moments
		// before it — e.g. a fleet run this maintainer just promoted to
		// Succeeded — leaving the maintainer blocked for the full timeout with
		// free capacity and an open backlog. The maintainer must reason from
		// current reality first, then wait with the returned cursor.
		return t.repoEventsResult(maintainerRepoEventsSnapshot{}, current, true, false, time.Time{}, false)
	}
	if repoEventsChanged(previous, current) {
		return t.repoEventsResult(previous, current, true, false, time.Time{}, true)
	}

	started := time.Now()
	deadline := time.NewTimer(time.Duration(timeout) * time.Second)
	defer deadline.Stop()
	backlogTicker := time.NewTicker(t.effectiveBacklogPollInterval())
	defer backlogTicker.Stop()
	fleetTicker := time.NewTicker(t.effectiveFleetPollInterval())
	defer fleetTicker.Stop()
	latest := current
	for {
		select {
		case <-ctx.Done():
			return Result{Content: fmt.Sprintf("wait cancelled: %v", ctx.Err()), IsError: true}, nil
		case <-deadline.C:
			return t.repoEventsResult(previous, latest, false, true, started, cursorProvided)
		case <-backlogTicker.C:
			backlog, backlogErr := t.backlogSnapshot(ctx, wd)
			if backlogErr != nil {
				latest.backlogError = backlogErr.Error()
			} else {
				latest.backlogFingerprint = backlog.backlogFingerprint
				latest.issueSignatures = backlog.issueSignatures
				latest.issues = backlog.issues
				latest.backlogAvailable = true
				latest.backlogError = ""
			}
			if repoEventsChanged(previous, latest) {
				return t.repoEventsResult(previous, latest, true, false, started, cursorProvided)
			}
		case <-fleetTicker.C:
			fleet, fleetErr := t.fleetEventsSnapshot(ctx)
			if fleetErr != nil {
				return Result{Content: fleetErr.Error(), IsError: true}, nil
			}
			latest.fleetSignatures = fleet.fleetSignatures
			latest.fleet = fleet.fleet
			if repoEventsChanged(previous, latest) {
				return t.repoEventsResult(previous, latest, true, false, started, cursorProvided)
			}
		}
	}
}

func (t *waitForRepoEventsTool) effectiveBacklogPollInterval() time.Duration {
	if t.backlogPollInterval > 0 {
		return t.backlogPollInterval
	}
	return defaultBacklogPollInterval
}

func (t *waitForRepoEventsTool) effectiveFleetPollInterval() time.Duration {
	if t.fleetPollInterval > 0 {
		return t.fleetPollInterval
	}
	return defaultFleetEventsPollInterval
}

func (t *waitForRepoEventsTool) repoEventsSnapshot(ctx context.Context, workDir string, includeBacklog bool) (maintainerRepoEventsSnapshot, error) {
	fleet, err := t.fleetEventsSnapshot(ctx)
	if err != nil {
		return maintainerRepoEventsSnapshot{}, err
	}
	if !includeBacklog {
		return fleet, nil
	}
	backlog, err := t.backlogSnapshot(ctx, workDir)
	if err != nil {
		fleet.backlogError = err.Error()
		return fleet, nil
	}
	fleet.backlogFingerprint = backlog.backlogFingerprint
	fleet.issueSignatures = backlog.issueSignatures
	fleet.issues = backlog.issues
	fleet.backlogAvailable = true
	return fleet, nil
}

func (t *waitForRepoEventsTool) backlogSnapshot(ctx context.Context, workDir string) (maintainerRepoEventsSnapshot, error) {
	runner := t.runner
	if runner == nil {
		runner = prReviewExecRunner{}
	}
	out, err := runner.RunGH(ctx, workDir, "issue", "list", "--state", "open", "--json", "number,title,labels,updatedAt,url", "--limit", "200")
	if err != nil {
		return maintainerRepoEventsSnapshot{}, fmt.Errorf("gh issue list failed: %w: %s", err, strings.TrimSpace(out))
	}
	var issues []maintainerBacklogIssue
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return maintainerRepoEventsSnapshot{}, fmt.Errorf("parse gh issue list output: %w", err)
	}
	return maintainerBacklogSnapshot(issues), nil
}

func maintainerBacklogSnapshot(issues []maintainerBacklogIssue) maintainerRepoEventsSnapshot {
	entries := make([]string, 0, len(issues))
	signatures := make(map[string]string, len(issues))
	output := make([]maintainerRepoEventIssue, 0, len(issues))
	for _, issue := range issues {
		labels := maintainerIssueLabels(issue.Labels)
		entry := strconv.Itoa(issue.Number) + "|" + issue.UpdatedAt + "|" + strings.Join(labels, ",")
		entries = append(entries, entry)
		signatures[strconv.Itoa(issue.Number)] = maintainerRepoEventSignature(entry)
		output = append(output, maintainerRepoEventIssue{Number: issue.Number, Title: issue.Title, URL: issue.URL, UpdatedAt: issue.UpdatedAt, Labels: labels})
	}
	sort.Strings(entries)
	sort.Slice(output, func(i, j int) bool { return output[i].Number < output[j].Number })
	fingerprint := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return maintainerRepoEventsSnapshot{
		backlogFingerprint: hex.EncodeToString(fingerprint[:]), issueSignatures: signatures, issues: output, backlogAvailable: true,
		fleetSignatures: map[string]string{}, fleet: map[string]maintainerRepoFleetEvent{},
	}
}

func maintainerIssueLabels(labels []maintainerBacklogLabel) []string {
	names := make([]string, 0, len(labels))
	for _, label := range labels {
		if name := strings.TrimSpace(label.Name); name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func (t *waitForRepoEventsTool) fleetEventsSnapshot(ctx context.Context) (maintainerRepoEventsSnapshot, error) {
	fleet, err := t.fleetRuns(ctx)
	if err != nil {
		return maintainerRepoEventsSnapshot{}, fmt.Errorf("failed to list fleet AgentRuns: %w", err)
	}
	signatures := make(map[string]string, len(fleet))
	events := make(map[string]maintainerRepoFleetEvent, len(fleet))
	for i := range fleet {
		run := &fleet[i]
		session, err := t.stateStore.GetSessionByRun(ctx, run.Name, run.Namespace)
		if err != nil {
			return maintainerRepoEventsSnapshot{}, fmt.Errorf("failed to resolve session for fleet AgentRun %q: %w", run.Name, err)
		}
		urls := waitPullRequestURLs(run)
		if urls == nil {
			urls = []string{}
		}
		blockedReason := maintainerBlockedReason(run)
		pendingInput := orchestration.PendingUserInputForSession(session) != nil
		signature := string(run.Status.Phase) + "|" + strconv.Itoa(len(urls)) + "|" + blockedReason + "|" + strconv.FormatBool(pendingInput)
		signatures[run.Name] = maintainerRepoEventSignature(signature)
		events[run.Name] = maintainerRepoFleetEvent{Name: run.Name, Phase: run.Status.Phase, PullRequestURLs: urls, BlockedReason: blockedReason, PendingInput: pendingInput}
	}
	return maintainerRepoEventsSnapshot{fleetSignatures: signatures, fleet: events}, nil
}

func maintainerRepoEventSignature(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawStdEncoding.EncodeToString(sum[:])
}

func repoEventsChanged(previous, current maintainerRepoEventsSnapshot) bool {
	if current.backlogAvailable && (!previous.backlogAvailable || previous.backlogFingerprint != current.backlogFingerprint) {
		return true
	}
	return !maintainerRepoEventSignaturesEqual(previous.fleetSignatures, current.fleetSignatures)
}

func maintainerRepoEventSignaturesEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for name, signature := range left {
		if right[name] != signature {
			return false
		}
	}
	return true
}

func (t *waitForRepoEventsTool) repoEventsResult(previous, current maintainerRepoEventsSnapshot, changed, timedOut bool, started time.Time, cursorKnown bool) (Result, error) {
	if current.backlogAvailable == false && previous.backlogAvailable {
		current.backlogFingerprint = previous.backlogFingerprint
		current.issueSignatures = previous.issueSignatures
		current.issues = previous.issues
	}
	changedIssues := changedRepoEventIssues(previous, current)
	fleetChanges := changedRepoFleetEvents(previous, current)
	if !changed {
		changedIssues = []maintainerRepoEventIssue{}
		fleetChanges = []maintainerRepoFleetEvent{}
	} else if !cursorKnown {
		changedIssues = append([]maintainerRepoEventIssue(nil), current.issues...)
	}

	// The cursor must acknowledge only the deltas actually emitted in this
	// result. Encoding every current signature while trimming the emitted
	// list (cap or byte budget) would mark suppressed issues and fleet runs
	// as already seen, permanently hiding them until they change again. With
	// an emitted-only cursor the next call detects the remaining difference
	// immediately and pages through the rest.
	for {
		cursor, err := encodeMaintainerRepoEventsCursor(repoEventsCursorForEmitted(previous, current, changedIssues, fleetChanges))
		if err != nil {
			return Result{}, err
		}
		out := waitForRepoEventsOutput{
			Changed: changed, TimedOut: timedOut, BacklogChanged: current.backlogAvailable && (!previous.backlogAvailable || previous.backlogFingerprint != current.backlogFingerprint),
			ChangedIssues: changedIssues, FleetChanges: fleetChanges, Cursor: cursor, BacklogError: truncateUTF8(current.backlogError, 1024),
		}
		if !started.IsZero() {
			out.ElapsedSeconds = int(time.Since(started).Seconds())
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			return Result{}, err
		}
		if len(encoded) <= maintainerRepoEventsResultLimit || (len(changedIssues) == 0 && len(fleetChanges) == 0) {
			return Result{Content: string(encoded)}, nil
		}
		if len(changedIssues) > 0 {
			changedIssues = changedIssues[:len(changedIssues)-1]
			continue
		}
		fleetChanges = fleetChanges[:len(fleetChanges)-1]
	}
}

// repoEventsCursorForEmitted advances the previous cursor by only the emitted
// deltas: emitted issues and fleet runs adopt their current signatures,
// removed entries are dropped, and everything else keeps the previously seen
// signature so it still registers as a pending change. The backlog
// fingerprint matches the live snapshot only when nothing was suppressed;
// otherwise a fingerprint derived from the acknowledged signatures keeps the
// cursor distinct from the live state so the next call returns immediately.
func repoEventsCursorForEmitted(previous, current maintainerRepoEventsSnapshot, emittedIssues []maintainerRepoEventIssue, emittedFleet []maintainerRepoFleetEvent) maintainerRepoEventsCursor {
	issueSignatures := map[string]string{}
	for number, signature := range previous.issueSignatures {
		if _, exists := current.issueSignatures[number]; exists {
			issueSignatures[number] = signature
		}
	}
	for _, issue := range emittedIssues {
		number := strconv.Itoa(issue.Number)
		if signature, exists := current.issueSignatures[number]; exists {
			issueSignatures[number] = signature
		}
	}
	backlogFingerprint := current.backlogFingerprint
	if !maintainerRepoEventSignaturesEqual(issueSignatures, current.issueSignatures) {
		backlogFingerprint = repoEventsSignatureFingerprint(issueSignatures)
	}

	fleetSignatures := map[string]string{}
	for name, signature := range previous.fleetSignatures {
		fleetSignatures[name] = signature
	}
	for _, event := range emittedFleet {
		if signature, exists := current.fleetSignatures[event.Name]; exists {
			fleetSignatures[event.Name] = signature
		} else {
			delete(fleetSignatures, event.Name)
		}
	}
	return maintainerRepoEventsCursor{
		BacklogFingerprint: backlogFingerprint,
		IssueSignatures:    issueSignatures,
		FleetSignatures:    fleetSignatures,
	}
}

func repoEventsSignatureFingerprint(signatures map[string]string) string {
	entries := make([]string, 0, len(signatures))
	for number, signature := range signatures {
		entries = append(entries, number+"|"+signature)
	}
	sort.Strings(entries)
	fingerprint := sha256.Sum256([]byte("acknowledged:" + strings.Join(entries, "\n")))
	return hex.EncodeToString(fingerprint[:])
}

func changedRepoEventIssues(previous, current maintainerRepoEventsSnapshot) []maintainerRepoEventIssue {
	if !current.backlogAvailable || previous.issueSignatures == nil {
		return []maintainerRepoEventIssue{}
	}
	issues := make([]maintainerRepoEventIssue, 0)
	for _, issue := range current.issues {
		if previous.issueSignatures[strconv.Itoa(issue.Number)] != current.issueSignatures[strconv.Itoa(issue.Number)] {
			issues = append(issues, issue)
		}
	}
	return issues
}

func changedRepoFleetEvents(previous, current maintainerRepoEventsSnapshot) []maintainerRepoFleetEvent {
	names := make([]string, 0)
	for name, signature := range current.fleetSignatures {
		if previous.fleetSignatures[name] != signature {
			names = append(names, name)
		}
	}
	for name := range previous.fleetSignatures {
		if _, exists := current.fleetSignatures[name]; !exists {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	events := make([]maintainerRepoFleetEvent, 0, len(names))
	for _, name := range names {
		if event, exists := current.fleet[name]; exists {
			events = append(events, event)
			continue
		}
		events = append(events, maintainerRepoFleetEvent{Name: name, PullRequestURLs: []string{}})
	}
	return events
}

func encodeMaintainerRepoEventsCursor(cursor maintainerRepoEventsCursor) (string, error) {
	if cursor.IssueSignatures == nil {
		cursor.IssueSignatures = map[string]string{}
	}
	if cursor.FleetSignatures == nil {
		cursor.FleetSignatures = map[string]string{}
	}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(encoded), nil
}

func decodeMaintainerRepoEventsCursor(value string) (maintainerRepoEventsCursor, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return maintainerRepoEventsCursor{}, err
	}
	var cursor maintainerRepoEventsCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return maintainerRepoEventsCursor{}, err
	}
	if cursor.IssueSignatures == nil || cursor.FleetSignatures == nil {
		return maintainerRepoEventsCursor{}, fmt.Errorf("snapshot signatures are required")
	}
	return cursor, nil
}

func snapshotFromMaintainerRepoEventsCursor(cursor maintainerRepoEventsCursor) maintainerRepoEventsSnapshot {
	return maintainerRepoEventsSnapshot{
		backlogFingerprint: cursor.BacklogFingerprint, issueSignatures: cursor.IssueSignatures, backlogAvailable: cursor.BacklogFingerprint != "",
		fleetSignatures: cursor.FleetSignatures, fleet: map[string]maintainerRepoFleetEvent{},
	}
}
