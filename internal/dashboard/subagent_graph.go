// Package dashboard provides the subagent graph builder that converts raw activity
// entries into a pre-built SubagentGraph proto. This eliminates the need for clients
// (web, Apple) to implement their own grouping, status derivation, and deduplication.
package dashboard

import (
	"fmt"
	"sort"
	"strings"

	pb "github.com/gratefulagents/gratefulagents/rpc/platform"
)

const rootID = "run-root"

// activityGroup is an internal grouping of related entries.
type activityGroup struct {
	kind    string // "subagent", "inline-subagent"
	id      string
	entries []*pb.ActivityEntry
	// inline-subagent specific
	parentEntry *pb.ActivityEntry
	children    []*pb.ActivityEntry
	resultEntry *pb.ActivityEntry
	// subagent specific
	taskID              string
	subagentType        string
	subagentDescription string
	subagentPrompt      string
	subagentStatus      string
	toolCount           int32
	totalTokens         int64
	durationMs          int64
	subagentModel       string
	subagentCostUsd     float64
	subagentNumTurns    int32
	subagentStopReason  string
	dependsOn           []string
	waitingOn           []string
	currentStep         string
	lastTool            string
	filesWritten        int32
	messagesReceived    int32
	lastParentMessage   string
}

var subagentEvidenceTypes = map[string]bool{
	"subagent_started":      true,
	"subagent_progress":     true,
	"subagent_notification": true,
	"subagent_completed":    true,
}

var terminalSubagentStatuses = map[string]bool{
	"completed": true,
	"succeeded": true,
	"failed":    true,
	"stopped":   true,
	"cancelled": true,
	"canceled":  true,
}

func isTerminalSubagentStatus(status string) bool {
	return terminalSubagentStatuses[status]
}

// subagentStatusNoise are lifecycle/status strings that ride in the event
// Message field for registry task snapshots. They must never be promoted to a
// node title — the task's objective (SubagentPrompt) is the real description.
var subagentStatusNoise = map[string]bool{
	"spawned":         true,
	"dependency_wait": true,
	"managed_wait":    true,
	"final_join":      true,
	"resumed":         true,
	"pending":         true,
	"waiting":         true,
	"running":         true,
	"started":         true,
	"completed":       true,
	"succeeded":       true,
	"failed":          true,
	"stopped":         true,
	"cancelled":       true,
	"canceled":        true,
}

// cleanSubagentDescription filters lifecycle noise out of a candidate title.
func cleanSubagentDescription(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || subagentStatusNoise[strings.ToLower(s)] {
		return ""
	}
	return s
}

// subagentTitleFromPrompt condenses a task prompt/objective into a one-line
// node title: first non-empty line, ellipsized. Truncation is rune-based —
// byte slicing could split a multi-byte character and produce invalid UTF-8,
// which proto marshaling rejects.
func subagentTitleFromPrompt(prompt string) string {
	for line := range strings.SplitSeq(prompt, "\n") {
		line = strings.ToValidUTF8(strings.TrimSpace(line), "")
		if line == "" {
			continue
		}
		const maxTitle = 90
		runes := []rune(line)
		if len(runes) > maxTitle {
			return strings.TrimSpace(string(runes[:maxTitle-1])) + "…"
		}
		return line
	}
	return ""
}

// latestSubagentWaitingOn returns the waiting set from the newest status
// snapshot: waiting_on shrinks as dependencies finish and is cleared on
// terminal states, so merging across snapshots would freeze the first (widest)
// set forever.
func latestSubagentWaitingOn(batch []*pb.ActivityEntry, status string) []string {
	if isTerminalSubagentStatus(status) {
		return nil
	}
	for i := len(batch) - 1; i >= 0; i-- {
		if batch[i].SubagentStatus != "" {
			return append([]string(nil), batch[i].SubagentWaitingOn...)
		}
	}
	return nil
}

