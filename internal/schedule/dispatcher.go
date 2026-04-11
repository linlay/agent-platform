package schedule

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
)

type DispatchFunc func(ctx context.Context, req api.QueryRequest) error

type Dispatcher struct {
	dispatch   DispatchFunc
	httpClient *http.Client
}

func NewDispatcher(dispatch DispatchFunc) *Dispatcher {
	return &Dispatcher{
		dispatch: dispatch,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
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
		"[schedule] dispatch start id=%s name=%s agentKey=%s teamId=%s source=%s triggeredAt=%s",
		def.ID,
		def.Name,
		def.AgentKey,
		def.TeamID,
		def.SourceFile,
		triggeredAt,
	)
	err := d.dispatch(ctx, def.ToQueryRequest())
	if err != nil {
		log.Printf(
			"[schedule] dispatch failed id=%s name=%s agentKey=%s teamId=%s source=%s triggeredAt=%s duration=%s err=%v",
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
		"[schedule] dispatch success id=%s name=%s agentKey=%s teamId=%s source=%s triggeredAt=%s duration=%s",
		def.ID,
		def.Name,
		def.AgentKey,
		def.TeamID,
		def.SourceFile,
		triggeredAt,
		time.Since(startedAt).Round(time.Millisecond),
	)

	// Push results to external URL if configured
	if strings.TrimSpace(def.PushURL) != "" {
		targetID := def.PushTargetID
		if targetID == "" {
			targetID = def.Query.ChatID
		}
		d.push(def.PushURL, targetID, def.ID)
	}
	return nil
}

func (d *Dispatcher) push(pushURL string, targetID string, scheduleID string) {
	payload, err := json.Marshal(map[string]any{
		"targetId": targetID,
		"markdown": "Schedule " + scheduleID + " completed",
	})
	if err != nil {
		log.Printf("[schedule] push marshal failed: %v", err)
		return
	}
	resp, err := d.httpClient.Post(pushURL, "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("[schedule] push to %s failed: %v", pushURL, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("[schedule] push to %s succeeded (status=%d)", pushURL, resp.StatusCode)
	} else {
		log.Printf("[schedule] push to %s returned status %d", pushURL, resp.StatusCode)
	}
}
