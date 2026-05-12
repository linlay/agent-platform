package chat

import (
	"sort"
	"strings"

	"agent-platform-runner-go/internal/stream"
)

type chatRunData struct {
	runID                     string
	agentKey                  string
	events                    []stream.EventData
	totalPromptTokens         int
	totalCompletionTokens     int
	totalTotalTokens          int
	chatTotalPromptTokens     int
	chatTotalCompletionTokens int
	chatTotalTotalTokens      int
	activeSubTasks            map[string]*replayedSubTask
}

type replayedSubTask struct {
	TaskID        string
	GroupID       string
	TaskName      string
	TaskDesc      string
	SubAgentKey   string
	MainToolID    string
	Status        string
	LastTimestamp int64
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

func reconcileReplayedSubTask(rd *chatRunData, runID string, taskID string, taskGroupID string, taskName string, taskDescription string, taskStatus string, taskSubAgentKey string, taskMainToolID string, ts int64, nextSeq func() int64) []stream.EventData {
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
			GroupID:       taskGroupID,
			TaskName:      taskName,
			TaskDesc:      taskDescription,
			SubAgentKey:   taskSubAgentKey,
			MainToolID:    taskMainToolID,
			Status:        taskStatus,
			LastTimestamp: ts,
		}
		rd.activeSubTasks[taskID] = active
		events = append(events, stream.EventData{
			Seq:       nextSeq(),
			Type:      "task.start",
			Timestamp: ts,
			Payload: map[string]any{
				"taskId":      taskID,
				"runId":       runID,
				"groupId":     taskGroupID,
				"taskName":    taskName,
				"description": taskDescription,
				"subAgentKey": taskSubAgentKey,
				"mainToolId":  taskMainToolID,
			},
		})
	}
	if strings.TrimSpace(taskGroupID) != "" {
		active.GroupID = taskGroupID
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
	if strings.TrimSpace(taskStatus) != "" {
		active.Status = taskStatus
	}
	active.LastTimestamp = ts
	if isTerminalSubTaskStatus(active.Status) {
		events = append(events, synthesizeReplayedSubTaskTerminal(runID, active, nextSeq)...)
		delete(rd.activeSubTasks, taskID)
	}
	return events
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
	case "cancelled":
		return []stream.EventData{{
			Seq:       nextSeq(),
			Type:      "task.cancel",
			Timestamp: task.LastTimestamp,
			Payload: map[string]any{
				"taskId":  task.TaskID,
				"groupId": task.GroupID,
				"status":  "cancelled",
			},
		}}
	case "error":
		return []stream.EventData{{
			Seq:       nextSeq(),
			Type:      "task.fail",
			Timestamp: task.LastTimestamp,
			Payload: map[string]any{
				"taskId":  task.TaskID,
				"groupId": task.GroupID,
				"status":  "error",
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
				"taskId":  task.TaskID,
				"groupId": task.GroupID,
				"status":  "completed",
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