// BuildSubagentGraph constructs a SubagentGraph proto from raw activity entries.
func BuildSubagentGraph(entries []*pb.ActivityEntry, runName string) *pb.SubagentGraph {
	if runName == "" {
		runName = "Run"
	}

	// Build an entry-index lookup for detail_entry_indices.
	entryIndex := make(map[*pb.ActivityEntry]int32, len(entries))
	for i, e := range entries {
		entryIndex[e] = int32(i)
	}

	// Root node.
	rootTs := int64(0)
	if len(entries) > 0 {
		rootTs = entries[0].TimestampUnix
	}
	rootNode := &pb.SubagentGraphNode{
		Id:            rootID,
		Kind:          "root",
		Label:         runName,
		Subtitle:      "Observed run root",
		Description:   "Observed root node built from the current run activity stream.",
		Status:        "running",
		Lineage:       "complete",
		TimestampUnix: rootTs,
		EntryCount:    int32(len(entries)),
	}

	// Group entries into subagent and inline-subagent groups.
	groups := groupEntries(entries)

	// Build sets for dedup: track inline-subagent toolUseIds and subagent spawn toolUseIds.
	inlineToolUseIDs := make(map[string]*activityGroup)        // toolUseId → inline group
	subagentSpawnToolUseIDs := make(map[string]*activityGroup) // toolUseId → subagent group

	for _, g := range groups {
		if g.kind == "inline-subagent" && g.parentEntry != nil && g.parentEntry.ToolUseId != "" {
			inlineToolUseIDs[g.parentEntry.ToolUseId] = g
		}
		if g.kind == "subagent" {
			// Find the spawn entry (tool_use with tool=Agent or subagent_started).
			for _, e := range g.entries {
				if e.Type == "subagent_started" && e.ToolUseId != "" {
					subagentSpawnToolUseIDs[e.ToolUseId] = g
				}
			}
		}
	}

	// Detect overlapping inline + subagent groups.
	// When an inline-subagent's children include entries that belong to a subagent
	// group (same toolUseId), they represent the same operation. Skip the inline node
	// and parent the subagent node to where the inline would have gone.
	mergedInlineIDs := make(map[string]bool) // inline toolUseIds that are merged into subagent nodes
	// Map from subagent group id → the inline group's parentEntry toolUseId
	subagentToInlineParent := make(map[string]string)
	for inlineToolUseID, inlineGroup := range inlineToolUseIDs {
		// Check if any child of this inline group is a subagent_started or if the
		// inline toolUseId matches a subagent spawn toolUseId.
		if saGroup, ok := subagentSpawnToolUseIDs[inlineToolUseID]; ok {
			mergedInlineIDs[inlineToolUseID] = true
			subagentToInlineParent[saGroup.taskID] = inlineToolUseID
			// Merge inline children into the subagent group's entries for detail display.
			saGroup.entries = append(saGroup.entries, inlineGroup.parentEntry)
			saGroup.entries = append(saGroup.entries, inlineGroup.children...)
			if inlineGroup.resultEntry != nil {
				saGroup.entries = append(saGroup.entries, inlineGroup.resultEntry)
			}
			continue
		}
		// Also check if any child entry has a taskId that matches a subagent group.
		for _, child := range inlineGroup.children {
			if child.TaskId != "" {
				for _, g := range groups {
					if g.kind == "subagent" && g.taskID == child.TaskId {
						mergedInlineIDs[inlineToolUseID] = true
						subagentToInlineParent[g.taskID] = inlineToolUseID
						g.entries = append(g.entries, inlineGroup.parentEntry)
						g.entries = append(g.entries, inlineGroup.children...)
						if inlineGroup.resultEntry != nil {
							g.entries = append(g.entries, inlineGroup.resultEntry)
						}
						break
					}
				}
			}
			if mergedInlineIDs[inlineToolUseID] {
				break
			}
		}
	}

	// Build the set of inline node IDs (excluding merged ones) for parent resolution.
	inlineIDs := make(map[string]bool)
	for _, g := range groups {
		if g.kind == "inline-subagent" && g.parentEntry != nil {
			tuID := g.parentEntry.ToolUseId
			if tuID != "" && !mergedInlineIDs[tuID] {
				inlineIDs[fmt.Sprintf("inline:%s", tuID)] = true
			}
		}
	}

	resolveDefaultParent := func(groupEntries []*pb.ActivityEntry) string {
		_ = groupEntries
		return rootID
	}

	var nodes []*pb.SubagentGraphNode
	nodes = append(nodes, rootNode)

	representedEntries := make(map[*pb.ActivityEntry]bool)

	for _, g := range groups {
		switch g.kind {
		case "inline-subagent":
			if g.parentEntry == nil {
				continue
			}
			toolUseID := g.parentEntry.ToolUseId
			if mergedInlineIDs[toolUseID] {
				continue // skip — merged into corresponding subagent node
			}

			allEntries := make([]*pb.ActivityEntry, 0, 1+len(g.children)+1)
			allEntries = append(allEntries, g.parentEntry)
			allEntries = append(allEntries, g.children...)
			if g.resultEntry != nil {
				allEntries = append(allEntries, g.resultEntry)
			}
			for _, e := range allEntries {
				representedEntries[e] = true
			}

			defaultParent := resolveDefaultParent(allEntries)
			requestedParent := defaultParent
			if g.parentEntry.ParentCallId != "" {
				requestedParent = fmt.Sprintf("inline:%s", g.parentEntry.ParentCallId)
			}
			parentId := requestedParent
			if g.parentEntry.ParentCallId != "" && !inlineIDs[requestedParent] {
				parentId = ""
			}
			if parentId == "" {
				parentId = defaultParent
			}

			status := deriveInlineSubagentStatus(g.parentEntry, g.children, g.resultEntry)
			toolCount := int32(0)
			totalTokens := int64(0)
			for _, e := range g.children {
				if e.Type == "tool_use" {
					toolCount++
				}
				totalTokens += sumEntryTokens(e)
			}

			lineage := "complete"
			lineageReason := ""
			if g.parentEntry.ParentCallId != "" && !inlineIDs[fmt.Sprintf("inline:%s", g.parentEntry.ParentCallId)] {
				lineage = "partial"
				lineageReason = "The parent call ID was present, but its parent inline node was not observed."
			}

			agentName := g.parentEntry.AgentName
			if agentName == "" {
				agentName = strings.TrimPrefix(g.parentEntry.Tool, "agent_")
			}
			if agentName == "" {
				agentName = "Inline subagent"
			}

			nodes = append(nodes, &pb.SubagentGraphNode{
				Id:                  fmt.Sprintf("inline:%s", toolUseID),
				Kind:                "inline-subagent",
				ParentId:            parentId,
				EdgeKind:            "inline-child",
				Label:               agentName,
				Subtitle:            firstNonEmpty(g.parentEntry.Tool, "Inline subagent"),
				Description:         firstNonEmpty(g.parentEntry.Message, g.parentEntry.Input, "Inline subagent activity observed via parentCallId."),
				Status:              status,
				Lineage:             lineage,
				LineageReason:       lineageReason,
				TimestampUnix:       g.parentEntry.TimestampUnix,
				EntryCount:          int32(len(allEntries)),
				ToolUseId:           toolUseID,
				ParentCallId:        g.parentEntry.ParentCallId,
				ToolCount:           toolCount,
				TotalTokens:         totalTokens,
				DetailEntryIndices:  entryIndices(allEntries, entryIndex),
				DetailEntryEventIds: entryEventIDs(allEntries),
			})

		case "subagent":
			for _, e := range g.entries {
				representedEntries[e] = true
			}

			started := findEntry(g.entries, func(e *pb.ActivityEntry) bool { return e.Type == "subagent_started" })
			spawnEntry := findEntry(g.entries, func(e *pb.ActivityEntry) bool { return e.Type == "tool_use" && e.Tool == "Agent" })
			spawnToolUseID := ""
			if spawnEntry != nil {
				spawnToolUseID = spawnEntry.ToolUseId
			} else if started != nil {
				spawnToolUseID = started.ToolUseId
			}

			// Determine parent: check if this was merged from an inline group.
			defaultParent := resolveDefaultParent(g.entries)
			parentId := defaultParent
			edgeKind := "spawned"

			inlineParentCallId := ""
			if spawnEntry != nil && spawnEntry.ParentCallId != "" && spawnEntry.ParentCallId != spawnToolUseID {
				inlineParentCallId = spawnEntry.ParentCallId
			}
			if inlineParentCallId == "" {
				for _, e := range g.entries {
					if e.ParentCallId != "" && e.ParentCallId != spawnToolUseID {
						inlineParentCallId = e.ParentCallId
						break
					}
				}
			}

			if inlineParentCallId != "" {
				requested := fmt.Sprintf("inline:%s", inlineParentCallId)
				if inlineIDs[requested] {
					parentId = requested
					edgeKind = "inline-child"
				}
			}

			lineage := "complete"
			lineageReason := ""
			if spawnToolUseID == "" && inlineParentCallId == "" {
				lineage = "partial"
				lineageReason = "Missing toolUseId on subagent_started, so the spawn edge could not be fully correlated."
			} else if parentId == "" {
				lineage = "partial"
				lineageReason = "The parent call ID was present, but the parent inline node was not observed."
			}

			status := deriveGroupedSubagentStatus(g.entries, g.subagentStatus)

			ts := int64(0)
			if len(g.entries) > 0 {
				ts = g.entries[0].TimestampUnix
			}

			toolUseID := ""
			if started != nil {
				toolUseID = started.ToolUseId
			} else if spawnEntry != nil {
				toolUseID = spawnEntry.ToolUseId
			}

			nodes = append(nodes, &pb.SubagentGraphNode{
				Id:                  fmt.Sprintf("task:%s", g.taskID),
				Kind:                "subagent",
				ParentId:            parentId,
				EdgeKind:            edgeKind,
				Label:               strings.ToValidUTF8(firstNonEmpty(g.subagentDescription, subagentTitleFromPrompt(g.subagentPrompt), g.subagentType, "Subagent"), ""),
				Subtitle:            firstNonEmpty(g.subagentType, "Spawned subagent"),
				Description:         strings.ToValidUTF8(firstNonEmpty(subagentTitleFromPrompt(g.subagentPrompt), buildDescription(g.entries)), ""),
				Status:              status,
				Lineage:             lineage,
				LineageReason:       lineageReason,
				TimestampUnix:       ts,
				EntryCount:          int32(len(g.entries)),
				TaskId:              g.taskID,
				ToolUseId:           toolUseID,
				ParentCallId:        inlineParentCallId,
				ToolCount:           g.toolCount,
				TotalTokens:         g.totalTokens,
				DurationMs:          g.durationMs,
				Model:               g.subagentModel,
				CostUsd:             g.subagentCostUsd,
				NumTurns:            g.subagentNumTurns,
				StopReason:          g.subagentStopReason,
				DependsOn:           append([]string(nil), g.dependsOn...),
				WaitingOn:           append([]string(nil), g.waitingOn...),
				CurrentStep:         g.currentStep,
				LastTool:            g.lastTool,
				FilesWritten:        g.filesWritten,
				MessagesReceived:    g.messagesReceived,
				LastParentMessage:   g.lastParentMessage,
				DetailEntryIndices:  entryIndices(g.entries, entryIndex),
				DetailEntryEventIds: entryEventIDs(g.entries),
			})
		}
	}

	// Fallback clusters for unrepresented subagent entries.
	fallbackClusters := buildFallbackClusters(entries, representedEntries)
	for _, cluster := range fallbackClusters {
		rep := pickRepresentativeEntry(cluster.entries)
		if rep == nil {
			continue
		}
		taskID := ""
		toolUseID := ""
		parentCallID := ""
		for _, e := range cluster.entries {
			if taskID == "" && e.TaskId != "" {
				taskID = e.TaskId
			}
			if toolUseID == "" && e.ToolUseId != "" {
				toolUseID = e.ToolUseId
			}
			if parentCallID == "" && e.ParentCallId != "" {
				parentCallID = e.ParentCallId
			}
		}

		defaultParent := resolveDefaultParent(cluster.entries)
		parentId := defaultParent
		kind := "subagent"
		eKind := "spawned"
		if parentCallID != "" {
			kind = "inline-subagent"
			eKind = "inline-child"
			requested := fmt.Sprintf("inline:%s", parentCallID)
			if inlineIDs[requested] {
				parentId = requested
			}
		}

		lineage := "complete"
		lineageReason := ""
		if parentId == "" {
			lineage = "partial"
			lineageReason = "The parent call ID was present, but the parent inline node was not observed."
		} else if taskID == "" && toolUseID == "" {
			lineage = "partial"
			lineageReason = "Observed subagent telemetry without the full task/tool correlation needed for normal grouping."
		}

		ts := rep.TimestampUnix
		if len(cluster.entries) > 0 {
			ts = cluster.entries[0].TimestampUnix
		}

		nodes = append(nodes, &pb.SubagentGraphNode{
			Id:                  fmt.Sprintf("raw-subagent:%s", cluster.key),
			Kind:                kind,
			ParentId:            parentId,
			EdgeKind:            eKind,
			Label:               firstNonEmpty(cleanSubagentDescription(rep.SubagentDescription), subagentTitleFromPrompt(rep.SubagentPrompt), rep.SubagentType, rep.Message, "Observed subagent"),
			Subtitle:            firstNonEmpty(rep.SubagentType, humanizeEventType(rep.Type)),
			Description:         buildDescription(cluster.entries),
			Status:              deriveFallbackClusterStatus(cluster.entries),
			Lineage:             lineage,
			LineageReason:       lineageReason,
			TimestampUnix:       ts,
			EntryCount:          int32(len(cluster.entries)),
			TaskId:              taskID,
			ToolUseId:           toolUseID,
			ParentCallId:        parentCallID,
			ToolCount:           maxInt32(mapInt32(cluster.entries, func(e *pb.ActivityEntry) int32 { return e.SubagentToolCount })),
			TotalTokens:         maxInt64(mapInt64(cluster.entries, func(e *pb.ActivityEntry) int64 { return e.SubagentTotalTokens })),
			DurationMs:          maxInt64(mapInt64(cluster.entries, func(e *pb.ActivityEntry) int64 { return e.SubagentDurationMs })),
			Model:               firstOf(cluster.entries, func(e *pb.ActivityEntry) string { return e.SubagentModel }),
			CostUsd:             maxFloat64(mapFloat64(cluster.entries, func(e *pb.ActivityEntry) float64 { return e.SubagentCostUsd })),
			NumTurns:            maxInt32(mapInt32(cluster.entries, func(e *pb.ActivityEntry) int32 { return e.SubagentNumTurns })),
			StopReason:          firstOf(cluster.entries, func(e *pb.ActivityEntry) string { return e.SubagentStopReason }),
			DependsOn:           mergedStringFields(cluster.entries, func(e *pb.ActivityEntry) []string { return e.SubagentDependsOn }),
			WaitingOn:           mergedStringFields(cluster.entries, func(e *pb.ActivityEntry) []string { return e.SubagentWaitingOn }),
			CurrentStep:         lastOf(cluster.entries, func(e *pb.ActivityEntry) string { return e.SubagentCurrentStep }),
			LastTool:            lastOf(cluster.entries, func(e *pb.ActivityEntry) string { return firstNonEmpty(e.SubagentLastTool, e.LastToolName) }),
			FilesWritten:        maxInt32(mapInt32(cluster.entries, func(e *pb.ActivityEntry) int32 { return e.SubagentFilesWritten })),
			MessagesReceived:    maxInt32(mapInt32(cluster.entries, func(e *pb.ActivityEntry) int32 { return e.SubagentMessagesReceived })),
			LastParentMessage:   lastOf(cluster.entries, func(e *pb.ActivityEntry) string { return e.SubagentLastParentMessage }),
			DetailEntryIndices:  entryIndices(cluster.entries, entryIndex),
			DetailEntryEventIds: entryEventIDs(cluster.entries),
		})
	}

	// Build node index and edges.
	nodesById := make(map[string]*pb.SubagentGraphNode, len(nodes))
	for _, n := range nodes {
		nodesById[n.Id] = n
	}

	var edges []*pb.SubagentGraphEdge
	var orphanIDs []string

	// Track structural (spawn/inline) edges so depends-on edges that merely
	// restate the spawn hierarchy are not drawn as redundant dashed overlays.
	structuralEdges := make(map[string]bool)

	for _, n := range nodes {
		if n.Kind == "root" {
			continue
		}
		if n.ParentId == "" || nodesById[n.ParentId] == nil {
			orphanIDs = append(orphanIDs, n.Id)
			continue
		}
		structuralEdges[n.ParentId+"\x00"+n.Id] = true
		edges = append(edges, &pb.SubagentGraphEdge{
			Id:      fmt.Sprintf("%s->%s", n.ParentId, n.Id),
			From:    n.ParentId,
			To:      n.Id,
			Kind:    n.EdgeKind,
			Lineage: n.Lineage,
		})
	}
	seenDepEdges := make(map[string]bool)
	for _, n := range nodes {
		if n.Kind == "root" {
			continue
		}
		for _, depID := range n.DependsOn {
			fromID := "task:" + depID
			if nodesById[fromID] == nil {
				fromID = depID
			}
			if fromID == n.Id || nodesById[fromID] == nil {
				continue
			}
			pairKey := fromID + "\x00" + n.Id
			// Skip if this dependency simply restates the spawn/inline edge, or
			// if we have already emitted the same depends-on pair.
			if structuralEdges[pairKey] || seenDepEdges[pairKey] {
				continue
			}
			seenDepEdges[pairKey] = true
			edges = append(edges, &pb.SubagentGraphEdge{
				Id:      fmt.Sprintf("%s=>%s", fromID, n.Id),
				From:    fromID,
				To:      n.Id,
				Kind:    "depends-on",
				Lineage: "complete",
			})
		}
	}

	// Sort nodes: root first, then by timestamp.
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		if a.Kind == "root" {
			return true
		}
		if b.Kind == "root" {
			return false
		}
		if a.TimestampUnix != b.TimestampUnix {
			return a.TimestampUnix < b.TimestampUnix
		}
		return a.Id < b.Id
	})

	hasSubagents := false
	for _, n := range nodes {
		if n.Kind != "root" {
			hasSubagents = true
			break
		}
	}

	return &pb.SubagentGraph{
		RootId:       rootID,
		Nodes:        nodes,
		Edges:        edges,
		OrphanIds:    orphanIDs,
		HasSubagents: hasSubagents,
	}
}

