package server

import (
	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"agent-platform/internal/runtime/proxy"
	"agent-platform/internal/runtime/runexec"
	"agent-platform/internal/stream"
)

// Keep API DTO conversion at the transport boundary during the remaining R18 work.
type proxyEventRecorder = runexec.ProxyEventRecorder

var newProxyUsageTracker = runexec.NewProxyUsageTracker

func newProxyEventRecorder(req api.QueryRequest, startedAt int64, def catalog.AgentDefinition, chats chat.Store, writer *chat.StepWriter, control *contracts.RunControl, notifications contracts.NotificationSink, usage chat.UsageData, models *models.ModelRegistry, billing config.BillingConfig) *proxyEventRecorder {
	return runexec.NewProxyEventRecorder(queryCommandFromAPI(req), startedAt, def, chats, writer, control, notifications, usage, models, billing)
}

func publishProxyLiveEvent(bus *stream.RunEventBus, recorder *proxyEventRecorder, req api.QueryRequest, seq *int64, event stream.EventData) (stream.EventData, error) {
	return runexec.PublishProxyLiveEvent(bus, recorder, queryCommandFromAPI(req), seq, event)
}

var proxyRequestTimeout = proxy.RequestTimeout
