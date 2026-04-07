package chat

import (
	"fmt"
	"strings"
)

type snapshotAssembly struct {
	chatStart *map[string]any
	runs      map[string]*snapshotRunBucket
	ordered   []*snapshotRunBucket
}

type snapshotRunBucket struct {
	runID    string
	requests []map[string]any
	starts   []map[string]any
	body     []map[string]any
}

type runReplayNormalizer struct {
	runID string

	reasoningSeq    int
	contentSeq      int
	toolSeq         int
	actionSeq       int
	toolResultSeq   int
	actionResultSeq int

	activeReasoningID string
	lastReasoningID   string
	activeContentID   string
	lastContentID     string
	lastToolID        string
	lastActionID      string

	openTools   []string
	openActions []string
}

func rebuildSnapshotEvents(events []map[string]any) []map[string]any {
	if len(events) == 0 {
		return nil
	}

	assembly := snapshotAssembly{runs: map[string]*snapshotRunBucket{}}
	var pendingRequests []map[string]any
	currentRunID := ""

	for _, raw := range events {
		if raw == nil {
			continue
		}
		event := cloneEventMap(raw)
		eventType := stringValue(event["type"])
		if eventType == "" {
			continue
		}
		if eventType == "chat.start" {
			if assembly.chatStart == nil {
				assembly.chatStart = &event
			}
			continue
		}

		runID := stringValue(event["runId"])
		if runID == "" && eventType != "request.query" && eventType != "run.start" && currentRunID != "" {
			runID = currentRunID
			event["runId"] = runID
		}

		switch eventType {
		case "request.query":
			if runID == "" {
				pendingRequests = append(pendingRequests, event)
				continue
			}
			assembly.bucket(runID).requests = append(assembly.bucket(runID).requests, event)
		case "run.start":
			if runID == "" {
				continue
			}
			bucket := assembly.bucket(runID)
			if len(pendingRequests) > 0 {
				request := pendingRequests[0]
				pendingRequests = pendingRequests[1:]
				request["runId"] = runID
				bucket.requests = append(bucket.requests, request)
			}
			bucket.starts = append(bucket.starts, event)
			currentRunID = runID
		default:
			if runID == "" {
				continue
			}
			assembly.bucket(runID).body = append(assembly.bucket(runID).body, event)
			if isTerminalRunEvent(eventType) && currentRunID == runID {
				currentRunID = ""
			}
		}
	}

	rebuilt := make([]map[string]any, 0, len(events))
	if assembly.chatStart != nil {
		rebuilt = append(rebuilt, *assembly.chatStart)
	}
	for _, request := range pendingRequests {
		rebuilt = append(rebuilt, request)
	}
	for _, bucket := range assembly.ordered {
		normalizer := newRunReplayNormalizer(bucket.runID)
		rebuilt = append(rebuilt, normalizer.normalize(bucket)...)
	}
	for index := range rebuilt {
		rebuilt[index]["seq"] = int64(index + 1)
	}
	return rebuilt
}

func (a *snapshotAssembly) bucket(runID string) *snapshotRunBucket {
	if bucket, ok := a.runs[runID]; ok {
		return bucket
	}
	bucket := &snapshotRunBucket{runID: runID}
	a.runs[runID] = bucket
	a.ordered = append(a.ordered, bucket)
	return bucket
}

func newRunReplayNormalizer(runID string) *runReplayNormalizer {
	return &runReplayNormalizer{runID: runID}
}

func (n *runReplayNormalizer) normalize(bucket *snapshotRunBucket) []map[string]any {
	out := make([]map[string]any, 0, len(bucket.requests)+len(bucket.starts)+len(bucket.body))
	for _, event := range bucket.requests {
		event["runId"] = n.runID
		out = append(out, event)
	}
	for _, event := range bucket.starts {
		event["runId"] = n.runID
		out = append(out, event)
	}
	for _, event := range bucket.body {
		event["runId"] = n.runID
		n.normalizeEvent(event)
		out = append(out, event)
	}
	return out
}

func (n *runReplayNormalizer) normalizeEvent(event map[string]any) {
	switch stringValue(event["type"]) {
	case "reasoning.start", "reasoning.delta", "reasoning.end", "reasoning.snapshot":
		n.normalizeReasoningEvent(event)
	case "content.start", "content.delta", "content.end", "content.snapshot":
		n.normalizeContentEvent(event)
	case "tool.start", "tool.args", "tool.end", "tool.snapshot", "tool.result":
		n.normalizeToolEvent(event)
	case "action.start", "action.args", "action.end", "action.result":
		n.normalizeActionEvent(event)
	}
}

