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

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/stream"
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
func proxyRequestTimeout(proxy *catalog.ProxyConfig) time.Duration {
	if proxy != nil && proxy.TimeoutMS > 0 {
		return time.Duration(proxy.TimeoutMS) * time.Millisecond
	}
	if proxy != nil && proxy.Timeout > 0 {
		return time.Duration(proxy.Timeout) * time.Second
	}
	return 5 * time.Minute
}

func (s *Server) handleProxyQuery(w http.ResponseWriter, r *http.Request, prepared preparedQuery) {
	defer releaseQuery(prepared.release)
	req := prepared.req
	agentDef := prepared.agentDef
	proxy := agentDef.ProxyConfig
	if proxy == nil || strings.TrimSpace(proxy.BaseURL) == "" {
		writeJSON(w, http.StatusBadGateway, api.Failure(http.StatusBadGateway, "PROXY agent missing proxyConfig.baseUrl"))
		return
	}

	baseURL := strings.TrimRight(proxy.BaseURL, "/")
	targetURL := baseURL + "/api/query"
	proxyReferences, err := prepareProxyReferences(s.deps.Chats, s.ticketService, proxyReferenceOptions{
		ChatID:          req.ChatID,
		RunID:           req.RunID,
		Subject:         prepared.session.Subject,
		ResourceBaseURL: prepared.resourceBaseURL,
		References:      req.References,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.Failure(http.StatusBadRequest, err.Error()))
		return
	}

	bodyPayload := map[string]any{
		"requestId":   req.RequestID,
		"runId":       req.RunID,
		"chatId":      req.ChatID,
		"agentKey":    proxyAgentKey(proxy, req.AgentKey),
		"role":        req.Role,
		"message":     req.Message,
		"accessLevel": req.AccessLevel,
		"references":  proxyReferences,
		"params":      proxyForwardParams(req, prepared.session.WorkspaceRoot),
		"model":       req.Model,
		"scene":       req.Scene,
	}
	if req.PlanningMode != nil {
		bodyPayload["planningMode"] = *req.PlanningMode
	}
	body, err := json.Marshal(bodyPayload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
		return
	}

	client := &http.Client{Timeout: proxyRequestTimeout(proxy)}

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

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "streaming not supported"))
		return
	}

	chatStore := s.deps.Chats
	// This direct HTTP proxy path does not use the in-memory run manager. Keep
	// one real platform start time and carry it through the synthetic request
	// line and completion record; never derive it from upstream traffic or a
	// later completion.
	startedAt := time.Now().UnixMilli()
	if chatStore != nil {
		recorder, ok := chatStore.(chat.RunStartRecorder)
		if !ok || recorder == nil {
			writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, "run start recorder is not configured"))
			return
		}
		if err := recorder.OnRunStarted(chat.RunStart{
			ChatID:          req.ChatID,
			RunID:           req.RunID,
			OwnerType:       prepared.summary.OwnerType,
			AgentKey:        req.AgentKey,
			TeamID:          req.TeamID,
			InitialMessage:  req.Message,
			StartedAtMillis: startedAt,
		}); err != nil {
			if isTimeContractViolation(err) {
				writeTimeContractViolation(w, err)
				return
			}
			writeJSON(w, http.StatusInternalServerError, api.Failure(http.StatusInternalServerError, err.Error()))
			return
		}
	}
	s.broadcast("run.started", map[string]any{
		"runId":     req.RunID,
		"chatId":    req.ChatID,
		"agentKey":  req.AgentKey,
		"timestamp": startedAt,
	})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	var stepWriter *chat.StepWriter
	var assistantText strings.Builder
	if chatStore != nil {
		stepWriter = chat.NewStepWriter(chatStore, req.ChatID, req.RunID, agentDef.Mode)
		stepWriter.SetPendingSystemInit(prepared.systemInitLine)
		stepWriter.SetPendingQueryMessages(prepared.session.CurrentMessages)
		queryPayload := map[string]any{
			"requestId": req.RequestID,
			"runId":     req.RunID,
			"chatId":    req.ChatID,
			"agentKey":  req.AgentKey,
			"role":      req.Role,
			"message":   req.Message,
		}
		if req.IncludeUsage {
			queryPayload["includeUsage"] = true
		}
		if req.IncludeFullText {
			queryPayload["includeFullText"] = true
		}
		if req.PlanningMode != nil {
			queryPayload["planningMode"] = *req.PlanningMode
		}
		stepWriter.OnEvent(stream.EventData{
			Type:      "request.query",
			Timestamp: startedAt,
			Payload:   queryPayload,
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
	finishReason := "complete"
	var runUsage chat.UsageData
	var chatUsage chat.UsageData
	if prepared.summary.Usage != nil {
		chatUsage = *prepared.summary.Usage
	}
	usageTracker := newProxyUsageTracker(chatUsage, &runUsage, s.deps.Models, s.deps.Config.Billing)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	streamStarted := false
	lastSeq := int64(0)
	var firstTimeContractViolation error
	terminatedByTimeContractViolation := false
	for scanner.Scan() {
		line := scanner.Text()
		outLine := line
		var event stream.EventData
		hasEvent := false
		terminateStream := false
		writeLine := true
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload != "" && payload != stream.DoneSentinel {
				decoded, ok, decodeErr := decodeProxyEventAt([]byte(payload), "proxy.sse.event")
				if decodeErr != nil {
					if isTimeContractViolation(decodeErr) {
						// Whether it arrives before or after the first SSE frame, an
						// invalid upstream time ends this run as an error. Never
						// persist a local run.error as a successful completion.
						finishReason = "error"
					}
					event = proxyRunErrorEvent(req, decodeErr)
					hasEvent = true
					terminateStream = true
					if !streamStarted {
						firstTimeContractViolation = decodeErr
						writeLine = false
					} else {
						if isTimeContractViolation(decodeErr) {
							event = localTimeContractRunErrorEvent(
								nextLocalTimeContractErrorSeq(lastSeq, event),
								req.RunID,
								req.ChatID,
								decodeErr,
							)
							terminatedByTimeContractViolation = true
						} else {
							event.Seq = nextLocalTimeContractErrorSeq(lastSeq, event)
						}
						if data, err := json.Marshal(event); err == nil {
							outLine = "data: " + string(data)
						}
					}
				} else if ok {
					event = normalizeProxyEventIdentity(decoded, req)
					usageTracker.Decorate(&event)
					hasEvent = true
					if data, err := json.Marshal(event); err == nil {
						outLine = "data: " + string(data)
					}
				}
			}
		}
		if writeLine {
			fmt.Fprintf(w, "%s\n", outLine)
			if outLine == "" || strings.HasPrefix(outLine, "data:") {
				flusher.Flush()
			}
			streamStarted = true
			if hasEvent && event.Seq > lastSeq {
				lastSeq = event.Seq
			}
		}

		if stepWriter == nil || !hasEvent {
			if terminateStream {
				break
			}
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
			fileChange, _ := event.Payload["fileChange"].(map[string]any)
			b := tools[id]
			delete(tools, id)
			if b == nil {
				b = &toolBucket{}
			}
			payload := map[string]any{
				"toolId":    id,
				"runId":     b.runID,
				"toolName":  b.toolName,
				"arguments": b.args.String(),
			}
			if len(fileChange) > 0 {
				payload["fileChange"] = fileChange
			}
			stepWriter.OnEvent(stream.EventData{
				Type:      "tool.snapshot",
				Timestamp: event.Timestamp,
				Payload:   payload,
			})

		case "usage.snapshot":
			stepWriter.OnEvent(event)
		case "run.complete":
			finishReason = "complete"
			stepWriter.OnEvent(event)
		case "run.cancel":
			finishReason = "cancel"
			stepWriter.OnEvent(event)
		case "run.error":
			finishReason = "error"
			stepWriter.OnEvent(event)
		case "tool.result",
			"task.start", "task.complete", "task.cancel", "task.error",
			"plan.create", "plan.update", "artifact.publish", "source.publish",
			"planning.start", "planning.delta", "planning.end", "planning.snapshot",
			"awaiting.ask", "request.submit", "awaiting.answer", "request.steer":
			stepWriter.OnEvent(event)
		}
		if terminateStream {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[proxy] stream read error: %v", err)
	}
	if terminatedByTimeContractViolation {
		// The local run.error above is terminal, but the direct HTTP proxy path
		// writes raw SSE frames rather than stream.Writer. Explicitly finish the
		// SSE protocol so Desktop/web clients do not wait for an upstream [DONE]
		// that we intentionally stopped reading.
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", stream.DoneSentinel)
		flusher.Flush()
	}

	completedAt := time.Now().UnixMilli()
	var persistedCompletion *chat.RunCompletion
	if stepWriter != nil {
		stepWriter.Flush()
		completion := chat.RunCompletion{
			ChatID:          req.ChatID,
			RunID:           req.RunID,
			OwnerType:       prepared.summary.OwnerType,
			AgentKey:        req.AgentKey,
			TeamID:          req.TeamID,
			AssistantText:   assistantText.String(),
			InitialMessage:  req.Message,
			FinishReason:    finishReason,
			StartedAtMillis: startedAt,
			UpdatedAtMillis: completedAt,
			Usage:           runUsage,
		}
		if err := chatStore.OnRunCompleted(completion); err != nil {
			log.Printf("[proxy] OnRunCompleted failed: %v", err)
		} else {
			persistedCompletion = &completion
		}
	}
	s.broadcast("run.finished", map[string]any{
		"runId":     req.RunID,
		"chatId":    req.ChatID,
		"timestamp": completedAt,
	})
	if persistedCompletion != nil {
		s.broadcastRunCompletionNotifications(*persistedCompletion)
	}
	if firstTimeContractViolation != nil {
		writeTimeContractViolation(w, firstTimeContractViolation)
		return
	}

	log.Printf("[proxy] stream completed (agent=%s, chatId=%s)", agentDef.Key, req.ChatID)
}
