package stream

import (
	"reflect"
	"sync/atomic"
)

type StreamRequest struct {
	RequestID          string
	RunID              string
	ChatID             string
	ChatName           string
	AgentKey           string
	Message            string
	Role               string
	References         any
	Params             map[string]any
	Model              any
	PlanningMode       bool
	Created            bool
	ContinueRun        bool
	MemoryUsageSummary map[string]any
}

type StreamEventAssembler struct {
	seq        atomic.Int64
	dispatcher *StreamEventDispatcher
	normalizer *SseEventNormalizer
	request    StreamRequest
}

func NewAssembler(request StreamRequest) *StreamEventAssembler {
	return &StreamEventAssembler{
		dispatcher: NewDispatcher(request),
		normalizer: NewNormalizer(),
		request:    request,
	}
}

// RegisterHiddenTools marks tools as clientVisible=false so their
// SSE tool.* events are suppressed.
func (a *StreamEventAssembler) RegisterHiddenTools(names ...string) {
	a.normalizer.RegisterHiddenTools(names...)
}

func (a *StreamEventAssembler) Bootstrap() []StreamEvent {
	queryPayload := map[string]any{
		"requestId": a.request.RequestID,
		"runId":     a.request.RunID,
		"chatId":    a.request.ChatID,
		"agentKey":  a.request.AgentKey,
		"role":      a.request.Role,
		"message":   a.request.Message,
	}
	if !isEmptyValue(a.request.References) {
		queryPayload["references"] = a.request.References
	}
	if len(a.request.Params) > 0 {
		queryPayload["params"] = a.request.Params
	}
	if !isEmptyValue(a.request.Model) {
		queryPayload["model"] = a.request.Model
	}
	if a.request.PlanningMode {
		queryPayload["planningMode"] = true
	}
	events := []StreamEvent{}
	if !a.request.ContinueRun {
		events = append(events, NewEvent("request.query", queryPayload))
	}
	if a.request.Created {
		events = append(events, NewEvent("chat.start", map[string]any{
			"chatId":   a.request.ChatID,
			"chatName": a.request.ChatName,
		}))
	}
	if len(a.request.MemoryUsageSummary) > 0 {
		events = append(events, NewEvent("memory.context", a.request.MemoryUsageSummary))
	}
	events = append(events, NewEvent("run.start", map[string]any{
		"runId":    a.request.RunID,
		"chatId":   a.request.ChatID,
		"agentKey": a.request.AgentKey,
	}))
	return a.stamp(a.normalizer.Normalize(events))
}

func isEmptyValue(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	default:
		return false
	}
}

func (a *StreamEventAssembler) Consume(input StreamInput) []StreamEvent {
	return a.stamp(a.normalizer.Normalize(a.dispatcher.Dispatch(input)))
}

func (a *StreamEventAssembler) Complete() []StreamEvent {
	return a.stamp(a.normalizer.Normalize(a.dispatcher.Complete()))
}

func (a *StreamEventAssembler) Fail(err error) []StreamEvent {
	return a.stamp(a.normalizer.Normalize(a.dispatcher.Fail(err)))
}

func (a *StreamEventAssembler) stamp(events []StreamEvent) []StreamEvent {
	for idx := range events {
		events[idx].Seq = a.seq.Add(1)
	}
	return events
}
