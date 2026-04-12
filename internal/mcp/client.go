package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"agent-platform-runner-go/internal/contracts"
	"agent-platform-runner-go/internal/observability"
)

type Client struct {
	registry   *Registry
	httpClient *http.Client
	gate       *AvailabilityGate
}

func NewClient(registry *Registry, httpClient *http.Client) *Client {
	return NewClientWithGate(registry, httpClient, nil)
}

func NewClientWithGate(registry *Registry, httpClient *http.Client, gate *AvailabilityGate) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{registry: registry, httpClient: httpClient, gate: gate}
}

func (c *Client) Initialize(ctx context.Context, serverKey string) error {
	server, ok := c.registry.Server(serverKey)
	if !ok {
		return fmt.Errorf("%w: server %s not found", contracts.ErrMCPCallFailed, serverKey)
	}
	return c.callWithRetry(ctx, server, "initialize", map[string]any{
		"protocolVersion": "2025-06",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "agent-platform-runner",
			"version": "0.0.1",
		},
	}, nil)
}

func (c *Client) CallTool(ctx context.Context, serverKey string, toolName string, args map[string]any, meta map[string]any) (any, error) {
	if c.gate != nil && c.gate.IsBlocked(serverKey) {
		return nil, fmt.Errorf("%w: server %s is temporarily unavailable", contracts.ErrMCPCallFailed, serverKey)
	}
	server, ok := c.registry.Server(serverKey)
	if !ok {
		return nil, fmt.Errorf("%w: server %s not found", contracts.ErrMCPCallFailed, serverKey)
	}
	params := map[string]any{
		"name":      toolName,
		"arguments": defaultArgs(args),
	}
	if len(meta) > 0 {
		params["_meta"] = meta
	}
	var result any
	if err := c.callWithRetry(ctx, server, "tools/call", params, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) ListTools(ctx context.Context, serverKey string) ([]ToolDefinition, error) {
	if c.gate != nil && c.gate.IsBlocked(serverKey) {
		return nil, fmt.Errorf("%w: server %s is temporarily unavailable", contracts.ErrMCPCallFailed, serverKey)
	}
	server, ok := c.registry.Server(serverKey)
	if !ok {
		return nil, fmt.Errorf("%w: server %s not found", contracts.ErrMCPCallFailed, serverKey)
	}
	var payload struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := c.callWithRetry(ctx, server, "tools/list", map[string]any{}, &payload); err != nil {
		return nil, err
	}
	return payload.Tools, nil
}

func (c *Client) callWithRetry(ctx context.Context, server ServerDefinition, method string, params map[string]any, target any) error {
	retries := server.Retry
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		lastErr = c.call(ctx, server, method, params, target)
		if lastErr == nil {
			return nil
		}
		if ctx.Err() != nil {
			return lastErr
		}
		if attempt < retries {
			time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
		}
	}
	return lastErr
}

func (c *Client) call(ctx context.Context, server ServerDefinition, method string, params map[string]any, target any) error {
	timeout := time.Duration(server.ReadTimeoutMs) * time.Millisecond
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	body, err := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.ResolvedURL(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range server.Headers {
		req.Header.Set(k, v)
	}
	if strings.TrimSpace(server.AuthToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(server.AuthToken))
	}
	observability.Log("mcp.request", map[string]any{
		"serverKey": server.Key,
		"method":    method,
	})

	resp, err := c.httpClientForServer(server).Do(req)
	if err != nil {
		if c.gate != nil {
			c.gate.MarkFailure(server.Key)
		}
		return fmt.Errorf("%w: %v", contracts.ErrMCPCallFailed, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%w: read response: %v", contracts.ErrMCPCallFailed, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if c.gate != nil {
			c.gate.MarkFailure(server.Key)
		}
		return fmt.Errorf("%w: status %d: %s", contracts.ErrMCPCallFailed, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	payload, err := parseResponsePayload(data)
	if err != nil {
		if c.gate != nil {
			c.gate.MarkFailure(server.Key)
		}
		return fmt.Errorf("%w: decode response: %v", contracts.ErrMCPCallFailed, err)
	}
	if payload.Error != nil {
		if c.gate != nil {
			c.gate.MarkFailure(server.Key)
		}
		return fmt.Errorf("%w: %s", contracts.ErrMCPCallFailed, payload.Error.Message)
	}
	if c.gate != nil {
		c.gate.MarkSuccess(server.Key)
	}
	observability.Log("mcp.response", map[string]any{
		"serverKey": server.Key,
		"method":    method,
		"status":    resp.StatusCode,
	})
	if target == nil {
		return nil
	}
	resultBytes, err := json.Marshal(payload.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(resultBytes, target)
}

func parseResponsePayload(data []byte) (JSONRPCResponse, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return JSONRPCResponse{}, fmt.Errorf("empty payload")
	}
	var payload JSONRPCResponse
	if err := json.Unmarshal(trimmed, &payload); err == nil {
		return payload, nil
	}
	var last JSONRPCResponse
	found := false
	block := bytes.Buffer{}
	lines := bytes.Split(trimmed, []byte{'\n'})
	for _, rawLine := range lines {
		line := bytes.TrimSpace(bytes.TrimRight(rawLine, "\r"))
		if len(line) == 0 {
			if parsed, ok := parseSSEBlock(block.Bytes()); ok {
				last = parsed
				found = true
			}
			block.Reset()
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			if block.Len() > 0 {
				block.WriteByte('\n')
			}
			block.Write(bytes.TrimSpace(line[5:]))
		}
	}
	if parsed, ok := parseSSEBlock(block.Bytes()); ok {
		last = parsed
		found = true
	}
	if !found {
		return JSONRPCResponse{}, fmt.Errorf("unrecognized payload")
	}
	return last, nil
}

func parseSSEBlock(data []byte) (JSONRPCResponse, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return JSONRPCResponse{}, false
	}
	var payload JSONRPCResponse
	if err := json.Unmarshal(trimmed, &payload); err != nil {
		return JSONRPCResponse{}, false
	}
	return payload, true
}

func defaultArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	return args
}

func (c *Client) httpClientForServer(server ServerDefinition) *http.Client {
	if c == nil || c.httpClient == nil {
		return &http.Client{}
	}
	if server.ConnectTimeoutMs <= 0 {
		return c.httpClient
	}
	cloned := *c.httpClient
	baseTransport := c.httpClient.Transport
	if baseTransport == nil {
		if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
			baseTransport = defaultTransport.Clone()
		}
	}
	if transport, ok := baseTransport.(*http.Transport); ok && transport != nil {
		transport = transport.Clone()
		transport.DialContext = (&net.Dialer{
			Timeout: time.Duration(server.ConnectTimeoutMs) * time.Millisecond,
		}).DialContext
		cloned.Transport = transport
	}
	return &cloned
}
