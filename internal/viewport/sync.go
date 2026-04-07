package viewport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

type jsonRPCRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      string         `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	Result any            `json:"result,omitempty"`
	Error  *jsonRPCError  `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Syncer) fetch(ctx context.Context, server ServerDefinition, viewportKey string) (map[string]any, bool, error) {
	reqCtx := ctx
	var cancel context.CancelFunc
	if server.TimeoutMs > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, time.Duration(server.TimeoutMs)*time.Millisecond)
		defer cancel()
	}

	rpcBody, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		Method:  "viewports/get",
		Params:  map[string]any{"viewportKey": viewportKey},
	})
	if err != nil {
		return nil, false, err
	}

	endpoint := strings.TrimRight(server.BaseURL, "/")
	if server.EndpointPath != "" {
		endpoint = endpoint + "/" + strings.TrimLeft(server.EndpointPath, "/")
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(rpcBody))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
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
	var rpcResp jsonRPCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, false, err
	}
	if rpcResp.Error != nil {
		return nil, false, nil
	}
	if rpcResp.Result == nil {
		return nil, false, nil
	}
	resultMap, ok := rpcResp.Result.(map[string]any)
	if !ok {
		return nil, false, nil
	}

	// Wrap based on viewport type
	viewportType, _ := resultMap["viewportType"].(string)
	payload := resultMap["payload"]
	if viewportType == "html" {
		if html, ok := payload.(string); ok {
			return map[string]any{"html": html}, true, nil
		}
	}
	if viewportType == "qlc" {
		if qlc, ok := payload.(map[string]any); ok {
			return qlc, true, nil
		}
	}
	// Fallback: return result as-is
	return resultMap, true, nil
}
