package stream

import (
	"reflect"
	"sync/atomic"
	"time"

	"agent-platform/internal/contracts/queryinput"
)

type StreamRequest struct {
	Mail      *queryinput.MailInput
	RequestID string
	RunID     string
	ChatID    string
	ChatName  string
	AgentKey  string

	Message         string
	Hidden          *bool
	Scene           *SceneRef
	References      any
	Params          map[string]any
	Model           any
	PlanningMode    bool
	EditingMode     bool
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
	// Lane is the side lane of a hidden branch Run; empty means main.
	Lane   string
	SideID string
	Origin *queryinput.Origin
	// AccessGrant audits how a chat_start Run obtained its access level.
	AccessGrant map[string]any
	// PlanRunID links a Run started by approving a plan to its planning Run.
	PlanRunID string
	// AnnounceHold is how long an announcement at least holds the progress
	// line; zero disables the hold.
	AnnounceHold time.Duration
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
	activities *ActivityTracker
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
	// Ephemeral events are delivered live with Seq 0 and never reserve a
	// public sequence: a progress activity is replaced by the next one and
	// resent on attach, so it has no place in the replay log.
	Ephemeral bool
	Cursor    int64
}

func NewAssembler(request StreamRequest) *StreamEventAssembler {
	assembler := &StreamEventAssembler{
		dispatcher: NewDispatcher(request),
		normalizer: NewNormalizer(),
		activities: NewActivityTracker(request.RunID, request.ChatID, request.AnnounceHold),
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

func (a *StreamEventAssembler) BootstrapEmissions() []EventEmission {
	queryPayload := RequestQueryPayload(a.request)
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

// RequestQueryPayload is the request.query event of a Run that starts from a
// caller's input. Native and proxied Runs both build it here so the persisted
// line records the same decisions.
func RequestQueryPayload(request StreamRequest) map[string]any {
	payload := map[string]any{
		"requestId": request.RequestID,
		"runId":     request.RunID,
		"chatId":    request.ChatID,
		"agentKey":  request.AgentKey,
		"message":   request.Message,
	}
	if request.Hidden != nil {
		payload["hidden"] = *request.Hidden
	}
	if !isEmptyValue(request.Params) {
		payload["params"] = request.Params
	}
	if scene := request.Scene.ToMap(); scene != nil {
		payload["scene"] = scene
	}
	if request.IncludeUsage {
		payload["includeUsage"] = true
	}
	if request.IncludeFullText {
		payload["includeFullText"] = true
	}
	addRunDecisions(request, payload)
	if request.PlanningMode {
		payload["planningMode"] = true
	}
	// Persist the resolved request intent, including false, for Run recovery
	// and execution after plan approval.
	payload["editingMode"] = request.EditingMode
	return payload
}

func addRunDecisions(request StreamRequest, payload map[string]any) {
	request.Mail.Payload(payload)
	if request.AccessLevel != "" {
		payload["accessLevel"] = request.AccessLevel
	}
	if !isEmptyValue(request.References) {
		payload["references"] = request.References
	}
	if !isEmptyValue(request.Model) {
		payload["model"] = request.Model
	}
	if request.Lane != "" {
		payload["lane"] = request.Lane
		payload["hidden"] = true
	}
	if request.SideID != "" {
		payload["sideId"] = request.SideID
	}
	if origin := request.Origin.Payload(); origin != nil {
		payload["origin"] = origin
	}
	if len(request.AccessGrant) > 0 {
		payload["accessGrant"] = request.AccessGrant
	}
	if request.PlanRunID != "" {
		payload["planRunId"] = request.PlanRunID
	}
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
	}
	if len(value.Messages) > 0 {
		payload["messages"] = cloneMessagePayloads(value.Messages)
	}
	if len(value.System) > 0 {
		payload["system"] = clonePayload(value.System)
	}
	if value.SystemSnapshot {
		// Only the system snapshot changed; there is no input to show.
		return payload
	}
	payload["agentKey"] = request.AgentKey
	payload["message"] = value.Message
	addRunDecisions(request, payload)
	payload["editingMode"] = request.EditingMode
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

func (a *StreamEventAssembler) ConsumeEmissions(input StreamInput) []EventEmission {
	return a.emit(a.dispatcher.Dispatch(input))
}

func (a *StreamEventAssembler) CompleteEmissions() []EventEmission {
	return a.emit(a.dispatcher.Complete())
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
		if event.Type == "run.activity" && a.hiddenToolActivity(event) {
			continue
		}
		normalized := a.normalizer.IsVisible(event)
		visible := normalized && IsClientVisibleEventData(event.Data())
		var derived []StreamEvent
		if visible {
			var keep bool
			keep, derived = a.activities.Track(event.Type, event.Timestamp, event.Payload)
			if !keep {
				continue
			}
		}
		emissions = append(emissions, a.stamp(event, normalized, visible))
		for _, activity := range derived {
			emissions = append(emissions, a.stamp(activity, true, true))
		}
	}
	return emissions
}

func (a *StreamEventAssembler) stamp(event StreamEvent, normalized bool, visible bool) EventEmission {
	cursor := a.seq.Load()
	ephemeral := visible && IsProgressActivity(event.Type, event.Payload)
	if visible && !ephemeral {
		cursor = a.seq.Add(1)
		event.Seq = cursor
	} else {
		event.Seq = 0
	}
	return EventEmission{Event: event, Normalized: normalized, Visible: visible, Ephemeral: ephemeral, Cursor: cursor}
}

func (a *StreamEventAssembler) hiddenToolActivity(event StreamEvent) bool {
	activity, ok := event.Payload["activity"].(map[string]any)
	if !ok {
		return false
	}
	// An announcement is the model's message to the user, not a view of the
	// tool that carried it, so a hidden announce tool still publishes it.
	if anyString(activity["kind"]) == ActivityKindAnnounce {
		return false
	}
	return a.normalizer.IsHiddenTool(anyString(activity["toolId"]), anyString(activity["toolName"]))
}
