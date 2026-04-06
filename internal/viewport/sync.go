package viewport

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type Syncer struct {
	servers    *ServerRegistry
	httpClient *http.Client
}

func NewSyncer(servers *ServerRegistry, httpClient *http.Client) *Syncer {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Syncer{servers: servers, httpClient: httpClient}
}

func (s *Syncer) Get(ctx context.Context, viewportKey string) (map[string]any, bool, error) {
	if s == nil || s.servers == nil {
		return nil, false, nil
	}
	servers, err := s.servers.List()
	if err != nil {
		return nil, false, err
	}
	for _, server := range servers {
		payload, ok, err := s.fetch(ctx, server, viewportKey)
		if err != nil {
			continue
		}
		if ok {
			return payload, true, nil
		}
	}
	return nil, false, nil
}

func (s *Syncer) fetch(ctx context.Context, server ServerDefinition, viewportKey string) (map[string]any, bool, error) {
	reqCtx := ctx
	var cancel context.CancelFunc
	if server.TimeoutMs > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, time.Duration(server.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(server.BaseURL, "/")+"?viewportKey="+viewportKey, nil)
	if err != nil {
		return nil, false, err
	}
	if server.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+server.AuthToken)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, nil
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, false, err
	}
	return payload, true, nil
}