// --- Activity grouping ---

func groupEntries(entries []*pb.ActivityEntry) []*activityGroup {
	// Phase 1: Bucket by taskId.
	validTaskIDs := make(map[string]bool)
	for _, e := range entries {
		if e.TaskId != "" && hasSubagentGroupingEvidence(e) {
			validTaskIDs[e.TaskId] = true
		}
	}

	subagentByTaskID := make(map[string][]*pb.ActivityEntry)
	for _, e := range entries {
		if e.TaskId != "" && validTaskIDs[e.TaskId] {
			subagentByTaskID[e.TaskId] = append(subagentByTaskID[e.TaskId], e)
		}
	}

	toolUseIDToTaskID := make(map[string]string)
	for tid, arr := range subagentByTaskID {
		for _, e := range arr {
			if e.Type == "subagent_started" && e.ToolUseId != "" {
				toolUseIDToTaskID[e.ToolUseId] = tid
			}
		}
	}

	// Attach parent Agent tool_use/tool_result to task bucket.
	for _, e := range entries {
		if e.TaskId != "" {
			continue
		}
		if e.Type == "tool_use" && e.Tool == "Agent" && e.ToolUseId != "" {
			if tid, ok := toolUseIDToTaskID[e.ToolUseId]; ok {
				subagentByTaskID[tid] = append(subagentByTaskID[tid], e)
			}
		} else if e.Type == "tool_result" && e.ToolUseId != "" {
			if tid, ok := toolUseIDToTaskID[e.ToolUseId]; ok {
				subagentByTaskID[tid] = append(subagentByTaskID[tid], e)
			}
		}
	}

	// Phase 2: Index tool_result by toolUseId.
	toolResultByUseID := make(map[string]*pb.ActivityEntry)
	for _, e := range entries {
		if e.Type == "tool_result" && e.ToolUseId != "" {
			if _, exists := toolResultByUseID[e.ToolUseId]; !exists {
				toolResultByUseID[e.ToolUseId] = e
			}
		}
	}

	// Phase 2b: Group by parentCallId.
	childrenByParentCallID := make(map[string][]*pb.ActivityEntry)
	for _, e := range entries {
		if e.ParentCallId != "" {
			childrenByParentCallID[e.ParentCallId] = append(childrenByParentCallID[e.ParentCallId], e)
		}
	}

	// Phase 3: Build groups.
	consumedEntries := make(map[*pb.ActivityEntry]bool)
	emittedTaskIDs := make(map[string]bool)

	// Pre-mark all task-bucketed entries as consumed.
	for _, arr := range subagentByTaskID {
		for _, e := range arr {
			consumedEntries[e] = true
			if e.Type == "tool_use" && e.Tool == "Agent" && e.ToolUseId != "" {
				if result, ok := toolResultByUseID[e.ToolUseId]; ok {
					consumedEntries[result] = true
				}
			}
		}
	}

	var groups []*activityGroup

	for i := 0; i < len(entries); i++ {
		e := entries[i]

		// Consumed by subagent group: emit on first encounter.
		if consumedEntries[e] {
			tid := e.TaskId
			if tid == "" {
				tid = toolUseIDToTaskID[e.ToolUseId]
			}
			if tid != "" && !emittedTaskIDs[tid] {
				emittedTaskIDs[tid] = true
				batch := subagentByTaskID[tid]
				if len(batch) == 0 {
					batch = []*pb.ActivityEntry{e}
				}

				agentType := ""
				desc := ""
				prompt := ""
				for _, b := range batch {
					if agentType == "" && b.SubagentType != "" {
						agentType = b.SubagentType
					}
					if desc == "" {
						desc = cleanSubagentDescription(b.SubagentDescription)
					}
					if prompt == "" && b.SubagentPrompt != "" {
						prompt = b.SubagentPrompt
					}
				}
				last := batch[len(batch)-1]
				status := "running"
				terminal := false
				for _, b := range batch {
					if isTerminalSubagentStatus(b.SubagentStatus) {
						status = b.SubagentStatus
						terminal = true
						break
					}
					if b.Type == "subagent_notification" && isTerminalSubagentStatus(b.Step) {
						status = b.Step
						terminal = true
						break
					}
				}
				if !terminal {
					if findEntry(batch, func(e *pb.ActivityEntry) bool { return e.Type == "subagent_notification" }) != nil {
						status = "completed"
					} else if last.SubagentStatus != "" {
						status = last.SubagentStatus
					}
				}

				groups = append(groups, &activityGroup{
					kind:                "subagent",
					id:                  fmt.Sprintf("task:%s", tid),
					entries:             batch,
					taskID:              tid,
					subagentType:        agentType,
					subagentDescription: desc,
					subagentPrompt:      prompt,
					subagentStatus:      status,
					// Progress snapshots are sparse: model commonly arrives on the
					// start event while usage/cost arrive on later snapshots. Reduce
					// each metric across the whole task instead of reading one event,
					// otherwise live graph updates make previously known values vanish.
					toolCount:          maxInt32(mapInt32(batch, func(e *pb.ActivityEntry) int32 { return e.SubagentToolCount })),
					totalTokens:        maxInt64(mapInt64(batch, func(e *pb.ActivityEntry) int64 { return e.SubagentTotalTokens })),
					durationMs:         maxInt64(mapInt64(batch, func(e *pb.ActivityEntry) int64 { return e.SubagentDurationMs })),
					subagentModel:      firstOf(batch, func(e *pb.ActivityEntry) string { return e.SubagentModel }),
					subagentCostUsd:    maxFloat64(mapFloat64(batch, func(e *pb.ActivityEntry) float64 { return e.SubagentCostUsd })),
					subagentNumTurns:   maxInt32(mapInt32(batch, func(e *pb.ActivityEntry) int32 { return e.SubagentNumTurns })),
					subagentStopReason: lastOf(batch, func(e *pb.ActivityEntry) string { return e.SubagentStopReason }),
					dependsOn:          mergedStringFields(batch, func(e *pb.ActivityEntry) []string { return e.SubagentDependsOn }),
					waitingOn:          latestSubagentWaitingOn(batch, status),
					currentStep:        lastOf(batch, func(e *pb.ActivityEntry) string { return e.SubagentCurrentStep }),
					lastTool:           lastOf(batch, func(e *pb.ActivityEntry) string { return firstNonEmpty(e.SubagentLastTool, e.LastToolName) }),
					filesWritten:       maxInt32(mapInt32(batch, func(e *pb.ActivityEntry) int32 { return e.SubagentFilesWritten })),
					messagesReceived:   maxInt32(mapInt32(batch, func(e *pb.ActivityEntry) int32 { return e.SubagentMessagesReceived })),
					lastParentMessage:  lastOf(batch, func(e *pb.ActivityEntry) string { return e.SubagentLastParentMessage }),
				})
			}
			continue
		}

		// Inline sub-agent: agent_* tool_use with children.
		if e.Type == "tool_use" && e.ToolUseId != "" && strings.HasPrefix(e.Tool, "agent_") {
			children := childrenByParentCallID[e.ToolUseId]
			if len(children) > 0 {
				for _, child := range children {
					consumedEntries[child] = true
					if child.Type == "tool_use" && child.ToolUseId != "" {
						if cr, ok := toolResultByUseID[child.ToolUseId]; ok {
							consumedEntries[cr] = true
						}
					}
				}
				parentResult := toolResultByUseID[e.ToolUseId]
				if parentResult != nil {
					consumedEntries[parentResult] = true
				}
				groups = append(groups, &activityGroup{
					kind:        "inline-subagent",
					id:          fmt.Sprintf("inline:%s", e.ToolUseId),
					parentEntry: e,
					children:    children,
					resultEntry: parentResult,
				})
				continue
			}
		}
	}

	return groups
}

