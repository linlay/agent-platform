package automation

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"agent-platform/internal/chat"
)

type Broadcaster interface {
	Broadcast(eventType string, data map[string]any)
}

type Dispatcher struct {
	dispatch   DispatchFunc
	executions ExecutionRecorder
}

func NewDispatcher(dispatch DispatchFunc, _ Broadcaster, executions ExecutionRecorder) *Dispatcher {
	return &Dispatcher{dispatch: dispatch, executions: executions}
}

func (d *Dispatcher) Dispatch(ctx context.Context, def Definition, zoneID string) error {
	if d == nil || d.dispatch == nil {
		return nil
	}
	if !def.Enabled {
		return nil
	}
	startedAt := time.Now()
	triggeredAt := startedAt.Format(time.RFC3339)
	log.Printf(
		"[automation] dispatch start id=%s name=%s agentKey=%s teamId=%s source=%s triggeredAt=%s",
		def.ID, def.Name, def.AgentKey, def.TeamID, def.SourceFile, triggeredAt,
	)

	execution := Execution{
		ID:             NewExecutionID(),
		AutomationID:   strings.TrimSpace(def.ID),
		AutomationName: strings.TrimSpace(def.Name),
		SourceFile:     strings.TrimSpace(def.SourceFile),
		AgentKey:       strings.TrimSpace(def.AgentKey),
		TeamID:         strings.TrimSpace(def.TeamID),
		ZoneID:         strings.TrimSpace(zoneID),
		QueryContent:   def.Query.Message,
		Status:         ExecutionStatusRunning,
		StartedAt:      startedAt.UnixMilli(),
	}
	d.submitExecution(execution)

	hooks := QueryRunHooks{OnRunStarted: func(start chat.RunStart) {
		bound := cloneExecution(execution)
		bound.ChatID = strings.TrimSpace(start.ChatID)
		bound.RunID = strings.TrimSpace(start.RunID)
		if start.StartedAtMillis > 0 {
			bound.RunStartedAt = executionInt64Ptr(start.StartedAtMillis)
		}
		d.submitExecution(bound)
	}}
	result, queryErr := d.dispatch(ctx, def.ToQueryRequest(), hooks)
	completed := completeExecution(execution, result.Completion, result.ErrorMessage, queryErr)
	d.submitExecution(completed)

	returnedErr := queryErr
	if returnedErr == nil {
		switch completed.Status {
		case ExecutionStatusFailed:
			message := strings.TrimSpace(completed.Error)
			if message == "" {
				message = "automation query failed"
			}
			returnedErr = fmt.Errorf("%s", message)
		case ExecutionStatusCanceled:
			message := strings.TrimSpace(completed.Error)
			if message == "" {
				message = "automation query canceled"
			}
			returnedErr = fmt.Errorf("%s", message)
		}
	}
	if returnedErr != nil {
		log.Printf(
			"[automation] dispatch failed id=%s name=%s agentKey=%s teamId=%s source=%s triggeredAt=%s duration=%s err=%v",
			def.ID, def.Name, def.AgentKey, def.TeamID, def.SourceFile, triggeredAt,
			time.Since(startedAt).Round(time.Millisecond), returnedErr,
		)
		return returnedErr
	}
	log.Printf(
		"[automation] dispatch success id=%s name=%s agentKey=%s teamId=%s source=%s triggeredAt=%s duration=%s",
		def.ID, def.Name, def.AgentKey, def.TeamID, def.SourceFile, triggeredAt,
		time.Since(startedAt).Round(time.Millisecond),
	)
	return nil
}

func (d *Dispatcher) submitExecution(item Execution) {
	if d == nil || d.executions == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("[automation] execution history submit panic recovered executionID=%s err=%v", item.ID, recovered)
		}
	}()
	d.executions.Submit(item)
}
