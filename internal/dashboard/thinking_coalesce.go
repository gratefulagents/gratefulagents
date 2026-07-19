package dashboard

import (
	"google.golang.org/protobuf/proto"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// thinkingGroup accumulates one streamed reasoning group (all
// assistant_thinking_delta entries sharing a ToolUseId, plus any
// assistant_thinking finals for that id) into a single merged entry.
type thinkingGroup struct {
	entry *platform.ActivityEntry
	// finalized holds the text of assistant_thinking finals received so far
	// (joined with "\n\n"); pending holds deltas accumulated since the last
	// final, which a subsequent final replaces.
	finalized string
	hasFinal  bool
	pending   string
	// lastIdx is the input index of the group's newest constituent — the
	// slot where the merged entry is emitted so the output stays ordered by
	// EventId.
	lastIdx int
}

func (g *thinkingGroup) render() {
	switch {
	case !g.hasFinal:
		g.entry.Message = g.pending
	case g.pending == "":
		g.entry.Message = g.finalized
	default:
		g.entry.Message = g.finalized + "\n\n" + g.pending
	}
}

func (g *thinkingGroup) absorb(e *platform.ActivityEntry, idx int) {
	g.render()
	if e.GetEventId() > g.entry.EventId {
		g.entry.EventId = e.GetEventId()
	}
	g.lastIdx = idx
}

// coalesceThinkingEntries merges streamed assistant_thinking_delta entries
// (grouped by ToolUseId) with their assistant_thinking finals into single
// assistant_thinking entries. The merged entry keeps the first delta's
// timestamp and identity fields — the frontend keys rows by
// timestamp/type/toolUseId, so identity must stay stable while the text
// grows — but takes the largest constituent EventId and is emitted at the
// LAST constituent's position. Raw entries are ordered by EventId, so
// id-at-last-slot keeps the output monotonically ordered (the
// since_event_id/before_event_id/limit options and the delta-watch cursor
// all assume monotonic ids) while still letting delta watchers re-send the
// entry as it grows. Once finalized, the merged entry sits exactly where the
// plain assistant_thinking entry would have been without streaming. Input
// protos are never mutated; changed entries are clones. The pass is O(n)
// and idempotent.
func coalesceThinkingEntries(entries []*platform.ActivityEntry) []*platform.ActivityEntry {
	hasDelta := false
	for _, e := range entries {
		if e.GetType() == "assistant_thinking_delta" {
			hasDelta = true
			break
		}
	}
	if !hasDelta {
		return entries
	}

	// Pass 1: fold deltas and matching finals into per-ToolUseId groups,
	// marking consumed constituents and tracking each group's newest
	// constituent index (its emission slot).
	groups := make(map[string]*thinkingGroup)
	consumed := make([]bool, len(entries))
	for i, e := range entries {
		switch e.GetType() {
		case "assistant_thinking_delta":
			id := e.GetToolUseId()
			if id == "" {
				// No stream to join; pass 2 rewrites it to a standalone
				// assistant_thinking entry in place.
				continue
			}
			g, ok := groups[id]
			if !ok {
				c := proto.Clone(e).(*platform.ActivityEntry)
				c.Type = "assistant_thinking"
				g = &thinkingGroup{entry: c}
				groups[id] = g
			}
			g.pending += e.GetMessage()
			g.absorb(e, i)
			consumed[i] = true
		case "assistant_thinking":
			g, ok := groups[e.GetToolUseId()]
			if !ok {
				// No delta stream for this id (legacy or non-streaming
				// provider): passes through untouched in pass 2.
				continue
			}
			if g.hasFinal {
				g.finalized += "\n\n" + e.GetMessage()
			} else {
				g.finalized = e.GetMessage()
				g.hasFinal = true
			}
			g.pending = ""
			g.absorb(e, i)
			consumed[i] = true
		}
	}
	emitAt := make(map[int]*thinkingGroup, len(groups))
	for _, g := range groups {
		emitAt[g.lastIdx] = g
	}

	// Pass 2: rebuild the slice, dropping consumed constituents and emitting
	// each merged entry at its newest constituent's slot.
	out := make([]*platform.ActivityEntry, 0, len(entries))
	for i, e := range entries {
		if g, ok := emitAt[i]; ok {
			out = append(out, g.entry)
			continue
		}
		if consumed[i] {
			continue
		}
		if e.GetType() == "assistant_thinking_delta" {
			c := proto.Clone(e).(*platform.ActivityEntry)
			c.Type = "assistant_thinking"
			out = append(out, c)
			continue
		}
		out = append(out, e)
	}
	return out
}