// --- Status derivation ---

func deriveGroupedSubagentStatus(entries []*pb.ActivityEntry, fallback string) string {
	for _, e := range entries {
		if isTerminalSubagentStatus(e.SubagentStatus) {
			return e.SubagentStatus
		}
		if e.Type == "subagent_notification" && isTerminalSubagentStatus(e.Step) {
			return e.Step
		}
	}
	if fallback != "" {
		return fallback
	}
	for _, e := range entries {
		if e.Type == "subagent_started" || e.Type == "subagent_progress" {
			return "running"
		}
	}
	return "completed"
}

func deriveInlineSubagentStatus(parent *pb.ActivityEntry, children []*pb.ActivityEntry, result *pb.ActivityEntry) string {
	if result != nil {
		if result.IsError {
			return "failed"
		}
		return "completed"
	}
	all := append([]*pb.ActivityEntry{parent}, children...)
	for _, e := range all {
		if isTerminalSubagentStatus(e.SubagentStatus) {
			return e.SubagentStatus
		}
		if e.Type == "subagent_notification" && isTerminalSubagentStatus(e.Step) {
			return e.Step
		}
		if e.Type == "tool_result" && e.ParentCallId == parent.ToolUseId {
			if e.IsError {
				return "failed"
			}
			return "completed"
		}
	}
	return "running"
}

