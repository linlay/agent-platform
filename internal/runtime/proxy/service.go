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
	"agent-platform/internal/httpclient"
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
	case r.SendQueue <- payload:
		return true
	case <-r.Done:
		return false
	case <-time.After(2 * time.Second):
		return false
	}
}

func (r *Route) PostControl(path string, payload map[string]any, target any) error {
	if r == nil || strings.TrimSpace(r.BaseURL) == "" {
		return apperrors.New(apperrors.CodeProxyRequestFailed, "proxy control endpoint is unavailable")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return apperrors.Wrap(apperrors.CodeInternalError, err)
	}
	timeout := r.Timeout
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(r.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return apperrors.Wrap(apperrors.CodeProxyRequestFailed, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
	resp, err := (httpclient.NewClient(timeout)).Do(req)
	if err != nil {
		return apperrors.Wrap(apperrors.CodeProxyRequestFailed, err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return apperrors.Wrap(apperrors.CodeProxyBadResponse, err)
	}
	if resp.StatusCode != http.StatusOK || envelope.Code != 0 {
		message := strings.TrimSpace(envelope.Msg)
		if message == "" {
			message = "proxy control request failed"
		}
		return apperrors.New(apperrors.CodeProxyUpstreamError, message)
	}
	if target != nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, target); err != nil {
			return apperrors.Wrap(apperrors.CodeProxyBadResponse, err)
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
