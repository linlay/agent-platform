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

// Broadcaster 是历史接口占位——当前 schedule 不再主动 broadcast 自定义 push，
// 触发后的 run 按普通对话走 request/stream/response 协议，由网关按 run.started
// 自行 attach 消费 stream 事件。保留入参只为避免破坏老的 NewDispatcher 调用点。
type Broadcaster interface {
	Broadcast(eventType string, data map[string]any)
}

type Dispatcher struct {
	dispatch   DispatchFunc
	httpClient *http.Client
}

func NewDispatcher(dispatch DispatchFunc, _ Broadcaster) *Dispatcher {
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

	// Legacy bridge-era 路径：若 YAML 显式配了 pushUrl，仍然 HTTP POST 一条静态 markdown
	// 给那个 URL（bridge /api/push）。不配就跳过——正常用法是不配。
	if strings.TrimSpace(def.PushURL) != "" {
		targetID := strings.TrimSpace(def.PushTargetID)
		if targetID == "" {
			targetID = strings.TrimSpace(def.Query.ChatID)
		}
		markdown := strings.TrimSpace(def.PushMessage)
		if markdown == "" {
			markdown = "Schedule " + def.ID + " completed"
		}
		d.push(def.PushURL, targetID, strings.TrimSpace(def.Query.ChatID), def.ID, markdown)
	}
	return nil
}

func (d *Dispatcher) push(pushURL string, targetID string, chatID string, scheduleID string, markdown string) {
	payload, err := json.Marshal(map[string]any{
		"scheduleId": scheduleID,
		"chatId":     chatID,
		"targetId":   targetID,
		"markdown":   markdown,
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
