package automation

import (
	"context"
	"log"
	"time"

	"agent-platform/internal/api"
)

type DispatchFunc func(ctx context.Context, req api.QueryRequest) error

type Broadcaster interface {
	Broadcast(eventType string, data map[string]any)
}

type Dispatcher struct {
	dispatch   DispatchFunc
	executions *ExecutionStore
}

func NewDispatcher(dispatch DispatchFunc, _ Broadcaster, executions *ExecutionStore) *Dispatcher {
	return &Dispatcher{
		dispatch:   dispatch,
		executions: executions,
	}
}

func (d *Dispatcher) Dispatch(ctx context.Context, def Definition) error {
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
		def.ID,
		def.Name,
		def.AgentKey,
		def.TeamID,
		def.SourceFile,
		triggeredAt,
	)
	executionID := ""
	if d.executions != nil {
		id, recordErr := d.executions.RecordStart(def.ID, def.Name, def.SourceFile, def.AgentKey, def.TeamID)
		if recordErr != nil {
			log.Printf("[automation] execution record start failed id=%s source=%s err=%v", def.ID, def.SourceFile, recordErr)
		} else {
			executionID = id
		}
	}
	err := d.dispatch(ctx, def.ToQueryRequest())
	if d.executions != nil && executionID != "" {
		if recordErr := d.executions.RecordComplete(executionID, err); recordErr != nil {
			log.Printf("[automation] execution record complete failed id=%s executionID=%s source=%s err=%v", def.ID, executionID, def.SourceFile, recordErr)
		}
	}
	if err != nil {
		log.Printf(
			"[automation] dispatch failed id=%s name=%s agentKey=%s teamId=%s source=%s triggeredAt=%s duration=%s err=%v",
			def.ID,
			def.Name,
			def.AgentKey,
			def.TeamID,
			def.SourceFile,
			triggeredAt,
			time.Since(startedAt).Round(time.Millisecond),
			err,
		)
		return err
	}
	log.Printf(
		"[automation] dispatch success id=%s name=%s agentKey=%s teamId=%s source=%s triggeredAt=%s duration=%s",
		def.ID,
		def.Name,
		def.AgentKey,
		def.TeamID,
		def.SourceFile,
		triggeredAt,
		time.Since(startedAt).Round(time.Millisecond),
	)
	return nil
}