func deriveFallbackClusterStatus(entries []*pb.ActivityEntry) string {
	for _, e := range entries {
		if e.Type == "subagent_notification" && isTerminalSubagentStatus(e.Step) {
			return e.Step
		}
	}
	for _, e := range entries {
		if isTerminalSubagentStatus(e.SubagentStatus) {
			return e.SubagentStatus
		}
	}
	for _, e := range entries {
		if e.Type == "tool_result" {
			if e.IsError {
				return "failed"
			}
			return "completed"
		}
	}
	for _, e := range entries {
		if e.Type == "subagent_started" || e.Type == "subagent_progress" {
			return "running"
		}
	}
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		if last.SubagentStatus != "" {
			return last.SubagentStatus
		}
	}
	return "running"
}

// --- Fallback clustering ---

type fallbackCluster struct {
	key     string
	entries []*pb.ActivityEntry
}

func isFallbackSubagentEntry(e *pb.ActivityEntry) bool {
	return e.Type == "subagent_started" ||
		e.Type == "subagent_progress" ||
		e.Type == "subagent_notification" ||
		(e.SubagentType != "" && e.SubagentDescription != "") ||
		(e.SubagentStatus != "" && e.SubagentDescription != "")
}

func hasSubagentGroupingEvidence(e *pb.ActivityEntry) bool {
	if subagentEvidenceTypes[e.Type] {
		return true
	}
	return e.SubagentType != "" ||
		e.SubagentDescription != "" ||
		e.SubagentStatus != "" ||
		e.SubagentModel != "" ||
		e.SubagentPrompt != "" ||
		e.SubagentResultText != ""
}

