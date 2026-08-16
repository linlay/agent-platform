package stream

import (
	"reflect"
	"sync/atomic"
)

type StreamRequest struct {
	RequestID       string
	RunID           string
	ChatID          string
	ChatName        string
	AgentKey        string
	TeamID          string
	Message         string
	Role            string
	Hidden          *bool
	Scene           *SceneRef
	References      any
	Params          map[string]any
	Model           any
	PlanningMode    bool
	EditingMode     bool
	MustUseSkills   []string
	IncludeUsage    bool
	IncludeFullText bool
	AccessLevel     string
	Created         bool
	ContinueRun     bool
	InitialSeq      int64
	// StartedAtMillis is the authoritative lifecycle clock captured by the run
	// manager. It is intentionally distinct from the timestamps of bootstrap
	// request/chat events: only run.start must describe that exact registration
	// instant.
	StartedAtMillis    int64
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

// EventEmission carries one internal event through persistence and publication.
// Cursor is the latest public stream sequence after processing the event.
// Hidden events keep Event.Seq at zero and reuse the current Cursor without
// reserving a public sequence number.
type EventEmission struct {
	Event      StreamEvent
	Normalized bool
	Visible    bool
	Cursor     int64
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

// SetRunStartedAtMillis binds a registered run's immutable lifecycle clock
// before Bootstrap is called. Callers must pass the same value exposed through
// activeRun.startedAt and the run.started push; this prevents Bootstrap's
// local wall clock from creating a second, subtly different run.start time.
// Invalid values are retained rather than repaired so the normal stream
// contract boundary can reject the producer deterministically.
func (a *StreamEventAssembler) SetRunStartedAtMillis(value int64) {
	if a == nil {
		return
	}
	a.request.StartedAtMillis = value
}

func (a *StreamEventAssembler) Bootstrap() []StreamEvent {
	return visibleEmissionEvents(a.BootstrapEmissions())
}

func (a *StreamEventAssembler) BootstrapEmissions() []EventEmission {
	queryPayload := map[string]any{
		"requestId": a.request.RequestID,
		"runId":     a.request.RunID,
		"chatId":    a.request.ChatID,
		"agentKey":  a.request.AgentKey,
		"teamId":    a.request.TeamID,
		"role":      a.request.Role,
		"message":   a.request.Message,
	}
	if a.request.Hidden != nil {
		queryPayload["hidden"] = *a.request.Hidden
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
	if a.request.EditingMode {
		queryPayload["editingMode"] = true
	}
	if len(a.request.MustUseSkills) > 0 {
		queryPayload["mustUseSkills"] = append([]string(nil), a.request.MustUseSkills...)
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
	if a.request.BootstrapSynthetic != nil {
		events = append(events, NewEvent("request.query", syntheticQueryPayload(a.request, *a.request.BootstrapSynthetic)))
	} else if !a.request.ContinueRun {
		events = append(events, NewEvent("request.query", queryPayload))
	}
	runStart := map[string]any{
		"runId":    a.request.RunID,
		"chatId":   a.request.ChatID,
		"agentKey": a.request.AgentKey,
		"teamId":   a.request.TeamID,
	}
	runStartEvent := NewEvent("run.start", runStart)
	if a.request.StartedAtMillis != 0 {
		// A valid registered value is verified by the run executor before it
		// reaches here. Preserve an invalid value too: replacing it with now
		// would hide an internal contract violation.
		runStartEvent.Timestamp = a.request.StartedAtMillis
	}
	events = append(events, runStartEvent)
	return a.emit(events)
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
		"teamId":    request.TeamID,
		"role":      value.Role,
		"message":   value.Message,
	}
	if len(value.Messages) > 0 {
		payload["messages"] = cloneMessagePayloads(value.Messages)
	}
	if len(value.System) > 0 {
		payload["system"] = clonePayload(value.System)
	}
	if value.Kind != "" {
		payload["kind"] = value.Kind
	}
	if value.Stage != "" {
		payload["stage"] = value.Stage
	}
	if value.Hidden {
		payload["hidden"] = true
	}
	if len(request.MustUseSkills) > 0 {
		payload["mustUseSkills"] = append([]string(nil), request.MustUseSkills...)
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
	return visibleEmissionEvents(a.ConsumeEmissions(input))
}

func (a *StreamEventAssembler) ConsumeEmissions(input StreamInput) []EventEmission {
	return a.emit(a.dispatcher.Dispatch(input))
}

func (a *StreamEventAssembler) Complete() []StreamEvent {
	return visibleEmissionEvents(a.CompleteEmissions())
}

func (a *StreamEventAssembler) CompleteEmissions() []EventEmission {
	return a.emit(a.dispatcher.Complete())
}

func (a *StreamEventAssembler) Fail(err error) []StreamEvent {
	return visibleEmissionEvents(a.FailEmissions(err))
}

func (a *StreamEventAssembler) FailEmissions(err error) []EventEmission {
	return a.emit(a.dispatcher.Fail(err))
}

// CurrentSeq returns the latest public run-local stream cursor without
// reserving another sequence number.
func (a *StreamEventAssembler) CurrentSeq() int64 {
	if a == nil {
		return 0
	}
	return a.seq.Load()
}

func (a *StreamEventAssembler) emit(events []StreamEvent) []EventEmission {
	if len(events) == 0 {
		return nil
	}
	emissions := make([]EventEmission, 0, len(events))
	for _, event := range events {
		normalized := a.normalizer.IsVisible(event)
		visible := normalized && IsClientVisibleEventData(event.Data())
		cursor := a.seq.Load()
		if visible {
			cursor = a.seq.Add(1)
			event.Seq = cursor
		} else {
			event.Seq = 0
		}
		emissions = append(emissions, EventEmission{Event: event, Normalized: normalized, Visible: visible, Cursor: cursor})
	}
	return emissions
}

func visibleEmissionEvents(emissions []EventEmission) []StreamEvent {
	if len(emissions) == 0 {
		return nil
	}
	events := make([]StreamEvent, 0, len(emissions))
	for _, emission := range emissions {
		if emission.Visible {
			events = append(events, emission.Event)
		}
	}
	return events
}
