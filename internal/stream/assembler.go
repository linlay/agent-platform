package stream

import "sync/atomic"

type StreamRequest struct {
	RequestID string
	RunID     string
	ChatID    string
	ChatName  string
	AgentKey  string
	Message   string
	Role      string
	Created   bool
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
	events := []StreamEvent{
		NewEvent("request.query", map[string]any{
			"requestId": a.request.RequestID,
			"runId":     a.request.RunID,
			"chatId":    a.request.ChatID,
			"role":      a.request.Role,
			"message":   a.request.Message,
		}),
	}
	if a.request.Created {
		events = append(events, NewEvent("chat.start", map[string]any{
			"chatId":   a.request.ChatID,
			"chatName": a.request.ChatName,
		}))
	}
	events = append(events, NewEvent("run.start", map[string]any{
		"runId":    a.request.RunID,
		"chatId":   a.request.ChatID,
		"agentKey": a.request.AgentKey,
	}))
	return a.stamp(a.normalizer.Normalize(events))
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
		// stage.marker is an internal step boundary signal (Java parity:
		// AgentDeltaToStreamInputMapper returns empty list for stage markers).
		// Do not assign a seq — the event reaches stepWriter for JSONL but
		// is filtered out before SSE by server.go. Assigning a seq would
		// create gaps in the client-visible sequence (e.g., #4 missing).
		if events[idx].Type == "stage.marker" {
			continue
		}
		events[idx].Seq = a.seq.Add(1)
	}
	return events
}