func fallbackClusterKey(e *pb.ActivityEntry, index int) string {
	if e.TaskId != "" {
		return fmt.Sprintf("task:%s", e.TaskId)
	}
	if e.ToolUseId != "" {
		return fmt.Sprintf("tool:%s", e.ToolUseId)
	}
	if e.ParentCallId != "" {
		return fmt.Sprintf("parent:%s", e.ParentCallId)
	}
	label := firstNonEmpty(e.SubagentDescription, e.SubagentType, e.Message, e.Type)
	if label != "" {
		return fmt.Sprintf("label:%s", label)
	}
	return fmt.Sprintf("index:%d", index)
}

func buildFallbackClusters(entries []*pb.ActivityEntry, represented map[*pb.ActivityEntry]bool) []fallbackCluster {
	var clusters []fallbackCluster
	latestByKey := make(map[string]int)

	for i, e := range entries {
		if represented[e] || !isFallbackSubagentEntry(e) {
			continue
		}
		key := fallbackClusterKey(e, i)
		existingIdx, exists := latestByKey[key]
		if !exists || shouldSplitFallbackCluster(clusters[existingIdx].entries, e) {
			clusters = append(clusters, fallbackCluster{
				key:     fmt.Sprintf("%s#%d", key, len(clusters)),
				entries: []*pb.ActivityEntry{e},
			})
			latestByKey[key] = len(clusters) - 1
		} else {
			clusters[existingIdx].entries = append(clusters[existingIdx].entries, e)
		}
	}
	return clusters
}

