package runexec

import (
	"context"
	"log"
	"strings"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/runtime/proxy"
	runtimetypes "agent-platform/internal/runtime/types"
	"agent-platform/internal/stream"
)

// ProxyExecutor owns the lifecycle of registered root Proxy runs. It does not
// own client HTTP/WS responses or the legacy unregistered blocking SSE path.
type ProxyExecutor struct {
	BackgroundContext context.Context
	Chats             chat.Store
	Runs              contracts.RunManager
	Models            *models.ModelRegistry
	Billing           config.BillingConfig
	Routes            *proxy.Service
	Driver            *proxy.Driver
	Notifications     contracts.NotificationSink
	OnUnreadChanged   func(chat.Summary)
}

func (e *ProxyExecutor) Start(prepared runtimetypes.PreparedQuery, registered runtimetypes.RegisteredRun, bus *stream.RunEventBus, wait bool) error {
	e.broadcast("run.started", map[string]any{
		"runId": prepared.Req.RunID, "chatId": prepared.Req.ChatID,
		"agentKey": prepared.Req.AgentKey, "startedAt": registered.StartedAtMillis,
	})
	route := proxy.NewRoute(prepared.Req.RunID, prepared.Req.ChatID, prepared.Req.AgentKey)
	cfg := prepared.AgentDef.ProxyConfig
	route.Protocol = proxy.Protocol(cfg)
	if cfg != nil {
		route.UpstreamAgentKey = proxy.AgentKey(cfg, prepared.Req.AgentKey)
		route.Transport = proxy.UpstreamTransport(cfg)
		route.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
		route.Token = cfg.Token
		route.Timeout = proxy.RequestTimeout(cfg)
	}
	e.Routes.Register(route)
	writer := chat.NewStepWriter(e.Chats, prepared.Req.ChatID, prepared.Req.RunID, prepared.AgentDef.Mode)
	writer.SetPendingSystemInit(prepared.SystemInitLine)
	writer.SetPendingQueryMessages(prepared.Session.CurrentMessages)
	var chatUsage chat.UsageData
	if prepared.Summary.Usage != nil {
		chatUsage = *prepared.Summary.Usage
	}
	recorder := NewProxyEventRecorder(prepared.Req, registered.StartedAtMillis, prepared.AgentDef, e.Chats, writer, registered.Control, e.Notifications, chatUsage, e.Models, e.Billing)
	ctx, cancel := context.WithCancel(registered.RunCtx)
	stopLifecycle := context.AfterFunc(e.BackgroundContext, cancel)
	var startup chan error
	if wait {
		startup = make(chan error, 1)
	}
	go func() {
		defer cancel()
		defer stopLifecycle()
		e.Execute(ctx, prepared, route, bus, recorder, startup, nil)
	}()
	if startup != nil {
		return <-startup
	}
	return nil
}

func (e *ProxyExecutor) Execute(ctx context.Context, prepared runtimetypes.PreparedQuery, route *proxy.Route, bus *stream.RunEventBus, recorder *ProxyEventRecorder, startup chan<- error, onCompletion func(chat.RunCompletion)) {
	defer func() {
		if route != nil {
			e.Routes.Unregister(prepared.Req.RunID, route)
			close(route.Done)
		}
		completedAt := time.Now().UnixMilli()
		reason := "error"
		var persisted bool
		var completion chat.RunCompletion
		if recorder != nil {
			persisted, completion = recorder.Finish()
			if completion.UpdatedAtMillis != 0 {
				completedAt = completion.UpdatedAtMillis
			}
			if strings.TrimSpace(completion.FinishReason) != "" {
				reason = completion.FinishReason
			}
		}
		// Deliver the recorder's result before closing observers. Waiting for
		// delivery first would deadlock a caller that still owns its observer.
		if onCompletion != nil && completion.RunID != "" {
			onCompletion(completion)
		}
		if bus != nil {
			bus.FreezeAndWait()
		}
		if prepared.Release != nil {
			prepared.Release()
		}
		e.Runs.Finish(prepared.Req.RunID)
		status := "failed"
		switch reason {
		case "complete":
			status = "completed"
		case "cancel":
			status = "interrupted"
		default:
			reason = "error"
		}
		e.broadcast("run.finished", map[string]any{
			"runId": prepared.Req.RunID, "chatId": prepared.Req.ChatID,
			"status": status, "finishReason": reason, "finishedAt": completedAt,
		})
		if persisted {
			BroadcastCompletion(NativeOptions{Chats: e.Chats, Notifications: e.Notifications, OnUnreadChanged: e.OnUnreadChanged}, completion)
		}
	}()
	e.Driver.Stream(ctx, prepared, route, &proxyEventSink{prepared.Req, recorder, bus}, startup)
}

func (e *ProxyExecutor) broadcast(kind string, payload map[string]any) {
	if e.Notifications != nil {
		e.Notifications.Broadcast(kind, payload)
	}
}

type proxyEventSink struct {
	request  runtimetypes.QueryCommand
	recorder *ProxyEventRecorder
	bus      *stream.RunEventBus
}

func (s *proxyEventSink) Publish(seq *int64, event stream.EventData) (stream.EventData, error) {
	return PublishProxyLiveEvent(s.bus, s.recorder, s.request, seq, event)
}
func (s *proxyEventSink) Error(err error) { s.ErrorAfter(err, 0) }
func (s *proxyEventSink) ErrorAfter(err error, lastSeq int64) {
	event := ProxyRunErrorEvent(s.request, err)
	event.Seq = lastSeq + 1
	if event.Seq <= 0 {
		event.Seq = 1
	}
	log.Printf("[proxy][ws] %s", err)
	if s.bus != nil {
		s.bus.Publish(event)
	}
	if s.recorder != nil {
		s.recorder.OnEvent(event)
	}
}
func (s *proxyEventSink) ObserverCount() int {
	if s.bus == nil {
		return 0
	}
	return s.bus.ObserverCount()
}

func ProxyRunErrorEvent(req runtimetypes.QueryCommand, err error) stream.EventData {
	payload := map[string]any{"runId": req.RunID, "chatId": req.ChatID, "message": err.Error(), "error": err.Error()}
	if IsTimeContractViolation(err) {
		data := TimeContractErrorData(err)
		payload["message"] = timeContractViolationMessage
		payload["error"] = data
		for _, key := range []string{"code", "field", "location", "expected"} {
			if value, ok := data[key]; ok {
				payload[key] = value
			}
		}
	}
	return stream.EventData{Seq: 1, Type: "run.error", Timestamp: time.Now().UnixMilli(), Payload: payload}
}
