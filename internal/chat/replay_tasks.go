package chat

import (
	"sort"
	"strings"

	"agent-platform/internal/stream"
)

type chatRunData struct {
	runID                           string
	events                          []stream.EventData
	totalPromptTokens               int
	totalCompletionTokens           int
	totalTotalTokens                int
	totalCachedTokens               int
	totalReasoningTokens            int
	totalPromptCacheHitTokens       int
	totalPromptCacheMissTokens      int
	totalLlmChatCompletionCount     int
	totalToolCallCount              int
	totalFirstTokenLatencyMs        int64
	totalFirstTokenLatencyCount     int
	totalGenerationDurationMs       int64
	estimatedCostCurrency           string
	estimatedCostInputHit           float64
	estimatedCostInputMiss          float64
	estimatedCostOutput             float64
	estimatedCostTotal              float64
	chatTotalPromptTokens           int
	chatTotalCompletionTokens       int
	chatTotalTotalTokens            int
	chatTotalCachedTokens           int
	chatTotalReasoningTokens        int
	chatTotalPromptCacheHitTokens   int
	chatTotalPromptCacheMissTokens  int
	chatTotalLlmChatCompletionCount int
	chatTotalToolCallCount          int
	chatTotalFirstTokenLatencyMs    int64
	chatTotalFirstTokenLatencyCount int
	chatTotalGenerationDurationMs   int64
	activeSubTasks                  map[string]*replayedSubTask
}

type replayedSubTask struct {
	TaskID        string
	TaskName      string
	TaskDesc      string
	SubAgentKey   string
	MainToolID    string
	Status        string
	LastTimestamp int64
}

type replayedSubTaskQuery struct {
	TaskID       string
	TaskName     string
	TaskDesc     string
	SubAgentKey  string
	MainToolID   string
	TeamID       string
	Presentation string
	RootContent  bool
}

func decorateReplayedTeamTaskEvents(events []stream.EventData, teamID string, agentKey string, presentation string) []stream.EventData {
	teamID = strings.TrimSpace(teamID)
	agentKey = strings.TrimSpace(agentKey)
	presentation = strings.TrimSpace(presentation)
	if teamID == "" {
		return events
	}
	if presentation == "" {
		presentation = "task"
	}
	for index := range events {
		if events[index].Payload == nil {
			events[index].Payload = map[string]any{}
		}
		events[index].Payload["teamId"] = teamID
		events[index].Payload["presentation"] = presentation
		actor := map[string]any{"type": "agent", "teamId": teamID}
		if agentKey != "" {
			actor["agentKey"] = agentKey
		}
		events[index].Payload["actor"] = actor
	}
	return events
}

func replayedTaskQueryKey(runID string, taskID string) string {
	return strings.TrimSpace(runID) + "\x00" + strings.TrimSpace(taskID)
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return text
}

func ensureRun(runs map[string]*chatRunData, order *[]string, runID string) *chatRunData {
	if rd, ok := runs[runID]; ok {
		return rd
	}
	rd := &chatRunData{runID: runID}
	runs[runID] = rd
	*order = append(*order, runID)
	return rd
}

func beginReplayedSubTask(rd *chatRunData, runID string, taskID string, taskName string, taskDescription string, taskSubAgentKey string, taskMainToolID string, ts int64, nextSeq func() int64) []stream.EventData {
	if rd == nil {
		return nil
	}
	var events []stream.EventData
	isCurrentSubTask := strings.TrimSpace(taskID) != "" && strings.TrimSpace(taskSubAgentKey) != ""
	if !isCurrentSubTask {
		return nil
	}
	if rd.activeSubTasks == nil {
		rd.activeSubTasks = map[string]*replayedSubTask{}
	}
	active := rd.activeSubTasks[taskID]
	if active == nil {
		active = &replayedSubTask{
			TaskID:        taskID,
			TaskName:      taskName,
			TaskDesc:      taskDescription,
			SubAgentKey:   taskSubAgentKey,
			MainToolID:    taskMainToolID,
			LastTimestamp: ts,
		}
		rd.activeSubTasks[taskID] = active
		events = append(events, stream.EventData{
			Seq:       nextSeq(),
			Type:      "task.start",
			Timestamp: ts,
			Payload: map[string]any{
				"taskId":         taskID,
				"runId":          runID,
				"taskName":       taskName,
				"description":    taskDescription,
				"subAgentKey":    taskSubAgentKey,
				"invokingToolId": taskMainToolID,
			},
		})
	}
	if strings.TrimSpace(taskName) != "" {
		active.TaskName = taskName
	}
	if strings.TrimSpace(taskDescription) != "" {
		active.TaskDesc = taskDescription
	}
	if strings.TrimSpace(taskMainToolID) != "" {
		active.MainToolID = taskMainToolID
	}
	active.LastTimestamp = ts
	return events
}

func finishReplayedSubTaskIfTerminal(rd *chatRunData, runID string, taskID string, taskStatus string, ts int64, nextSeq func() int64) []stream.EventData {
	if rd == nil || strings.TrimSpace(taskID) == "" {
		return nil
	}
	active := rd.activeSubTasks[taskID]
	if active == nil {
		return nil
	}
	if strings.TrimSpace(taskStatus) != "" {
		active.Status = taskStatus
	}
	active.LastTimestamp = ts
	if isTerminalSubTaskStatus(active.Status) {
		events := synthesizeReplayedSubTaskTerminal(runID, active, nextSeq)
		delete(rd.activeSubTasks, taskID)
		return events
	}
	return nil
}

func flushReplayedSubTask(rd *chatRunData, nextSeq func() int64) []stream.EventData {
	if rd == nil || len(rd.activeSubTasks) == 0 {
		return nil
	}
	taskIDs := make([]string, 0, len(rd.activeSubTasks))
	for taskID := range rd.activeSubTasks {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	events := make([]stream.EventData, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		events = append(events, synthesizeReplayedSubTaskTerminal(rd.runID, rd.activeSubTasks[taskID], nextSeq)...)
		delete(rd.activeSubTasks, taskID)
	}
	return events
}

func synthesizeReplayedSubTaskTerminal(runID string, task *replayedSubTask, nextSeq func() int64) []stream.EventData {
	if task == nil {
		return nil
	}
	status := strings.TrimSpace(task.Status)
	if status == "" {
		status = "completed"
	}
	switch status {
	case "cancelled", "canceled":
		return []stream.EventData{{
			Seq:       nextSeq(),
			Type:      "task.cancel",
			Timestamp: task.LastTimestamp,
			Payload: map[string]any{
				"taskId": task.TaskID,
			},
		}}
	case "error", "failed", "fail":
		return []stream.EventData{{
			Seq:       nextSeq(),
			Type:      "task.error",
			Timestamp: task.LastTimestamp,
			Payload: map[string]any{
				"taskId": task.TaskID,
				"error": map[string]any{
					"code":     "sub_agent_failed",
					"message":  "sub-agent failed",
					"scope":    "task",
					"category": "system",
				},
			},
		}}
	default:
		return []stream.EventData{{
			Seq:       nextSeq(),
			Type:      "task.complete",
			Timestamp: task.LastTimestamp,
			Payload: map[string]any{
				"taskId": task.TaskID,
			},
		}}
	}
}

func isTerminalSubTaskStatus(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "completed", "complete", "cancelled", "canceled", "error", "failed", "fail":
		return true
	default:
		return false
	}
}