func shouldSplitFallbackCluster(cluster []*pb.ActivityEntry, next *pb.ActivityEntry) bool {
	if len(cluster) == 0 {
		return true
	}
	last := cluster[len(cluster)-1]
	lastStatus := last.SubagentStatus
	if lastStatus == "" && last.Type == "subagent_notification" {
		lastStatus = last.Step
	}
	if lastStatus == "" && last.Type == "tool_result" {
		if last.IsError {
			lastStatus = "failed"
		} else {
			lastStatus = "completed"
		}
	}
	if lastStatus == "" && (last.Type == "subagent_started" || last.Type == "subagent_progress") {
		lastStatus = "running"
	}
	newStart := next.Type == "subagent_started" || next.Type == "tool_use"
	return isTerminalSubagentStatus(lastStatus) && newStart
}

// --- Helpers ---

func buildDescription(entries []*pb.ActivityEntry) string {
	for _, e := range entries {
		if e.Message != "" {
			return e.Message
		}
		if e.Tool != "" {
			return e.Tool
		}
		if e.Type != "" {
			return e.Type
		}
	}
	return "Observed subagent activity from grouped telemetry."
}

func pickRepresentativeEntry(entries []*pb.ActivityEntry) *pb.ActivityEntry {
	if e := findEntry(entries, func(e *pb.ActivityEntry) bool { return e.Type == "subagent_notification" }); e != nil {
		return e
	}
	if e := findEntry(entries, func(e *pb.ActivityEntry) bool { return e.SubagentDescription != "" || e.SubagentType != "" }); e != nil {
		return e
	}
	if len(entries) > 0 {
		return entries[len(entries)-1]
	}
	return nil
}