func (n *runReplayNormalizer) normalizeReasoningEvent(event map[string]any) {
	eventType := stringValue(event["type"])
	reasoningID := stringValue(event["reasoningId"])
	switch eventType {
	case "reasoning.start", "reasoning.delta":
		if reasoningID == "" {
			if n.activeReasoningID == "" {
				n.activeReasoningID = n.nextReasoningID()
			}
			reasoningID = n.activeReasoningID
		} else {
			n.activeReasoningID = reasoningID
		}
		n.lastReasoningID = reasoningID
	case "reasoning.end":
		if reasoningID == "" {
			switch {
			case n.activeReasoningID != "":
				reasoningID = n.activeReasoningID
			case n.lastReasoningID != "":
				reasoningID = n.lastReasoningID
			default:
				reasoningID = n.nextReasoningID()
			}
		}
		n.lastReasoningID = reasoningID
		if n.activeReasoningID == reasoningID || n.activeReasoningID == "" {
			n.activeReasoningID = ""
		}
	case "reasoning.snapshot":
		if reasoningID == "" {
			switch {
			case n.lastReasoningID != "":
				reasoningID = n.lastReasoningID
			case n.activeReasoningID != "":
				reasoningID = n.activeReasoningID
			default:
				reasoningID = n.nextReasoningID()
			}
		}
		n.lastReasoningID = reasoningID
	}
	event["reasoningId"] = reasoningID
}

func (n *runReplayNormalizer) normalizeContentEvent(event map[string]any) {
	eventType := stringValue(event["type"])
	contentID := stringValue(event["contentId"])
	switch eventType {
	case "content.start", "content.delta":
		if contentID == "" {
			if n.activeContentID == "" {
				n.activeContentID = n.nextContentID()
			}
			contentID = n.activeContentID
		} else {
			n.activeContentID = contentID
		}
		n.lastContentID = contentID
	case "content.end":
		if contentID == "" {
			switch {
			case n.activeContentID != "":
				contentID = n.activeContentID
			case n.lastContentID != "":
				contentID = n.lastContentID
			default:
				contentID = n.nextContentID()
			}
		}
		n.lastContentID = contentID
		if n.activeContentID == contentID || n.activeContentID == "" {
			n.activeContentID = ""
		}
	case "content.snapshot":
		if contentID == "" {
			switch {
			case n.lastContentID != "":
				contentID = n.lastContentID
			case n.activeContentID != "":
				contentID = n.activeContentID
			default:
				contentID = n.nextContentID()
			}
		}
		n.lastContentID = contentID
	}
	event["contentId"] = contentID
}

func (n *runReplayNormalizer) normalizeToolEvent(event map[string]any) {
	eventType := stringValue(event["type"])
	toolID := stringValue(event["toolId"])
	switch eventType {
	case "tool.start":
		if toolID == "" {
			toolID = n.nextToolID()
		}
		n.openTool(toolID)
		n.lastToolID = toolID
	case "tool.args":
		if toolID == "" {
			if current := n.currentOpenTool(); current != "" {
				toolID = current
			} else {
				toolID = n.nextToolID()
				n.openTool(toolID)
			}
		}
		if !n.hasOpenTool(toolID) {
			n.openTool(toolID)
		}
		n.lastToolID = toolID
	case "tool.snapshot":
		if toolID == "" {
			switch {
			case n.currentOpenTool() != "":
				toolID = n.currentOpenTool()
			case n.lastToolID != "":
				toolID = n.lastToolID
			default:
				toolID = n.nextToolID()
			}
		}
		n.lastToolID = toolID
	case "tool.end":
		if toolID == "" {
			switch {
			case n.currentOpenTool() != "":
				toolID = n.currentOpenTool()
			case n.lastToolID != "":
				toolID = n.lastToolID
			default:
				toolID = n.nextToolID()
			}
		}
		n.closeTool(toolID)
		n.lastToolID = toolID
	case "tool.result":
		if toolID == "" {
			if current := n.currentOpenTool(); current != "" {
				toolID = current
			} else {
				toolID = n.nextToolResultID()
			}
		}
		n.closeTool(toolID)
	}
	event["toolId"] = toolID
}

