package plantasks

import (
	"fmt"
	"strings"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/contracts"
)

type TransitionError struct {
	Code           apperrors.Code
	TaskID         string
	FromStatus     string
	ToStatus       string
	CurrentTaskID  string
	BlockingTaskID string
}

func (e *TransitionError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: task %s cannot transition from %s to %s", e.Code, e.TaskID, e.FromStatus, e.ToStatus)
}

func ApplyTaskUpdate(state *contracts.PlanRuntimeState, taskID string, toStatus string, description string) *TransitionError {
	if state == nil {
		return &TransitionError{Code: apperrors.CodeInvalidPlanTaskTransition, TaskID: strings.TrimSpace(taskID), ToStatus: contracts.NormalizePlanTaskStatus(toStatus)}
	}
	taskID = strings.TrimSpace(taskID)
	toStatus = contracts.NormalizePlanTaskStatus(toStatus)
	taskIndex := -1
	for index := range state.Tasks {
		if strings.TrimSpace(state.Tasks[index].TaskID) == taskID {
			taskIndex = index
			break
		}
	}
	if taskIndex < 0 {
		return &TransitionError{Code: apperrors.CodeInvalidPlanTaskTransition, TaskID: taskID, ToStatus: toStatus}
	}

	fromStatus := contracts.NormalizePlanTaskStatus(state.Tasks[taskIndex].Status)
	currentTaskID, multipleActive := currentTask(state)
	failure := func(code apperrors.Code, blockingTaskID string) *TransitionError {
		return &TransitionError{
			Code:           code,
			TaskID:         taskID,
			FromStatus:     fromStatus,
			ToStatus:       toStatus,
			CurrentTaskID:  currentTaskID,
			BlockingTaskID: strings.TrimSpace(blockingTaskID),
		}
	}

	if multipleActive {
		return failure(apperrors.CodeInvalidPlanTaskTransition, currentTaskID)
	}
	if fromStatus == "" || toStatus == "" {
		return failure(apperrors.CodeInvalidPlanTaskTransition, "")
	}
	if fromStatus == toStatus {
		applyTaskMutation(state, taskIndex, toStatus, description)
		return nil
	}

	switch fromStatus {
	case "init":
		if toStatus != "in_progress" && !IsTerminalStatus(toStatus) {
			return failure(apperrors.CodeInvalidPlanTaskTransition, "")
		}
		if IsTerminalStatus(toStatus) && currentTaskID != "" {
			return failure(apperrors.CodePlanTaskNotCurrent, currentTaskID)
		}
		if blockingTaskID := precedingNonTerminalTaskID(state, taskIndex); blockingTaskID != "" {
			return failure(apperrors.CodePlanTaskPredecessorIncomplete, blockingTaskID)
		}
		if currentTaskID != "" {
			return failure(apperrors.CodeInvalidPlanTaskTransition, currentTaskID)
		}
	case "in_progress":
		if !IsTerminalStatus(toStatus) {
			return failure(apperrors.CodeInvalidPlanTaskTransition, "")
		}
		if currentTaskID != taskID {
			return failure(apperrors.CodePlanTaskNotCurrent, currentTaskID)
		}
	default:
		return failure(apperrors.CodeInvalidPlanTaskTransition, "")
	}

	applyTaskMutation(state, taskIndex, toStatus, description)
	return nil
}

func ReconcileState(state *contracts.PlanRuntimeState) {
	if state == nil {
		return
	}
	state.ActiveTaskID = ""
	predecessorsTerminal := true
	for index := range state.Tasks {
		status := contracts.NormalizePlanTaskStatus(state.Tasks[index].Status)
		state.Tasks[index].Status = status
		if status == "in_progress" {
			if state.ActiveTaskID == "" && predecessorsTerminal {
				state.ActiveTaskID = strings.TrimSpace(state.Tasks[index].TaskID)
			} else {
				state.Tasks[index].Status = "init"
				status = "init"
			}
		}
		if !IsTerminalStatus(status) {
			predecessorsTerminal = false
		}
	}
}

func CurrentTaskID(state *contracts.PlanRuntimeState) string {
	currentTaskID, _ := currentTask(state)
	return currentTaskID
}

func IsTerminalStatus(status string) bool {
	switch contracts.NormalizePlanTaskStatus(status) {
	case "completed", "failed", "canceled":
		return true
	default:
		return false
	}
}

func currentTask(state *contracts.PlanRuntimeState) (string, bool) {
	if state == nil {
		return "", false
	}
	currentTaskID := ""
	for _, task := range state.Tasks {
		if contracts.NormalizePlanTaskStatus(task.Status) != "in_progress" {
			continue
		}
		if currentTaskID != "" {
			return currentTaskID, true
		}
		currentTaskID = strings.TrimSpace(task.TaskID)
	}
	return currentTaskID, false
}

func precedingNonTerminalTaskID(state *contracts.PlanRuntimeState, taskIndex int) string {
	for index := 0; index < taskIndex; index++ {
		if !IsTerminalStatus(state.Tasks[index].Status) {
			return strings.TrimSpace(state.Tasks[index].TaskID)
		}
	}
	return ""
}

func applyTaskMutation(state *contracts.PlanRuntimeState, taskIndex int, status string, description string) {
	state.Tasks[taskIndex].Status = status
	if description = strings.TrimSpace(description); description != "" {
		state.Tasks[taskIndex].Description = description
	}
	state.ActiveTaskID = CurrentTaskID(state)
}