func humanizeEventType(t string) string {
	t = strings.ReplaceAll(t, "_", " ")
	if len(t) == 0 {
		return t
	}
	words := strings.Fields(t)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func sumEntryTokens(e *pb.ActivityEntry) int64 {
	if e.SubagentTotalTokens > 0 {
		return e.SubagentTotalTokens
	}
	return e.InputTokens + e.OutputTokens
}

func findEntry(entries []*pb.ActivityEntry, pred func(*pb.ActivityEntry) bool) *pb.ActivityEntry {
	for _, e := range entries {
		if pred(e) {
			return e
		}
	}
	return nil
}

func firstOf(entries []*pb.ActivityEntry, fn func(*pb.ActivityEntry) string) string {
	for _, e := range entries {
		if v := fn(e); v != "" {
			return v
		}
	}
	return ""
}

func lastOf(entries []*pb.ActivityEntry, fn func(*pb.ActivityEntry) string) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if v := fn(entries[i]); v != "" {
			return v
		}
	}
	return ""
}

func mergedStringFields(entries []*pb.ActivityEntry, fn func(*pb.ActivityEntry) []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		for _, v := range fn(e) {
			v = strings.TrimSpace(v)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func entryIndices(entries []*pb.ActivityEntry, indexMap map[*pb.ActivityEntry]int32) []int32 {
	seen := make(map[int32]bool, len(entries))
	var indices []int32
	for _, e := range entries {
		if idx, ok := indexMap[e]; ok && !seen[idx] {
			seen[idx] = true
			indices = append(indices, idx)
		}
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	return indices
}

// entryEventIDs returns the durable event IDs behind a node's detail entries.
// Unlike entryIndices (positions in one specific response), event IDs stay
// valid for paginated/delta clients whose local buffer is a different slice
// of the run's history.
func entryEventIDs(entries []*pb.ActivityEntry) []int64 {
	seen := make(map[int64]bool, len(entries))
	var ids []int64
	for _, e := range entries {
		if e.EventId == 0 || seen[e.EventId] {
			continue
		}
		seen[e.EventId] = true
		ids = append(ids, e.EventId)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func mapInt32(entries []*pb.ActivityEntry, fn func(*pb.ActivityEntry) int32) []int32 {
	out := make([]int32, len(entries))
	for i, e := range entries {
		out[i] = fn(e)
	}
	return out
}

func mapInt64(entries []*pb.ActivityEntry, fn func(*pb.ActivityEntry) int64) []int64 {
	out := make([]int64, len(entries))
	for i, e := range entries {
		out[i] = fn(e)
	}
	return out
}

func mapFloat64(entries []*pb.ActivityEntry, fn func(*pb.ActivityEntry) float64) []float64 {
	out := make([]float64, len(entries))
	for i, e := range entries {
		out[i] = fn(e)
	}
	return out
}

func maxInt32(vals []int32) int32 {
	m := int32(0)
	for _, v := range vals {
		if v > m {
			m = v
		}
	}
	return m
}

func maxInt64(vals []int64) int64 {
	m := int64(0)
	for _, v := range vals {
		if v > m {
			m = v
		}
	}
	return m
}

func maxFloat64(vals []float64) float64 {
	m := 0.0
	for _, v := range vals {
		if v > m {
			m = v
		}
	}
	return m
}