func (n *runReplayNormalizer) normalizeActionEvent(event map[string]any) {
	eventType := stringValue(event["type"])
	actionID := stringValue(event["actionId"])
	switch eventType {
	case "action.start":
		if actionID == "" {
			actionID = n.nextActionID()
		}
		n.openAction(actionID)
		n.lastActionID = actionID
	case "action.args":
		if actionID == "" {
			if current := n.currentOpenAction(); current != "" {
				actionID = current
			} else {
				actionID = n.nextActionID()
				n.openAction(actionID)
			}
		}
		if !n.hasOpenAction(actionID) {
			n.openAction(actionID)
		}
		n.lastActionID = actionID
	case "action.end":
		if actionID == "" {
			switch {
			case n.currentOpenAction() != "":
				actionID = n.currentOpenAction()
			case n.lastActionID != "":
				actionID = n.lastActionID
			default:
				actionID = n.nextActionID()
			}
		}
		n.closeAction(actionID)
		n.lastActionID = actionID
	case "action.result":
		if actionID == "" {
			if current := n.currentOpenAction(); current != "" {
				actionID = current
			} else {
				actionID = n.nextActionResultID()
			}
		}
		n.closeAction(actionID)
	}
	event["actionId"] = actionID
}

func (n *runReplayNormalizer) nextReasoningID() string {
	n.reasoningSeq++
	return fmt.Sprintf("%s_r_%d", n.runID, n.reasoningSeq)
}

func (n *runReplayNormalizer) nextContentID() string {
	n.contentSeq++
	return fmt.Sprintf("%s_c_%d", n.runID, n.contentSeq)
}

func (n *runReplayNormalizer) nextToolID() string {
	n.toolSeq++
	return fmt.Sprintf("%s_tool_%d", n.runID, n.toolSeq)
}

func (n *runReplayNormalizer) nextActionID() string {
	n.actionSeq++
	return fmt.Sprintf("%s_action_%d", n.runID, n.actionSeq)
}

func (n *runReplayNormalizer) nextToolResultID() string {
	n.toolResultSeq++
	return fmt.Sprintf("%s_tool_result_%d", n.runID, n.toolResultSeq)
}

func (n *runReplayNormalizer) nextActionResultID() string {
	n.actionResultSeq++
	return fmt.Sprintf("%s_action_result_%d", n.runID, n.actionResultSeq)
}

func (n *runReplayNormalizer) currentOpenTool() string {
	if len(n.openTools) == 0 {
		return ""
	}
	return n.openTools[len(n.openTools)-1]
}

func (n *runReplayNormalizer) currentOpenAction() string {
	if len(n.openActions) == 0 {
		return ""
	}
	return n.openActions[len(n.openActions)-1]
}

func (n *runReplayNormalizer) openTool(toolID string) {
	if toolID == "" || n.hasOpenTool(toolID) {
		return
	}
	n.openTools = append(n.openTools, toolID)
}

func (n *runReplayNormalizer) openAction(actionID string) {
	if actionID == "" || n.hasOpenAction(actionID) {
		return
	}
	n.openActions = append(n.openActions, actionID)
}

func (n *runReplayNormalizer) closeTool(toolID string) {
	if toolID == "" {
		return
	}
	for index := len(n.openTools) - 1; index >= 0; index-- {
		if n.openTools[index] != toolID {
			continue
		}
		n.openTools = append(n.openTools[:index], n.openTools[index+1:]...)
		return
	}
}

func (n *runReplayNormalizer) closeAction(actionID string) {
	if actionID == "" {
		return
	}
	for index := len(n.openActions) - 1; index >= 0; index-- {
		if n.openActions[index] != actionID {
			continue
		}
		n.openActions = append(n.openActions[:index], n.openActions[index+1:]...)
		return
	}
}

func (n *runReplayNormalizer) hasOpenTool(toolID string) bool {
	for _, candidate := range n.openTools {
		if candidate == toolID {
			return true
		}
	}
	return false
}

func (n *runReplayNormalizer) hasOpenAction(actionID string) bool {
	for _, candidate := range n.openActions {
		if candidate == actionID {
			return true
		}
	}
	return false
}

func isTerminalRunEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "run.complete", "run.cancel", "run.error":
		return true
	default:
		return false
	}
}
