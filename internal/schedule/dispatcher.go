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
	err := d.dispatch(ctx, def.ToQueryRequest())
	if err != nil {
		log.Printf("[schedule] dispatch failed for %s: %v", def.ID, err)
		return err
	}

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
