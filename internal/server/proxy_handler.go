package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"agent-platform-runner-go/internal/api"
	"agent-platform-runner-go/internal/catalog"
	"agent-platform-runner-go/internal/chat"
	"agent-platform-runner-go/internal/stream"
)

// handleProxyQuery forwards /api/query to a remote AGW-compatible service
// (e.g. claude-code relay-server) and pipes its SSE response back to the
// client unchanged. While piping, it also tees the events into a StepWriter
// so the local chat store ends up in the same {chatId}.jsonl shape produced
// by non-PROXY agents.
//
// Upstream emits delta-style events (content.start / content.delta /
// content.end, reasoning.start/delta/end, tool.start / tool.args / tool.end,
// tool.result, run.complete). StepWriter only consumes snapshot-style events
// (content.snapshot, reasoning.snapshot, tool.snapshot, tool.result, …), so
// we accumulate per-id buffers and synthesise snapshot events at *.end.
func (s *Server) handleProxyQuery(w http.ResponseWriter, r *http.Request, req api.QueryRequest, agentDef catalog.AgentDefinition) {
	proxy := agentDef.ProxyConfig
	if proxy == nil || strings.TrimSpace(proxy.BaseURL) == "" {
		writeJSON(w, http.StatusBadGateway, api.Failure(http.StatusBadGateway, "PROXY agent missing proxyConfig.baseUrl"))
		return
	}

	baseURL := strings.TrimRight(proxy.BaseURL, "/")
	targetURL := baseURL + "/api/query"

	body, err := json.Marshal(map[string]any{
		"requestId":  req.RequestID,
		"chatId":     req.ChatID,
		"agentKey":   req.AgentKey,
		"role":       req.Role,
		"message":    req.Message,
		"references": req.References,
		"params":     req.Params,
		"scene":      req.Scene,
		"stream":     true,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	timeout := time.Duration(proxy.TimeoutMs) * time.Millisecond
	client := &http.Client{Timeout: timeout}

	proxyReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, api.Failure(http.StatusBadGateway, "failed to create proxy request: "+err.Error()))
		return
	}
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "text/event-stream")
	if proxy.Token != "" {
		proxyReq.Header.Set("Authorization", "Bearer "+proxy.Token)
	}

	log.Printf("[proxy] forwarding query to %s (agent=%s, chatId=%s)", targetURL, agentDef.Key, req.ChatID)

	resp, err := client.Do(proxyReq)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, api.Failure(http.StatusBadGateway, "proxy request failed: "+err.Error()))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		writeJSON(w, resp.StatusCode, api.Failure(resp.StatusCode, fmt.Sprintf("upstream returned %d: %s", resp.StatusCode, string(data))))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "streaming not supported"))
		return
	}

	chatStore := s.deps.Chats
	var stepWriter *chat.StepWriter
	var assistantText strings.Builder
	if chatStore != nil {
		stepWriter = chat.NewStepWriter(chatStore, req.ChatID, req.RunID, agentDef.Mode, isHiddenRequest(req))
		stepWriter.OnEvent(stream.EventData{
			Type:      "request.query",
			Timestamp: time.Now().UnixMilli(),
			Payload: map[string]any{
				"requestId": req.RequestID,
				"runId":     req.RunID,
				"chatId":    req.ChatID,
				"agentKey":  req.AgentKey,
				"role":      req.Role,
				"message":   req.Message,
			},
		})
	}

	type contentBucket struct {
		runID string
		text  strings.Builder
	}
	type toolBucket struct {
		runID    string
		toolName string
		args     strings.Builder
	}
	contents := map[string]*contentBucket{}
	reasonings := map[string]*contentBucket{}
	tools := map[string]*toolBucket{}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		if line == "" || strings.HasPrefix(line, "data:") {
			flusher.Flush()
		}

		if stepWriter == nil || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == stream.DoneSentinel {
			continue
		}
		var event stream.EventData
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if event.Type == "" {
			continue
		}

		switch event.Type {
		case "content.start":
			id, _ := event.Payload["contentId"].(string)
			runID, _ := event.Payload["runId"].(string)
			if id != "" {
				contents[id] = &contentBucket{runID: runID}
			}
		case "content.delta":
			id, _ := event.Payload["contentId"].(string)
			delta, _ := event.Payload["delta"].(string)
			if delta == "" {
				break
			}
			assistantText.WriteString(delta)
			if b := contents[id]; b != nil {
				b.text.WriteString(delta)
			}
		case "content.end":
			id, _ := event.Payload["contentId"].(string)
			b := contents[id]
			delete(contents, id)
			text, _ := event.Payload["text"].(string)
			if b == nil {
				b = &contentBucket{}
			}
			if text == "" {
				text = b.text.String()
			}
			if text == "" {
				break
			}
			stepWriter.OnEvent(stream.EventData{
				Type:      "content.snapshot",
				Timestamp: event.Timestamp,
				Payload: map[string]any{
					"contentId": id,
					"runId":     b.runID,
					"text":      text,
				},
			})

		case "reasoning.start":
			id, _ := event.Payload["reasoningId"].(string)
			runID, _ := event.Payload["runId"].(string)
			if id != "" {
				reasonings[id] = &contentBucket{runID: runID}
			}
		case "reasoning.delta":
			id, _ := event.Payload["reasoningId"].(string)
			delta, _ := event.Payload["delta"].(string)
			if delta == "" {
				break
			}
			if b := reasonings[id]; b != nil {
				b.text.WriteString(delta)
			}
		case "reasoning.end":
			id, _ := event.Payload["reasoningId"].(string)
			b := reasonings[id]
			delete(reasonings, id)
			text, _ := event.Payload["text"].(string)
			if b == nil {
				b = &contentBucket{}
			}
			if text == "" {
				text = b.text.String()
			}
			if text == "" {
				break
			}
			stepWriter.OnEvent(stream.EventData{
				Type:      "reasoning.snapshot",
				Timestamp: event.Timestamp,
				Payload: map[string]any{
					"reasoningId": id,
					"runId":       b.runID,
					"text":        text,
				},
			})

		case "tool.start":
			id, _ := event.Payload["toolId"].(string)
			runID, _ := event.Payload["runId"].(string)
			toolName, _ := event.Payload["toolName"].(string)
			if id != "" {
				tools[id] = &toolBucket{runID: runID, toolName: toolName}
			}
		case "tool.args":
			id, _ := event.Payload["toolId"].(string)
			delta, _ := event.Payload["delta"].(string)
			if b := tools[id]; b != nil && delta != "" {
				b.args.WriteString(delta)
			}
		case "tool.end":
			id, _ := event.Payload["toolId"].(string)
			b := tools[id]
			delete(tools, id)
			if b == nil {
				b = &toolBucket{}
			}
			stepWriter.OnEvent(stream.EventData{
				Type:      "tool.snapshot",
				Timestamp: event.Timestamp,
				Payload: map[string]any{
					"toolId":    id,
					"runId":     b.runID,
					"toolName":  b.toolName,
					"arguments": b.args.String(),
				},
			})

		case "tool.result", "run.complete", "run.cancel", "run.error",
			"task.start", "task.complete", "task.cancel", "task.fail",
			"plan.create", "plan.update", "artifact.publish":
			stepWriter.OnEvent(event)
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[proxy] stream read error: %v", err)
	}

	if stepWriter != nil {
		stepWriter.Flush()
		if err := chatStore.OnRunCompleted(chat.RunCompletion{
			ChatID:          req.ChatID,
			RunID:           req.RunID,
			AssistantText:   assistantText.String(),
			InitialMessage:  req.Message,
			UpdatedAtMillis: time.Now().UnixMilli(),
		}); err != nil {
			log.Printf("[proxy] OnRunCompleted failed: %v", err)
		} else if sum, err := chatStore.Summary(req.ChatID); err == nil && sum != nil {
			if agentUnreadCount, err := s.agentUnreadCount(sum.AgentKey); err == nil {
				s.broadcastChatReadState("chat.unread", *sum, agentUnreadCount)
			}
		}
	}

	log.Printf("[proxy] stream completed (agent=%s, chatId=%s)", agentDef.Key, req.ChatID)
}
