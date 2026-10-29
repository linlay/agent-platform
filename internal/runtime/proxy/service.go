package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"agent-platform/internal/apperrors"
	"agent-platform/internal/config"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/httpclient"
	"agent-platform/internal/stream"
)

// Route is the transport-neutral runtime record for one active proxy run.
// It owns the upstream control channel and HTTP control endpoint; local HTTP
// handlers only translate requests into Runtime control commands.
type Route struct {
	RunID            string
	ChatID           string
	AgentKey         string
	UpstreamAgentKey string
	Protocol         string
	Transport        string
	BaseURL          string
	Token            string
	Timeout          time.Duration
	SendQueue        chan map[string]any
	Done             chan struct{}
	steerMu          sync.Mutex
	steerOrigins     map[string]*queryinput.Origin
	steerMails       map[string]*queryinput.MailInput
	steerMessages    map[string]string
}

func (r *Route) RememberSteerMail(id string, mail *queryinput.MailInput, message string) {
	if mail == nil {
		return
	}
	r.steerMu.Lock()
	defer r.steerMu.Unlock()
	if r.steerMails == nil {
		r.steerMails = map[string]*queryinput.MailInput{}
		r.steerMessages = map[string]string{}
	}
	copy := *mail
	r.steerMails[id] = &copy
	r.steerMessages[id] = message
}

// RememberSteerOrigin stores local provenance before forwarding. It never
// changes the upstream request and is discarded with this active route.
func (r *Route) RememberSteerOrigin(id string, origin *queryinput.Origin) {
	if r == nil || origin == nil {
		return
	}
	r.steerMu.Lock()
	defer r.steerMu.Unlock()
	if r.steerOrigins == nil {
		r.steerOrigins = map[string]*queryinput.Origin{}
	}
	r.steerOrigins[id] = origin.Clone()
}

func (r *Route) DecorateSteerEvent(event stream.EventData) stream.EventData {
	if r == nil || event.Type != "request.steer" {
		return event
	}
	r.steerMu.Lock()
	origin := r.steerOrigins[event.String("steerId")].Clone()
	mail := r.steerMails[event.String("steerId")]
	message := r.steerMessages[event.String("steerId")]
	r.steerMu.Unlock()
	// Upstream provenance is untrusted locally. Only a matching local Agent
	// invocation can add origin; do not invent an event when forwarding.
	payload := make(map[string]any, len(event.Payload)+1)
	for key, value := range event.Payload {
		if key != "origin" {
			payload[key] = value
		}
	}
	if origin != nil {
		payload["origin"] = origin.Payload()
	}
	for _, key := range []string{"mailId", "mailKind", "mailAttempt", "mailHop", "outcome"} {
		delete(payload, key)
	}
	if mail != nil {
		mail.Payload(payload)
		payload["message"] = message
	}
	event.Payload = payload
	return event
}

func NewRoute(runID, chatID, agentKey string) *Route {
	return &Route{
		RunID:     strings.TrimSpace(runID),
		ChatID:    strings.TrimSpace(chatID),
		AgentKey:  strings.TrimSpace(agentKey),
		SendQueue: make(chan map[string]any, 16),
		Done:      make(chan struct{}),
	}
}

func (r *Route) RequestType(name string) string {
	if r != nil && strings.EqualFold(strings.TrimSpace(r.Protocol), config.ChannelProtocolPlatformWS) {
		return "/api/" + strings.TrimSpace(name)
	}
	return "request." + strings.TrimSpace(name)
}

func (r *Route) Send(payload map[string]any) bool {
	if r == nil {
		return false
	}
	select {
	case <-r.Done:
		return false
	default:
	}
	select {
	case r.SendQueue <- payload:
		return true
	case <-r.Done:
		return false
	case <-time.After(2 * time.Second):
		return false
	}
}

// ControlError distinguishes a definite rejection before sending (or by the
// upstream) from an HTTP request whose handling cannot be confirmed. Unwrap
// preserves the existing application error codes used by human controls.
type ControlError struct {
	Err            error
	OutcomeUnknown bool
}

func (e *ControlError) Error() string { return e.Err.Error() }
func (e *ControlError) Unwrap() error { return e.Err }

func (r *Route) PostControl(path string, payload map[string]any, target any) error {
	if r == nil || strings.TrimSpace(r.BaseURL) == "" {
		return &ControlError{Err: apperrors.New(apperrors.CodeProxyRequestFailed, "proxy control endpoint is unavailable")}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &ControlError{Err: apperrors.Wrap(apperrors.CodeInternalError, err)}
	}
	timeout := r.Timeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(r.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return &ControlError{Err: apperrors.Wrap(apperrors.CodeProxyRequestFailed, err)}
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	resp, err := (httpclient.NewClient(timeout)).Do(req)
	if err != nil {
		return &ControlError{Err: apperrors.Wrap(apperrors.CodeProxyRequestFailed, err), OutcomeUnknown: true}
	}
	defer resp.Body.Close()
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	decodeErr := json.NewDecoder(resp.Body).Decode(&envelope)
	if resp.StatusCode != http.StatusOK || (decodeErr == nil && envelope.Code != 0) {
		message := strings.TrimSpace(envelope.Msg)
		if message == "" {
			message = "proxy control request failed"
		}
		return &ControlError{Err: apperrors.New(apperrors.CodeProxyUpstreamError, message)}
	}
	if decodeErr != nil {
		return &ControlError{Err: apperrors.Wrap(apperrors.CodeProxyBadResponse, decodeErr), OutcomeUnknown: true}
	}
	if target != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, target); err != nil {
			return &ControlError{Err: apperrors.Wrap(apperrors.CodeProxyBadResponse, err), OutcomeUnknown: true}
		}
	}
	return nil
}

// Service owns active proxy run routing. Keeping this registry outside Server
// lets HTTP, WebSocket, automation, and run tools share one control plane.
type Service struct {
	mu     sync.RWMutex
	routes map[string]*Route
}

func NewService() *Service {
	return &Service{routes: map[string]*Route{}}
}

func (s *Service) Register(route *Route) {
	if s == nil || route == nil || strings.TrimSpace(route.RunID) == "" {
		return
	}
	s.mu.Lock()
	if s.routes == nil {
		s.routes = map[string]*Route{}
	}
	s.routes[route.RunID] = route
	s.mu.Unlock()
}

func (s *Service) Unregister(runID string, route *Route) {
	if s == nil || strings.TrimSpace(runID) == "" {
		return
	}
	s.mu.Lock()
	if current := s.routes[runID]; current == route {
		delete(s.routes, runID)
	}
	s.mu.Unlock()
}

func (s *Service) Lookup(runID string) (*Route, bool) {
	if s == nil || strings.TrimSpace(runID) == "" {
		return nil, false
	}
	s.mu.RLock()
	route, ok := s.routes[runID]
	s.mu.RUnlock()
	return route, ok
}
