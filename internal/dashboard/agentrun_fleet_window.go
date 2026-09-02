package dashboard

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// agentRunSourceFilter keeps runs whose trigger or project ref matches the
// requested kind and/or name. Empty kind and name match every run; the
// semantics mirror the fleet page's client-side filter so a server-filtered
// stream shows exactly what the page used to filter locally.
type agentRunSourceFilter struct {
	kind string
	name string
}

func newAgentRunSourceFilter(kind, name string) agentRunSourceFilter {
	return agentRunSourceFilter{kind: strings.TrimSpace(kind), name: strings.TrimSpace(name)}
}

func (f agentRunSourceFilter) empty() bool { return f.kind == "" && f.name == "" }

func (f agentRunSourceFilter) matches(run *platformv1alpha1.AgentRun) bool {
	if f.empty() {
		return true
	}
	if (f.name == "" || run.Spec.Trigger.Name == f.name) && (f.kind == "" || run.Spec.Trigger.Kind == f.kind) {
		return true
	}
	if run.Spec.Context != nil && run.Spec.Context.ProjectRef != nil {
		ref := run.Spec.Context.ProjectRef
		if (f.name == "" || ref.Name == f.name) && (f.kind == "" || ref.Kind == f.kind) {
			return true
		}
	}
	return false
}

// agentRunFleetCursor is the opaque page token of a fleet window: the
// creation time and key of the last terminal run on the previous page.
type agentRunFleetCursor struct {
	createdUnix int64
	key         string // namespace/name
}

func encodeAgentRunFleetCursor(run *platformv1alpha1.AgentRun) string {
	raw := strconv.FormatInt(run.CreationTimestamp.Unix(), 10) + "|" + run.Namespace + "/" + run.Name
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeAgentRunFleetCursor(token string) (agentRunFleetCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return agentRunFleetCursor{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid page_token"))
	}
	ts, key, ok := strings.Cut(string(raw), "|")
	if !ok {
		return agentRunFleetCursor{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid page_token"))
	}
	createdUnix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return agentRunFleetCursor{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid page_token"))
	}
	return agentRunFleetCursor{createdUnix: createdUnix, key: key}, nil
}

// agentRunNewerThan orders runs newest first: by creation time, then by key
// so runs created in the same second have a stable order.
func agentRunNewerThan(a, b *platformv1alpha1.AgentRun) bool {
	at, bt := a.CreationTimestamp.Unix(), b.CreationTimestamp.Unix()
	if at != bt {
		return at > bt
	}
	return a.Namespace+"/"+a.Name > b.Namespace+"/"+b.Name
}

// olderThanCursor reports whether run sorts strictly after the cursor in
// newest-first order, i.e. belongs to a later page.
func (c agentRunFleetCursor) olderThan(run *platformv1alpha1.AgentRun) bool {
	if t := run.CreationTimestamp.Unix(); t != c.createdUnix {
		return t < c.createdUnix
	}
	return run.Namespace+"/"+run.Name < c.key
}

// agentRunFleetWindow is one page of the fleet view.
type agentRunFleetWindow struct {
	// runs is the page content, newest first (non-terminal runs first on
	// the initial page).
	runs []*platformv1alpha1.AgentRun
	// nextPageToken pages to older terminal runs; empty when exhausted.
	nextPageToken string
	// total is the number of runs that passed the filters across all pages.
	total int
}

// selectAgentRunFleetWindow applies the fleet-window contract shared by
// ListAgentRuns and WatchAgentRuns to an already visibility-filtered,
// source-filtered set of runs (candidates may be in any order and are not
// mutated):
//
//   - limit <= 0 and no cursor: every candidate, newest first (legacy).
//   - limit > 0, no cursor: every non-terminal run plus the newest `limit`
//     terminal runs; nextPageToken points past the last terminal run when
//     older ones remain.
//   - cursor set: the next `limit` terminal runs strictly older than the
//     cursor (non-terminal runs were already returned on the first page).
//
// Non-terminal runs are always part of the first page because they are what
// a fleet operator needs to see and act on regardless of age; terminal runs
// are immutable, so paging through them by creation time is stable.
func selectAgentRunFleetWindow(candidates []*platformv1alpha1.AgentRun, limit int, pageToken string) (agentRunFleetWindow, error) {
	sorted := make([]*platformv1alpha1.AgentRun, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool { return agentRunNewerThan(sorted[i], sorted[j]) })
	window := agentRunFleetWindow{total: len(sorted)}

	if limit <= 0 && pageToken == "" {
		window.runs = sorted
		return window, nil
	}
	if limit <= 0 {
		return window, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("page_token requires a positive limit"))
	}

	var cursor *agentRunFleetCursor
	if pageToken != "" {
		c, err := decodeAgentRunFleetCursor(pageToken)
		if err != nil {
			return window, err
		}
		cursor = &c
	}

	var active, terminal []*platformv1alpha1.AgentRun
	for _, run := range sorted {
		if !isTerminalAgentRunPhase(run.Status.Phase) {
			if cursor == nil {
				active = append(active, run)
			}
			continue
		}
		if cursor != nil && !cursor.olderThan(run) {
			continue
		}
		terminal = append(terminal, run)
	}
	if len(terminal) > limit {
		window.nextPageToken = encodeAgentRunFleetCursor(terminal[limit-1])
		terminal = terminal[:limit]
	}
	window.runs = append(active, terminal...)
	return window, nil
}

// agentRunFleetCandidates applies the caller's visibility predicate and the
// source filter to a fleet snapshot, returning pointers into the (read-only)
// snapshot. keep, when non-nil, applies an additional ownership/share filter
// keyed by namespace/name.
func agentRunFleetCandidates(items []platformv1alpha1.AgentRun, visible func(*platformv1alpha1.AgentRun) bool, source agentRunSourceFilter, keep func(key string) bool) []*platformv1alpha1.AgentRun {
	out := make([]*platformv1alpha1.AgentRun, 0, len(items))
	for i := range items {
		run := &items[i]
		if !visible(run) || !source.matches(run) {
			continue
		}
		if keep != nil && !keep(run.Namespace+"/"+run.Name) {
			continue
		}
		out = append(out, run)
	}
	return out
}

// agentRunAccessKeep builds the ownedByMe/sharedWithMe key filter for list
// requests, or nil when neither flag is set. It is evaluated before the fleet
// window is cut so page sizes and totals reflect the filtered set.
func (s *Server) agentRunAccessKeep(ctx context.Context, ownedByMe, sharedWithMe bool) func(key string) bool {
	if !ownedByMe && !sharedWithMe || s.stateStore == nil {
		return nil
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil
	}
	allowed := func(keys map[string]bool) func(string) bool {
		return func(key string) bool { return keys[key] }
	}
	var filters []func(string) bool
	if ownedByMe {
		owned := map[string]bool{}
		items, _ := s.stateStore.ListOwnedResources(ctx, actor.Subject, "agent_run")
		for _, o := range items {
			owned[o.ResourceNamespace+"/"+o.ResourceID] = true
		}
		filters = append(filters, allowed(owned))
	}
	if sharedWithMe {
		shared := map[string]bool{}
		items, _ := s.stateStore.ListSharedWithMe(ctx, actor.Subject, "agent_run")
		for _, sh := range items {
			shared[sh.ResourceNamespace+"/"+sh.ResourceID] = true
		}
		filters = append(filters, allowed(shared))
	}
	return func(key string) bool {
		for _, f := range filters {
			if !f(key) {
				return false
			}
		}
		return true
	}
}
