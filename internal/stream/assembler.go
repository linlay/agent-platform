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
	Scene              *SceneRef
	References         any
	Params             map[string]any
	Model              any
	PlanningMode       bool
	IncludeUsage       bool
	IncludeFullText    bool
	AccessLevel        string
	Created            bool
	ContinueRun        bool
	InitialSeq         int64
	BootstrapSynthetic *SyntheticQuery
	MemoryUsageSummary map[string]any
	QueryMetadata      map[string]any
}

type SceneRef struct {
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
}

func (s *SceneRef) ToMap() map[string]any {
	if s == nil {
		return nil
	}
	m := map[string]any{}
	if s.URL != "" {
		m["url"] = s.URL
	}
	if s.Title != "" {
		m["title"] = s.Title
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

type StreamEventAssembler struct {
	seq        atomic.Int64
	dispatcher *StreamEventDispatcher
	normalizer *SseEventNormalizer
	request    StreamRequest
}

func NewAssembler(request StreamRequest) *StreamEventAssembler {
	assembler := &StreamEventAssembler{
		dispatcher: NewDispatcher(request),
		normalizer: NewNormalizer(),
		request:    request,
	}
	if request.InitialSeq > 0 {
		assembler.seq.Store(request.InitialSeq)
	}
	return assembler
}

// RegisterHiddenTools marks tools as clientVisible=false so their
// SSE tool.* events are suppressed.
func (a *StreamEventAssembler) RegisterHiddenTools(names ...string) {
	a.normalizer.RegisterHiddenTools(names...)
}

func (a *StreamEventAssembler) Bootstrap() []StreamEvent {
	_, normalized := a.BootstrapWithRaw()
	return normalized
}

func (a *StreamEventAssembler) BootstrapWithRaw() ([]StreamEvent, []StreamEvent) {
	queryPayload := map[string]any{
		"requestId": a.request.RequestID,
		"runId":     a.request.RunID,
		"chatId":    a.request.ChatID,
		"agentKey":  a.request.AgentKey,
		"role":      a.request.Role,
		"message":   a.request.Message,
	}
	if a.request.AccessLevel != "" {
		queryPayload["accessLevel"] = a.request.AccessLevel
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
	if a.request.IncludeUsage {
		queryPayload["includeUsage"] = true
	}
	if a.request.IncludeFullText {
		queryPayload["includeFullText"] = true
	}
	if scene := a.request.Scene.ToMap(); scene != nil {
		queryPayload["scene"] = scene
	}
	for key, value := range a.request.QueryMetadata {
		if _, reserved := queryPayload[key]; reserved {
			continue
		}
		queryPayload[key] = value
	}
	events := []StreamEvent{}
	if !a.request.ContinueRun && a.request.Created {
		events = append(events, NewEvent("chat.start", map[string]any{
			"chatId":   a.request.ChatID,
			"chatName": a.request.ChatName,
		}))
	}
	if !a.request.ContinueRun {
		if a.request.BootstrapSynthetic != nil {
			events = append(events, NewEvent("request.query", syntheticQueryPayload(a.request, *a.request.BootstrapSynthetic)))
		} else {
			events = append(events, NewEvent("request.query", queryPayload))
		}
	}
	events = append(events, NewEvent("run.start", map[string]any{
		"runId":    a.request.RunID,
		"chatId":   a.request.ChatID,
		"agentKey": a.request.AgentKey,
	}))
	raw := a.stamp(events)
	return raw, a.normalizer.Normalize(raw)
}

func syntheticQueryPayload(request StreamRequest, value SyntheticQuery) map[string]any {
	chatID := value.ChatID
	if chatID == "" {
		chatID = request.ChatID
	}
	requestID := request.RequestID
	if requestID == "" {
		requestID = request.RunID
	}
	payload := map[string]any{
		"requestId": requestID,
		"runId":     request.RunID,
		"chatId":    chatID,
		"role":      value.Role,
		"message":   value.Message,
	}
	if len(value.Messages) > 0 {
		payload["messages"] = cloneMessagePayloads(value.Messages)
	}
	if len(value.Systems) > 0 {
		payload["systems"] = cloneMessagePayloads(value.Systems)
	}
	for key, item := range request.QueryMetadata {
		if _, reserved := payload[key]; reserved {
			continue
		}
		payload[key] = item
	}
	return payload
}

func isEmptyValue(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return true
		}
		return v.Len() == 0
	case reflect.Func, reflect.Ptr:
		return v.IsNil()
	case reflect.Interface:
		if v.IsNil() {
			return true
		}
		return isEmptyValue(v.Elem().Interface())
	case reflect.Array, reflect.String:
		return v.Len() == 0
	default:
		return false
	}
}

func (a *StreamEventAssembler) Consume(input StreamInput) []StreamEvent {
	_, normalized := a.ConsumeWithRaw(input)
	return normalized
}

func (a *StreamEventAssembler) ConsumeWithRaw(input StreamInput) ([]StreamEvent, []StreamEvent) {
	raw := a.stamp(a.dispatcher.Dispatch(input))
	return raw, a.normalizer.Normalize(raw)
}

func (a *StreamEventAssembler) Complete() []StreamEvent {
	_, normalized := a.CompleteWithRaw()
	return normalized
}

func (a *StreamEventAssembler) CompleteWithRaw() ([]StreamEvent, []StreamEvent) {
	raw := a.stamp(a.dispatcher.Complete())
	return raw, a.normalizer.Normalize(raw)
}

func (a *StreamEventAssembler) Fail(err error) []StreamEvent {
	_, normalized := a.FailWithRaw(err)
	return normalized
}

func (a *StreamEventAssembler) FailWithRaw(err error) ([]StreamEvent, []StreamEvent) {
	raw := a.stamp(a.dispatcher.Fail(err))
	return raw, a.normalizer.Normalize(raw)
}

// NextSeq reserves the next run-local live sequence number.
func (a *StreamEventAssembler) NextSeq() int64 {
	return a.seq.Add(1)
}

func (a *StreamEventAssembler) stamp(events []StreamEvent) []StreamEvent {
	for idx := range events {
		events[idx].Seq = a.NextSeq()
	}
	return events
}
