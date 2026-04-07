package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-platform-runner-go/internal/engine"
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
		return fmt.Errorf("%w: server %s not found", engine.ErrMCPCallFailed, serverKey)
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

func (c *Client) CallTool(ctx context.Context, serverKey string, toolName string, args map[string]any) (map[string]any, error) {
	if c.gate != nil && !c.gate.Allow(serverKey) {
		return nil, fmt.Errorf("%w: server %s is temporarily unavailable", engine.ErrMCPCallFailed, serverKey)
	}
	server, ok := c.registry.Server(serverKey)
	if !ok {
		return nil, fmt.Errorf("%w: server %s not found", engine.ErrMCPCallFailed, serverKey)
	}
	var result map[string]any
	if err := c.callWithRetry(ctx, server, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": args,
	}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) ListTools(ctx context.Context, serverKey string) ([]ToolDefinition, error) {
	if c.gate != nil && !c.gate.Allow(serverKey) {
		return nil, fmt.Errorf("%w: server %s is temporarily unavailable", engine.ErrMCPCallFailed, serverKey)
	}
	server, ok := c.registry.Server(serverKey)
	if !ok {
		return nil, fmt.Errorf("%w: server %s not found", engine.ErrMCPCallFailed, serverKey)
	}
	var payload struct {
		Tools []ToolDefinition `json:"tools"`
	}
	if err := c.callWithRetry(ctx, server, "tools/list", nil, &payload); err != nil {
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
	timeout := time.Duration(server.TimeoutMs) * time.Millisecond
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

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if c.gate != nil {
			c.gate.MarkFailure(server.Key)
		}
		return fmt.Errorf("%w: %v", engine.ErrMCPCallFailed, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%w: read response: %v", engine.ErrMCPCallFailed, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if c.gate != nil {
			c.gate.MarkFailure(server.Key)
		}
		return fmt.Errorf("%w: status %d: %s", engine.ErrMCPCallFailed, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if c.gate != nil {
		c.gate.MarkSuccess(server.Key)
	}
	observability.Log("mcp.response", map[string]any{
		"serverKey": server.Key,
		"method":    method,
		"status":    resp.StatusCode,
	})
	var decoded JSONRPCResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return fmt.Errorf("%w: decode response: %v", engine.ErrMCPCallFailed, err)
	}
	if decoded.Error != nil {
		return fmt.Errorf("%w: %s", engine.ErrMCPCallFailed, decoded.Error.Message)
	}
	if target == nil {
		return nil
	}
	payload, err := json.Marshal(decoded.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}
