package runexec

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/runtime/proxy"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
	"agent-platform/internal/timecontract"
)

type ProxyEventRecorder struct {
	req               runtimetypes.QueryCommand
	agentDef          catalog.AgentDefinition
	chatStore         chat.Store
	stepWriter        *chat.StepWriter
	control           *contracts.RunControl
	notifications     contracts.NotificationSink
	usageTracker      *ProxyUsageTracker
	awaiting          AwaitingTracker
	assistantText     strings.Builder
	startedAt         int64
	finishReason      string
	runUsage          chat.UsageData
	contents          map[string]*proxyContentBucket
	reasonings        map[string]*proxyContentBucket
	tools             map[string]*proxyToolBucket
	planningSnapshots map[string]bool
	markdownGuards    map[string]*stream.MarkdownDestinationGuard
	markdownText      map[string]*strings.Builder
}

type proxyContentBucket struct {
	runID string
	text  strings.Builder
}

type proxyToolBucket struct {
	runID    string
	toolName string
	args     strings.Builder
}

func NewProxyEventRecorder(
	req runtimetypes.QueryCommand,
	startedAtMillis int64,
	agentDef catalog.AgentDefinition,
	chatStore chat.Store,
	stepWriter *chat.StepWriter,
	control *contracts.RunControl,
	notifications contracts.NotificationSink,
	chatUsage chat.UsageData,
	models *models.ModelRegistry,
	billing config.BillingConfig,
) *ProxyEventRecorder {
	if stepWriter == nil {
		return nil
	}
	queryPayload := map[string]any{
		"requestId": req.RequestID,
		"runId":     req.RunID,
		"chatId":    req.ChatID,
		"agentKey":  req.AgentKey,
		"role":      req.Role,
		"message":   req.Message,
	}
	if req.Hidden != nil {
		queryPayload["hidden"] = *req.Hidden
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
	if len(req.MustUseSkills) > 0 {
		queryPayload["mustUseSkills"] = append([]string(nil), req.MustUseSkills...)
	}
	for key, value := range req.TrustedQueryMetadata {
		if _, reserved := queryPayload[key]; !reserved {
			queryPayload[key] = value
		}
	}
	stepWriter.OnEvent(stream.EventData{
		Type:      "request.query",
		Timestamp: startedAtMillis,
		Payload:   queryPayload,
	})
	recorder := &ProxyEventRecorder{
		req:               req,
		agentDef:          agentDef,
		chatStore:         chatStore,
		stepWriter:        stepWriter,
		control:           control,
		notifications:     notifications,
		startedAt:         startedAtMillis,
		contents:          map[string]*proxyContentBucket{},
		reasonings:        map[string]*proxyContentBucket{},
		tools:             map[string]*proxyToolBucket{},
		planningSnapshots: map[string]bool{},
		markdownGuards:    map[string]*stream.MarkdownDestinationGuard{},
		markdownText:      map[string]*strings.Builder{},
	}
	recorder.usageTracker = NewProxyUsageTracker(chatUsage, &recorder.runUsage, models, billing)
	return recorder
}

func (r *ProxyEventRecorder) DecorateEvent(event *stream.EventData) {
	if r == nil || r.usageTracker == nil {
		return
	}
	r.usageTracker.Decorate(event)
}

func (r *ProxyEventRecorder) SanitizeMarkdownEvent(event *stream.EventData) {
	if r == nil || event == nil {
		return
	}
	contentID := strings.TrimSpace(event.String("contentId"))
	switch event.Type {
	case "content.start":
		if contentID != "" {
			r.markdownGuards[contentID] = stream.NewMarkdownDestinationGuard(r.req.ChatID)
			r.markdownText[contentID] = &strings.Builder{}
		}
	case "content.delta":
		if contentID == "" {
			return
		}
		guard := r.markdownGuards[contentID]
		if guard == nil {
			guard = stream.NewMarkdownDestinationGuard(r.req.ChatID)
			r.markdownGuards[contentID] = guard
		}
		safeDelta := guard.Write(event.String("delta"))
		event.Payload["delta"] = safeDelta
		buffer := r.markdownText[contentID]
		if buffer == nil {
			buffer = &strings.Builder{}
			r.markdownText[contentID] = buffer
		}
		buffer.WriteString(safeDelta)
	case "content.end":
		guard := r.markdownGuards[contentID]
		delete(r.markdownGuards, contentID)
		buffer := r.markdownText[contentID]
		delete(r.markdownText, contentID)
		if text := event.String("text"); text != "" {
			fullGuard := stream.NewMarkdownDestinationGuard(r.req.ChatID)
			event.Payload["text"] = fullGuard.Write(text) + fullGuard.Flush()
		} else {
			var safeText strings.Builder
			if buffer != nil {
				safeText.WriteString(buffer.String())
			}
			if guard != nil {
				safeText.WriteString(guard.Flush())
			}
			event.Payload["text"] = safeText.String()
		}
	case "content.snapshot":
		if text := event.String("text"); text != "" {
			fullGuard := stream.NewMarkdownDestinationGuard(r.req.ChatID)
			event.Payload["text"] = fullGuard.Write(text) + fullGuard.Flush()
		}
	}
}

func PublishProxyLiveEvent(eventBus *stream.RunEventBus, recorder *ProxyEventRecorder, req runtimetypes.QueryCommand, seq *int64, event stream.EventData) (stream.EventData, error) {
	event = proxy.NormalizeEventIdentity(event, req)
	if recorder != nil {
		recorder.SanitizeMarkdownEvent(&event)
	}
	if err := timecontract.ValidateEpochMillis(event.Timestamp, "timestamp", "proxy.upstream.event"); err != nil {
		return stream.EventData{}, err
	}
	if snapshot, ok := recorder.syntheticPlanningSnapshotBeforeAwaiting(event); ok {
		assignProxySyntheticSeq(&snapshot, seq, event.Seq)
		publishProxyEventData(eventBus, recorder, snapshot)
	}
	assignProxyEventSeq(&event, seq)
	publishProxyEventData(eventBus, recorder, event)
	return event, nil
}

func assignProxyEventSeq(event *stream.EventData, seq *int64) {
	if event == nil || seq == nil {
		return
	}
	if event.Seq <= 0 || event.Seq <= *seq {
		*seq = *seq + 1
		event.Seq = *seq
		return
	}
	*seq = event.Seq
}

func assignProxySyntheticSeq(event *stream.EventData, seq *int64, beforeSeq int64) {
	if event == nil || seq == nil {
		return
	}
	if beforeSeq > 0 && beforeSeq > *seq {
		event.Seq = beforeSeq
		*seq = beforeSeq
		return
	}
	*seq = *seq + 1
	event.Seq = *seq
}

func publishProxyEventData(eventBus *stream.RunEventBus, recorder *ProxyEventRecorder, event stream.EventData) {
	if recorder != nil {
		recorder.DecorateEvent(&event)
	}
	if eventBus != nil {
		eventBus.Publish(event)
	}
	if recorder != nil {
		recorder.OnEvent(event)
	}
}

func (r *ProxyEventRecorder) syntheticPlanningSnapshotBeforeAwaiting(event stream.EventData) (stream.EventData, bool) {
	if r == nil || event.Type != "awaiting.ask" || !strings.EqualFold(strings.TrimSpace(event.String("mode")), "planning") {
		return stream.EventData{}, false
	}
	planning := contracts.AnyMapNode(event.Value("planning"))
	if strings.TrimSpace(contracts.AnyStringNode(planning["text"])) == "" {
		return stream.EventData{}, false
	}
	chatDir := ""
	if r.chatStore != nil {
		chatDir = r.chatStore.ChatDir(r.req.ChatID)
	}
	if strings.TrimSpace(contracts.AnyStringNode(planning["planningFile"])) == "" {
		planningID := strings.TrimSpace(contracts.AnyStringNode(planning["planningId"]))
		if planningID == "" || filepath.Base(planningID) != planningID || chatDir == "" {
			return stream.EventData{}, false
		}
		planningFile := filepath.Join(chatDir, chat.ToolRootDirName, chat.ToolPlanningDirName, planningID+".md")
		if err := os.MkdirAll(filepath.Dir(planningFile), 0o755); err != nil {
			return stream.EventData{}, false
		}
		if err := os.WriteFile(planningFile, []byte(contracts.AnyStringNode(planning["text"])), 0o644); err != nil {
			return stream.EventData{}, false
		}
		planning["planningFile"] = planningFile
		event.Payload["planning"] = planning
	}
	state, snapshot := chat.PlanningSnapshotFromAwaitingItem(eventPayloadWithType(event), r.req.ChatID, r.req.RunID)
	if state == nil || snapshot == nil || strings.TrimSpace(state.Markdown) == "" || r.hasPlanningSnapshot(state.PlanningID) {
		return stream.EventData{}, false
	}
	return *snapshot, true
}

func (r *ProxyEventRecorder) hasPlanningSnapshot(planningID string) bool {
	if r == nil || strings.TrimSpace(planningID) == "" {
		return false
	}
	return r.planningSnapshots[strings.TrimSpace(planningID)]
}

func (r *ProxyEventRecorder) markPlanningSnapshot(event stream.EventData) {
	if r == nil {
		return
	}
	planningID := strings.TrimSpace(event.String("planningId"))
	if planningID == "" {
		return
	}
	if r.planningSnapshots == nil {
		r.planningSnapshots = map[string]bool{}
	}
	r.planningSnapshots[planningID] = true
}

func eventPayloadWithType(event stream.EventData) map[string]any {
	payload := make(map[string]any, len(event.Payload)+3)
	for key, value := range event.Payload {
		payload[key] = value
	}
	payload["type"] = event.Type
	if event.Seq > 0 {
		payload["seq"] = event.Seq
	}
	if event.Timestamp > 0 {
		payload["timestamp"] = event.Timestamp
	}
	return payload
}

func (r *ProxyEventRecorder) OnEvent(event stream.EventData) {
	if r == nil || r.stepWriter == nil {
		return
	}
	if event.Type == "planning.snapshot" {
		r.markPlanningSnapshot(event)
	}
	switch event.Type {
	case "content.start":
		id, _ := event.Payload["contentId"].(string)
		runID, _ := event.Payload["runId"].(string)
		if id != "" {
			r.contents[id] = &proxyContentBucket{runID: runID}
		}
	case "content.delta":
		id, _ := event.Payload["contentId"].(string)
		delta, _ := event.Payload["delta"].(string)
		if delta == "" {
			return
		}
		r.assistantText.WriteString(delta)
		if b := r.contents[id]; b != nil {
			b.text.WriteString(delta)
		}
	case "content.end":
		id, _ := event.Payload["contentId"].(string)
		b := r.contents[id]
		delete(r.contents, id)
		text, _ := event.Payload["text"].(string)
		if b == nil {
			b = &proxyContentBucket{}
		}
		if text == "" {
			text = b.text.String()
		}
		if text != "" {
			r.stepWriter.OnEvent(stream.EventData{
				Type:      "content.snapshot",
				Timestamp: event.Timestamp,
				Payload: map[string]any{
					"contentId": id,
					"runId":     b.runID,
					"text":      text,
				},
			})
		}
	case "reasoning.start":
		id, _ := event.Payload["reasoningId"].(string)
		runID, _ := event.Payload["runId"].(string)
		if id != "" {
			r.reasonings[id] = &proxyContentBucket{runID: runID}
		}
	case "reasoning.delta":
		id, _ := event.Payload["reasoningId"].(string)
		delta, _ := event.Payload["delta"].(string)
		if b := r.reasonings[id]; b != nil && delta != "" {
			b.text.WriteString(delta)
		}
	case "reasoning.end":
		id, _ := event.Payload["reasoningId"].(string)
		b := r.reasonings[id]
		delete(r.reasonings, id)
		text, _ := event.Payload["text"].(string)
		if b == nil {
			b = &proxyContentBucket{}
		}
		if text == "" {
			text = b.text.String()
		}
		if text != "" {
			r.stepWriter.OnEvent(stream.EventData{
				Type:      "reasoning.snapshot",
				Timestamp: event.Timestamp,
				Payload: map[string]any{
					"reasoningId": id,
					"runId":       b.runID,
					"text":        text,
				},
			})
		}
	case "tool.start":
		id, _ := event.Payload["toolId"].(string)
		runID, _ := event.Payload["runId"].(string)
		toolName, _ := event.Payload["toolName"].(string)
		if id != "" {
			r.tools[id] = &proxyToolBucket{runID: runID, toolName: toolName}
		}
	case "tool.args":
		id, _ := event.Payload["toolId"].(string)
		delta, _ := event.Payload["delta"].(string)
		if b := r.tools[id]; b != nil && delta != "" {
			b.args.WriteString(delta)
		}
	case "tool.end":
		id, _ := event.Payload["toolId"].(string)
		fileChange, _ := event.Payload["fileChange"].(map[string]any)
		b := r.tools[id]
		delete(r.tools, id)
		if b == nil {
			b = &proxyToolBucket{}
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
		r.stepWriter.OnEvent(stream.EventData{
			Type:      "tool.snapshot",
			Timestamp: event.Timestamp,
			Payload:   payload,
		})
	case "usage.snapshot":
		r.stepWriter.OnEvent(event)
	case "awaiting.ask":
		r.handleLiveLifecycle(event)
		r.stepWriter.OnEvent(event)
	case "awaiting.answer":
		r.handleLiveLifecycle(event)
		r.stepWriter.OnEvent(event)
	case "run.complete":
		r.finishReason = "complete"
		r.stepWriter.OnEvent(event)
	case "run.cancel":
		r.maybeResolvePendingAwaiting()
		r.finishReason = "cancel"
		r.stepWriter.OnEvent(event)
	case "run.error":
		r.maybeResolvePendingAwaiting()
		r.finishReason = "error"
		r.stepWriter.OnEvent(event)
	case "artifact.publish":
		r.stepWriter.OnEvent(event)
		if r.stepWriter.Err() == nil {
			NotifyArtifactPublished(r.notifications, event)
		}
	case "tool.result",
		"task.start", "task.complete", "task.cancel", "task.error",
		"plan.create", "plan.update", "source.publish",
		"planning.start", "planning.delta", "planning.end", "planning.snapshot",
		"request.submit", "request.steer":
		r.stepWriter.OnEvent(event)
	}
}

func (r *ProxyEventRecorder) handleLiveLifecycle(event stream.EventData) {
	if r == nil {
		return
	}
	HandleAwaitingLifecycle(NativeOptions{
		Session: contracts.QuerySession{
			ChatID:   r.req.ChatID,
			RunID:    r.req.RunID,
			AgentKey: r.req.AgentKey,
			TeamID:   r.req.TeamID,
			RunOwner: contracts.AgentRunOwner(r.req.AgentKey, r.req.TeamID),
		},
		Chats:         r.chatStore,
		RunControl:    r.control,
		Notifications: r.notifications,
	}, event, &r.awaiting)
}

func (r *ProxyEventRecorder) maybeResolvePendingAwaiting() {
	if r == nil {
		return
	}
	MaybeBroadcastInterruptedAwaiting(NativeOptions{
		Session: contracts.QuerySession{
			ChatID:   r.req.ChatID,
			RunID:    r.req.RunID,
			AgentKey: r.req.AgentKey,
			TeamID:   r.req.TeamID,
			RunOwner: contracts.AgentRunOwner(r.req.AgentKey, r.req.TeamID),
		},
		Chats:         r.chatStore,
		Notifications: r.notifications,
	}, &r.awaiting)
}

func (r *ProxyEventRecorder) Finish() (bool, chat.RunCompletion) {
	if r == nil {
		return false, chat.RunCompletion{}
	}
	if r.stepWriter != nil {
		r.stepWriter.Flush()
	}
	finishReason := r.finishReason
	if strings.TrimSpace(finishReason) == "" {
		finishReason = "complete"
	}
	completion := chat.RunCompletion{
		ChatID:          r.req.ChatID,
		RunID:           r.req.RunID,
		AgentKey:        r.req.AgentKey,
		AgentMode:       catalog.AgentModeForAPI(r.agentDef.Mode),
		AssistantText:   r.assistantText.String(),
		InitialMessage:  r.req.Message,
		FinishReason:    finishReason,
		StartedAtMillis: r.startedAt,
		UpdatedAtMillis: time.Now().UnixMilli(),
		Usage:           r.runUsage,
	}
	if r.req.ChatID == "" || r.req.RunID == "" || r.chatStore == nil {
		return false, completion
	}
	if err := r.chatStore.OnRunCompleted(completion); err != nil {
		log.Printf("[proxy][ws] OnRunCompleted failed: %v", err)
		// The completion clock was captured before the persistence attempt. Keep
		// returning it so run.finished carries the same real completion instant
		// even when storage rejects a historic/invalid record.
		return false, completion
	}
	return true, completion
}
